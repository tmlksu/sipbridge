package io.github.tmlksu.sipbridge

import android.util.Log
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.Socket
import java.util.concurrent.TimeUnit
import javax.net.SocketFactory
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString

/**
 * relay (`wss://<host>/v1/session`) への WebSocket クライアント。
 * docs/PROTOCOL.md 準拠。テキストフレーム = JSON 制御、バイナリ = RTP。
 *
 * - ヘッダ: Access Client ID/Secret が設定されていれば Service Token 方式、
 *   空なら開発用 `Authorization: Bearer <devToken>`。`X-Device-Id` /
 *   `X-Client-Version` を必ず付ける。
 * - WS ping: relay が server→client に 20 秒周期で送る (OkHttp が自動で pong)。
 *   アプリ側の OkHttp ping は [CLIENT_PING_INTERVAL_SEC] (60 秒) の補助。切断時は 1,2,4,…30 秒の
 *   指数バックオフで再接続し、接続直後の `hello` で状態同期する (呼び出し側で処理)。
 *   ただし通話中 ([callActive] が true) は relay の resume 猶予内に戻れるよう
 *   バックオフを [CALL_MAX_BACKOFF_SEC] にクランプする。
 * - 網切替 (Wi-Fi ↔ モバイル) は OkHttp が検知するまで時間がかかるため、
 *   呼び出し側が [onNetworkChanged] で通知する。
 * - 送信に失敗した制御メッセージは [PENDING_TTL_MS] 以内なら再接続後に送り直す。
 *   RTP は再送しない (遅れた音声は価値が無い)。
 * - UDP 送受信は一切行わない (端末側 LISTEN ゼロ)。
 */
class RelayClient(
    private val relayUrl: String,
    private val accessClientId: String,
    private val accessClientSecret: String,
    private val devToken: String,
    private val deviceId: String,
    private val listener: Listener,
    /** 通話中かどうか。true の間は再接続バックオフを短く保つ。 */
    private val callActive: () -> Boolean = { false }
) {
    interface Listener {
        fun onHello(v: RelayProtocol.Hello)
        fun onRegistration(ok: Boolean, detail: String)
        fun onIncoming(v: RelayProtocol.Incoming)
        fun onRinging(v: RelayProtocol.Ringing)
        fun onAnswered(v: RelayProtocol.Answered)
        fun onEnded(v: RelayProtocol.Ended)
        fun onError(code: String, message: String)
        fun onDisconnected()
        /** バイナリフレーム (RTP パケットそのまま)。通話中のみ届く。 */
        fun onRtpReceived(rtp: ByteArray)
        /** JSON `pong` (`ping` の ts をそのまま返す)。通話品質の RTT 計測用 (issue #26)。 */
        fun onPong(ts: Long) {}
        /**
         * WS が開いた (hello より前)。[ws] に代入する前に呼ぶため、ここで記録した時刻は
         * 新しい接続が送信に使われ始めるより必ず前になる (通話中の下り途絶監視の猶予用)。
         */
        fun onConnected() {}
    }

    companion object {
        private const val TAG = "RelayClient"
        private const val MAX_BACKOFF_SEC = 30L
        /**
         * OkHttp の client→server WS ping 周期 (#25)。
         *
         * relay の pingLoop が server→client に 20 秒周期で ping を送り、無応答なら切るため、
         * 接続の維持 (Cloudflare の約 100 秒アイドル切断・NAT) と relay 側の半死に刈り取りは
         * そちらで足りる。アプリ側で 20 秒周期を重ねると 20 秒あたり制御フレームが倍になり、
         * relay の ping と位相がずれるぶんモバイルの radio も余計に起こす。
         *
         * 一方 OkHttp は受けた ping を生存確認に使わず (readTimeout も WS では無効)、
         * ブラックホール化した TCP をアプリ側で検知できるのはこの ping の pong 待ちだけ
         * (次の ping 時点で前の pong が未着なら onFailure → 再接続)。網切替 (Wi-Fi ↔ モバイル)
         * による主なブラックホール化は BridgeService の NetworkCallback → [onNetworkChanged]
         * で即座に張り直すため、ここで拾うのは「同じ網のまま上流が死んだ」残りのケース。
         * 検知は 1〜2 周期 (60〜120 秒) で、待機中の着信取りこぼし窓として許容できる長さに留める。
         */
        const val CLIENT_PING_INTERVAL_SEC = 60L
        /** 通話中の再接続バックオフ上限。relay の resume 猶予より十分短くする。 */
        private const val CALL_MAX_BACKOFF_SEC = 2L
        /** 再送待ちに残せる制御メッセージ数。 */
        private const val PENDING_MAX = 8
        /** 再送待ちの有効期限。これを過ぎた操作 (応答・切断) は既に意味を失っている。 */
        private const val PENDING_TTL_MS = 10_000L
        /**
         * relay への TCP ソケットの送信バッファ (SO_SNDBUF) 要求値。
         *
         * [WebSocket.queueSize] は OkHttp 内部キューの滞留しか数えず、カーネルの TCP
         * 送信バッファが満杯になって書き込みスレッドが詰まるまで 0 のまま。Android の
         * Wi-Fi/LTE では送信バッファが自動調整で 256KiB〜1MiB まで育つため、網が細いと
         * 数十秒分の音声がカーネルに溜まり、[RTP_QUEUE_DROP_BYTES] のゲートが発火しない。
         * そこで送信バッファを小さく固定し、滞留を OkHttp のキュー側に押し戻す。
         *
         * 根拠: Linux は SO_SNDBUF の要求値を 2 倍して採用し (管理領域込み)、明示設定した
         * ソケットは自動調整を止める。実効 32KiB は 1 フレーム ≒ 200B (172B の RTP +
         * WS/TLS のヘッダ) 換算で約 1.5〜3 秒分。送信バッファは 1 RTT 内に未 ACK で置ける
         * 量の上限でもあるが、RTT 300ms でも ≒ 100KB/s で、上り音声に要る ≒ 10KB/s
         * (50fps × 約 200B) の 10 倍あるため通常時の送出は妨げない。
         * 下り (relay → 端末) は受信バッファ側のため影響しない。
         */
        const val RELAY_SOCKET_SNDBUF_BYTES = 16 * 1024
        /**
         * 上り RTP を捨て始める OkHttp 送信キューの滞留バイト数 ([WebSocket.queueSize] 参照)。
         * カーネルの送信バッファ ([RELAY_SOCKET_SNDBUF_BYTES]、実効約 1.5〜3 秒分) が
         * 埋まった後、さらに OkHttp のキューに 8KiB (1 フレーム ≒ 178B = 172B の RTP +
         * WS フレームヘッダ 6B、50fps で約 46 フレーム ≒ 約 1 秒分) 溜まったら捨てる。
         * OkHttp は滞留が 16MiB を超えると積まずに WebSocket を閉じる (1001) ため、
         * それより十分手前で捨て、同じキューを通る制御メッセージ (hangup 等) が
         * 古い RTP の後ろに埋もれないようにする。
         */
        const val RTP_QUEUE_DROP_BYTES = 8 * 1024
        /** 破棄ログの間引き間隔。relay 側 rtp.go の「50 パケットごとに 1 回」相当。 */
        const val RTP_DROP_LOG_EVERY = 50L
    }

    /**
     * 上り RTP のバックプレッシャー判定 + 破棄計数。`queueSize()` が [RTP_QUEUE_DROP_BYTES]
     * 以上ならそのフレームを捨てる (遅れた音声は価値が無い)。
     * WebSocket から切り離した純粋な判定のため JVM テストで直接検証できる。
     * 送信側 (rtp-send スレッド) のみが [shouldDrop]/[onSent] を呼び、
     * [reset] は接続 (ws) の切り替わりで呼ぶ想定。件数の厳密さより欠落の無さを優先する。
     */
    class RtpSendGate(
        private val dropBytes: Long = RTP_QUEUE_DROP_BYTES.toLong(),
        private val logEvery: Long = RTP_DROP_LOG_EVERY
    ) {
        init {
            require(logEvery >= 1) { "logEvery は 1 以上: $logEvery" }
        }

        /** 滞留で捨てた累計。[reset] で接続ごとに捨てる。 */
        var dropped: Long = 0L
            private set
        private var unreported = false

        /** 滞留が閾値以上なら件数を数えて true (そのフレームを捨てる)。 */
        fun shouldDrop(queueSize: Long): Boolean {
            if (queueSize < dropBytes) return false
            dropped++
            unreported = true
            return true
        }

        /**
         * [shouldDrop] が true の直後に呼ぶ。間引きログの番 (1, 1+logEvery, … 件目) なら true。
         * logEvery=1 なら毎回 true。
         */
        fun shouldLogDrop(): Boolean = dropped > 0 && (dropped - 1) % logEvery == 0L

        /** 送信成功時に呼ぶ。未報告の破棄があれば累計を返して報告済みにする。 */
        fun onSent(): Long {
            if (!unreported) return 0
            unreported = false
            return dropped
        }

        /** 接続 (ws) ごとに呼ぶ。再接続後は滞留も解消している想定のため累計は残さない。 */
        fun reset() {
            dropped = 0
            unreported = false
        }
    }

    /**
     * 作るソケットすべてに SO_SNDBUF = [sendBufferBytes] を設定する [SocketFactory]。
     * OkHttp は引数無しの [createSocket] で未接続ソケットを作って自分で connect し、
     * その上に TLS を被せる (SSLSocketFactory の layered 版) ため、TLS 下の TCP にも効く。
     * 他のオーバーロードも同じく「未接続で作る → 設定 → connect」の順にし、
     * 接続前に確定させる。根拠は [RELAY_SOCKET_SNDBUF_BYTES]。
     */
    class SendBufferSocketFactory(
        private val sendBufferBytes: Int = RELAY_SOCKET_SNDBUF_BYTES,
        private val delegate: SocketFactory = SocketFactory.getDefault(),
        /** 設定後の実効値 (カーネルが採用した SO_SNDBUF) の通知先。実機確認のログ用。 */
        private val onCreated: (Int) -> Unit = {}
    ) : SocketFactory() {
        override fun createSocket(): Socket =
            delegate.createSocket().apply {
                sendBufferSize = sendBufferBytes
                runCatching { onCreated(sendBufferSize) }
            }

        override fun createSocket(host: String, port: Int): Socket =
            connected(InetSocketAddress(host, port), local = null)

        override fun createSocket(
            host: String,
            port: Int,
            localHost: InetAddress?,
            localPort: Int
        ): Socket = connected(InetSocketAddress(host, port), InetSocketAddress(localHost, localPort))

        override fun createSocket(host: InetAddress, port: Int): Socket =
            connected(InetSocketAddress(host, port), local = null)

        override fun createSocket(
            address: InetAddress,
            port: Int,
            localAddress: InetAddress?,
            localPort: Int
        ): Socket = connected(InetSocketAddress(address, port), InetSocketAddress(localAddress, localPort))

        /** 未接続で作って SO_SNDBUF を設定してから bind/connect する。失敗時は閉じる。 */
        private fun connected(remote: InetSocketAddress, local: InetSocketAddress?): Socket {
            val s = createSocket()
            try {
                if (local != null) s.bind(local)
                s.connect(remote)
                return s
            } catch (e: Exception) {
                runCatching { s.close() }
                throw e
            }
        }
    }

    private class PendingMsg(val json: String, val queuedAt: Long)

    // RelayClient ごとの専用クライアント (他の通信とは共有しない) のため、
    // 送信バッファの縮小は relay への WebSocket だけに効く。
    private val http = OkHttpClient.Builder()
        .socketFactory(SendBufferSocketFactory { actual ->
            // 端末が SO_SNDBUF を本当に絞ったかの確認用 (接続ごとに 1 行)。
            Log.i(TAG, "relay socket SO_SNDBUF requested=$RELAY_SOCKET_SNDBUF_BYTES actual=$actual")
        })
        .pingInterval(CLIENT_PING_INTERVAL_SEC, TimeUnit.SECONDS)
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(0, TimeUnit.MILLISECONDS) // 長時間接続のためリードタイムアウト無し
        .build()

    // ws / pending / connected の更新は必ず `lock` 内で行う (openSocket の代入前に
    // onOpen が走って pending が取り残されるのを防ぐ)。読み出しはロック外からも
    // あるため @Volatile を維持する。
    @Volatile private var ws: WebSocket? = null
    /** 接続ハンドシェイク中のソケット (connect() 二重呼び出しでの重複接続防止用)。 */
    @Volatile private var pending: WebSocket? = null
    @Volatile private var wantConnect = false
    @Volatile private var connected = false
    private val lock = Any()
    private var backoffSec = 1L
    private var reconnectPosted = false
    /** 送信に失敗した制御メッセージ。再接続直後に古い順で送り直す (`lock` で保護)。 */
    private val pendingControl = ArrayDeque<PendingMsg>()
    /** 上り RTP の滞留判定。接続 (ws) ごとに [RtpSendGate.reset] する。 */
    private val rtpGate = RtpSendGate()
    /**
     * 上り RTP の滞留破棄の累計 (接続をまたいで数える。[rtpGate] は接続ごとにリセットされる)。
     * 書くのは rtp-send スレッドのみ。通話品質の `tx.drop` は通話開始・終了時の差分で取る。
     */
    @Volatile var rtpDropTotal: Long = 0L
        private set

    /** WS 未接続 (再接続待ち) で送れなかった上り RTP の累計 (call_stats の tx.lost)。rtp-send スレッドのみ書く。 */
    @Volatile var rtpNoConnTotal: Long = 0L
        private set
    private val handler = android.os.Handler(android.os.Looper.getMainLooper())

    fun isConnected(): Boolean = connected && ws != null

    fun connect() {
        synchronized(lock) {
            wantConnect = true
            backoffSec = 1L
            if (ws != null || pending != null) return  // 既に接続済み/接続中
        }
        openSocket()
    }

    fun disconnect() {
        // 状態のクリアはロック内、close() 呼び出しはロック外で行う。
        val (old, pend) = synchronized(lock) {
            wantConnect = false
            val o = ws
            val p = pending
            ws = null
            pending = null
            connected = false
            pendingControl.clear()
            rtpGate.reset()
            o to p
        }
        runCatching { old?.close(1000, "client disconnect") }
        runCatching { pend?.close(1000, "client disconnect") }
    }

    /**
     * 端末のネットワークが切り替わった/失われたときに呼ぶ。
     * 旧網に紐づいたソケットはブラックホール化して ping タイムアウトまで生きて見えるため、
     * 明示的に畳んでから再接続する。[available] が true ならバックオフをリセットして即接続する。
     * [replaceSocket] が false なら既存のソケットや接続試行は畳まない
     * (網が 1 つ目に見つかっただけで、網から網へ切り替わったわけではない場合)。
     */
    fun onNetworkChanged(available: Boolean, replaceSocket: Boolean = true) {
        val (old, pend) = synchronized(lock) {
            if (!wantConnect) return
            val o = ws
            val p = pending
            if (!replaceSocket && o != null) return
            if (available) backoffSec = 1L
            // 接続試行中なら任せる (失敗しても、戻したバックオフで即再試行される)。
            if (!replaceSocket && p != null) return
            if (o == null && p == null) return@synchronized null to null
            ws = null
            pending = null
            connected = false
            o to p
        }
        Log.i(TAG, "network changed (available=$available)")
        runCatching { old?.cancel() }
        runCatching { pend?.cancel() }
        if (old != null) listener.onDisconnected()
        if (available) handler.post { if (wantConnect) openSocket() } else scheduleReconnect()
    }

    fun shutdown() {
        disconnect()
        http.dispatcher.executorService.shutdownNow()
    }

    private fun openSocket() {
        synchronized(lock) { if (ws != null || pending != null) return }
        val url = try {
            normalizeRelayUrl(relayUrl)
        } catch (e: Exception) {
            Log.w(TAG, "bad relay URL: ${e.message}")
            listener.onError("config", "relay URL が不正です: ${e.message}")
            scheduleReconnect()
            return
        }
        val req = Request.Builder().url(url)
            .addHeader("X-Device-Id", deviceId)
            .addHeader("X-Client-Version", RelayProtocol.CLIENT_VERSION)
            .also { b ->
                if (accessClientId.isNotBlank()) {
                    b.addHeader("CF-Access-Client-Id", accessClientId)
                    b.addHeader("CF-Access-Client-Secret", accessClientSecret)
                } else if (devToken.isNotBlank()) {
                    b.addHeader("Authorization", "Bearer $devToken")
                }
            }
            .build()
        Log.i(TAG, "connecting to $url")
        synchronized(lock) {
            // ロックを外していた間に他経路が接続を開始していないか再確認する。
            // newWebSocket と pending への代入をロック内で行うことで、OkHttp スレッドの
            // onOpen が代入前の状態を見る (pending が null のまま残る) ことを防ぐ。
            if (ws != null || pending != null) return
            pending = http.newWebSocket(req, SocketListener())
        }
    }

    private inner class SocketListener : WebSocketListener() {
        override fun onOpen(webSocket: WebSocket, response: Response) {
            runCatching { listener.onConnected() }
            synchronized(lock) {
                ws = webSocket
                if (pending === webSocket) pending = null
                connected = true
                backoffSec = 1L
                // 新しい ws はキューが空のため、前の接続の破棄累計は残さない。
                rtpGate.reset()
            }
            Log.i(TAG, "websocket open")
            flushPendingControl()
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            val msg = try {
                RelayProtocol.parseRelayMessage(text)
            } catch (e: Exception) {
                Log.w(TAG, "parse fail: ${e.message}")
                return
            }
            when (msg) {
                is RelayProtocol.RelayMsg.HelloMsg -> listener.onHello(msg.v)
                is RelayProtocol.RelayMsg.RegistrationMsg ->
                    listener.onRegistration(msg.v.ok, msg.v.detail)
                is RelayProtocol.RelayMsg.IncomingMsg -> listener.onIncoming(msg.v)
                is RelayProtocol.RelayMsg.RingingMsg -> listener.onRinging(msg.v)
                is RelayProtocol.RelayMsg.AnsweredMsg -> listener.onAnswered(msg.v)
                is RelayProtocol.RelayMsg.EndedMsg -> listener.onEnded(msg.v)
                is RelayProtocol.RelayMsg.ErrorMsg -> listener.onError(msg.v.code, msg.v.message)
                is RelayProtocol.RelayMsg.PongMsg -> listener.onPong(msg.v.ts)
            }
        }

        override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
            listener.onRtpReceived(bytes.toByteArray())
        }

        override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
            Log.i(TAG, "closing: $code $reason")
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
            Log.i(TAG, "closed: $code $reason")
            onLost(webSocket)
        }

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
            Log.w(TAG, "failure: ${t.message} http=${response?.code}")
            if (response?.code == 401 || response?.code == 403) {
                listener.onError("auth", "認証失敗 (${response.code}): Access トークン/Dev Token を確認してください")
            }
            onLost(webSocket)
        }

        private fun onLost(socket: WebSocket) {
            // 状態更新はロック内、listener 通知・再接続予約はロック外。
            var stale = false
            val was = synchronized(lock) {
                if (socket !== ws && socket !== pending) {
                    // disconnect() → connect() 後に、古いソケットの onClosed/onFailure が
                    // 遅れて届いたケース。現行の接続状態を壊さないよう何もしない。
                    stale = true
                    return@synchronized false
                }
                val prev = connected
                connected = false
                if (ws === socket) ws = null
                if (pending === socket) pending = null
                prev
            }
            if (stale) {
                Log.d(TAG, "stale socket の close/failure を無視")
                return
            }
            if (was) listener.onDisconnected()
            scheduleReconnect()
        }
    }

    private fun scheduleReconnect() {
        synchronized(lock) {
            if (!wantConnect || reconnectPosted) return
            reconnectPosted = true
        }
        val cap = if (callActive()) CALL_MAX_BACKOFF_SEC else MAX_BACKOFF_SEC
        val delay = synchronized(lock) { backoffSec }.coerceAtMost(cap)
        Log.i(TAG, "reconnect in ${delay}s")
        handler.postDelayed({
            synchronized(lock) { reconnectPosted = false }
            if (!wantConnect) return@postDelayed
            synchronized(lock) { backoffSec = (backoffSec * 2).coerceAtMost(MAX_BACKOFF_SEC) }
            openSocket()
        }, delay * 1000)
    }

    // ---------- app -> relay 送信 ----------

    /**
     * 制御メッセージを送る。切断中や `send()` 拒否 (送信キュー満・クローズ済) で落とすと
     * 応答・切断が相手に届かないままになるため、[queueOnFailure] なら再送待ちに入れる。
     */
    private fun sendText(json: String, queueOnFailure: Boolean = true): Boolean {
        val s = ws
        val ok = s != null && runCatching { s.send(json) }.getOrDefault(false)
        if (!ok && queueOnFailure) queueControl(json)
        return ok
    }

    private fun queueControl(json: String) {
        val now = System.currentTimeMillis()
        val size = synchronized(lock) {
            if (!wantConnect) return
            // 期限切れと同一内容 (連打された hangup など) を落としてから積む。
            pendingControl.removeAll { now - it.queuedAt > PENDING_TTL_MS || it.json == json }
            if (pendingControl.size >= PENDING_MAX) pendingControl.removeFirst()
            pendingControl.addLast(PendingMsg(json, now))
            pendingControl.size
        }
        // 内容には sip_account のパスワードが含まれ得るためログには出さない。
        Log.w(TAG, "制御メッセージの送信に失敗。再送待ち $size 件")
    }

    /** 再接続直後に呼ぶ。期限内の制御メッセージを古い順で送り直す。 */
    private fun flushPendingControl() {
        val now = System.currentTimeMillis()
        val msgs = synchronized(lock) {
            val ready = pendingControl.filter { now - it.queuedAt <= PENDING_TTL_MS }
            pendingControl.clear()
            ready
        }
        if (msgs.isEmpty()) return
        Log.i(TAG, "再接続後に制御メッセージを再送: ${msgs.size} 件")
        for (m in msgs) {
            if (!sendText(m.json, queueOnFailure = false)) {
                queueControl(m.json)
                break
            }
        }
    }

    fun answer(callId: String, pt: Int? = null): Boolean = sendText(RelayProtocol.buildAnswer(callId, pt))
    fun reject(callId: String, code: Int? = null): Boolean = sendText(RelayProtocol.buildReject(callId, code))
    fun hangup(callId: String): Boolean = sendText(RelayProtocol.buildHangup(callId))
    fun dial(to: String): Boolean = sendText(RelayProtocol.buildDial(to))
    fun dtmf(callId: String, digits: String): Boolean = sendText(RelayProtocol.buildDtmf(callId, digits))
    fun registerPush(provider: String, token: String): Boolean =
        sendText(RelayProtocol.buildRegisterPush(provider, token))

    /** v1.1: SIP アカウント登録。接続ごとの最初の hello 受信後に呼ぶ。 */
    fun sendSipAccount(user: String, password: String, display: String = ""): Boolean =
        sendText(RelayProtocol.buildSipAccount(user, password, display))

    /**
     * JSON レベルの keep-alive (`{"t":"ping"}`)。落としても次の ping で足りるので再送しない。
     * 現状アプリ内に呼び出し箇所は無い (WS ping で足りるため周期送信はしていない)。
     */
    fun pingNow(): Boolean =
        sendText(RelayProtocol.buildPing(System.currentTimeMillis()), queueOnFailure = false)

    /** RTT 計測用の JSON ping (通話の開始直後・終了直前に 1 回ずつ)。再送しない。 */
    fun ping(ts: Long): Boolean = sendText(RelayProtocol.buildPing(ts), queueOnFailure = false)

    /** 通話品質の `call_stats` (issue #26)。切断中なら捨てる (再送待ちに積まない)。 */
    fun sendCallStats(json: String): Boolean = sendText(json, queueOnFailure = false)

    /**
     * 通話中に下り RTP が途絶えたときの張り直し (issue #26 の途絶監視)。
     * 同じ網のまま上流が死ぬと OkHttp の ping では検知が最大 120 秒かかり、relay の
     * resume 猶予 (10 秒) に間に合わないため、確立済みの接続を捨てて即座に接続し直す。
     * 接続中 (ハンドシェイク中) や未接続なら既に再接続の途中なので何もせず false。
     * どのスレッドから呼んでもよい (実接続は main スレッドで行う)。
     */
    fun reconnectStalled(): Boolean {
        val old = synchronized(lock) {
            if (!wantConnect) return false
            val o = ws ?: return false
            if (pending != null) return false
            ws = null
            connected = false
            backoffSec = 1L
            o
        }
        Log.w(TAG, "下り RTP が途絶えたため WS を張り直す")
        runCatching { old.cancel() }
        listener.onDisconnected()
        handler.post { if (wantConnect) openSocket() }
        return true
    }

    /**
     * RTP パケット (12B ヘッダ + ペイロード) をバイナリフレームで送る。通話中のみ。
     * OkHttp の送信キューの滞留が [RTP_QUEUE_DROP_BYTES] 以上なら (カーネルの送信バッファ
     * [RELAY_SOCKET_SNDBUF_BYTES] が埋まった上に約 1 秒分) そのフレームを捨てて false を返す。シーケンス番号・タイムスタンプは
     * 呼び出し側で進め続けるため、相手からは損失に見える (再送はしない)。
     */
    fun sendRtp(rtp: ByteArray): Boolean {
        val s = ws ?: run {
            rtpNoConnTotal++
            return false
        }
        if (rtpGate.shouldDrop(s.queueSize())) {
            rtpDropTotal++
            if (rtpGate.shouldLogDrop()) {
                Log.w(TAG, "RTP 送信キュー滞留のため破棄 (累計 ${rtpGate.dropped} 件)")
            }
            return false
        }
        // toByteString() は of(*rtp) (spread + okio 内部で 2 回コピー) と違い 1 回だけ確保する。
        val ok = runCatching { s.send(rtp.toByteString()) }.getOrDefault(false)
        if (ok) {
            // 捨てていた期間が終わったら累計を 1 行出す (間引きログの締め)。
            val total = rtpGate.onSent()
            if (total > 0) Log.i(TAG, "RTP 送信が回復 (滞留中に $total 件破棄)")
        }
        return ok
    }
}

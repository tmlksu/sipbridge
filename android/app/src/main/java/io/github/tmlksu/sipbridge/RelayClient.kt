package io.github.tmlksu.sipbridge

import android.util.Log
import java.util.concurrent.TimeUnit
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString

/**
 * relay (`wss://<host>/v1/session`) への WebSocket クライアント。
 * docs/PROTOCOL.md 準拠。テキストフレーム = JSON 制御、バイナリ = RTP。
 *
 * - ヘッダ: Access Client ID/Secret が設定されていれば Service Token 方式、
 *   空なら開発用 `Authorization: Bearer <devToken>`。`X-Device-Id` /
 *   `X-Client-Version` を必ず付ける。
 * - WS ping/pong 20 秒 (OkHttp の pingInterval)。切断時は 1,2,4,…30 秒の
 *   指数バックオフで再接続し、接続直後の `hello` で状態同期する (呼び出し側で処理)。
 * - UDP 送受信は一切行わない (端末側 LISTEN ゼロ)。
 */
class RelayClient(
    private val relayUrl: String,
    private val accessClientId: String,
    private val accessClientSecret: String,
    private val devToken: String,
    private val deviceId: String,
    private val listener: Listener
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
    }

    companion object {
        private const val TAG = "RelayClient"
        private const val MAX_BACKOFF_SEC = 30L
    }

    private val http = OkHttpClient.Builder()
        .pingInterval(20, TimeUnit.SECONDS)
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
            o to p
        }
        runCatching { old?.close(1000, "client disconnect") }
        runCatching { pend?.close(1000, "client disconnect") }
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
            synchronized(lock) {
                ws = webSocket
                if (pending === webSocket) pending = null
                connected = true
                backoffSec = 1L
            }
            Log.i(TAG, "websocket open")
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
                is RelayProtocol.RelayMsg.PongMsg -> { /* keep-alive 応答。特段処理なし */ }
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
        val delay = synchronized(lock) { backoffSec }.coerceAtMost(MAX_BACKOFF_SEC)
        Log.i(TAG, "reconnect in ${delay}s")
        handler.postDelayed({
            synchronized(lock) { reconnectPosted = false }
            if (!wantConnect) return@postDelayed
            synchronized(lock) { backoffSec = (backoffSec * 2).coerceAtMost(MAX_BACKOFF_SEC) }
            openSocket()
        }, delay * 1000)
    }

    // ---------- app -> relay 送信 ----------

    private fun sendText(json: String): Boolean {
        val s = ws ?: return false
        return runCatching { s.send(json) }.getOrDefault(false)
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

    fun pingNow(): Boolean = sendText(RelayProtocol.buildPing(System.currentTimeMillis()))

    /** RTP パケット (12B ヘッダ + ペイロード) をバイナリフレームで送る。通話中のみ。 */
    fun sendRtp(rtp: ByteArray): Boolean {
        val s = ws ?: return false
        return runCatching { s.send(ByteString.of(*rtp)) }.getOrDefault(false)
    }
}

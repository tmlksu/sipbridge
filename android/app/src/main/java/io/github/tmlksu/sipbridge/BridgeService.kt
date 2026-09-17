package io.github.tmlksu.sipbridge

import android.Manifest
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.media.AudioManager
import android.net.Uri
import android.net.wifi.WifiManager
import android.os.Bundle
import android.telecom.DisconnectCause
import android.telecom.TelecomManager
import android.os.Binder
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.PowerManager
import android.util.Log

/**
 * 常駐 ForegroundService。EchoSIP の SipService を置換する。
 * [RelayClient] を保持し、relay との WSS 接続・通話状態・RTP を管理する。
 *
 * モード:
 * - PERSISTENT: WSS 常時接続 (Echo Show 用。従来の運用と同じ)。
 * - PUSH: 着信 push・発信操作で接続し、通話終了 60 秒後に切断 (S25 用。FCM 受信は T5)。
 */
class BridgeService : Service(), RelayClient.Listener {

    companion object {
        private const val TAG = "BridgeService"
        const val ACT_ANSWER = "sipbridge.ANSWER"
        const val ACT_REJECT = "sipbridge.REJECT"
        const val ACT_HANGUP = "sipbridge.HANGUP"
        const val ACT_DIAL = "sipbridge.DIAL"
        const val ACT_CONNECT = "sipbridge.CONNECT"
        /** T5 (FCM) が着信起床で送る。現時点では CONNECT と同等。 */
        const val ACT_WAKE_INCOMING = "sipbridge.WAKE_INCOMING"
        const val ACT_STOP = "sipbridge.STOP"
        /** §6.3 手順 1: 7 日ごとの push 再登録。PUSH モードのときだけ再接続して register_push を送り直す。
         *  HealthCheckReceiver が needsReregister == true のときに送る。
         *  同一プロセスで登録済みでも [registeredPushToken] を null に戻してから接続するため、
         *  restorePushToken() が null を返して何もしない (＝再登録が永久に実行されない) 不具合を避ける。 */
        const val ACT_REREGISTER_PUSH = "sipbridge.REREGISTER_PUSH"
        const val ACT_UI_SHOWN = "sipbridge.UI_SHOWN"
        const val ACT_UI_HIDDEN = "sipbridge.UI_HIDDEN"
        /** `SipConnectionService.onCreateOutgoingConnection` からの発信継続
         *  (`CallActivity` は起動しない — OS 標準画面が出ているため)。 */
        const val ACT_DIAL_TELECOM = "sipbridge.DIAL_TELECOM"
        /** self-managed の `onShowIncomingCallUi` からの自前着信 UI 要求。 */
        const val ACT_SHOW_INCOMING_UI = "sipbridge.SHOW_INCOMING_UI"
        const val EXTRA_TO = "to"
        /** [ACT_DIAL_TELECOM] で ConnectionService が渡すティア名 ([CallTier])。 */
        const val EXTRA_TELECOM_TIER = "telecomTier"

        /** `addNewIncomingCall` / `placeCall` の後、この時間以内に
         *  `onCreate...Connection` が来なければ沈黙失敗としてティア C に落とす。 */
        const val TELECOM_WATCHDOG_MS = 2000L

        /** PUSH モードで通話終了後に切断するまでの猶予。 */
        const val PUSH_IDLE_DISCONNECT_MS = 60_000L

        // gms flavor の BridgeMessagingService / GmsApplication が保存する FCM トークン。
        // (foss では該当 prefs が存在しないため常に null で無害)
        internal const val FCM_PREFS = "sipbridge_gms"
        internal const val FCM_TOKEN_KEY = "last_fcm_token"

        /** Service が生きているか (設定画面の開始/停止表示・再接続判定に使う)。 */
        @Volatile
        var running: Boolean = false
            private set

        fun start(ctx: Context) {
            val i = Intent(ctx, BridgeService::class.java)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) ctx.startForegroundService(i)
            else ctx.startService(i)
        }

        /** §6.3 手順 1 用の起動 ([ACT_REREGISTER_PUSH] を付けて起こす)。 */
        fun startReregister(ctx: Context) {
            val i = Intent(ctx, BridgeService::class.java).setAction(ACT_REREGISTER_PUSH)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) ctx.startForegroundService(i)
            else ctx.startService(i)
        }
    }

    private var client: RelayClient? = null
    private var wifiLock: WifiManager.WifiLock? = null
    private var wakeLock: PowerManager.WakeLock? = null
    private var overlay: CallOverlayManager? = null
    private var audioManager: AudioManager? = null
    /** 全画面着信UIが前面にある間は通知・バブルを出さない (二重表示防止) */
    @Volatile private var uiVisible = false
    private val mainHandler = Handler(Looper.getMainLooper())
    private var pushDisconnectRunnable: Runnable? = null
    /** 未接続状態で発信要求されたとき、hello 受信後に送る発信先。 */
    @Volatile private var pendingDial: String? = null
    /** T5 が渡す FCM トークン。接続確立後の hello で register_push する。 */
    @Volatile private var pendingPushToken: String? = null
    /** このプロセスで relay に register_push 済みのトークン。同一トークンの再登録を避ける。 */
    @Volatile private var registeredPushToken: String? = null
    /** 接続ごとの sip_account 送信済みフラグ。最初の hello でのみ送り、2 回目以降は送らない。
     *  onDisconnected でリセットする。 */
    @Volatile private var sipAccountSent = false
    // ---- P3 履歴・連絡先 ----
    private val historyStore by lazy(LazyThreadSafetyMode.SYNCHRONIZED) { HistoryStore.fromContext(this) }
    private val contactStore by lazy(LazyThreadSafetyMode.SYNCHRONIZED) { ContactStore.fromContext(this) }
    /** 応答済み通話の履歴エントリ ID (終了時に通話時間を更新する)。未応答なら null。 */
    @Volatile private var activeHistoryId: String? = null
    /** 履歴書き込み済みの callId (MISSED の二重書き込み防止。ローカル拒否と ended の両経路用)。 */
    private val historyDoneFor = mutableSetOf<String>()
    /** 提示中の着信の開始時刻 (MISSED エントリの startedAt 用)。 */
    @Volatile private var incomingStartedAt: Long = 0L
    /** 音声経路の変更前の状態 (通話終了で元に戻す)。 */
    private var prevSpeakerOn: Boolean = false
    private var audioRouteSet: Boolean = false
    /** 通話中ピル代替 (オーバーレイ権限無し) の常駐通知ティッカー。 */
    private var inCallNotifyTick: Runnable? = null
    /** この呼のティア。呼ごとにセットアップ時点で確定し、通話中は変えない。
     *  `CallHub.resetCall()` のタイミングで LEGACY に戻す。 */
    @Volatile private var currentTier: CallTier = CallTier.LEGACY
    /** Telecom ウォッチドッグ (沈黙失敗の検出用)。 */
    private var telecomWatchdog: Runnable? = null
    /** Telecom 生成待ちの呼の向き (`onCreate...Failed` 即時フォールバックの振り分け用)。 */
    @Volatile private var telecomPendingIncoming = false
    /** Telecom 生成待ちの発信先 (同上)。 */
    @Volatile private var telecomPendingTo: String? = null

    inner class LocalBinder : Binder() { fun service(): BridgeService = this@BridgeService }
    override fun onBind(intent: Intent?): IBinder = LocalBinder()

    override fun onCreate() {
        super.onCreate()
        running = true
        // Telecom アカウントの登録/解除 (起動時)。失敗しても何も起きない。
        TelecomTierManager.sync(this)
        NotificationHelper.ensureChannels(this)
        // §6.3 定期チェックの登録 (Service 生成のたびに。重複登録は上書きされるだけ)。
        HealthCheckReceiver.schedule(this)
        // API 31+: バックグラウンドからの bind (FCM onNewToken 経由など) では
        // ForegroundServiceStartNotAllowedException になり得るため保護する。
        runCatching {
            val cfg0 = BridgeConfig.load(this)
            startForeground(
                NotificationHelper.ID_SERVICE,
                NotificationHelper.serviceNotification(
                    this, cfg0.mode == BridgeMode.PUSH, "", cfg0.serviceNotificationQuiet
                )
            )
        }.onFailure { Log.w(TAG, "startForeground failed (background start?)", it) }
        audioManager = getSystemService(Context.AUDIO_SERVICE) as AudioManager
        overlay = CallOverlayManager(this)
        val wm = getSystemService(Context.WIFI_SERVICE) as WifiManager
        wifiLock = wm.createWifiLock(WifiManager.WIFI_MODE_FULL_HIGH_PERF, "SipBridge:lock").apply {
            runCatching { acquire() }
        }
        val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
        wakeLock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "SipBridge:lock").apply {
            runCatching { acquire(12 * 60 * 60 * 1000L) }
        }
        ensureClient()
    }

    /** T5: gms flavor が prefs に保存した FCM トークンを復元する。
     *  onNewToken → setPushToken がサービス再生成前に起きると pendingPushToken が
     *  失われ、hello 後の register_push が送られない (relay に push が届かない) のを防ぐ。
     *  復元した (または既に保持している) トークンを返す。
     *  既に本プロセスで登録済みのトークンと同じなら復元せず null を返す
     *  (同じトークンでの再接続・再 register_push を避ける)。 */
    private fun restorePushToken(): String? {
        pendingPushToken?.let { return it }
        val fromPrefs = runCatching {
            getSharedPreferences(FCM_PREFS, Context.MODE_PRIVATE)
                .getString(FCM_TOKEN_KEY, null)?.takeIf { it.isNotBlank() }
        }.getOrNull()
        if (fromPrefs != null && fromPrefs == registeredPushToken) return null
        if (fromPrefs != null) {
            Log.i(TAG, "FCM トークンを prefs から復元 (len=${fromPrefs.length})")
            pendingPushToken = fromPrefs
        }
        return pendingPushToken
    }

    /** 設定を読み、PERSISTENT なら接続、PUSH なら待機する。 */
    private fun ensureClient() {
        val cfg = BridgeConfig.load(this)
        restorePushToken()
        if (cfg.relayUrl.isBlank()) {
            CallHub.updateStatus("未設定: relay URL を入力してください")
            updateServiceNote()
            return
        }
        CallHub.micGain = cfg.micGain
        if (client == null) {
            client = RelayClient(
                relayUrl = cfg.relayUrl,
                accessClientId = cfg.accessClientId,
                accessClientSecret = cfg.accessClientSecret,
                devToken = cfg.devToken,
                deviceId = cfg.deviceId,
                listener = this
            )
        }
        if (cfg.mode == BridgeMode.PERSISTENT) {
            cancelPushDisconnect()
            CallHub.updateStatus("接続中… ${cfg.relayUrl}")
            updateServiceNote()
            connectFresh()
        } else {
            CallHub.updateStatus("待機中 (PUSH モード)")
            updateServiceNote()
            // 未登録の FCM トークンを保持しているときだけ接続して register_push を
            // 済ませる (hello 後は idle 猶予で自動切断)。本プロセスで登録済みの
            // トークンしか無い場合は接続しない。
            if (restorePushToken() != null) {
                ensureConnected()
            } else if (BuildConfig.FLAVOR == "gms") {
                // トークンは GmsApplication が取得する (取得でき次第 setPushToken が呼ばれ、
                // そこで接続して register_push を送る)。
                Log.w(TAG, "FCM トークン未取得 (register_push は取得後に送る)")
            }
        }
    }

    /** PUSH モードのオンデマンド接続 (発信・FCM 起床用)。 */
    private fun ensureConnected(): Boolean {
        val cfg = BridgeConfig.load(this)
        restorePushToken()
        if (cfg.relayUrl.isBlank()) {
            CallHub.updateStatus("未設定: relay URL を入力してください")
            return false
        }
        if (client == null) {
            client = RelayClient(
                relayUrl = cfg.relayUrl,
                accessClientId = cfg.accessClientId,
                accessClientSecret = cfg.accessClientSecret,
                devToken = cfg.devToken,
                deviceId = cfg.deviceId,
                listener = this
            )
        }
        cancelPushDisconnect()
        if (client?.isConnected() != true) {
            CallHub.updateStatus("接続中… ${cfg.relayUrl}")
            connectFresh()
        }
        return true
    }

    /**
     * 新規接続の確立。未接続のときだけ [sipAccountSent] をリセットし、
     * 接続ごとの最初の hello で `sip_account` が 1 回だけ送られるようにする。
     * 手動切断 (PUSH 待機) では onDisconnected が発火しないため、ここでの
     * リセットが無いと再接続時に sip_account が送られない。
     */
    private fun connectFresh() {
        if (client?.isConnected() != true) sipAccountSent = false
        client?.connect()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACT_ANSWER -> answerFromAnywhere()
            ACT_REJECT -> rejectFromAnywhere()
            ACT_HANGUP -> hangupFromAnywhere()
            ACT_DIAL -> dialFromAnywhere(intent.getStringExtra(EXTRA_TO).orEmpty())
            ACT_CONNECT, ACT_WAKE_INCOMING -> ensureConnected()
            ACT_REREGISTER_PUSH -> {
                // PUSH モードのときだけ、登録済みトークンを捨てて接続し直す (register_push 再送)。
                // PERSISTENT は何もしない。再登録のためだけの接続は既存の idle 猶予で自動切断される。
                if (BridgeConfig.load(this).mode == BridgeMode.PUSH) {
                    registeredPushToken = null
                    restorePushToken()
                    ensureConnected()
                }
            }
            ACT_UI_SHOWN -> onCallUiShown()
            ACT_UI_HIDDEN -> onCallUiHidden()
            ACT_DIAL_TELECOM -> {
                val to = intent.getStringExtra(EXTRA_TO).orEmpty()
                // 進行中の呼があるときはティアを上書きしない。遅れて届いた
                // Connection はその場で切って何もしない (currentTier は触らない)。
                // 宛先が空の要求も同じく捨てる。
                if (to.trim().isEmpty() || CallHub.state != CallHub.State.IDLE) {
                    telecomPendingIncoming = false
                    telecomPendingTo = null
                    TelecomCallRegistry.setDisconnected(DisconnectCause.ERROR)
                    TelecomCallRegistry.clear()
                } else {
                    telecomPendingIncoming = false
                    telecomPendingTo = null
                    // 標準ダイヤラー起点の発信は dialFromAnywhere を通らないため、
                    // currentTier がまだ LEGACY のまま。ここで確定させないと
                    // 自前の通話画面が二重に出て、音声経路も Telecom と取り合いになる。
                    val tier =
                        TelecomTierManager.tierFromName(intent.getStringExtra(EXTRA_TELECOM_TIER))
                    currentTier = tier
                    CallHub.tier = tier
                    dialViaRelay(to, showUi = false)
                }
            }
            ACT_SHOW_INCOMING_UI -> presentIncomingLegacy()
            ACT_STOP -> {
                stopSelf()
                return START_NOT_STICKY
            }
            null -> {
                // 通常起動: PERSISTENT なら接続を確保する
                if (client == null) ensureClient()
                else if (BridgeConfig.load(this).mode == BridgeMode.PERSISTENT) ensureConnected()
                // PUSH: サービスが生きている状態で未登録のトークンが届いた場合
                // (GmsApplication の取得完了がサービス起動より遅れたとき) も登録のため接続する。
                else if (restorePushToken() != null) ensureConnected()
            }
        }
        return START_STICKY
    }

    override fun onDestroy() {
        running = false
        mainHandler.removeCallbacksAndMessages(null)
        runCatching { CallHub.rtp?.stop() }
        CallHub.rtp = null
        client?.shutdown()
        client = null
        overlay?.hide()
        runCatching { wifiLock?.release() }
        runCatching { wakeLock?.release() }
        TelecomCallRegistry.clear()
        resetCallState()
        super.onDestroy()
    }

    /** 常駐通知を現在の状態に合わせて更新する (文言は UI-DESIGN §2.1)。 */
    private fun updateServiceNote() {
        val cfg = BridgeConfig.load(this)
        val pushIdle = cfg.mode == BridgeMode.PUSH &&
            client?.isConnected() != true && CallHub.state == CallHub.State.IDLE
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as android.app.NotificationManager
        runCatching {
            nm.notify(
                NotificationHelper.ID_SERVICE,
                NotificationHelper.serviceNotification(
                    this, pushIdle, CallHub.extension, cfg.serviceNotificationQuiet
                )
            )
        }
    }

    /**
     * §6.5「常駐通知を隠す」の切り替えを反映する。`startForeground` を出し直すだけで、
     * Service の再起動はしない (チャンネルは通常用と静音用の 2 つを持ち、出し直しで切替)。
     */
    fun refreshServiceNotification() {
        if (!running) return
        updateServiceNote()
    }

    /** 常駐通知の静音フラグ (通話中ピル代替の通知にも適用)。 */
    private fun serviceQuiet(): Boolean = BridgeConfig.load(this).serviceNotificationQuiet

    // ---- RelayClient.Listener (OkHttp スレッドで呼ばれる) ----

    override fun onHello(v: RelayProtocol.Hello) {
        // hello.account を使う (無ければ extension。RelayProtocol.parseRelayMessage で吸収済み)。
        CallHub.extension = v.account
        CallHub.relayVersion = v.relayVersion
        CallHub.registered = v.registered
        // 接続ごとの最初の hello でのみ、設定済みなら sip_account を送る。
        // relay は受理後に改めて hello を送るが、その 2 回目では送らない。
        if (!sipAccountSent) {
            sipAccountSent = true
            val cfg = BridgeConfig.load(this)
            if (cfg.sipUser.isNotBlank()) {
                Log.i(TAG, "send sip_account user=${cfg.sipUser}")
                client?.sendSipAccount(cfg.sipUser, cfg.sipPassword, cfg.sipDisplay)
            }
        }
        // §6.2 到達性の記録: hello 到達 = relay 到達。
        PushHealth.markRelayOk(this)
        // 保留トークン (T5) があれば登録する
        var tokenRegistered = false
        pendingPushToken?.let { token ->
            pendingPushToken = null
            client?.registerPush("fcm", token)
            registeredPushToken = token
            tokenRegistered = true
            // §6.2: register_push を送って hello まで到達した = 登録完了。
            PushHealth.markPushRegistered(this)
        }
        if (v.call == null) {
            pendingDial?.let { dest ->
                // 未接続で発信要求 → 接続できたのでここで dial を送る
                pendingDial = null
                client?.dial(dest)
                return
            }
            if (CallHub.state == CallHub.State.IDLE) {
                if (tokenRegistered) schedulePushDisconnect()  // 登録済みなら PUSH は切断猶予へ
                val cfg = BridgeConfig.load(this)
                val note = when {
                    // hello.account が空で設定も空 → SIP アカウント未設定
                    v.account.isBlank() && cfg.sipUser.isBlank() ->
                        "SIP アカウント未設定: 設定タブで内線番号を入力してください"
                    v.registered -> "登録OK: ${v.account}"
                    else -> "未登録 (${v.relayVersion})"
                }
                CallHub.updateStatus(note)
                updateServiceNote()
            } else if (CallHub.outgoing && CallHub.callId.isBlank()) {
                // **発信直後で relay がまだ呼を作っていないだけ**。畳んではいけない。
                //
                // relay は sip_account を受理したあと hello をもう一度送る。発信は 1 通目の
                // hello で送る (pendingDial) ため、2 通目が「通話なし」で届くレースがある。
                // ここで畳むと、発信側だけ即「通話終了」になり、SIP の呼は生きたままなので
                // 着信側は鳴り続ける (ティア A では OS の通話画面ごと消える)。
                //
                // 判定は callId で行う: relay が把握している呼には必ず callId があり、
                // それは ringing/answered で入る。callId が空の発信は「relay が知らない呼」
                // ではなく「これから作られる呼」。失敗した場合は relay が ended/error を
                // 返すので、ここで畳まなくても取り残されない。
                Log.i(TAG, "hello: 発信直後 (callId 未確定) のため畳まない")
            } else {
                // relay 側に通話が無いのに表示中 → 他端末応答・タイムアウト。履歴を書いて表示を畳む。
                finalizeActiveHistory()
                if (CallHub.state == CallHub.State.RINGING && !CallHub.outgoing && CallHub.session != null) {
                    writeMissedOnce(CallHub.callId, CallHub.from, CallHub.display, incomingStartedAt)
                } else {
                    markHistoryDone(CallHub.callId)
                }
                stopCallMedia()
                restoreAudioRoute()
                resetCallState()
                hideInCallPill()
                overlay?.hide()
                runCatching {
                    (getSystemService(Context.NOTIFICATION_SERVICE) as android.app.NotificationManager)
                        .cancel(NotificationHelper.ID_INCOMING)
                }
                CallHub.updateStatus("通話なし (他端末応答/終了)")
                updateServiceNote()
                schedulePushDisconnect()
            }
        } else {
            val c = v.call
            when (c.state) {
                "ringing" -> {
                    if (CallHub.state == CallHub.State.IDLE) {
                        if (c.direction == "out") presentOutgoingRinging(c.callId, c.from)
                        else presentIncoming(c.callId, c.from, c.display, c.pt)
                    }
                }
                "active" -> {
                    // 再接続後の通話継続 (10 秒 resume)。メディアを作り直す。
                    if (CallHub.state == CallHub.State.IDLE) {
                        CallHub.callId = c.callId
                        CallHub.from = c.from
                        CallHub.display = resolveName(c.display, c.from)
                        CallHub.state = CallHub.State.IN_CALL
                        CallHub.callStartedAt = if (c.startedAt > 0) c.startedAt else System.currentTimeMillis()
                        CallHub.session = RelayCallSession(c.callId, c.from, CallHub.display, c.pt, canAnswer = false)
                        // 履歴: 継続した通話も IN として記録する (終了時に通話時間を更新)
                        activeHistoryId = runCatching {
                            historyStore.add(
                                HistoryDirection.IN, c.from, CallHub.display.ifBlank { c.from },
                                System.currentTimeMillis(), 0L
                            ).id
                        }.getOrNull()
                        applyInCallAudioRoute()
                        startCallMedia(c.pt)
                        CallHub.notifyChanged()
                        CallHub.updateStatus("通話継続中")
                    }
                }
            }
        }
    }

    override fun onRegistration(ok: Boolean, detail: String) {
        CallHub.registered = ok
        val note = if (ok) "登録OK: ${CallHub.extension}" else "未登録 ($detail)"
        CallHub.updateStatus(note)
        updateServiceNote()
    }

    override fun onIncoming(v: RelayProtocol.Incoming) {
        if (CallHub.state != CallHub.State.IDLE) {
            Log.i(TAG, "incoming while busy, ignore: ${v.callId}")
            return
        }
        presentIncoming(v.callId, v.from, v.display, v.pt)
    }

    override fun onRinging(v: RelayProtocol.Ringing) {
        // 発信応答 (v1.1)。PUSH 発信などで outgoing 中のみ有効。
        if (CallHub.outgoing && (CallHub.state == CallHub.State.IDLE || CallHub.state == CallHub.State.RINGING)) {
            CallHub.callId = v.callId
            CallHub.state = CallHub.State.RINGING
            CallHub.earlyMedia = v.early
            CallHub.session = RelayCallSession(v.callId, CallHub.from, CallHub.display, 0, canAnswer = false)
            CallHub.notifyChanged()
            CallHub.updateStatus(if (v.early) "呼出中 (早期メディアあり)" else "呼出中…")
            TelecomCallRegistry.setDialing()
        }
    }

    override fun onAnswered(v: RelayProtocol.Answered) {
        if (CallHub.callId.isNotEmpty() && v.callId != CallHub.callId) return
        val wasOutgoing = CallHub.outgoing
        CallHub.callId = v.callId
        CallHub.state = CallHub.State.IN_CALL
        CallHub.earlyMedia = false
        CallHub.callStartedAt = System.currentTimeMillis()
        if (CallHub.session == null) {
            CallHub.session = RelayCallSession(v.callId, CallHub.from, CallHub.display, v.pt, canAnswer = false)
        }
        startCallMedia(v.pt)
        CallHub.notifyChanged()
        CallHub.updateStatus("通話中")
        TelecomCallRegistry.setActive()
        overlay?.hide()
        runCatching {
            (getSystemService(Context.NOTIFICATION_SERVICE) as android.app.NotificationManager)
                .cancel(NotificationHelper.ID_INCOMING)
        }
        // ティア A では OS 標準画面が出ているため自前 UI は出さない。
        if (suppressOwnUi()) return
        // 通話画面を前面へ。ただし発信を自分で縮小している間は割り込まず、
        // ピル (または代替通知) を通話中表示に切り替えるだけにする。
        if (wasOutgoing && !uiVisible) {
            showInCallPill()
        } else {
            startActivity(CallOverlayManager.callActivityIntent(this))
        }
    }

    override fun onEnded(v: RelayProtocol.Ended) {
        if (CallHub.callId.isNotEmpty() && v.callId != CallHub.callId) return
        // ---- P3 履歴書き込み ----
        val activeId = activeHistoryId
        activeHistoryId = null
        if (activeId != null) {
            // 応答済み (IN / OUT): 通話時間を確定する。未応答の発信は duration 0 のまま。
            val dur = if (CallHub.state == CallHub.State.IN_CALL && CallHub.callStartedAt > 0) {
                ((System.currentTimeMillis() - CallHub.callStartedAt) / 1000).coerceAtLeast(0L)
            } else 0L
            runCatching { historyStore.updateDuration(activeId, dur) }
            markHistoryDone(v.callId)
        } else if (CallHub.state == CallHub.State.RINGING && !CallHub.outgoing && CallHub.session != null) {
            // 未応答の着信 (timeout / cancel / answered_elsewhere / reject) → MISSED
            writeMissedOnce(v.callId, CallHub.from, CallHub.display, incomingStartedAt)
        } else {
            markHistoryDone(v.callId)
        }
        stopCallMedia()
        restoreAudioRoute()
        TelecomCallRegistry.setDisconnected(TelecomCompat.disconnectCauseFor(v.reason))
        TelecomCallRegistry.clear()
        val msg = when (v.reason) {
            "answered_elsewhere" -> "他端末で応答"
            "bye" -> "通話終了"
            "cancel" -> "相手がキャンセル"
            "reject" -> "拒否 (${v.code})"
            "timeout" -> "無応答タイムアウト (${v.code})"
            else -> "終了 (${v.reason} ${v.code})"
        }
        resetCallState()
        hideInCallPill()
        overlay?.hide()
        (getSystemService(Context.NOTIFICATION_SERVICE) as android.app.NotificationManager)
            .cancel(NotificationHelper.ID_INCOMING)
        CallHub.updateStatus(msg)
        updateServiceNote()
        schedulePushDisconnect()
    }

    override fun onError(code: String, message: String) {
        // SIP アカウント系エラーは設定画面の状態に日本語で反映する。
        val note = when (code) {
            "no_account" ->
                "SIP アカウント未設定のため発着信できません。設定タブで内線番号を入力してください"
            "account_password_mismatch" ->
                "内線のパスワードが relay の登録済み値と一致しません"
            "account_failed" ->
                "SIP アカウントの登録に失敗しました: $message"
            else -> "エラー ($code): $message"
        }
        CallHub.updateStatus(note)
    }

    override fun onDisconnected() {
        // 接続ごとの sip_account 送信フラグをリセットする。
        sipAccountSent = false
        CallHub.registered = false
        CallHub.updateStatus("再接続中…")
        updateServiceNote()
    }

    override fun onRtpReceived(rtp: ByteArray) {
        CallHub.rtp?.onRtpReceived(rtp)
    }

    // ---- 着信提示 (EchoSIP 流用: 全画面優先・2.5 秒後フォールバック) ----

    private fun presentIncoming(callId: String, from: String, display: String, pt: Int) {
        CallHub.callId = callId
        CallHub.from = from
        // P3: 表示名解決 (relay の display → 連絡先 → 番号)。CallActivity と通知に名前が出る。
        CallHub.display = resolveName(display, from)
        CallHub.outgoing = false
        CallHub.earlyMedia = false
        CallHub.state = CallHub.State.RINGING
        incomingStartedAt = System.currentTimeMillis()
        CallHub.session = RelayCallSession(callId, from, CallHub.display, pt, canAnswer = true)
        CallHub.notifyChanged()
        Log.i(TAG, "incoming from $from ($callId)")
        uiVisible = false
        val tier = TelecomTierManager.decide(this, incoming = true)
        if (tier != CallTier.LEGACY) {
            // SELF_MANAGED は直前にも許可を確認し、false ならティア落ち。
            if (tier == CallTier.SELF_MANAGED &&
                !TelecomCompat.isSelfManagedUsable(this, incoming = true)
            ) {
                degradeIncoming(tier, "self-managed not permitted")
                return
            }
            currentTier = tier
            CallHub.tier = tier
            val accountId = if (tier == CallTier.MANAGED) TelecomCompat.ACCOUNT_ID_MANAGED
            else TelecomCompat.ACCOUNT_ID_SELF
            val extras = Bundle().apply {
                putParcelable(
                    TelecomManager.EXTRA_INCOMING_CALL_ADDRESS,
                    Uri.fromParts("tel", from, null)
                )
                putParcelable(
                    TelecomManager.EXTRA_PHONE_ACCOUNT_HANDLE,
                    TelecomCompat.handle(this@BridgeService, accountId)
                )
                putString(TelecomCompat.EXTRA_CALL_ID, callId)
            }
            telecomPendingIncoming = true
            telecomPendingTo = null
            armTelecomWatchdog(
                onTimeout = { degradeIncoming(tier, "no onCreateIncomingConnection") },
                onFailed = { degradeIncoming(tier, "onCreateIncomingConnectionFailed") },
            )
            // relay には何も変化を返さない。180 を返したままで 25 秒タイムアウトの内側。
            val ok = TelecomCompat.addNewIncomingCall(this, accountId, extras)
            Log.i(TAG, "incoming via telecom tier=$tier ok=$ok")
            if (!ok) degradeIncoming(tier, "addNewIncomingCall false")
            return
        }
        presentIncomingLegacy()
    }

    /**
     * 現行 `presentIncoming` の UI 部分を切り出したもの (ティア C の経路)。
     * ティア A/B からのフォールバックと self-managed の `onShowIncomingCallUi` もここに来る。
     */
    private fun presentIncomingLegacy() {
        // 既に片付いた呼 (終了・応答済み) には UI を出さない。
        if (CallHub.state != CallHub.State.RINGING || CallHub.outgoing) return
        var activityLaunched = false
        runCatching {
            startActivity(CallOverlayManager.callActivityIntent(this))
            activityLaunched = true
        }
        Log.i(TAG, "incoming activity launched=$activityLaunched")
        mainHandler.postDelayed({
            if (CallHub.state == CallHub.State.RINGING && !uiVisible) {
                Log.i(TAG, "fullscreen not visible, fallback to notification+bubble")
                showIncomingNotification()
                showOverlayBubble()
            }
        }, 2500)
    }

    /** ティアが MANAGED の呼では自前 UI を出さない (OS 標準の通話中通知があるため)。 */
    private fun suppressOwnUi(): Boolean = currentTier == CallTier.MANAGED

    /** `CallHub.resetCall()` + ティアを LEGACY に戻す + 待ち状態の片付け。
     *  WSS 切断後の再 hello で通話畳み込みに来たときも OS の通話画面を残さないよう、
     *  ここで Telecom の呼を切る。`onEnded`/`reject`/`hangup` 済みなら connection は
     *  null のため二重実行は no-op。 */
    private fun resetCallState() {
        TelecomCallRegistry.setDisconnected(DisconnectCause.UNKNOWN)
        TelecomCallRegistry.clear()
        cancelTelecomWatchdog()
        telecomPendingIncoming = false
        telecomPendingTo = null
        currentTier = CallTier.LEGACY
        CallHub.tier = CallTier.LEGACY
        CallHub.resetCall()
    }

    /**
     * Telecom ウォッチドッグ。`onCreate...Connection` / `...Failed` のどちらが来ても解除する。
     * `Failed` なら即フォールバックする (同じプロセス内のコールバック経由)。
     */
    private fun armTelecomWatchdog(onTimeout: () -> Unit, onFailed: () -> Unit) {
        cancelTelecomWatchdog()
        TelecomCallRegistry.onCreated = {
            mainHandler.post {
                telecomPendingIncoming = false
                telecomPendingTo = null
                cancelTelecomWatchdog()
            }
        }
        TelecomCallRegistry.onCreateFailed = { mainHandler.post { onFailed() } }
        val r = Runnable {
            telecomWatchdog = null
            onTimeout()
        }
        telecomWatchdog = r
        mainHandler.postDelayed(r, TELECOM_WATCHDOG_MS)
    }

    private fun cancelTelecomWatchdog() {
        telecomWatchdog?.let { mainHandler.removeCallbacks(it) }
        telecomWatchdog = null
        TelecomCallRegistry.onCreated = null
        TelecomCallRegistry.onCreateFailed = null
    }

    /** 着信のティア落ち: 学習に記録し、ティア C で自前 UI を出す。 */
    private fun degradeIncoming(tier: CallTier, reason: String) {
        if (CallHub.state != CallHub.State.RINGING || CallHub.outgoing) return
        TelecomTierManager.recordDegrade(this, tier, CallTier.LEGACY, reason)
        cancelTelecomWatchdog()
        telecomPendingIncoming = false
        telecomPendingTo = null
        currentTier = CallTier.LEGACY
        CallHub.tier = CallTier.LEGACY
        TelecomCallRegistry.clear()
        presentIncomingLegacy()
    }

    /** 発信のティア落ち: 学習に記録し、ティア C で発信し直す。 */
    private fun degradeOutgoing(tier: CallTier, to: String, reason: String) {
        // 既に片付いた試行 (成功・終了済み) なら何もしない。
        if (telecomPendingTo != to) return
        telecomPendingTo = null
        telecomPendingIncoming = false
        TelecomTierManager.recordDegrade(this, tier, CallTier.LEGACY, reason)
        cancelTelecomWatchdog()
        currentTier = CallTier.LEGACY
        CallHub.tier = CallTier.LEGACY
        TelecomCallRegistry.clear()
        if (CallHub.state != CallHub.State.IDLE) return
        dialViaRelay(to, showUi = true)
    }

    private fun presentOutgoingRinging(callId: String, to: String) {
        CallHub.callId = callId
        CallHub.outgoing = true
        CallHub.state = CallHub.State.RINGING
        CallHub.session = RelayCallSession(callId, to, "", 0, canAnswer = false)
        CallHub.notifyChanged()
        CallHub.updateStatus("呼出中… $to")
        startActivity(CallOverlayManager.callActivityIntent(this))
    }

    // ---- P3 履歴・連絡先 ----

    /**
     * 表示名解決: relay の display → [ContactStore.lookup] → [DeviceContacts.lookup] → 番号 の順。
     * 解決結果を CallHub.display にも反映することで CallActivity と通知に名前が出る。
     * 端末の連絡先解決は権限があるときだけ行う。呼び出し元が UI スレッドの場合は
     * ブロックしないよう番号を返し、バックグラウンド解決後に表示を更新する。
     */
    private fun resolveName(relayDisplay: String, number: String): String {
        if (relayDisplay.isNotBlank()) return relayDisplay
        if (number.isBlank()) return ""
        runCatching { contactStore.lookup(number) }.getOrNull()?.let { return it }
        if (!DeviceContacts.hasPermission(this)) return number
        if (Looper.myLooper() == Looper.getMainLooper()) {
            // UI スレッド (発信時など): 後から更新する
            Thread {
                val name = runCatching { DeviceContacts.lookup(this, number) }.getOrNull()
                if (!name.isNullOrBlank() && CallHub.from == number && CallHub.display != name) {
                    val cur = CallHub.display
                    if (cur.isBlank() || cur == number) {
                        CallHub.display = name
                        CallHub.notifyChanged()
                    }
                }
            }.start()
            return number
        }
        return runCatching { DeviceContacts.lookup(this, number) }.getOrNull() ?: number
    }

    /**
     * 応答済みエントリの通話時間を確定する (ローカル切断時用)。
     * onEnded が後から届いても activeHistoryId は null のため二重更新しない。
     */
    private fun finalizeActiveHistory() {
        val id = activeHistoryId ?: return
        activeHistoryId = null
        if (CallHub.state == CallHub.State.IN_CALL && CallHub.callStartedAt > 0) {
            val dur = ((System.currentTimeMillis() - CallHub.callStartedAt) / 1000).coerceAtLeast(0L)
            runCatching { historyStore.updateDuration(id, dur) }
        }
    }

    /** callId を履歴書き込み済みにする。MISSED の二重書き込み防止用。新規なら true。 */
    @Synchronized
    private fun markHistoryDone(callId: String): Boolean {
        if (callId.isBlank()) return false
        if (historyDoneFor.size > 500) historyDoneFor.clear()
        return historyDoneFor.add(callId)
    }

    /** MISSED を 1 回だけ書く (ローカル拒否と ended の両経路から呼ばれる)。 */
    private fun writeMissedOnce(callId: String, number: String, name: String, startedAt: Long) {
        if (!markHistoryDone(callId)) return
        val at = if (startedAt > 0) startedAt else System.currentTimeMillis()
        runCatching {
            historyStore.add(HistoryDirection.MISSED, number, name.ifBlank { number }, at, 0L)
        }
    }

    // ---- 通話メディア ----

    private fun startCallMedia(pt: Int) {
        stopCallMedia()
        val engine = RtpEngine(
            payloadType = if (pt == 8) 8 else 0,
            micGain = CallHub.micGain
        )
        engine.sink = RtpEngine.MediaSink { rtp -> client?.sendRtp(rtp) }
        CallHub.rtp = engine
        runCatching { engine.start() }
    }

    private fun stopCallMedia() {
        runCatching { CallHub.rtp?.stop() }
        CallHub.rtp = null
    }

    // ---- 全画面UIの表示状態に合わせた排他制御 ----

    private fun incomingNm() =
        getSystemService(Context.NOTIFICATION_SERVICE) as android.app.NotificationManager

    private fun showIncomingNotification() {
        // ティア A では OS 標準の着信表示があるため通知は出さない。
        if (suppressOwnUi()) return
        runCatching {
            incomingNm().notify(
                NotificationHelper.ID_INCOMING,
                NotificationHelper.incomingNotification(this, CallHub.from, CallHub.display)
            )
        }
    }

    private fun showOverlayBubble() {
        // ティア A では OS 標準画面が出ているためバブルは出さない。
        if (suppressOwnUi()) return
        if (!BridgeConfig.load(this).overlayEnabled) return
        if (overlay?.canDraw() != true) return
        overlay?.show(CallHub.from, { answerFromAnywhere() }, { rejectFromAnywhere() })
    }

    private fun onCallUiShown() {
        uiVisible = true
        runCatching { incomingNm().cancel(NotificationHelper.ID_INCOMING) }
        // 通話画面への復帰: ピル・代替通知を畳んで通常の常駐通知に戻す
        hideInCallPill()
        overlay?.hide()
    }

    private fun onCallUiHidden() {
        uiVisible = false
        when {
            // 着信呼出中: 応答/拒否つきバブル + 着信通知
            CallHub.state == CallHub.State.RINGING && !CallHub.outgoing -> {
                showIncomingNotification()
                showOverlayBubble()
            }
            // 発信呼出中・通話中に画面から離れた (縮小・ホーム): ピルを出す。
            // 発信中に着信バブル (応答/拒否) を出してはいけない。
            CallHub.state == CallHub.State.RINGING || CallHub.state == CallHub.State.IN_CALL ->
                showInCallPill()
        }
    }

    /**
     * 通話中ピルを表示する (UI-DESIGN §3.2)。**発信呼出中も対象**
     * (この間は「📞 呼出中」表示)。
     * オーバーレイ権限が無い場合は常駐通知を「通話中 mm:ss — タップで戻る」
     * (呼出中は「<番号> を呼び出し中」) に更新して代替する。
     */
    fun showInCallPill() {
        // ティア A では OS 標準の通話中通知があるためピルは出さない。
        if (suppressOwnUi()) return
        val ringingOut = CallHub.state == CallHub.State.RINGING && CallHub.outgoing
        if (CallHub.state != CallHub.State.IN_CALL && !ringingOut) return
        hideInCallPill()
        val canOverlay = BridgeConfig.load(this).overlayEnabled && overlay?.canDraw() == true
        if (canOverlay) {
            // 呼出中は startedAt=0 を渡す (経過時間ではなく「呼出中」表示)
            overlay?.showInCallPill(if (ringingOut) 0L else CallHub.callStartedAt) {
                startActivity(CallOverlayManager.callActivityIntent(this))
            }
        } else if (ringingOut) {
            runCatching {
                (getSystemService(Context.NOTIFICATION_SERVICE) as android.app.NotificationManager)
                    .notify(
                        NotificationHelper.ID_SERVICE,
                        NotificationHelper.outgoingNotification(this, CallHub.from, serviceQuiet())
                    )
            }
        } else {
            // 代替: 常駐通知を通話中表示にし、1 秒ごとに経過時間を更新する
            val tick = object : Runnable {
                override fun run() {
                    if (CallHub.state != CallHub.State.IN_CALL) return
                    val s = ((System.currentTimeMillis() - CallHub.callStartedAt) / 1000).toInt().coerceAtLeast(0)
                    runCatching {
                        (getSystemService(Context.NOTIFICATION_SERVICE) as android.app.NotificationManager)
                            .notify(NotificationHelper.ID_SERVICE, NotificationHelper.inCallNotification(this@BridgeService, s, serviceQuiet()))
                    }
                    inCallNotifyTick = this
                    mainHandler.postDelayed(this, 1000)
                }
            }
            inCallNotifyTick = tick
            mainHandler.post(tick)
        }
    }

    /** 通話中ピル・代替通知を畳み、通常の常駐通知に戻す。 */
    fun hideInCallPill() {
        inCallNotifyTick?.let { mainHandler.removeCallbacks(it) }
        inCallNotifyTick = null
        overlay?.hideInCallPill()
        updateServiceNote()
    }

    /**
     * 音声経路を通話用にする (answer/dial 時)。
     * MODE_IN_COMMUNICATION + スピーカーは設定 (speakerOnAnswer。既定 OFF) に従う。
     * ただし受話口を持たない端末 (Echo Show など) では設定に関わらずスピーカーへ出す
     * (受話口へ回すと無音になるため)。
     * 変更前のスピーカー状態を覚え、通話終了で元に戻す。
     */
    private fun applyInCallAudioRoute() {
        // ティア A/B では Telecom がオーディオフォーカスと MODE_IN_COMMUNICATION を
        // 管理するため、こちらでは経路を触らない (終了時の restore は呼ぶ)。
        if (currentTier != CallTier.LEGACY) return
        val am = audioManager ?: return
        if (!audioRouteSet) {
            prevSpeakerOn = AudioRoute.isSpeakerOn(am)
            audioRouteSet = true
        }
        am.mode = AudioManager.MODE_IN_COMMUNICATION
        val cfg = BridgeConfig.load(this)
        AudioRoute.setSpeaker(am, cfg.speakerOnAnswer || !AudioRoute.hasEarpiece(am))
    }

    /** 通話終了時に音声経路を元に戻す (MODE_NORMAL + スピーカー復元)。 */
    private fun restoreAudioRoute() {
        if (!audioRouteSet) return
        audioRouteSet = false
        val am = audioManager ?: return
        runCatching {
            AudioRoute.clear(am)
            am.mode = AudioManager.MODE_NORMAL
            @Suppress("DEPRECATION")
            am.isSpeakerphoneOn = prevSpeakerOn
        }
    }

    // ---- 操作 (通知/オーバーレイ/Activityのどこからでも) ----

    fun answerFromAnywhere() {
        val s = CallHub.session ?: return
        // P3: 着信応答 → IN エントリを作成する (通話時間は終了時に更新)
        if (CallHub.state == CallHub.State.RINGING && !CallHub.outgoing && activeHistoryId == null) {
            activeHistoryId = runCatching {
                historyStore.add(
                    HistoryDirection.IN, CallHub.from,
                    CallHub.display.ifBlank { CallHub.from },
                    System.currentTimeMillis(), 0L
                ).id
            }.getOrNull()
        }
        applyInCallAudioRoute()
        s.answer()
        // メディアは relay の `answered` 受信 (onAnswered) で開始する (二重起動防止)
        CallHub.state = CallHub.State.IN_CALL
        CallHub.earlyMedia = false
        CallHub.callStartedAt = System.currentTimeMillis()
        CallHub.notifyChanged()
        overlay?.hide()
        runCatching { incomingNm().cancel(NotificationHelper.ID_INCOMING) }
        // ティア A では OS 標準画面が出ているため自前 UI は出さない。
        if (!suppressOwnUi()) {
            startActivity(CallOverlayManager.callActivityIntent(this))
        }
    }

    fun rejectFromAnywhere() {
        // P3: 応答済みなら通話時間を確定する。未応答の着信拒否は MISSED
        // (ended が後から届いても writeMissedOnce の dedupe で二重書き込みしない)。
        finalizeActiveHistory()
        if (CallHub.state == CallHub.State.RINGING && !CallHub.outgoing && CallHub.session != null) {
            writeMissedOnce(CallHub.callId, CallHub.from, CallHub.display, incomingStartedAt)
        } else {
            markHistoryDone(CallHub.callId)
        }
        TelecomCallRegistry.setDisconnected(DisconnectCause.REJECTED)
        TelecomCallRegistry.clear()
        CallHub.session?.reject()
        stopCallMedia()
        restoreAudioRoute()
        val hadCall = CallHub.session != null
        resetCallState()
        if (!hadCall) CallHub.updateStatus("拒否")
        hideInCallPill()
        overlay?.hide()
        runCatching { incomingNm().cancel(NotificationHelper.ID_INCOMING) }
        schedulePushDisconnect()
    }

    fun hangupFromAnywhere() {
        // P3: reject と同じく履歴を確定する (呼出中の切断・通話終了の両方)。
        finalizeActiveHistory()
        if (CallHub.state == CallHub.State.RINGING && !CallHub.outgoing && CallHub.session != null) {
            writeMissedOnce(CallHub.callId, CallHub.from, CallHub.display, incomingStartedAt)
        } else {
            // finalizeActiveHistory() で activeHistoryId はクリア済み。
            // OUT 未応答は dial 時のエントリ (duration 0) が残る。
            markHistoryDone(CallHub.callId)
        }
        TelecomCallRegistry.setDisconnected(DisconnectCause.LOCAL)
        TelecomCallRegistry.clear()
        CallHub.session?.hangup()
        stopCallMedia()
        restoreAudioRoute()
        pendingDial = null
        val hadCall = CallHub.session != null
        resetCallState()
        if (!hadCall) CallHub.updateStatus("終了")
        hideInCallPill()
        overlay?.hide()
        schedulePushDisconnect()
    }

    fun dialFromAnywhere(to: String) {
        val dest = to.trim()
        if (dest.isEmpty() || CallHub.state != CallHub.State.IDLE) return
        val tier = TelecomTierManager.decide(this, incoming = false)
        if (tier != CallTier.LEGACY) {
            // CALL_PHONE 未許可なら placeCall を試さずその呼だけ LEGACY に落とす
            // (例外を投げさせない)。学習には記録しない — telecomMaxTier を LEGACY に
            // 固定すると着信も含めて以後ティア A/B が選ばれなくなるため。
            if (!hasCallPhonePermission()) {
                Log.i(TAG, "CALL_PHONE denied, dial via relay (no degrade recorded)")
            } else {
                val accountId = if (tier == CallTier.MANAGED) TelecomCompat.ACCOUNT_ID_MANAGED
                else TelecomCompat.ACCOUNT_ID_SELF
                val extras = Bundle().apply {
                    putParcelable(
                        TelecomManager.EXTRA_PHONE_ACCOUNT_HANDLE,
                        TelecomCompat.handle(this@BridgeService, accountId)
                    )
                    putBundle(
                        TelecomManager.EXTRA_OUTGOING_CALL_EXTRAS,
                        Bundle().apply { putBoolean(TelecomCompat.EXTRA_FROM_APP, true) }
                    )
                }
                currentTier = tier
                CallHub.tier = tier
                telecomPendingIncoming = false
                telecomPendingTo = dest
                armTelecomWatchdog(
                    onTimeout = { degradeOutgoing(tier, dest, "no onCreateOutgoingConnection") },
                    onFailed = { degradeOutgoing(tier, dest, "onCreateOutgoingConnectionFailed") },
                )
                // 続きは onCreateOutgoingConnection からの ACT_DIAL_TELECOM。
                val ok = TelecomCompat.placeCall(this, dest, extras)
                Log.i(TAG, "outgoing via telecom tier=$tier ok=$ok")
                if (ok) return
                degradeOutgoing(tier, dest, "placeCall false")
                return
            }
        }
        dialViaRelay(dest, showUi = true)
    }

    private fun hasCallPhonePermission(): Boolean =
        checkSelfPermission(Manifest.permission.CALL_PHONE) == PackageManager.PERMISSION_GRANTED

    /**
     * 現行 `dialFromAnywhere` の relay 発信部分を切り出したもの。
     * 標準ダイヤラー起点 (`showUi=false`) では `CallActivity` を起動しない。
     * 未接続で発信要求されたときは `ensureConnected()` し、駄目なら
     * OS 画面を畳んで終わる (自前 UI 起点では従来どおり何もしない)。
     */
    fun dialViaRelay(to: String, showUi: Boolean = true) {
        val dest = to.trim()
        if (dest.isEmpty() || CallHub.state != CallHub.State.IDLE) return
        if (!ensureConnected()) {
            if (!showUi) {
                TelecomCallRegistry.setDisconnected(DisconnectCause.ERROR)
                TelecomCallRegistry.clear()
                currentTier = CallTier.LEGACY
                CallHub.tier = CallTier.LEGACY
            }
            return
        }
        applyInCallAudioRoute()
        CallHub.from = dest
        // P3: 発信先の名前解決 (連絡先一致で CallActivity に名前が出る) と OUT 記録。
        // 応答しなければ durationSec=0 のまま残る。発生時点の解決名を name に保存する。
        val dialName = runCatching { contactStore.lookup(dest) }.getOrNull().orEmpty()
        CallHub.display = dialName
        CallHub.outgoing = true
        CallHub.earlyMedia = false
        CallHub.state = CallHub.State.RINGING
        activeHistoryId = runCatching {
            historyStore.add(HistoryDirection.OUT, dest, dialName, System.currentTimeMillis(), 0L).id
        }.getOrNull()
        CallHub.notifyChanged()
        CallHub.updateStatus("発信中… $dest")
        if (client?.isConnected() == true) {
            client?.dial(dest)
        } else {
            // 接続確立後の hello 受信時 (onHello) に送る
            pendingDial = dest
        }
        // OS 標準画面が出ている発信 (標準ダイヤラー起点) では自前 UI は出さない。
        if (showUi) {
            startActivity(CallOverlayManager.callActivityIntent(this))
        }
    }

    /** T5 用: FCM トークンを保持し、接続中なら即登録する。
     *  本プロセスで登録済みのトークンと同じなら何もしない
     *  (gms 側は push 受信のたびに再配送するため)。 */
    fun setPushToken(token: String) {
        if (token == registeredPushToken) {
            Log.d(TAG, "FCM トークンは登録済みのため無視")
            return
        }
        pendingPushToken = token
        if (client?.isConnected() == true) {
            pendingPushToken = null
            client?.registerPush("fcm", token)
            registeredPushToken = token
            return
        }
        // サービス起動後にトークンが届いた (onNewToken / GmsApplication の取得完了)。
        // PUSH モードの待機中は接続していないため、register_push を送るためだけに
        // 一度接続する。以前はここで接続せず、UI を開くか着信するまで relay に
        // 登録されなかった (新規インストール直後に push が届かない不具合)。
        Log.i(TAG, "FCM トークン到着 (未接続)。register_push のため接続する")
        ensureConnected()
    }

    private fun schedulePushDisconnect() {
        if (BridgeConfig.load(this).mode != BridgeMode.PUSH) return
        if (CallHub.state != CallHub.State.IDLE) return
        cancelPushDisconnect()
        val r = Runnable {
            pushDisconnectRunnable = null
            if (BridgeConfig.load(this).mode == BridgeMode.PUSH && CallHub.state == CallHub.State.IDLE) {
                Log.i(TAG, "PUSH idle timeout, disconnect")
                client?.disconnect()
                CallHub.updateStatus("待機中 (PUSH モード)")
                updateServiceNote()
            }
        }
        pushDisconnectRunnable = r
        mainHandler.postDelayed(r, PUSH_IDLE_DISCONNECT_MS)
    }

    private fun cancelPushDisconnect() {
        pushDisconnectRunnable?.let { mainHandler.removeCallbacks(it) }
        pushDisconnectRunnable = null
    }

    /** relay 背後のセッション。SIP ではなく JSON 送信で動く。 */
    private inner class RelayCallSession(
        override val callId: String,
        override val from: String,
        override val display: String,
        override val pt: Int,
        private val canAnswer: Boolean
    ) : CallHub.CallSession {
        // 注意: 呼び出し元は UI スレッドの場合がある。WS 送信は非ブロッキング
        // (OkHttp がキューイング) のためそのまま呼んでよい。
        override fun answer() {
            if (canAnswer) client?.answer(callId)
        }
        override fun reject() {
            if (canAnswer) client?.reject(callId) else client?.hangup(callId)
        }
        override fun hangup() {
            // 通話中は BYE 相当、呼出中は CANCEL 相当。relay 側で使い分ける。
            client?.hangup(callId)
        }
    }
}

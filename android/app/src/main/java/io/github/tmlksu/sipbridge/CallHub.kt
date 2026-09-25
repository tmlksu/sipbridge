package io.github.tmlksu.sipbridge

/** Service⇔Activity間の通話状態共有 (常駐Serviceが真実の保持者)。EchoSIP の CallHub を継承。 */
object CallHub {
    enum class State { IDLE, RINGING, IN_CALL }

    /**
     * 通話セッション。EchoSIP の `SipEngine.IncomingSession` を置換する
     * インタフェース。SIP トランザクションではなく relay への JSON 送信で動く。
     */
    interface CallSession {
        val callId: String
        val from: String
        val display: String
        val pt: Int
        /** 着信応答 (relay が 200 OK)。発信側セッションでは何もしない。 */
        fun answer()
        fun reject()
        fun hangup()
    }

    @Volatile var state: State = State.IDLE
    /** 着信・発信共通の相手表示 (incoming.from / hello.call.from)。 */
    @Volatile var from: String = ""
    @Volatile var display: String = ""
    @Volatile var callId: String = ""
    @Volatile var status: String = "未起動"
    @Volatile var registered: Boolean = false
    /** relay が把握している内線番号 (hello.account。無ければ hello.extension)。 */
    @Volatile var extension: String = ""
    /** relay のバージョン (hello.relayVersion。設定画面フッター表示用)。 */
    @Volatile var relayVersion: String = ""
    /** 発信中か (dial 送信後 ringing/answered 待ち)。 */
    @Volatile var outgoing: Boolean = false
    /**
     * IN_CALL に入った時刻 (System.currentTimeMillis = epoch ms)。経過時間表示の唯一の基準。
     * 通知 (`setWhen`) はこの epoch 値をそのまま使い、`Chronometer` (elapsedRealtime 基準) には
     * [callStartedElapsedRealtime] で換算した値を渡す (基準の取り違え防止)。
     */
    @Volatile var callStartedAt: Long = 0L

    /**
     * [callStartedAt] を `SystemClock.elapsedRealtime()` 基準に換算する (`Chronometer.base` 用)。
     * 未開始 (0) なら現在時刻 (= 00:00) を返す。
     */
    fun callStartedElapsedRealtime(): Long {
        val nowElapsed = android.os.SystemClock.elapsedRealtime()
        val start = callStartedAt
        if (start <= 0L) return nowElapsed
        val sinceStart = (System.currentTimeMillis() - start).coerceAtLeast(0L)
        return nowElapsed - sinceStart
    }
    /** 発信呼出中、183 early media (相手側応答音あり) のとき true。 */
    @Volatile var earlyMedia: Boolean = false
    @Volatile var session: CallSession? = null
    @Volatile var rtp: RtpEngine? = null
    @Volatile var micGain: Float = 2.0f
    /** この呼のティア。呼ごとにセットアップ時点で確定し、通話中は変えない。
     *  ティア A のとき [CallActivity] は誤って開かれても `finish()` する。 */
    @Volatile var tier: CallTier = CallTier.LEGACY

    interface StateListener { fun onChanged() }
    private val listeners = mutableSetOf<StateListener>()

    @Synchronized fun addListener(l: StateListener) { listeners.add(l) }
    @Synchronized fun removeListener(l: StateListener) { listeners.remove(l) }
    @Synchronized fun notifyChanged() { listeners.toList().forEach { runCatching { it.onChanged() } } }

    @Synchronized fun updateStatus(s: String) { status = s; notifyChanged() }

    @Synchronized
    fun resetCall() {
        state = State.IDLE
        from = ""
        display = ""
        callId = ""
        outgoing = false
        callStartedAt = 0L
        earlyMedia = false
        session = null
        notifyChanged()
    }
}

package io.github.tmlksu.sipbridge

import org.json.JSONObject

/**
 * docs/PROTOCOL.md v1 の JSON 制御メッセージの encode/decode。
 * org.json のみ使用 (外部 JSON ライブラリ不要)。
 * Android 依存なし (JVM 単体テスト可)。
 */
object RelayProtocol {

    // versionName から組み立てる (build.gradle と二重管理にしない)。
    // flavor (gms/foss) も付ける。relay のログで「FCM の無い foss を入れてしまった」を
    // 即座に見分けられるようにするため (push が登録されない不具合の切り分け)。
    val CLIENT_VERSION = "SipBridge/" + BuildConfig.VERSION_NAME + "-" + BuildConfig.FLAVOR
    const val SESSION_PATH = "/v1/session"

    // ---------- relay -> app ----------

    data class CallInfo(
        val callId: String = "",
        val direction: String = "in",
        val state: String = "ringing",
        val from: String = "",
        val display: String = "",
        val pt: Int = 0,
        val startedAt: Long = 0L
    )

    data class Hello(
        val relayVersion: String = "",
        val extension: String = "",
        /** v1.1: 結び付いている SIP user。無ければ extension を採用する。未設定なら ""。 */
        val account: String = "",
        val registered: Boolean = false,
        val call: CallInfo? = null,
        val serverTime: Long = 0L
    )

    data class Registration(val ok: Boolean = false, val detail: String = "")
    data class Incoming(val callId: String = "", val from: String = "", val display: String = "", val pt: Int = 0)
    data class Ringing(val callId: String = "", val early: Boolean = false)
    data class Answered(val callId: String = "", val pt: Int = 0)
    data class Ended(val callId: String = "", val reason: String = "", val code: Int = 0)
    data class ProtoError(val code: String = "", val message: String = "")
    data class Pong(val ts: Long = 0L)

    /** relay -> app メッセージの判別結果。未知の t は [parseRelayMessage] が例外を投げる。 */
    sealed interface RelayMsg {
        data class HelloMsg(val v: Hello) : RelayMsg
        data class RegistrationMsg(val v: Registration) : RelayMsg
        data class IncomingMsg(val v: Incoming) : RelayMsg
        data class RingingMsg(val v: Ringing) : RelayMsg
        data class AnsweredMsg(val v: Answered) : RelayMsg
        data class EndedMsg(val v: Ended) : RelayMsg
        data class ErrorMsg(val v: ProtoError) : RelayMsg
        data class PongMsg(val v: Pong) : RelayMsg
    }

    fun parseRelayMessage(json: String): RelayMsg {
        val o = JSONObject(json)
        return when (o.optString("t", "")) {
            "hello" -> RelayMsg.HelloMsg(
                Hello(
                    relayVersion = o.optString("relayVersion", ""),
                    extension = o.optString("extension", ""),
                    // account が無ければ extension を採用する (旧 relay / 未結び付け対応)
                    account = o.optString("account", "").ifBlank { o.optString("extension", "") },
                    registered = o.optBoolean("registered", false),
                    call = if (o.isNull("call")) null else parseCallInfo(o.getJSONObject("call")),
                    serverTime = o.optLong("serverTime", 0L)
                )
            )
            "registration" -> RelayMsg.RegistrationMsg(
                Registration(ok = o.optBoolean("ok", false), detail = o.optString("detail", ""))
            )
            "incoming" -> RelayMsg.IncomingMsg(
                Incoming(
                    callId = o.optString("callId", ""),
                    from = o.optString("from", ""),
                    display = o.optString("display", ""),
                    pt = o.optInt("pt", 0)
                )
            )
            "ringing" -> RelayMsg.RingingMsg(
                Ringing(callId = o.optString("callId", ""), early = o.optBoolean("early", false))
            )
            "answered" -> RelayMsg.AnsweredMsg(
                Answered(callId = o.optString("callId", ""), pt = o.optInt("pt", 0))
            )
            "ended" -> RelayMsg.EndedMsg(
                Ended(
                    callId = o.optString("callId", ""),
                    reason = o.optString("reason", ""),
                    code = o.optInt("code", 0)
                )
            )
            "error" -> RelayMsg.ErrorMsg(
                ProtoError(code = o.optString("code", ""), message = o.optString("message", ""))
            )
            "pong" -> RelayMsg.PongMsg(Pong(ts = o.optLong("ts", 0L)))
            else -> throw IllegalArgumentException("unknown message type: ${o.optString("t", "")}")
        }
    }

    fun parseCallInfo(o: JSONObject): CallInfo = CallInfo(
        callId = o.optString("callId", ""),
        direction = o.optString("direction", "in"),
        state = o.optString("state", "ringing"),
        from = o.optString("from", ""),
        display = o.optString("display", ""),
        pt = o.optInt("pt", 0),
        startedAt = o.optLong("startedAt", 0L)
    )

    // ---------- app -> relay ----------

    fun buildAnswer(callId: String, pt: Int? = null): String =
        JSONObject().put("t", "answer").put("callId", callId)
            .also { if (pt != null) it.put("pt", pt) }.toString()

    fun buildReject(callId: String, code: Int? = null): String =
        JSONObject().put("t", "reject").put("callId", callId)
            .also { if (code != null) it.put("code", code) }.toString()

    fun buildHangup(callId: String): String =
        JSONObject().put("t", "hangup").put("callId", callId).toString()

    fun buildDial(to: String): String =
        JSONObject().put("t", "dial").put("to", to).toString()

    fun buildDtmf(callId: String, digits: String): String =
        JSONObject().put("t", "dtmf").put("callId", callId).put("digits", digits).toString()

    /** v1.1: この端末が使う SIP アカウントを relay に登録する。display は任意。 */
    fun buildSipAccount(user: String, password: String, display: String = ""): String =
        JSONObject().put("t", "sip_account").put("user", user)
            .put("password", password).put("display", display).toString()

    fun buildRegisterPush(provider: String, token: String): String =
        JSONObject().put("t", "register_push").put("provider", provider).put("token", token).toString()

    fun buildPing(ts: Long): String =
        JSONObject().put("t", "ping").put("ts", ts).toString()

    /** `call_stats` を送ってよい relay の最低バージョン (旧 relay は未知の t に error を返す)。 */
    const val CALL_STATS_MIN_RELAY_VERSION = "0.3.0"

    fun supportsCallStats(relayVersion: String): Boolean =
        relayVersionAtLeast(relayVersion, CALL_STATS_MIN_RELAY_VERSION)

    /**
     * v1.2: 通話終了時に 1 回送る品質統計 (docs/QUALITY_STATS.md)。キー名は relay と合意済み。
     * 番号・表示名などの PII は含めない (logcat にも同じ文字列を出す)。
     */
    fun buildCallStats(r: CallStatsReport): String =
        JSONObject()
            .put("t", "call_stats")
            .put("callId", r.callId)
            .put("dur", r.durMs)
            .put("net", r.net)
            .put(
                "rx", JSONObject()
                    .put("pkts", r.rxPkts)
                    .put("gaps", r.rxGaps)
                    .put("reorder", r.rxReorder)
                    .put("jitterMs", r.rxJitterMs)
                    .put("maxGapMs", r.rxMaxGapMs)
                    .put("stall100", r.rxStall100)
                    .put("stall200", r.rxStall200)
                    .put("stall500", r.rxStall500)
                    .put("reconnects", r.rxReconnects)
            )
            .put("jb", JSONObject().put("underrun", r.jbUnderrun).put("overflow", r.jbOverflow))
            .put("playUnderrun", r.playUnderrun)
            .put("tx", JSONObject().put("pkts", r.txPkts).put("drop", r.txDrop).put("lost", r.txLost).put("lateMs", r.txLateMs))
            .put("rttMs", org.json.JSONArray().put(r.rttStartMs).put(r.rttEndMs))
            .toString()
}

/**
 * 設定の relay URL (例 `wss://relay.example.com`, `https://relay.example.com/`, `relay.example.com`)
 * を WebSocket 接続先 (`.../v1/session`) に正規化する。Android 依存なし。
 */
fun normalizeRelayUrl(input: String): String {
    var u = input.trim()
    require(u.isNotEmpty()) { "relay URL is empty" }
    if (u.startsWith("http://")) u = "ws://" + u.removePrefix("http://")
    else if (u.startsWith("https://")) u = "wss://" + u.removePrefix("https://")
    if (!u.startsWith("ws://") && !u.startsWith("wss://")) u = "wss://$u"
    u = u.trimEnd('/')
    if (!u.endsWith(RelayProtocol.SESSION_PATH)) u += RelayProtocol.SESSION_PATH
    return u
}

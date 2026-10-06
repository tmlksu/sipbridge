package io.github.tmlksu.sipbridge

import org.json.JSONException
import org.json.JSONObject

/**
 * 設定 QR コード (または貼り付けたテキスト) の中身。生成側は `tools/provision-qr.html`。
 *
 * 形式は 1 行の JSON。`sipbridge` が版番号 (今は 1) で、ほかのキーはすべて省略できる。
 * **含まれていたキーだけ**を上書きし、無いキーの設定はそのまま残す
 * (例: SIP パスワードを QR に入れず、受け取った人が手で入れる運用もできる)。
 *
 * ```
 * {"sipbridge":1,"url":"wss://sip.example.com","cid":"….access","cs":"…",
 *  "user":"2106","pw":"…","name":"台所","mode":"PERSISTENT"}
 * ```
 *
 * | キー | 設定項目 |
 * |---|---|
 * | `url` | relay URL |
 * | `cid` / `cs` | Cloudflare Access Service Token の Client ID / Secret (`cid` には `cs` が必須) |
 * | `tok` | Dev Token (AUTH_MODE=token の relay 用) |
 * | `user` / `pw` / `name` | SIP アカウントの内線番号 / パスワード / 表示名 |
 * | `mode` | `PERSISTENT` / `PUSH` |
 *
 * 秘密 (Access Secret・SIP パスワード) を含むため、読み取った文字列はログに出さない。
 * 適用前に必ず確認ダイアログを出す (他人の relay を指す QR を読まされて SIP パスワードを
 * 送ってしまうのを防ぐため。接続先ホストを見せる)。
 */
data class ProvisioningPayload(
    val relayUrl: String? = null,
    val accessClientId: String? = null,
    val accessClientSecret: String? = null,
    val devToken: String? = null,
    val sipUser: String? = null,
    val sipPassword: String? = null,
    val sipDisplay: String? = null,
    val mode: BridgeMode? = null,
) {
    /** 含まれていた項目だけを [cur] に上書きした設定。 */
    fun applyTo(cur: BridgeConfigData): BridgeConfigData = cur.copy(
        relayUrl = relayUrl ?: cur.relayUrl,
        accessClientId = accessClientId ?: cur.accessClientId,
        accessClientSecret = accessClientSecret ?: cur.accessClientSecret,
        devToken = devToken ?: cur.devToken,
        sipUser = sipUser ?: cur.sipUser,
        sipPassword = sipPassword ?: cur.sipPassword,
        sipDisplay = sipDisplay ?: cur.sipDisplay,
        mode = mode ?: cur.mode,
    )

    /** relay URL のホスト名 (確認ダイアログ用)。URL を含まなければ null。 */
    fun relayHost(): String? {
        val url = relayUrl?.takeIf { it.isNotBlank() } ?: return null
        val ok = checkRelayUrl(url, allowLoopbackCleartext = true) as? RelayUrlCheck.Ok
        return ok?.let { parseRelayWsUrl(it.url)?.host } ?: url
    }

    sealed class Result {
        data class Ok(val payload: ProvisioningPayload) : Result()
        /** [message] は利用者向けの理由 (秘密は含めない)。 */
        data class Error(val message: String) : Result()
    }

    companion object {
        const val VERSION = 1
        /** QR (バージョン 40, 誤り訂正 L) のバイトモード上限より少し小さく取る。 */
        const val MAX_LENGTH = 2048

        private val KNOWN_KEYS = setOf("url", "cid", "cs", "tok", "user", "pw", "name", "mode")

        /**
         * QR / 貼り付けの文字列を解釈・検証する。
         * [allowLoopbackCleartext] は [checkRelayUrl] と同じ (debug ビルドのみ true)。
         */
        fun parse(text: String, allowLoopbackCleartext: Boolean = BuildConfig.DEBUG): Result {
            val s = text.trim()
            if (s.isEmpty()) return Result.Error("内容が空です")
            if (s.length > MAX_LENGTH) return Result.Error("内容が長すぎます")
            val o = try {
                JSONObject(s)
            } catch (e: JSONException) {
                return Result.Error("SIP Bridge の設定コードではありません")
            }
            if (!o.has("sipbridge")) return Result.Error("SIP Bridge の設定コードではありません")
            // optInt は "1" (文字列) も 1 にしてしまうので、数値であることを確かめる。
            val ver = (o.opt("sipbridge") as? Int) ?: -1
            if (ver > VERSION) return Result.Error("新しい形式の設定コードです。アプリを更新してください")
            if (ver != VERSION) return Result.Error("設定コードの版が不正です")
            if (KNOWN_KEYS.none { o.has(it) }) return Result.Error("設定項目が含まれていません")

            fun str(key: String): String? {
                if (!o.has(key) || o.isNull(key)) return null
                return o.opt(key) as? String
                    ?: throw IllegalArgumentException("「$key」は文字列で指定してください")
            }

            return try {
                val url = str("url")?.trim()
                if (!url.isNullOrEmpty()) {
                    when (val c = checkRelayUrl(url, allowLoopbackCleartext)) {
                        is RelayUrlCheck.Ok -> Unit
                        else -> return Result.Error("relay URL を使えません: ${c.message}")
                    }
                }
                val cid = str("cid")?.trim()
                val cs = str("cs")?.trim()
                if (!cid.isNullOrEmpty() && cs.isNullOrEmpty()) {
                    return Result.Error("Access Client ID に対応する Client Secret がありません")
                }
                val mode = str("mode")?.trim()?.let { m ->
                    BridgeMode.entries.firstOrNull { it.name.equals(m, ignoreCase = true) }
                        ?: return Result.Error("モード「$m」は不明です (PERSISTENT / PUSH)")
                }
                Result.Ok(
                    ProvisioningPayload(
                        relayUrl = url,
                        accessClientId = cid,
                        accessClientSecret = cs,
                        devToken = str("tok"),
                        sipUser = str("user")?.trim(),
                        sipPassword = str("pw"),
                        sipDisplay = str("name")?.trim(),
                        mode = mode,
                    )
                )
            } catch (e: IllegalArgumentException) {
                Result.Error(e.message ?: "設定コードが不正です")
            }
        }
    }
}

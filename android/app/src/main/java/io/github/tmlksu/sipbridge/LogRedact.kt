package io.github.tmlksu.sipbridge

/**
 * logcat に出す通話メタデータ (電話番号・内線番号・SIP ユーザー名・relay URL) を伏せる (#47)。
 *
 * logcat は READ_LOGS を持つアプリ・バグレポート・adb から読めるため、release では
 * 番号をそのまま出さない。切り分けに使えるよう末尾の数桁だけ残す。
 * debug ビルド ([BuildConfig.DEBUG]) では検証しやすいよう素のまま出す。
 *
 * 判定ロジック ([mask], [relayUrl]) は Android 依存なし (JVM 単体テスト可)。
 */
object LogRedact {

    /** 番号・ユーザー名をログ用に変換する。release は [mask]、debug は素のまま。 */
    fun id(value: String?, reveal: Boolean = BuildConfig.DEBUG): String =
        if (reveal) value.orEmpty() else mask(value)

    /**
     * 末尾だけ残して `*` で伏せる。8 文字以上 (外線番号など) は末尾 4 桁、
     * 5〜7 文字は末尾 2 桁、3〜4 文字 (内線番号など) は末尾 1 桁を残す。2 文字以下は全て伏せる。
     * 空・null は `""`。
     */
    fun mask(value: String?): String {
        val s = value.orEmpty()
        if (s.isEmpty()) return ""
        val keep = when {
            s.length >= 8 -> 4
            s.length >= 5 -> 2
            s.length >= 3 -> 1
            else -> 0
        }
        return "*".repeat(s.length - keep) + s.takeLast(keep)
    }

    /**
     * relay URL をログ用に `スキーム://ホスト[:ポート]` まで落とす (パス・クエリ・userinfo は出さない)。
     * 解釈できなければ `(invalid)`。
     */
    fun relayUrl(url: String?): String {
        val u = url.orEmpty().trim()
        val parsed = parseRelayWsUrl(u)
            ?: (if (u.contains("://")) null else parseRelayWsUrl("wss://$u"))
            ?: return "(invalid)"
        val scheme = if (parsed.isHttps) "wss" else "ws"
        val host = if (parsed.host.contains(':')) "[${parsed.host}]" else parsed.host
        val defaultPort = if (parsed.isHttps) 443 else 80
        val port = if (parsed.port != defaultPort) ":${parsed.port}" else ""
        return "$scheme://$host$port"
    }
}

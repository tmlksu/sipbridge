package io.github.tmlksu.sipbridge

import android.content.Context

/**
 * 到達性の履歴と判定 (UI-DESIGN §6.2)。
 *
 * 平文 SharedPreferences `sipbridge_health` に epoch millis を記録する
 * (秘密を含まないため暗号化不要)。記録点:
 * - [markUserOpen]: `MainActivity.onResume` (= OS から見た「使用」)
 * - [markPushRegistered]: `register_push` を送って `hello` まで到達したとき (`BridgeService`)
 * - [markPushReceived]: gms の `BridgeMessagingService.onMessageReceived`
 * - [markRelayOk]: `hello` 受信 (`BridgeService`)
 *
 * 判定 [evaluate] は Android に一切依存しない純関数 (JVM テスト対象)。
 * `Context` や `Build.VERSION` は [buildSnapshot] 側でのみ扱う。
 */
object PushHealth {

    const val PREFS = "sipbridge_health"
    private const val KEY_USER_OPEN = "lastUserOpenAt"
    private const val KEY_PUSH_REGISTERED = "lastPushRegisteredAt"
    private const val KEY_PUSH_RECEIVED = "lastPushReceivedAt"
    private const val KEY_RELAY_OK = "lastRelayOkAt"
    private const val KEY_WARN_AT = "lastWarnAt"
    private const val KEY_WARN_KIND = "lastWarnKind"
    private const val KEY_SETUP_SHOWN = "lastSetupShownAt"

    const val DAY_MS = 86_400_000L
    /** PUSH_REGISTRATION_STALE の閾値 (§6.2 の表: 14 日)。 */
    const val STALE_PUSH_REGISTRATION_MS = 14 * DAY_MS
    /** HIBERNATION_SOON の閾値 (§6.2 の表: 45 日)。 */
    const val HIBERNATION_WARN_MS = 45 * DAY_MS
    /** HealthCheckReceiver が再登録のために接続する閾値 (§6.3: 7 日)。 */
    const val REREGISTER_MS = 7 * DAY_MS
    /** 同一 kind の通知の抑止期間 (§6.3: 3 日に 1 回まで)。 */
    const val WARN_SUPPRESS_MS = 3 * DAY_MS
    /** BLOCKING 不足時の SetupSheet 自動表示の間隔 (§6.4: 1 日 1 回)。 */
    const val SETUP_AUTO_SHOW_MS = DAY_MS

    enum class Severity { BLOCKING, WARN }

    enum class Kind {
        NOTIFICATIONS_OFF,
        MIC_DENIED,
        PUSH_TOKEN_MISSING,
        PUSH_REGISTRATION_STALE,
        HIBERNATION_SOON,
        BATTERY_OPTIMIZED,
        OVERLAY_DENIED,
        FULLSCREEN_DENIED,
        TELECOM_ACCOUNT_DISABLED,
    }

    data class Issue(val kind: Kind, val severity: Severity)

    /**
     * [evaluate] への入力。OS 値 ([SystemStatus])・設定・履歴時刻をまとめたもの。
     * `null` = 非対応端末 (その項目は判定しない)。
     */
    data class Snapshot(
        val mode: BridgeMode,
        val isGms: Boolean,
        val micGranted: Boolean,
        val notificationsEnabled: Boolean,
        val pushTokenPresent: Boolean,
        val ignoringBatteryOptimizations: Boolean,
        val hibernationExempt: Boolean?,
        val overlayRequired: Boolean,
        val overlayGranted: Boolean,
        val fullScreenIntentAllowed: Boolean?,
        val lastUserOpenAt: Long,
        val lastPushRegisteredAt: Long,
        /** 通話アカウントが有効か。null = 非対応端末・アプリ独自固定 (判定しない)。 */
        val telecomAccountEnabled: Boolean? = null,
        /** telecomPref == APP (アプリ独自に固定) か。true なら通話アカウントを判定しない。 */
        val telecomPrefIsApp: Boolean = false,
    )

    data class Times(
        val lastUserOpenAt: Long = 0L,
        val lastPushRegisteredAt: Long = 0L,
        val lastPushReceivedAt: Long = 0L,
        val lastRelayOkAt: Long = 0L,
    )

    /**
     * 純関数の判定 (§6.2 の表どおり。BLOCKING を先に、WARN を後に並べる)。
     * Android API に触らないこと (Context/Build.VERSION の参照禁止)。
     */
    fun evaluate(now: Long, s: Snapshot): List<Issue> {
        val out = mutableListOf<Issue>()
        if (!s.notificationsEnabled) out += Issue(Kind.NOTIFICATIONS_OFF, Severity.BLOCKING)
        if (!s.micGranted) out += Issue(Kind.MIC_DENIED, Severity.BLOCKING)
        if (s.mode == BridgeMode.PUSH && s.isGms && !s.pushTokenPresent) {
            out += Issue(Kind.PUSH_TOKEN_MISSING, Severity.BLOCKING)
        }
        // PUSH モードでのみ push 系を出す (PERSISTENT では PUSH_* を出さない)。
        // foss には FCM が無く PUSH モード自体が使えないため、WARN も出さない。
        if (s.mode == BridgeMode.PUSH && s.isGms &&
            now - s.lastPushRegisteredAt > STALE_PUSH_REGISTRATION_MS
        ) {
            // lastPushRegisteredAt == 0 (未登録) も含む (now - 0 > 14 日)。
            out += Issue(Kind.PUSH_REGISTRATION_STALE, Severity.WARN)
        }
        if (s.hibernationExempt == false && now - s.lastUserOpenAt > HIBERNATION_WARN_MS) {
            out += Issue(Kind.HIBERNATION_SOON, Severity.WARN)
        }
        if (!s.ignoringBatteryOptimizations) out += Issue(Kind.BATTERY_OPTIMIZED, Severity.WARN)
        if (s.overlayRequired && !s.overlayGranted) out += Issue(Kind.OVERLAY_DENIED, Severity.WARN)
        if (s.fullScreenIntentAllowed == false) out += Issue(Kind.FULLSCREEN_DENIED, Severity.WARN)
        if (!s.telecomPrefIsApp && s.telecomAccountEnabled == false) {
            out += Issue(Kind.TELECOM_ACCOUNT_DISABLED, Severity.WARN)
        }
        return out
    }

    /** PUSH モードでトークン登録が古ければ再登録のために接続する (§6.3 手順 1)。純関数。 */
    fun needsReregister(now: Long, mode: BridgeMode, lastPushRegisteredAt: Long): Boolean =
        mode == BridgeMode.PUSH && now - lastPushRegisteredAt > REREGISTER_MS

    /**
     * SetupSheet で対処できる kind か (純関数)。PUSH_TOKEN_MISSING と
     * PUSH_REGISTRATION_STALE はシートに項目が無くユーザーが何もできないため false。
     * 健康チェック通知は「開いて」と促すのが役目のため、通知側は全 kind を対象にする。
     */
    fun isActionable(kind: Kind): Boolean = when (kind) {
        Kind.NOTIFICATIONS_OFF,
        Kind.MIC_DENIED,
        Kind.BATTERY_OPTIMIZED,
        Kind.HIBERNATION_SOON,
        Kind.OVERLAY_DENIED,
        Kind.FULLSCREEN_DENIED,
        Kind.TELECOM_ACCOUNT_DISABLED -> true
        Kind.PUSH_TOKEN_MISSING,
        Kind.PUSH_REGISTRATION_STALE -> false
    }

    /**
     * 通知を出してよいか (§6.3 手順 3: 同一 kind は 3 日に 1 回まで)。純関数。
     * 通知は 1 本で先頭の理由が表題になるため、抑止キーも先頭 kind とする。
     */
    fun shouldNotify(
        now: Long,
        issues: List<Issue>,
        lastWarnAt: Long,
        lastWarnKind: String?,
    ): Boolean {
        if (issues.isEmpty()) return false
        val lead = issues.first().kind.name
        if (lastWarnKind == lead && now - lastWarnAt < WARN_SUPPRESS_MS) return false
        return true
    }

    // ---- 記録 (Android 側。テスト対象外) ----

    private fun prefs(ctx: Context) =
        ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    fun markUserOpen(ctx: Context, now: Long = System.currentTimeMillis()) {
        prefs(ctx).edit().putLong(KEY_USER_OPEN, now).apply()
    }

    fun markPushRegistered(ctx: Context, now: Long = System.currentTimeMillis()) {
        prefs(ctx).edit().putLong(KEY_PUSH_REGISTERED, now).apply()
    }

    fun markPushReceived(ctx: Context, now: Long = System.currentTimeMillis()) {
        prefs(ctx).edit().putLong(KEY_PUSH_RECEIVED, now).apply()
    }

    fun markRelayOk(ctx: Context, now: Long = System.currentTimeMillis()) {
        prefs(ctx).edit().putLong(KEY_RELAY_OK, now).apply()
    }

    fun readTimes(ctx: Context): Times {
        val p = prefs(ctx)
        return Times(
            lastUserOpenAt = p.getLong(KEY_USER_OPEN, 0L),
            lastPushRegisteredAt = p.getLong(KEY_PUSH_REGISTERED, 0L),
            lastPushReceivedAt = p.getLong(KEY_PUSH_RECEIVED, 0L),
            lastRelayOkAt = p.getLong(KEY_RELAY_OK, 0L),
        )
    }

    /** 通知の重複抑止用 (時刻, kind 名)。未通知なら (0, null)。 */
    fun getLastWarn(ctx: Context): Pair<Long, String?> {
        val p = prefs(ctx)
        return p.getLong(KEY_WARN_AT, 0L) to p.getString(KEY_WARN_KIND, null)
    }

    fun setLastWarn(ctx: Context, now: Long, kind: Kind) {
        prefs(ctx).edit().putLong(KEY_WARN_AT, now).putString(KEY_WARN_KIND, kind.name).apply()
    }

    /** 全部解消したら抑止状態も消す (次回の警告を遅らせない)。 */
    fun clearWarn(ctx: Context) {
        prefs(ctx).edit().remove(KEY_WARN_AT).remove(KEY_WARN_KIND).apply()
    }

    fun getLastSetupShownAt(ctx: Context): Long =
        prefs(ctx).getLong(KEY_SETUP_SHOWN, 0L)

    fun markSetupShown(ctx: Context, now: Long = System.currentTimeMillis()) {
        prefs(ctx).edit().putLong(KEY_SETUP_SHOWN, now).apply()
    }

    /** 現在の OS 状態・設定・履歴から [Snapshot] を組み立てる。 */
    fun buildSnapshot(ctx: Context): Snapshot {
        val cfg = BridgeConfig.load(ctx)
        val st = SystemStatus.read(ctx)
        val t = readTimes(ctx)
        return Snapshot(
            mode = cfg.mode,
            isGms = BuildConfig.FLAVOR == "gms",
            micGranted = st.micGranted,
            notificationsEnabled = st.notificationsEnabled,
            pushTokenPresent = st.pushTokenPresent,
            ignoringBatteryOptimizations = st.ignoringBatteryOptimizations,
            hibernationExempt = st.hibernationExempt,
            overlayRequired = cfg.overlayEnabled,
            overlayGranted = st.overlayGranted,
            fullScreenIntentAllowed = st.fullScreenIntentAllowed,
            lastUserOpenAt = t.lastUserOpenAt,
            lastPushRegisteredAt = t.lastPushRegisteredAt,
            telecomAccountEnabled = st.telecomAccountEnabled,
            telecomPrefIsApp = cfg.telecomPref == TelecomTierManager.Pref.APP,
        )
    }
}

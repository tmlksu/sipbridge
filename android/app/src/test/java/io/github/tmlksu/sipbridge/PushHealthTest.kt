package io.github.tmlksu.sipbridge

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PushHealth.evaluate / shouldNotify / needsReregister の JVM テスト (T12)。
 * evaluate は Android に一切依存しない純関数であることが条件。
 */
class PushHealthTest {

    companion object {
        private const val NOW = 1_700_000_000_000L
        private const val DAY = PushHealth.DAY_MS
    }

    /** 全項目良好の Snapshot (PUSH+gms)。時刻は「今」。 */
    private fun good(): PushHealth.Snapshot = PushHealth.Snapshot(
        mode = BridgeMode.PUSH,
        isGms = true,
        micGranted = true,
        notificationsEnabled = true,
        pushTokenPresent = true,
        ignoringBatteryOptimizations = true,
        hibernationExempt = true,
        overlayRequired = true,
        overlayGranted = true,
        fullScreenIntentAllowed = true,
        lastUserOpenAt = NOW,
        lastPushRegisteredAt = NOW,
    )

    private fun kinds(issues: List<PushHealth.Issue>): List<PushHealth.Kind> =
        issues.map { it.kind }

    @Test
    fun `all good yields no issues`() {
        assertTrue(PushHealth.evaluate(NOW, good()).isEmpty())
    }

    @Test
    fun `notifications off is blocking`() {
        val issues = PushHealth.evaluate(NOW, good().copy(notificationsEnabled = false))
        assertEquals(
            listOf(PushHealth.Kind.NOTIFICATIONS_OFF),
            kinds(issues)
        )
        assertEquals(PushHealth.Severity.BLOCKING, issues.first().severity)
    }

    @Test
    fun `mic denied is blocking`() {
        val issues = PushHealth.evaluate(NOW, good().copy(micGranted = false))
        assertEquals(listOf(PushHealth.Kind.MIC_DENIED), kinds(issues))
        assertEquals(PushHealth.Severity.BLOCKING, issues.first().severity)
    }

    @Test
    fun `push token missing is blocking only in push gms`() {
        val issues = PushHealth.evaluate(NOW, good().copy(pushTokenPresent = false))
        assertEquals(listOf(PushHealth.Kind.PUSH_TOKEN_MISSING), kinds(issues))
        assertEquals(PushHealth.Severity.BLOCKING, issues.first().severity)
    }

    @Test
    fun `blocking comes before warn`() {
        val issues = PushHealth.evaluate(
            NOW,
            good().copy(
                micGranted = false,
                ignoringBatteryOptimizations = false,
                overlayGranted = false,
            )
        )
        assertEquals(
            listOf(
                PushHealth.Kind.MIC_DENIED,
                PushHealth.Kind.BATTERY_OPTIMIZED,
                PushHealth.Kind.OVERLAY_DENIED,
            ),
            kinds(issues)
        )
        assertEquals(PushHealth.Severity.BLOCKING, issues[0].severity)
        assertEquals(PushHealth.Severity.WARN, issues[1].severity)
    }

    @Test
    fun `stale push registration threshold is 14 days`() {
        // 14 日ちょうどはセーフ、1ms 超えたら WARN。未登録 (0) も含む。
        val ok = PushHealth.evaluate(
            NOW, good().copy(lastPushRegisteredAt = NOW - 14 * DAY)
        )
        assertFalse(kinds(ok).contains(PushHealth.Kind.PUSH_REGISTRATION_STALE))

        val stale = PushHealth.evaluate(
            NOW, good().copy(lastPushRegisteredAt = NOW - 14 * DAY - 1)
        )
        assertTrue(kinds(stale).contains(PushHealth.Kind.PUSH_REGISTRATION_STALE))
        assertEquals(
            PushHealth.Severity.WARN,
            stale.first { it.kind == PushHealth.Kind.PUSH_REGISTRATION_STALE }.severity
        )

        val never = PushHealth.evaluate(NOW, good().copy(lastPushRegisteredAt = 0L))
        assertTrue(kinds(never).contains(PushHealth.Kind.PUSH_REGISTRATION_STALE))
    }

    @Test
    fun `hibernation soon threshold is 45 days and needs opt-out missing`() {
        val ok = PushHealth.evaluate(
            NOW,
            good().copy(
                hibernationExempt = false,
                lastUserOpenAt = NOW - 45 * DAY
            )
        )
        assertFalse(kinds(ok).contains(PushHealth.Kind.HIBERNATION_SOON))

        val soon = PushHealth.evaluate(
            NOW,
            good().copy(
                hibernationExempt = false,
                lastUserOpenAt = NOW - 45 * DAY - 1
            )
        )
        assertTrue(kinds(soon).contains(PushHealth.Kind.HIBERNATION_SOON))

        // 除外済みなら 45 日超でも出ない。非対応 (null) でも出ない。
        assertFalse(
            kinds(
                PushHealth.evaluate(
                    NOW,
                    good().copy(hibernationExempt = true, lastUserOpenAt = 0L)
                )
            ).contains(PushHealth.Kind.HIBERNATION_SOON)
        )
        assertFalse(
            kinds(
                PushHealth.evaluate(
                    NOW,
                    good().copy(hibernationExempt = null, lastUserOpenAt = 0L)
                )
            ).contains(PushHealth.Kind.HIBERNATION_SOON)
        )
    }

    @Test
    fun `reregister threshold is 7 days in push mode only`() {
        assertFalse(PushHealth.needsReregister(NOW, BridgeMode.PUSH, NOW - 7 * DAY))
        assertTrue(PushHealth.needsReregister(NOW, BridgeMode.PUSH, NOW - 7 * DAY - 1))
        assertTrue(PushHealth.needsReregister(NOW, BridgeMode.PUSH, 0L))
        assertFalse(PushHealth.needsReregister(NOW, BridgeMode.PERSISTENT, 0L))
    }

    @Test
    fun `battery overlay fullscreen warns`() {
        val issues = PushHealth.evaluate(
            NOW,
            good().copy(
                ignoringBatteryOptimizations = false,
                overlayGranted = false,
                fullScreenIntentAllowed = false,
            )
        )
        assertEquals(
            listOf(
                PushHealth.Kind.BATTERY_OPTIMIZED,
                PushHealth.Kind.OVERLAY_DENIED,
                PushHealth.Kind.FULLSCREEN_DENIED,
            ),
            kinds(issues)
        )
    }

    @Test
    fun `overlay denied only when required and fullscreen null is unsupported`() {
        // オーバーレイ設定 OFF なら権限なしでも出ない。
        assertFalse(
            kinds(
                PushHealth.evaluate(
                    NOW, good().copy(overlayRequired = false, overlayGranted = false)
                )
            ).contains(PushHealth.Kind.OVERLAY_DENIED)
        )
        // API 34 未満 (null) では全画面行は出ない。
        assertFalse(
            kinds(
                PushHealth.evaluate(NOW, good().copy(fullScreenIntentAllowed = null))
            ).contains(PushHealth.Kind.FULLSCREEN_DENIED)
        )
    }

    @Test
    fun `persistent mode skips push issues`() {
        val issues = PushHealth.evaluate(
            NOW,
            good().copy(
                mode = BridgeMode.PERSISTENT,
                pushTokenPresent = false,
                lastPushRegisteredAt = 0L,
            )
        )
        assertFalse(kinds(issues).contains(PushHealth.Kind.PUSH_TOKEN_MISSING))
        assertFalse(kinds(issues).contains(PushHealth.Kind.PUSH_REGISTRATION_STALE))
    }

    @Test
    fun `foss build skips push token missing`() {
        val issues = PushHealth.evaluate(
            NOW, good().copy(isGms = false, pushTokenPresent = false)
        )
        assertFalse(kinds(issues).contains(PushHealth.Kind.PUSH_TOKEN_MISSING))
    }

    @Test
    fun `foss build skips push registration stale`() {
        // foss には FCM が無く PUSH モードは使えないため、WARN も出さない。
        val issues = PushHealth.evaluate(
            NOW, good().copy(isGms = false, lastPushRegisteredAt = 0L)
        )
        assertFalse(kinds(issues).contains(PushHealth.Kind.PUSH_REGISTRATION_STALE))
    }

    @Test
    fun `only setup actionable kinds are actionable`() {
        assertTrue(PushHealth.isActionable(PushHealth.Kind.NOTIFICATIONS_OFF))
        assertTrue(PushHealth.isActionable(PushHealth.Kind.MIC_DENIED))
        assertTrue(PushHealth.isActionable(PushHealth.Kind.BATTERY_OPTIMIZED))
        assertTrue(PushHealth.isActionable(PushHealth.Kind.HIBERNATION_SOON))
        assertTrue(PushHealth.isActionable(PushHealth.Kind.OVERLAY_DENIED))
        assertTrue(PushHealth.isActionable(PushHealth.Kind.FULLSCREEN_DENIED))
        // シートに項目が無い push 系は対処できない。
        assertFalse(PushHealth.isActionable(PushHealth.Kind.PUSH_TOKEN_MISSING))
        assertFalse(PushHealth.isActionable(PushHealth.Kind.PUSH_REGISTRATION_STALE))
    }

    @Test
    fun `unactionable blocking alone leaves nothing actionable`() {
        // セットアップカードと SetupSheet 自動表示は actionable のみを数える。
        // push 系だけの不足では「あと N 件」にも自動表示にもならない。
        val issues = PushHealth.evaluate(
            NOW,
            good().copy(pushTokenPresent = false, lastPushRegisteredAt = 0L)
        )
        assertTrue(issues.any { it.severity == PushHealth.Severity.BLOCKING })
        val actionable = issues.filter { PushHealth.isActionable(it.kind) }
        assertTrue(actionable.isEmpty())
        assertFalse(
            actionable.any { it.severity == PushHealth.Severity.BLOCKING }
        )
    }

    @Test
    fun `same kind suppressed for 3 days`() {
        val issues = PushHealth.evaluate(NOW, good().copy(micGranted = false))
        // 未通知なら出す。
        assertTrue(PushHealth.shouldNotify(NOW, issues, 0L, null))
        // 同一 kind を出した直後は抑止。
        assertFalse(
            PushHealth.shouldNotify(NOW, issues, NOW, PushHealth.Kind.MIC_DENIED.name)
        )
        // 3 日ちょうどで抑止明け (3 日未満だけ抑止)。
        assertFalse(
            PushHealth.shouldNotify(
                NOW + 3 * DAY - 1, issues, NOW, PushHealth.Kind.MIC_DENIED.name
            )
        )
        assertTrue(
            PushHealth.shouldNotify(
                NOW + 3 * DAY, issues, NOW, PushHealth.Kind.MIC_DENIED.name
            )
        )
        // 別の kind が先頭なら抑止しない。
        val battery = PushHealth.evaluate(
            NOW, good().copy(ignoringBatteryOptimizations = false)
        )
        assertTrue(
            PushHealth.shouldNotify(NOW, battery, NOW, PushHealth.Kind.MIC_DENIED.name)
        )
        // 問題なしは何も出さない。
        assertFalse(PushHealth.shouldNotify(NOW, emptyList(), 0L, null))
    }
}

package io.github.tmlksu.sipbridge

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 通話アカウント無効の健康診断 (PushHealth.Kind.TELECOM_ACCOUNT_DISABLED) の JVM テスト。
 */
class TelecomHealthTest {

    private fun good(): PushHealth.Snapshot = PushHealth.Snapshot(
        mode = BridgeMode.PERSISTENT,
        isGms = false,
        micGranted = true,
        notificationsEnabled = true,
        pushTokenPresent = false,
        ignoringBatteryOptimizations = true,
        hibernationExempt = true,
        overlayRequired = false,
        overlayGranted = false,
        fullScreenIntentAllowed = null,
        lastUserOpenAt = 1_700_000_000_000L,
        lastPushRegisteredAt = 1_700_000_000_000L,
    )

    @Test
    fun `telecom null means no issue`() {
        // 非対応端末・アプリ独自固定では判定しない。
        assertTrue(PushHealth.evaluate(1_700_000_000_000L, good()).isEmpty())
    }

    @Test
    fun `disabled account is warn`() {
        val issues = PushHealth.evaluate(
            1_700_000_000_000L, good().copy(telecomAccountEnabled = false)
        )
        assertEquals(
            listOf(PushHealth.Kind.TELECOM_ACCOUNT_DISABLED),
            issues.map { it.kind }
        )
        assertEquals(PushHealth.Severity.WARN, issues.first().severity)
    }

    @Test
    fun `disabled account ignored when pref is APP`() {
        val issues = PushHealth.evaluate(
            1_700_000_000_000L,
            good().copy(telecomAccountEnabled = false, telecomPrefIsApp = true)
        )
        assertTrue(issues.isEmpty())
    }

    @Test
    fun `telecom issue is actionable`() {
        assertTrue(PushHealth.isActionable(PushHealth.Kind.TELECOM_ACCOUNT_DISABLED))
    }
}

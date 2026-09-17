package io.github.tmlksu.sipbridge

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * SystemStatus.telecomAccountEnabledFrom の JVM テスト。
 * READ_PHONE_STATE 未許可のときは判定不能だが行は出したいので false
 * (null にすると行が消えて詰む)。非対応端末・APP 固定では null。
 */
class SystemStatusLogicTest {

    @Test
    fun `no telecom means null`() {
        assertNull(
            SystemStatus.telecomAccountEnabledFrom(
                hasTelecom = false,
                prefIsApp = false,
                readPhoneStateGranted = true,
                managedEnabled = true,
            )
        )
    }

    @Test
    fun `app pref means null`() {
        assertNull(
            SystemStatus.telecomAccountEnabledFrom(
                hasTelecom = true,
                prefIsApp = true,
                readPhoneStateGranted = true,
                managedEnabled = true,
            )
        )
    }

    @Test
    fun `missing read phone state means false not null`() {
        // 権限が無いことが原因だと分かるよう、行は出す (false)。
        assertEquals(
            false,
            SystemStatus.telecomAccountEnabledFrom(
                hasTelecom = true,
                prefIsApp = false,
                readPhoneStateGranted = false,
                managedEnabled = false,
            )
        )
    }

    @Test
    fun `granted and enabled means true`() {
        assertEquals(
            true,
            SystemStatus.telecomAccountEnabledFrom(
                hasTelecom = true,
                prefIsApp = false,
                readPhoneStateGranted = true,
                managedEnabled = true,
            )
        )
    }

    @Test
    fun `granted but not enabled means false`() {
        assertFalse(
            SystemStatus.telecomAccountEnabledFrom(
                hasTelecom = true,
                prefIsApp = false,
                readPhoneStateGranted = true,
                managedEnabled = false,
            ) == true
        )
    }
}

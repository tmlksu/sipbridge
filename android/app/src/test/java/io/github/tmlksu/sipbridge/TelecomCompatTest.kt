package io.github.tmlksu.sipbridge

import android.telecom.DisconnectCause
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * TelecomCompat の純関数 (disconnectCauseFor / numberFrom) の JVM テスト。
 * `DisconnectCause` の static final 定数はコンパイル時にインライン化されるため、
 * android.jar スタブの JVM テストでも参照できる。
 */
class TelecomCompatTest {

    @Test
    fun `disconnect cause mapping`() {
        assertEquals(DisconnectCause.REMOTE, TelecomCompat.disconnectCauseFor("bye"))
        assertEquals(DisconnectCause.CANCELED, TelecomCompat.disconnectCauseFor("cancel"))
        assertEquals(DisconnectCause.BUSY, TelecomCompat.disconnectCauseFor("reject"))
        assertEquals(DisconnectCause.MISSED, TelecomCompat.disconnectCauseFor("timeout"))
        assertEquals(
            DisconnectCause.ANSWERED_ELSEWHERE,
            TelecomCompat.disconnectCauseFor("answered_elsewhere")
        )
    }

    @Test
    fun `unknown reason maps to UNKNOWN`() {
        assertEquals(DisconnectCause.UNKNOWN, TelecomCompat.disconnectCauseFor(""))
        assertEquals(DisconnectCause.UNKNOWN, TelecomCompat.disconnectCauseFor("something-else"))
    }

    @Test
    fun `number from tel uri`() {
        assertEquals("2104", TelecomCompat.numberFrom("tel:2104"))
    }

    @Test
    fun `number from sip uri strips domain`() {
        assertEquals("2104", TelecomCompat.numberFrom("sip:2104@example.com"))
    }

    @Test
    fun `bare number passes through`() {
        assertEquals("2104", TelecomCompat.numberFrom("2104"))
    }
}

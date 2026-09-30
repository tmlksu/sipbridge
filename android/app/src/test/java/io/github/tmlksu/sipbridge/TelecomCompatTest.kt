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

    // ---- #32: percent-encode された発信先を decode して取り出す ----

    @Test
    fun `tel uri is percent-decoded`() {
        // Uri.fromParts("tel", "+81312345678", null).toString() は "tel:%2B81312345678"
        assertEquals("+81312345678", TelecomCompat.numberFrom("tel:%2B81312345678"))
        assertEquals("*67#", TelecomCompat.numberFrom("tel:*67%23"))
        assertEquals("#31#0312345678", TelecomCompat.numberFrom("tel:%2331%230312345678"))
        // '+' は空白にしない
        assertEquals("+81", TelecomCompat.numberFrom("tel:+81"))
    }

    @Test
    fun `tel uri drops parameters`() {
        assertEquals(
            "2104",
            TelecomCompat.numberFrom("tel:2104;phone-context=example.com")
        )
    }

    @Test
    fun `sip uri keeps only user part`() {
        assertEquals("+8190", TelecomCompat.numberFrom("sip:%2B8190@example.com;user=phone"))
        assertEquals("alice", TelecomCompat.numberFrom("sips:alice@example.com"))
        assertEquals("2104", TelecomCompat.numberFrom("sip:2104;x=y@example.com"))
        assertEquals("2104", TelecomCompat.numberFrom("SIP:2104@example.com"))
    }

    @Test
    fun `number from decoded parts`() {
        // android.net.Uri#getSchemeSpecificPart は decode 済み。その値を渡す経路。
        assertEquals("*67#", TelecomCompat.numberFromParts("tel", "*67#"))
        assertEquals("+81312345678", TelecomCompat.numberFromParts("tel", "+81312345678"))
        assertEquals("2104", TelecomCompat.numberFromParts("sip", "2104@pbx.example.com"))
        assertEquals("", TelecomCompat.numberFromParts(null, null))
        assertEquals("2104", TelecomCompat.numberFromParts("voicemail", "2104"))
    }

    @Test
    fun `raw hash split into fragment is restored`() {
        // Uri.parse("tel:*67#") → ssp="*67", fragment=""
        assertEquals("*67#", TelecomCompat.numberFromParts("tel", "*67", ""))
        // Uri.parse("tel:*31#0312345678") → ssp="*31", fragment="0312345678"
        assertEquals("*31#0312345678", TelecomCompat.numberFromParts("tel", "*31", "0312345678"))
        // フラグメント無し (null) は連結しない
        assertEquals("2104", TelecomCompat.numberFromParts("tel", "2104", null))
        // sip: は連結後に user 部を取り出す
        assertEquals("*67#", TelecomCompat.numberFromParts("sip", "*67", "@pbx.example.com"))
    }

    @Test
    fun `percent decode handles utf8 and malformed input`() {
        assertEquals("あ", TelecomCompat.percentDecode("%E3%81%82"))
        assertEquals("100%", TelecomCompat.percentDecode("100%"))
        assertEquals("%zz1", TelecomCompat.percentDecode("%zz1"))
        assertEquals("a#b", TelecomCompat.percentDecode("a%23b"))
    }
}

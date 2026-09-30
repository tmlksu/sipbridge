package io.github.tmlksu.sipbridge

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test

/** #47: logcat に出す番号・ユーザー名・relay URL の伏せ字。 */
class LogRedactTest {

    @Test
    fun `mask keeps only the last digits`() {
        // 内線 (3〜4 文字) は末尾 1 桁
        assertEquals("***4", LogRedact.mask("2104"))
        assertEquals("**1", LogRedact.mask("101"))
        // 5〜7 文字は末尾 2 桁
        assertEquals("***45", LogRedact.mask("12345"))
        assertEquals("*****67", LogRedact.mask("1234567"))
        // 外線 (8 文字以上) は末尾 4 桁
        assertEquals("*******5678", LogRedact.mask("09012345678"))
        assertEquals("********5678", LogRedact.mask("+81312345678"))
        // 2 文字以下は全て伏せる
        assertEquals("**", LogRedact.mask("12"))
        assertEquals("*", LogRedact.mask("1"))
        assertEquals("", LogRedact.mask(""))
        assertEquals("", LogRedact.mask(null))
    }

    @Test
    fun `id reveals only when asked`() {
        assertEquals("2104", LogRedact.id("2104", reveal = true))
        assertEquals("***4", LogRedact.id("2104", reveal = false))
        assertEquals("", LogRedact.id(null, reveal = true))
    }

    @Test
    fun `relay url is reduced to scheme and host`() {
        assertEquals(
            "wss://relay.example.com",
            LogRedact.relayUrl("wss://relay.example.com/v1/session?token=secret")
        )
        assertEquals("wss://relay.example.com:8443", LogRedact.relayUrl("wss://relay.example.com:8443/v1/session"))
        assertEquals("ws://127.0.0.1:18080", LogRedact.relayUrl("ws://127.0.0.1:18080/v1/session"))
        assertEquals("ws://[::1]:18080", LogRedact.relayUrl("ws://[::1]:18080/v1/session"))
        // userinfo は出さない
        val withUser = LogRedact.relayUrl("wss://user:pass@relay.example.com/v1/session")
        assertEquals("wss://relay.example.com", withUser)
        assertFalse(withUser.contains("pass"))
        // スキーム無しは wss とみなす
        assertEquals("wss://relay.example.com", LogRedact.relayUrl("relay.example.com/x"))
        assertEquals("(invalid)", LogRedact.relayUrl(""))
        assertEquals("(invalid)", LogRedact.relayUrl("ftp://relay.example.com"))
    }
}

package io.github.tmlksu.sipbridge

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** 設定 QR コードの解釈 (tools/provision-qr.html が作る JSON)。 */
class ProvisioningPayloadTest {

    private fun ok(text: String): ProvisioningPayload {
        val r = ProvisioningPayload.parse(text, allowLoopbackCleartext = false)
        assertTrue("expected Ok: $r", r is ProvisioningPayload.Result.Ok)
        return (r as ProvisioningPayload.Result.Ok).payload
    }

    private fun err(text: String): String {
        val r = ProvisioningPayload.parse(text, allowLoopbackCleartext = false)
        assertTrue("expected Error: $r", r is ProvisioningPayload.Result.Error)
        return (r as ProvisioningPayload.Result.Error).message
    }

    @Test
    fun `full payload is parsed and trimmed`() {
        val p = ok(
            """{"sipbridge":1,"url":" wss://sip.example.com ","cid":"abc.access","cs":"s3cr3t",
               "user":" 2106 ","pw":" p w ","name":" 台所 ","mode":"push"}"""
        )
        assertEquals("wss://sip.example.com", p.relayUrl)
        assertEquals("abc.access", p.accessClientId)
        assertEquals("s3cr3t", p.accessClientSecret)
        assertEquals("2106", p.sipUser)
        // パスワードは前後の空白も値の一部として扱う (設定画面の入力欄と同じ)
        assertEquals(" p w ", p.sipPassword)
        assertEquals("台所", p.sipDisplay)
        assertEquals(BridgeMode.PUSH, p.mode)
        assertNull(p.devToken)
        assertEquals("sip.example.com", p.relayHost())
    }

    /**
     * tools/provision-qr.html が実際に出した文字列 (headless Chrome で描画した QR を ZXing で
     * デコードしたもの)。非 ASCII は \uXXXX (サロゲートペア含む) で来る。
     */
    @Test
    fun `parses generator output with escaped non ascii`() {
        val p = ok(
            "{\"sipbridge\":1,\"url\":\"wss://sip.example.com\"," +
                "\"cid\":\"0123456789abcdef0123456789abcdef.access\"," +
                "\"cs\":\"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210\"," +
                "\"user\":\"2106\",\"pw\":\"Pa55w0rd-\\\"q\\\"\\\\x\"," +
                "\"name\":\"\\u53f0\\u6240 \\ud83d\\ude00\",\"mode\":\"PERSISTENT\"}"
        )
        assertEquals("0123456789abcdef0123456789abcdef.access", p.accessClientId)
        assertEquals("Pa55w0rd-\"q\"\\x", p.sipPassword)
        assertEquals("台所 😀", p.sipDisplay)
        assertEquals(BridgeMode.PERSISTENT, p.mode)
    }

    @Test
    fun `only present keys overwrite the current config`() {
        val cur = BridgeConfigData(
            relayUrl = "wss://old.example.com", accessClientId = "old.access",
            accessClientSecret = "old", sipUser = "2104", sipPassword = "keep",
            sipDisplay = "居間", mode = BridgeMode.PUSH, micGain = 3.0f, deviceId = "dev-1",
        )
        val p = ok("""{"sipbridge":1,"cid":"new.access","cs":"new","user":"2106"}""")
        val next = p.applyTo(cur)
        assertEquals("wss://old.example.com", next.relayUrl)
        assertEquals("new.access", next.accessClientId)
        assertEquals("new", next.accessClientSecret)
        assertEquals("2106", next.sipUser)
        assertEquals("keep", next.sipPassword)
        assertEquals("居間", next.sipDisplay)
        assertEquals(BridgeMode.PUSH, next.mode)
        assertEquals(3.0f, next.micGain)
        assertEquals("dev-1", next.deviceId)
    }

    @Test
    fun `explicit empty string clears the field`() {
        val cur = BridgeConfigData(sipDisplay = "居間", devToken = "t")
        val next = ok("""{"sipbridge":1,"name":"","tok":""}""").applyTo(cur)
        assertEquals("", next.sipDisplay)
        assertEquals("", next.devToken)
    }

    @Test
    fun `rejects non sipbridge content`() {
        err("")
        err("https://example.com/")
        err("""{"url":"wss://sip.example.com"}""")
        err("""{"sipbridge":"1","url":"wss://sip.example.com"}""")
        err("""{"sipbridge":1}""")
        err("""{"sipbridge":1,"unknown":"x"}""")
        err("{" + "\"x\":\"" + "a".repeat(ProvisioningPayload.MAX_LENGTH) + "\"}")
    }

    @Test
    fun `newer version asks for an app update`() {
        assertTrue(err("""{"sipbridge":2,"url":"wss://sip.example.com"}""").contains("更新"))
    }

    @Test
    fun `rejects cleartext relay url`() {
        err("""{"sipbridge":1,"url":"ws://sip.example.com"}""")
        err("""{"sipbridge":1,"url":"http://192.168.1.10:8080"}""")
        err("""{"sipbridge":1,"url":"ftp://sip.example.com"}""")
    }

    @Test
    fun `loopback cleartext only in debug`() {
        val r = ProvisioningPayload.parse(
            """{"sipbridge":1,"url":"ws://127.0.0.1:8080"}""", allowLoopbackCleartext = true
        )
        assertTrue(r is ProvisioningPayload.Result.Ok)
        err("""{"sipbridge":1,"url":"ws://127.0.0.1:8080"}""")
    }

    @Test
    fun `client id requires secret`() {
        err("""{"sipbridge":1,"cid":"abc.access"}""")
        err("""{"sipbridge":1,"cid":"abc.access","cs":" "}""")
    }

    @Test
    fun `rejects bad types and unknown mode`() {
        err("""{"sipbridge":1,"user":2106}""")
        err("""{"sipbridge":1,"mode":"SOMETIMES"}""")
    }

    @Test
    fun `secrets are not echoed in error messages`() {
        val msg = err("""{"sipbridge":1,"cid":"abc.access","pw":"topsecret","mode":"X"}""")
        assertTrue(!msg.contains("topsecret"))
    }

    @Test
    fun `null values are treated as absent`() {
        val p = ok("""{"sipbridge":1,"user":"2106","pw":null}""")
        assertNull(p.sipPassword)
    }
}

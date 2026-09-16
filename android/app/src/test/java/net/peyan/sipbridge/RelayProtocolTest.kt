package net.peyan.sipbridge

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

/** docs/PROTOCOL.md v1 の encode/decode テスト。 */
class RelayProtocolTest {

    @Test
    fun `hello with pending call parses`() {
        val json = """{
            "t":"hello","relayVersion":"0.1.0","extension":"101","registered":true,
            "call":{"callId":"c1","direction":"in","state":"ringing","from":"102","display":"test","pt":0,"startedAt":123},
            "serverTime":456
        }"""
        val msg = RelayProtocol.parseRelayMessage(json)
        assertTrue(msg is RelayProtocol.RelayMsg.HelloMsg)
        val h = (msg as RelayProtocol.RelayMsg.HelloMsg).v
        assertEquals("0.1.0", h.relayVersion)
        assertEquals("101", h.extension)
        assertTrue(h.registered)
        val call = h.call!!
        assertEquals("c1", call.callId)
        assertEquals("102", call.from)
        assertEquals(456L, h.serverTime)
    }

    @Test
    fun `hello with null call parses`() {
        val msg = RelayProtocol.parseRelayMessage(
            """{"t":"hello","relayVersion":"x","extension":"101","registered":false,"call":null,"serverTime":0}"""
        ) as RelayProtocol.RelayMsg.HelloMsg
        assertNull(msg.v.call)
    }

    @Test
    fun registration() {
        val msg = RelayProtocol.parseRelayMessage("""{"t":"registration","ok":true,"detail":"ok"}""")
        val r = (msg as RelayProtocol.RelayMsg.RegistrationMsg).v
        assertTrue(r.ok)
        assertEquals("ok", r.detail)
    }

    @Test
    fun incoming() {
        val msg = RelayProtocol.parseRelayMessage(
            """{"t":"incoming","callId":"c9","from":"102","display":"Bob","pt":8}"""
        ) as RelayProtocol.RelayMsg.IncomingMsg
        assertEquals("c9", msg.v.callId)
        assertEquals("102", msg.v.from)
        assertEquals("Bob", msg.v.display)
        assertEquals(8, msg.v.pt)
    }

    @Test
    fun ringingAnsweredEnded() {
        val r = RelayProtocol.parseRelayMessage("""{"t":"ringing","callId":"c1","early":true}""")
        assertTrue((r as RelayProtocol.RelayMsg.RingingMsg).v.early)
        val a = RelayProtocol.parseRelayMessage("""{"t":"answered","callId":"c1","pt":0}""")
        assertEquals("c1", (a as RelayProtocol.RelayMsg.AnsweredMsg).v.callId)
        val e = RelayProtocol.parseRelayMessage("""{"t":"ended","callId":"c1","reason":"bye","code":200}""")
        assertEquals("bye", (e as RelayProtocol.RelayMsg.EndedMsg).v.reason)
        assertEquals(200, e.v.code)
    }

    @Test
    fun errorAndPong() {
        val e = RelayProtocol.parseRelayMessage("""{"t":"error","code":"bad","message":"m"}""")
        assertEquals("bad", (e as RelayProtocol.RelayMsg.ErrorMsg).v.code)
        val p = RelayProtocol.parseRelayMessage("""{"t":"pong","ts":99}""")
        assertEquals(99L, (p as RelayProtocol.RelayMsg.PongMsg).v.ts)
    }

    @Test
    fun unknownTypeThrows() {
        try {
            RelayProtocol.parseRelayMessage("""{"t":"future","x":1}""")
            fail("expected IllegalArgumentException")
        } catch (e: IllegalArgumentException) {
            // ok
        }
    }

    @Test
    fun appToRelayBuilders() {
        var o = JSONObject(RelayProtocol.buildAnswer("c1", 0))
        assertEquals("answer", o.getString("t"))
        assertEquals("c1", o.getString("callId"))
        assertEquals(0, o.getInt("pt"))

        // pt 省略時はキーが無い
        o = JSONObject(RelayProtocol.buildAnswer("c1"))
        assertEquals("answer", o.getString("t"))
        assertTrue(!o.has("pt"))

        o = JSONObject(RelayProtocol.buildReject("c1"))
        assertEquals(486, o.optInt("code", 486))

        o = JSONObject(RelayProtocol.buildReject("c1", 603))
        assertEquals(603, o.getInt("code"))

        o = JSONObject(RelayProtocol.buildHangup("c1"))
        assertEquals("hangup", o.getString("t"))

        o = JSONObject(RelayProtocol.buildDial("102"))
        assertEquals("102", o.getString("to"))

        o = JSONObject(RelayProtocol.buildDtmf("c1", "123#"))
        assertEquals("123#", o.getString("digits"))

        o = JSONObject(RelayProtocol.buildRegisterPush("fcm", "tok"))
        assertEquals("fcm", o.getString("provider"))
        assertEquals("tok", o.getString("token"))

        o = JSONObject(RelayProtocol.buildPing(12345L))
        assertEquals(12345L, o.getLong("ts"))
    }

    @Test
    fun `sip_account builder`() {
        var o = JSONObject(RelayProtocol.buildSipAccount("101", "secret", "display"))
        assertEquals("sip_account", o.getString("t"))
        assertEquals("101", o.getString("user"))
        assertEquals("secret", o.getString("password"))
        assertEquals("display", o.getString("display"))
        // display 省略時は空文字
        o = JSONObject(RelayProtocol.buildSipAccount("101", "secret"))
        assertEquals("", o.getString("display"))
    }

    @Test
    fun `hello with account parses`() {
        val msg = RelayProtocol.parseRelayMessage(
            """{"t":"hello","relayVersion":"0.2.0","extension":"101","account":"101","registered":true,"call":null,"serverTime":1}"""
        ) as RelayProtocol.RelayMsg.HelloMsg
        assertEquals("101", msg.v.account)
        assertEquals("101", msg.v.extension)
    }

    @Test
    fun `hello without account falls back to extension`() {
        // account キー無し (旧 relay) → extension を採用する
        val msg = RelayProtocol.parseRelayMessage(
            """{"t":"hello","relayVersion":"0.1.0","extension":"101","registered":true,"call":null,"serverTime":1}"""
        ) as RelayProtocol.RelayMsg.HelloMsg
        assertEquals("101", msg.v.account)
        // どちらも空なら空のまま (SIP アカウント未設定の判定用)
        val empty = RelayProtocol.parseRelayMessage(
            """{"t":"hello","relayVersion":"0.2.0","extension":"","registered":false,"call":null,"serverTime":1}"""
        ) as RelayProtocol.RelayMsg.HelloMsg
        assertEquals("", empty.v.account)
    }

    @Test
    fun normalizeRelayUrl() {
        assertEquals("wss://relay.example.com/v1/session", normalizeRelayUrl("wss://relay.example.com"))
        assertEquals("wss://relay.example.com/v1/session", normalizeRelayUrl("relay.example.com"))
        assertEquals("wss://relay.example.com/v1/session", normalizeRelayUrl("https://relay.example.com/"))
        assertEquals("ws://192.168.1.10:8080/v1/session", normalizeRelayUrl("http://192.168.1.10:8080"))
        // パス付きはそのまま
        assertEquals(
            "wss://relay.example.com/v1/session",
            normalizeRelayUrl("wss://relay.example.com/v1/session")
        )
        try {
            normalizeRelayUrl("  ")
            fail("expected IllegalArgumentException")
        } catch (e: IllegalArgumentException) {
            // ok
        }
    }
}

package io.github.tmlksu.sipbridge

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
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
        // 平文はループバック (adb reverse 試験用) のみ
        assertEquals("ws://127.0.0.1:18080/v1/session", normalizeRelayUrl("http://127.0.0.1:18080", allowLoopbackCleartext = true))
        assertEquals("ws://localhost:18080/v1/session", normalizeRelayUrl("ws://localhost:18080/", allowLoopbackCleartext = true))
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
        // #43: 平文の LAN 宛ては接続先として受け付けない (以前は ws://192.168.1.10:8080/v1/session)
        try {
            normalizeRelayUrl("http://192.168.1.10:8080")
            fail("expected IllegalArgumentException")
        } catch (e: IllegalArgumentException) {
            // ok
        }
    }

    @Test
    fun `checkRelayUrl rejects cleartext to non-loopback hosts`() {
        assertEquals(RelayUrlCheck.Cleartext("192.168.1.10"), checkRelayUrl("ws://192.168.1.10:8080", allowLoopbackCleartext = true))
        assertEquals(RelayUrlCheck.Cleartext("relay.example.com"), checkRelayUrl("http://relay.example.com", allowLoopbackCleartext = true))
        assertEquals(RelayUrlCheck.Cleartext("relay.example.com"), checkRelayUrl("WS://Relay.Example.com", allowLoopbackCleartext = true))
        // 127.0.0.1 以外の 127/8 や 0.0.0.0 はループバック扱いしない
        assertEquals(RelayUrlCheck.Cleartext("127.0.0.2"), checkRelayUrl("ws://127.0.0.2", allowLoopbackCleartext = true))
        assertEquals(RelayUrlCheck.Cleartext("0.0.0.0"), checkRelayUrl("ws://0.0.0.0:18080", allowLoopbackCleartext = true))
        // userinfo やバックスラッシュでホスト判定をすり抜けさせない (OkHttp と同じ解釈)
        assertEquals(RelayUrlCheck.Cleartext("evil.example"), checkRelayUrl("ws://127.0.0.1@evil.example", allowLoopbackCleartext = true))
        assertEquals(RelayUrlCheck.Cleartext("evil.example"), checkRelayUrl("ws://evil.example\\@127.0.0.1", allowLoopbackCleartext = true))
        assertEquals(RelayUrlCheck.Cleartext("localhost.evil.example"), checkRelayUrl("ws://localhost.evil.example", allowLoopbackCleartext = true))
    }

    @Test
    fun `checkRelayUrl accepts tls and loopback cleartext`() {
        assertEquals(RelayUrlCheck.Ok("wss://relay.example.com/v1/session"), checkRelayUrl("relay.example.com", allowLoopbackCleartext = true))
        assertEquals(RelayUrlCheck.Ok("wss://relay.example.com/v1/session"), checkRelayUrl("HTTPS://relay.example.com", allowLoopbackCleartext = true))
        assertEquals(RelayUrlCheck.Ok("wss://192.168.1.10:8443/v1/session"), checkRelayUrl("wss://192.168.1.10:8443", allowLoopbackCleartext = true))
        assertEquals(RelayUrlCheck.Ok("ws://127.0.0.1:18080/v1/session"), checkRelayUrl("ws://127.0.0.1:18080", allowLoopbackCleartext = true))
        assertEquals(RelayUrlCheck.Ok("ws://LOCALHOST:18080/v1/session"), checkRelayUrl("ws://LOCALHOST:18080", allowLoopbackCleartext = true))
        // ::1 は debug の network_security_config に無いので許可しない (N1)
        assertEquals(
            RelayUrlCheck.Cleartext("::1"),
            checkRelayUrl("ws://[::1]:18080", allowLoopbackCleartext = true)
        )
    }

    @Test
    fun `release build rejects loopback cleartext too`() {
        // release (BuildConfig.DEBUG=false) はループバックの ws:// も保存前に弾く (N2)
        assertEquals(
            RelayUrlCheck.Cleartext("127.0.0.1", loopbackAllowed = false),
            checkRelayUrl("ws://127.0.0.1:18080", allowLoopbackCleartext = false)
        )
        assertEquals(
            RelayUrlCheck.Cleartext("localhost", loopbackAllowed = false),
            checkRelayUrl("http://localhost", allowLoopbackCleartext = false)
        )
        assertEquals(
            RelayUrlCheck.Ok("wss://relay.example.com/v1/session"),
            checkRelayUrl("relay.example.com", allowLoopbackCleartext = false)
        )
        try {
            normalizeRelayUrl("ws://127.0.0.1:18080", allowLoopbackCleartext = false)
            fail("expected IllegalArgumentException")
        } catch (e: IllegalArgumentException) {
            // ok
        }
    }

    @Test
    fun `checkRelayUrl reports empty and malformed input`() {
        assertEquals(RelayUrlCheck.Empty, checkRelayUrl(""))
        assertEquals(RelayUrlCheck.Empty, checkRelayUrl("   "))
        assertTrue(checkRelayUrl("ftp://relay.example.com") is RelayUrlCheck.Invalid)
        assertTrue(checkRelayUrl("wss://") is RelayUrlCheck.Invalid)
        assertTrue(checkRelayUrl("wss://bad host") is RelayUrlCheck.Invalid)
    }

    @Test
    fun `cleartext policy failure is recognized`() {
        assertTrue(
            RelayClient.isCleartextBlocked(
                java.net.UnknownServiceException(
                    "CLEARTEXT communication to 192.168.1.10 not permitted by network security policy"
                )
            )
        )
        assertFalse(RelayClient.isCleartextBlocked(java.net.UnknownServiceException("other")))
        assertFalse(RelayClient.isCleartextBlocked(java.io.IOException("CLEARTEXT")))
    }

    @Test
    fun loopbackHosts() {
        assertTrue(isLoopbackHost("127.0.0.1"))
        assertTrue(isLoopbackHost("localhost"))
        assertTrue(isLoopbackHost("LocalHost"))
        assertFalse(isLoopbackHost("::1"))
        assertFalse(isLoopbackHost("[::1]"))
        assertFalse(isLoopbackHost("127.0.0.2"))
        assertFalse(isLoopbackHost("192.168.1.10"))
        assertFalse(isLoopbackHost("localhost.example.com"))
    }

    @Test
    fun `call_stats has agreed schema`() {
        val r = CallStatsReport(
            callId = "c1", durMs = 123456, net = "wifi",
            rxPkts = 6000, rxGaps = 1, rxReorder = 2, rxJitterMs = 3, rxMaxGapMs = 480,
            rxStall100 = 4, rxStall200 = 2, rxStall500 = 0, rxReconnects = 1,
            jbUnderrun = 7, jbOverflow = 8, playUnderrun = 9,
            txPkts = 6100, txDrop = 10, txLost = 12, txLateMs = 11,
            rttStartMs = 40, rttEndMs = -1
        )
        val o = JSONObject(RelayProtocol.buildCallStats(r))
        assertEquals("call_stats", o.getString("t"))
        assertEquals("c1", o.getString("callId"))
        assertEquals(123456L, o.getLong("dur"))
        assertEquals("wifi", o.getString("net"))
        val rx = o.getJSONObject("rx")
        assertEquals(
            setOf("pkts", "gaps", "reorder", "jitterMs", "maxGapMs", "stall100", "stall200", "stall500", "reconnects"),
            rx.keySet()
        )
        assertEquals(6000L, rx.getLong("pkts"))
        assertEquals(1L, rx.getLong("gaps"))
        assertEquals(2L, rx.getLong("reorder"))
        assertEquals(3, rx.getInt("jitterMs"))
        assertEquals(480L, rx.getLong("maxGapMs"))
        assertEquals(4L, rx.getLong("stall100"))
        assertEquals(2L, rx.getLong("stall200"))
        assertEquals(0L, rx.getLong("stall500"))
        assertEquals(1, rx.getInt("reconnects"))
        val jb = o.getJSONObject("jb")
        assertEquals(setOf("underrun", "overflow"), jb.keySet())
        assertEquals(7L, jb.getLong("underrun"))
        assertEquals(8L, jb.getLong("overflow"))
        assertEquals(9, o.getInt("playUnderrun"))
        val tx = o.getJSONObject("tx")
        assertEquals(setOf("pkts", "drop", "lost", "lateMs"), tx.keySet())
        assertEquals(6100L, tx.getLong("pkts"))
        assertEquals(10L, tx.getLong("drop"))
        assertEquals(12L, tx.getLong("lost"))
        assertEquals(11L, tx.getLong("lateMs"))
        val rtt = o.getJSONArray("rttMs")
        assertEquals(2, rtt.length())
        assertEquals(40L, rtt.getLong(0))
        assertEquals(-1L, rtt.getLong(1))
        assertEquals(
            setOf("t", "callId", "dur", "net", "rx", "jb", "playUnderrun", "tx", "rttMs"),
            o.keySet()
        )
    }

    @Test
    fun `call_stats is only for relay 0_3_0 or later`() {
        assertTrue(RelayProtocol.supportsCallStats("0.3.0"))
        assertTrue(RelayProtocol.supportsCallStats("0.4.2"))
        assertTrue(!RelayProtocol.supportsCallStats("0.2.0"))
        assertTrue(!RelayProtocol.supportsCallStats(""))
        assertTrue(!RelayProtocol.supportsCallStats("unknown"))
    }
}

package io.github.tmlksu.sipbridge

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * OutgoingCallPolicy の JVM テスト (R5)。
 * 発信の成否判定は Android API に依存しない純関数のため JVM で検証する。
 */
class OutgoingCallPolicyTest {

    @Test
    fun `発信中の dial 失敗系エラーは発信を畳む`() {
        listOf("dial_failed", "no_account").forEach { code ->
            assertTrue(code, OutgoingCallPolicy.shouldFailOutgoingOnError(code, outgoing = true, ringing = true))
        }
    }

    @Test
    fun `sip_account への応答エラーでは発信を畳まない`() {
        // 以前の結び付けのまま dial は成立しうる。畳むと relay 側の呼だけが残る。
        listOf("account_failed", "account_password_mismatch").forEach { code ->
            assertFalse(code, OutgoingCallPolicy.shouldFailOutgoingOnError(code, outgoing = true, ringing = true))
        }
    }

    @Test
    fun `発信中でない・無関係なコードでは畳まない`() {
        // 発信中ではない
        assertFalse(OutgoingCallPolicy.shouldFailOutgoingOnError("dial_failed", outgoing = false, ringing = true))
        assertFalse(OutgoingCallPolicy.shouldFailOutgoingOnError("dial_failed", outgoing = true, ringing = false))
        assertFalse(OutgoingCallPolicy.shouldFailOutgoingOnError("no_account", outgoing = false, ringing = false))
        // 発信失敗系以外のコード (通話中などに来ても発信表示は畳まない)
        assertFalse(OutgoingCallPolicy.shouldFailOutgoingOnError("bad_message", outgoing = true, ringing = true))
        assertFalse(
            OutgoingCallPolicy.shouldFailOutgoingOnError("answered_elsewhere", outgoing = true, ringing = true)
        )
        assertFalse(OutgoingCallPolicy.shouldFailOutgoingOnError("", outgoing = true, ringing = true))
    }

    @Test
    fun `未接続または hello 未処理なら dial を積む`() {
        assertTrue(OutgoingCallPolicy.shouldQueueDialForHello(connected = false, helloProcessed = false))
        assertTrue(OutgoingCallPolicy.shouldQueueDialForHello(connected = false, helloProcessed = true))
        // R5: 接続直後で最初の hello 未処理なら積む (sip_account 前の dial 防止)
        assertTrue(OutgoingCallPolicy.shouldQueueDialForHello(connected = true, helloProcessed = false))
        // hello 処理済みで接続中だけすぐ送る
        assertFalse(OutgoingCallPolicy.shouldQueueDialForHello(connected = true, helloProcessed = true))
    }

    @Test
    fun `callId が空のままならウォッチドッグで畳む`() {
        assertTrue(OutgoingCallPolicy.shouldTimeoutOutgoing(outgoing = true, ringing = true, hasCallId = false))
        // callId が付けば (ringing/answered 受信) 終了させない
        assertFalse(OutgoingCallPolicy.shouldTimeoutOutgoing(outgoing = true, ringing = true, hasCallId = true))
        assertFalse(OutgoingCallPolicy.shouldTimeoutOutgoing(outgoing = true, ringing = false, hasCallId = false))
        assertFalse(OutgoingCallPolicy.shouldTimeoutOutgoing(outgoing = false, ringing = true, hasCallId = false))
    }

    @Test
    fun `ウォッチドッグ猶予は 30 秒`() {
        assertEquals(30_000L, OutgoingCallPolicy.OUTGOING_WATCHDOG_MS)
    }
}

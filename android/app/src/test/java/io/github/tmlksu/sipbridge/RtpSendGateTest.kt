package io.github.tmlksu.sipbridge

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** 上り RTP のバックプレッシャー判定 (RelayClient.RtpSendGate) のテスト。 */
class RtpSendGateTest {

    @Test
    fun `does not drop below threshold`() {
        val gate = RelayClient.RtpSendGate(dropBytes = 8192)
        assertFalse(gate.shouldDrop(0))
        assertFalse(gate.shouldDrop(8191))
        assertEquals(0L, gate.dropped)
        // 捨てていない送信成功では回復報告も無い
        assertEquals(0L, gate.onSent())
    }

    @Test
    fun `drops at and above threshold`() {
        val gate = RelayClient.RtpSendGate(dropBytes = 8192)
        assertTrue(gate.shouldDrop(8192))
        assertTrue(gate.shouldDrop(16_000))
        assertEquals(2L, gate.dropped)
    }

    @Test
    fun `logs every 50 drops`() {
        val gate = RelayClient.RtpSendGate(dropBytes = 100, logEvery = 50)
        repeat(120) { i ->
            assertTrue(gate.shouldDrop(1000))
            // 1, 51, 101 件目 (relay 側 rtp.go と同じ「%50 == 1」) だけ true
            assertEquals(i % 50 == 0, gate.shouldLogDrop())
        }
        assertEquals(120L, gate.dropped)
    }

    @Test
    fun `logEvery 1 logs every drop`() {
        val gate = RelayClient.RtpSendGate(dropBytes = 100, logEvery = 1)
        repeat(5) {
            assertTrue(gate.shouldDrop(1000))
            assertTrue(gate.shouldLogDrop())
        }
    }

    @Test
    fun `no log before any drop`() {
        val gate = RelayClient.RtpSendGate(dropBytes = 100, logEvery = 1)
        assertFalse(gate.shouldLogDrop())
    }

    @Test(expected = IllegalArgumentException::class)
    fun `logEvery must be positive`() {
        RelayClient.RtpSendGate(logEvery = 0)
    }

    @Test
    fun `reports cumulative drops once on recovery`() {
        val gate = RelayClient.RtpSendGate(dropBytes = 100)
        assertTrue(gate.shouldDrop(1000))
        assertTrue(gate.shouldDrop(1000))
        // 送信が回復したら累計を 1 回だけ返す
        assertEquals(2L, gate.onSent())
        assertEquals(0L, gate.onSent())
        // その後の破棄はまた報告対象になる
        assertTrue(gate.shouldDrop(1000))
        assertEquals(3L, gate.onSent())
    }

    @Test
    fun `reset clears count per connection`() {
        val gate = RelayClient.RtpSendGate(dropBytes = 100)
        assertTrue(gate.shouldDrop(1000))
        gate.reset()
        assertEquals(0L, gate.dropped)
        assertEquals(0L, gate.onSent())
        assertFalse(gate.shouldDrop(0))
    }

    @Test
    fun `default thresholds`() {
        // カーネル送信バッファ 16KiB (Linux 実効 32KiB) の上に OkHttp キュー 8KiB ≒ 約 1 秒分
        assertEquals(16 * 1024, RelayClient.RELAY_SOCKET_SNDBUF_BYTES)
        assertEquals(8 * 1024, RelayClient.RTP_QUEUE_DROP_BYTES)
        assertEquals(50L, RelayClient.RTP_DROP_LOG_EVERY)
    }
}

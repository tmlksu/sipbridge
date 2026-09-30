package io.github.tmlksu.sipbridge

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** 通話品質の計測 (issue #26) のテスト。 */
class CallStatsTest {

    private val ms = 1_000_000L

    /** seq/ts を 20 ms 刻みで進めて [n] パケット、到着も 20 ms 刻みで流す。 */
    private fun RtpRxStats.feed(startSeq: Int, startTs: Long, startNs: Long, n: Int, ssrc: Long = 1L) {
        for (i in 0 until n) {
            onPacket((startSeq + i) and 0xFFFF, (startTs + 160L * i) and 0xFFFFFFFFL, ssrc, startNs + 20 * ms * i)
        }
    }

    // ---------- RtpRxStats ----------

    @Test
    fun `steady stream has no gaps, reorder, stalls or jitter`() {
        val s = RtpRxStats()
        s.feed(100, 1000, 0, 500)
        assertEquals(500L, s.pkts)
        assertEquals(0L, s.gaps)
        assertEquals(0L, s.reorder)
        assertEquals(0, s.jitterMs)
        assertEquals(20L, s.maxGapMs)
        assertEquals(0L, s.stall100)
    }

    @Test
    fun `missing seq counts gaps`() {
        val s = RtpRxStats()
        s.onPacket(10, 0, 1, 0)
        s.onPacket(11, 160, 1, 20 * ms)
        s.onPacket(14, 640, 1, 80 * ms) // 12, 13 欠け
        assertEquals(2L, s.gaps)
        assertEquals(0L, s.reorder)
    }

    @Test
    fun `seq wrap around 65535 is not a gap`() {
        val s = RtpRxStats()
        s.feed(65530, 0, 0, 12) // 65530..65535, 0..5
        assertEquals(0L, s.gaps)
        assertEquals(0L, s.reorder)
        s.onPacket(7, 160L * 13, 1, 20 * ms * 13) // 6 が欠け
        assertEquals(1L, s.gaps)
    }

    @Test
    fun `gap across wrap is counted`() {
        val s = RtpRxStats()
        s.onPacket(65534, 0, 1, 0)
        s.onPacket(1, 480, 1, 60 * ms) // 65535, 0 が欠け
        assertEquals(2L, s.gaps)
    }

    @Test
    fun `late and duplicate packets count as reorder`() {
        val s = RtpRxStats()
        s.onPacket(1, 0, 1, 0)
        s.onPacket(3, 320, 1, 40 * ms) // 2 が欠け (gaps=1)
        s.onPacket(2, 160, 1, 41 * ms) // 遅れて到着
        s.onPacket(3, 320, 1, 42 * ms) // 重複
        assertEquals(1L, s.gaps)
        assertEquals(2L, s.reorder)
        s.onPacket(4, 480, 1, 60 * ms)
        assertEquals(1L, s.gaps)
    }

    @Test
    fun `large seq jump or ssrc change resyncs without counting`() {
        val s = RtpRxStats()
        s.feed(100, 0, 0, 10)
        s.onPacket(20000, 5000, 1, 220 * ms) // 大きく飛んだ
        assertEquals(0L, s.gaps)
        s.onPacket(20001, 5160, 1, 240 * ms)
        assertEquals(0L, s.gaps)
        s.onPacket(5, 0, 2, 260 * ms) // SSRC 変更
        s.onPacket(6, 160, 2, 280 * ms)
        assertEquals(0L, s.gaps)
        assertEquals(0L, s.reorder)
        assertEquals(14L, s.pkts)
    }

    @Test
    fun `stall buckets are cumulative by threshold`() {
        val s = RtpRxStats()
        var t = 0L
        var seq = 0
        fun pkt(afterMs: Long) {
            t += afterMs * ms
            s.onPacket(seq, 160L * seq, 1, t)
            seq++
        }
        pkt(0)
        pkt(20)
        pkt(150) // >100
        pkt(250) // >100, >200
        pkt(600) // >100, >200, >500
        pkt(100) // ちょうど 100 は超えていない
        assertEquals(3L, s.stall100)
        assertEquals(2L, s.stall200)
        assertEquals(1L, s.stall500)
        assertEquals(600L, s.maxGapMs)
    }

    @Test
    fun `jitter follows RFC 3550 estimator`() {
        val s = RtpRxStats()
        // 20 ms 間隔の送信を、到着だけ 10 ms 早い/遅いに交互に揺らす → |D| = 20 ms 前後
        for (i in 0 until 400) {
            val wobble = if (i % 2 == 0) 0L else 10L * ms
            s.onPacket(i, 160L * i, 1, 20 * ms * i + wobble)
        }
        // |D| は毎回 10 ms。J は 10 ms に収束する。
        assertEquals(10, s.jitterMs)
    }

    @Test
    fun `rtp timestamp wrap does not explode jitter`() {
        val s = RtpRxStats()
        s.feed(0, 0xFFFFFFFFL - 800, 0, 20)
        assertEquals(0, s.jitterMs)
    }

    // ---------- RxStallMonitor ----------

    private val sec = 1_000_000_000L

    @Test
    fun `stall monitor does not fire before first rtp`() {
        val m = RxStallMonitor(3 * sec)
        var calls = 0
        assertFalse(m.poll(10 * sec) { calls++; true })
        assertFalse(m.poll(100 * sec) { calls++; true })
        assertEquals(0, calls)
        assertEquals(0, m.reconnects)
    }

    @Test
    fun `stall monitor fires once per stall and resets on next rtp`() {
        val m = RxStallMonitor(3 * sec)
        var calls = 0
        val act = { calls++; true }
        m.onRx(1 * sec)
        assertFalse(m.poll(3 * sec, act)) // 2 秒 → まだ
        assertTrue(m.poll(4 * sec, act)) // 3 秒 → 発動
        // 保留などで RTP が止まったままなら、何秒経っても再発動しない
        assertFalse(m.poll(5 * sec, act))
        assertFalse(m.poll(60 * sec, act))
        assertEquals(1, calls)
        // RTP が戻ってから再び途絶えたら、もう 1 回
        m.onRx(61 * sec)
        assertFalse(m.poll(63 * sec, act))
        assertTrue(m.poll(64 * sec, act))
        assertEquals(2, calls)
        assertEquals(2, m.reconnects)
    }

    @Test
    fun `steady rtp never fires`() {
        val m = RxStallMonitor(3 * sec)
        var t = 0L
        repeat(10_000) {
            t += 20_000_000L
            m.onRx(t)
            assertFalse(m.poll(t + 10_000_000L) { true })
        }
        assertEquals(0, m.reconnects)
    }

    @Test
    fun `stall monitor retries when reconnect could not be issued`() {
        val m = RxStallMonitor(3 * sec)
        m.onRx(1 * sec)
        assertFalse(m.poll(5 * sec) { false }) // 未接続で張り直せず → 未発動のまま
        assertEquals(0, m.reconnects)
        assertTrue(m.poll(5 * sec) { true })
        assertEquals(1, m.reconnects)
    }

    @Test
    fun `fresh connection gets grace before firing`() {
        val m = RxStallMonitor(3 * sec)
        m.onRx(1 * sec)
        // 別経路 (網切替) で張り直し、8 秒時点で新しい接続が開いた
        m.onConnected(8 * sec)
        assertFalse(m.poll(9 * sec) { true }) // 最終受信からは 8 秒だが、接続から 1 秒
        assertFalse(m.poll(10_900_000_000L) { true })
        assertTrue(m.poll(11 * sec) { true }) // 接続から 3 秒 RTP が来ない → 1 回だけ
        m.onConnected(12 * sec)
        assertFalse(m.poll(20 * sec) { true }) // 同じ途絶では再発動しない
        assertEquals(1, m.reconnects)
    }

    // ---------- CallRtt ----------

    @Test
    fun `rtt matches start and end pings`() {
        val r = CallRtt()
        r.markStartPing(1000)
        assertTrue(r.onPong(1000, 1045))
        assertFalse(r.endPingSent)
        r.markEndPing(9000)
        assertTrue(r.endPingSent)
        assertFalse(r.onPong(1234, 9100)) // 別の ping
        assertTrue(r.onPong(9000, 9080))
        assertEquals(45L, r.startMs)
        assertEquals(80L, r.endMs)
    }

    @Test
    fun `rtt defaults to minus one`() {
        val r = CallRtt()
        assertEquals(-1L, r.startMs)
        assertEquals(-1L, r.endMs)
        assertFalse(r.onPong(-1, 10)) // 未送信の ts (-1) に一致させない
    }

    // ---------- relayVersionAtLeast ----------

    @Test
    fun `relay version comparison`() {
        assertTrue(relayVersionAtLeast("0.3.0", "0.3.0"))
        assertTrue(relayVersionAtLeast("0.3.1", "0.3.0"))
        assertTrue(relayVersionAtLeast("0.10.0", "0.3.0"))
        assertTrue(relayVersionAtLeast("1.0.0", "0.3.0"))
        assertTrue(relayVersionAtLeast("v0.3.0", "0.3.0"))
        assertTrue(relayVersionAtLeast("0.3.0+abc", "0.3.0"))
        assertTrue(relayVersionAtLeast("0.4.0-rc1", "0.3.0"))
        assertFalse(relayVersionAtLeast("0.3.0-rc1", "0.3.0"))
        assertFalse(relayVersionAtLeast("0.2.0", "0.3.0"))
        assertFalse(relayVersionAtLeast("0.2.99", "0.3.0"))
        assertFalse(relayVersionAtLeast("", "0.3.0"))
        assertFalse(relayVersionAtLeast("0.3", "0.3.0"))
        assertFalse(relayVersionAtLeast("x.y.z", "0.3.0"))
        assertFalse(relayVersionAtLeast("dev", "0.3.0"))
    }
}

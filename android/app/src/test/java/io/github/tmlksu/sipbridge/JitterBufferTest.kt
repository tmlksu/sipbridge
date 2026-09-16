package io.github.tmlksu.sipbridge

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Test

/** ジッタバッファ (5 フレーム開始/上限 20) のテスト。 */
class JitterBufferTest {

    private fun frame(v: Short) = ShortArray(160) { v }

    @Test
    fun `warms up after 5 frames`() {
        val jb = JitterBuffer(warmupFrames = 5, maxFrames = 20)
        repeat(4) {
            jb.offer(frame(it.toShort()))
            assertNull(jb.pollReady())
        }
        jb.offer(frame(4))
        val first = jb.pollReady()
        assertNotNull(first)
        assertEquals(0, first!![0].toInt())
    }

    @Test
    fun `order preserved`() {
        val jb = JitterBuffer()
        repeat(6) { jb.offer(frame(it.toShort())) }
        repeat(6) { i ->
            assertEquals(i.toShort(), jb.pollReady()!![0])
        }
        assertNull(jb.pollReady())
    }

    @Test
    fun `drops oldest beyond max`() {
        val jb = JitterBuffer(warmupFrames = 5, maxFrames = 20)
        var dropped = 0
        repeat(25) { dropped += jb.offer(frame(it.toShort())) }
        assertEquals(5, dropped)
        assertEquals(20, jb.size())
        // 最初の 5 フレームが捨てられ、5 から始まる
        assertEquals(5.toShort(), jb.pollReady()!![0])
    }

    @Test
    fun clearResets() {
        val jb = JitterBuffer()
        repeat(5) { jb.offer(frame(1)) }
        assertNotNull(jb.pollReady())
        jb.clear()
        assertNull(jb.pollReady())
        assertEquals(0, jb.size())
    }
}

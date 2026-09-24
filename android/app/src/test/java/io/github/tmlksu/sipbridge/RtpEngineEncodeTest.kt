package io.github.tmlksu.sipbridge

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Test

/** 送信側の直接エンコード (RtpEngine.buildRtpPacket) が従来経路と等価なことのテスト。 */
class RtpEngineEncodeTest {

    private fun pcm(n: Int = 160): ShortArray = ShortArray(n) { i -> ((i * 257) % 65536 - 32768).toShort() }

    /** 従来経路 (中間ペイロード配列 + RtpPacket.build) での期待値。 */
    private fun legacyEncode(e: RtpEngine, pcm: ShortArray, count: Int, encodeMuted: Boolean): ByteArray {
        val gain = e.micGain
        val pay = ByteArray(count)
        val isPcmu = e.payloadType != 8
        for (i in 0 until count) {
            var s = (pcm[i] * gain).toInt().coerceIn(-32768, 32767)
            if (encodeMuted && e.muted) s = 0
            pay[i] = if (isPcmu) G711.linearToUlaw(s) else G711.linearToAlaw(s)
        }
        return RtpPacket.build(0, 0L, 0L, e.payloadType, pay)
    }

    @Test
    fun `pcmu matches legacy path`() {
        val e = RtpEngine(payloadType = 0)
        val frame = pcm()
        val got = e.buildRtpPacket(frame, frame.size, encodeMuted = true)
        val want = legacyEncode(e, frame, frame.size, encodeMuted = true)
        // ヘッダ (seq/ts/ssrc は乱数のため別) を除いたペイロード部が一致する
        assertEquals(172, got.size)
        assertArrayEquals(
            want.copyOfRange(RtpPacket.HEADER_SIZE, want.size),
            got.copyOfRange(RtpPacket.HEADER_SIZE, got.size)
        )
        // ヘッダは parse できる
        val parsed = RtpPacket.parse(got)!!
        assertEquals(0, parsed.payloadType)
        assertEquals(160, parsed.payload.size)
    }

    @Test
    fun `pcma and mute match legacy path`() {
        val e = RtpEngine(payloadType = 8).apply { muted = true }
        val frame = pcm()
        val got = e.buildRtpPacket(frame, frame.size, encodeMuted = true)
        val want = legacyEncode(e, frame, frame.size, encodeMuted = true)
        assertArrayEquals(
            want.copyOfRange(RtpPacket.HEADER_SIZE, want.size),
            got.copyOfRange(RtpPacket.HEADER_SIZE, got.size)
        )
        // ミュート時は A-law の無音符号 (linearToAlaw(0) = 0xD5) の連続になる
        val parsed = RtpPacket.parse(got)!!
        assertEquals(8, parsed.payloadType)
        assertEquals(0xD5.toByte(), G711.linearToAlaw(0))
        assertTrueAll("muted payload", parsed.payload) { it == 0xD5.toByte() }
    }

    @Test
    fun `pcmu mute payload is ulaw silence`() {
        val e = RtpEngine(payloadType = 0).apply { muted = true }
        val frame = pcm()
        val got = e.buildRtpPacket(frame, frame.size, encodeMuted = true)
        // μ-law の無音符号は linearToUlaw(0) = 0xFF
        assertEquals(0xFF.toByte(), G711.linearToUlaw(0))
        val payload = got.copyOfRange(RtpPacket.HEADER_SIZE, got.size)
        assertEquals(160, payload.size)
        assertTrueAll("muted payload", payload) { it == 0xFF.toByte() }
    }

    @Test
    fun `header bytes are absolute`() {
        for (pt in intArrayOf(0, 8)) {
            val e = RtpEngine(payloadType = pt)
            val got = e.buildRtpPacket(pcm(), 160, encodeMuted = true)
            val seq = e.seq
            val ts = e.timestamp.toLong()
            val ssrc = e.ssrc.toLong()
            val want = byteArrayOf(
                0x80.toByte(), // V=2, P=0, X=0, CC=0
                pt.toByte(), // M=0, PT
                (seq ushr 8).toByte(), seq.toByte(),
                (ts ushr 24).toByte(), (ts ushr 16).toByte(), (ts ushr 8).toByte(), ts.toByte(),
                (ssrc ushr 24).toByte(), (ssrc ushr 16).toByte(), (ssrc ushr 8).toByte(), ssrc.toByte()
            )
            assertArrayEquals(want, got.copyOfRange(0, RtpPacket.HEADER_SIZE))
        }
    }

    @Test
    fun `full scale samples encode to known codes`() {
        // gain 1.0 で 0 / +最大 / -最大 を並べ、G.711 の既知の符号になること
        val frame = shortArrayOf(0, Short.MAX_VALUE, Short.MIN_VALUE)
        val u = RtpEngine(payloadType = 0, micGain = 1.0f).buildRtpPacket(frame, 3, encodeMuted = true)
        assertArrayEquals(
            byteArrayOf(0xFF.toByte(), 0x80.toByte(), 0x00),
            u.copyOfRange(RtpPacket.HEADER_SIZE, u.size)
        )
        val a = RtpEngine(payloadType = 8, micGain = 1.0f).buildRtpPacket(frame, 3, encodeMuted = true)
        assertArrayEquals(
            byteArrayOf(0xD5.toByte(), 0xAA.toByte(), 0x2A),
            a.copyOfRange(RtpPacket.HEADER_SIZE, a.size)
        )
    }

    @Test
    fun `partial frame without mute flag ignores mute`() {
        val e = RtpEngine(payloadType = 0).apply { muted = true }
        val frame = pcm(80)
        // DTMF 置換経路 (encodeMuted=false) ではミュート中でもトーンが残る
        val got = e.buildRtpPacket(frame, 80, encodeMuted = false)
        assertEquals(RtpPacket.HEADER_SIZE + 80, got.size)
        val want = legacyEncode(e, frame, 80, encodeMuted = false)
        assertArrayEquals(
            want.copyOfRange(RtpPacket.HEADER_SIZE, want.size),
            got.copyOfRange(RtpPacket.HEADER_SIZE, got.size)
        )
    }

    @Test
    fun `gain clipping matches legacy path`() {
        val e = RtpEngine(payloadType = 0).apply { micGain = 10.0f }
        val frame = ShortArray(160) { 20000 }
        val got = e.buildRtpPacket(frame, frame.size, encodeMuted = true)
        val want = legacyEncode(e, frame, frame.size, encodeMuted = true)
        assertArrayEquals(
            want.copyOfRange(RtpPacket.HEADER_SIZE, want.size),
            got.copyOfRange(RtpPacket.HEADER_SIZE, got.size)
        )
    }

    private fun assertTrueAll(msg: String, arr: ByteArray, pred: (Byte) -> Boolean) {
        for (b in arr) {
            if (!pred(b)) throw AssertionError("$msg: unexpected byte $b")
        }
    }
}

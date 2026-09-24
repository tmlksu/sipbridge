package io.github.tmlksu.sipbridge

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/** RTP パケット組み立て・分解のラウンドトリップテスト。 */
class RtpPacketTest {

    @Test
    fun roundTrip() {
        val payload = ByteArray(160) { it.toByte() }
        val pkt = RtpPacket.build(
            sequence = 1234,
            timestamp = 567890L,
            ssrc = 0x11223344L,
            payloadType = 0,
            payload = payload
        )
        assertEquals(172, pkt.size)
        val p = RtpPacket.parse(pkt)!!
        assertEquals(1234, p.sequence)
        assertEquals(567890L, p.timestamp)
        assertEquals(0x11223344L, p.ssrc)
        assertEquals(0, p.payloadType)
        assertArrayEquals(payload, p.payload)
    }

    @Test
    fun pcmaAndSequenceWrap() {
        val pkt = RtpPacket.build(0xFFFF, 0L, 1L, 8, byteArrayOf(1, 2, 3), marker = true)
        val p = RtpPacket.parse(pkt)!!
        assertEquals(0xFFFF, p.sequence)
        assertEquals(8, p.payloadType)
        assertEquals(true, p.marker)
        assertEquals(3, p.payload.size)
    }

    @Test
    fun writeHeaderMatchesBuild() {
        val payload = ByteArray(160) { it.toByte() }
        val viaBuild = RtpPacket.build(
            sequence = 4321,
            timestamp = 987654L,
            ssrc = 0xAABBCCDDL,
            payloadType = 8,
            payload = payload,
            marker = true
        )
        // 送信側の直接エンコード経路: パケット配列へペイロードを書いてからヘッダを足す
        val direct = ByteArray(RtpPacket.HEADER_SIZE + payload.size)
        payload.copyInto(direct, RtpPacket.HEADER_SIZE)
        RtpPacket.writeHeader(
            direct,
            sequence = 4321,
            timestamp = 987654L,
            ssrc = 0xAABBCCDDL,
            payloadType = 8,
            marker = true
        )
        assertArrayEquals(viaBuild, direct)
    }

    @Test
    fun writeHeaderRejectsShortBuffer() {
        try {
            RtpPacket.writeHeader(ByteArray(11), 0, 0L, 0L, 0)
            throw AssertionError("12B 未満で失敗すべき")
        } catch (e: IllegalArgumentException) {
            // 期待どおり
        }
    }

    @Test
    fun shortPacketReturnsNull() {
        assertNull(RtpPacket.parse(ByteArray(11)))
        assertNull(RtpPacket.parse(ByteArray(0)))
    }

    @Test
    fun g711Sanity() {
        // 無音付近の往復 (厳密一致は求めない。発散しないことのみ)
        val s: Short = 1000
        val back = G711.ulawToLinear(G711.linearToUlaw(s.toInt()))
        assertEquals(true, kotlin.math.abs(back - s) < 600)
        val backA = G711.alawToLinear(G711.linearToAlaw(s.toInt()))
        assertEquals(true, kotlin.math.abs(backA - s) < 600)
    }
}

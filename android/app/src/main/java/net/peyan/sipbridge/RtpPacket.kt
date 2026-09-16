package net.peyan.sipbridge

/**
 * RTP パケット (12 バイト固定ヘッダ + G.711 ペイロード) の組み立て・分解。
 * PROTOCOL.md: バイナリフレーム = RTP パケットそのまま。Android 依存なし。
 */
object RtpPacket {
    const val HEADER_SIZE = 12
    const val VERSION = 2

    data class Parsed(
        val sequence: Int,
        val timestamp: Long,
        val ssrc: Long,
        val payloadType: Int,
        val marker: Boolean,
        val payload: ByteArray
    )

    fun build(
        sequence: Int,
        timestamp: Long,
        ssrc: Long,
        payloadType: Int,
        payload: ByteArray,
        marker: Boolean = false
    ): ByteArray {
        val out = ByteArray(HEADER_SIZE + payload.size)
        out[0] = (VERSION shl 6).toByte()
        out[1] = ((if (marker) 0x80 else 0) or (payloadType and 0x7F)).toByte()
        out[2] = (sequence ushr 8).toByte()
        out[3] = sequence.toByte()
        out[4] = (timestamp ushr 24).toByte()
        out[5] = (timestamp ushr 16).toByte()
        out[6] = (timestamp ushr 8).toByte()
        out[7] = timestamp.toByte()
        out[8] = (ssrc ushr 24).toByte()
        out[9] = (ssrc ushr 16).toByte()
        out[10] = (ssrc ushr 8).toByte()
        out[11] = ssrc.toByte()
        payload.copyInto(out, HEADER_SIZE)
        return out
    }

    /** 12 バイト未満・バージョン不一致は null。 */
    fun parse(packet: ByteArray): Parsed? {
        if (packet.size < HEADER_SIZE) return null
        if (((packet[0].toInt() and 0xFF) ushr 6) != VERSION) return null
        val marker = (packet[1].toInt() and 0x80) != 0
        val pt = packet[1].toInt() and 0x7F
        val seq = ((packet[2].toInt() and 0xFF) shl 8) or (packet[3].toInt() and 0xFF)
        val ts = ((packet[4].toLong() and 0xFF) shl 24) or
            ((packet[5].toLong() and 0xFF) shl 16) or
            ((packet[6].toLong() and 0xFF) shl 8) or
            (packet[7].toLong() and 0xFF)
        val ssrc = ((packet[8].toLong() and 0xFF) shl 24) or
            ((packet[9].toLong() and 0xFF) shl 16) or
            ((packet[10].toLong() and 0xFF) shl 8) or
            (packet[11].toLong() and 0xFF)
        return Parsed(
            sequence = seq,
            timestamp = ts,
            ssrc = ssrc,
            payloadType = pt,
            marker = marker,
            payload = packet.copyOfRange(HEADER_SIZE, packet.size)
        )
    }
}

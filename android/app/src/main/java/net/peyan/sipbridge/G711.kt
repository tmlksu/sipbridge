package net.peyan.sipbridge

/** G.711 μ-law / A-law 変換。参考: Sipdroid (org.zoolu.sip) の PCM 処理と ITU-T G.711 */
object G711 {
    private const val BIAS = 0x84
    private const val SIGN_BIT = 0x80
    private const val QUANT_MASK = 0x0F
    private const val SEG_MASK = 0x70
    private const val SEG_SHIFT = 4

    private val SEG_END = intArrayOf(
        0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF, 0x1FFF, 0x3FFF, 0x7FFF
    )

    private fun search(v: Int, table: IntArray): Int {
        for (i in table.indices) if (v <= table[i]) return i
        return table.size
    }

    /** 16bit PCM -> μ-law byte */
    fun linearToUlaw(pcm: Int): Byte {
        var p = pcm.coerceIn(-32768, 32767)
        val mask: Int
        if (p < 0) { p = BIAS - p; mask = 0x7F } else { p += BIAS; mask = 0xFF }
        val seg = search(p, SEG_END)
        return if (seg >= 8) {
            (0x7F xor mask).toByte()
        } else {
            val uval = (seg shl 4) or ((p shr (seg + 3)) and 0xF)
            (uval xor mask).toByte()
        }
    }

    /** μ-law byte -> 16bit PCM */
    fun ulawToLinear(u: Byte): Short {
        var uval = u.toInt() xor 0xFF
        var t = ((uval and QUANT_MASK) shl 3) + BIAS
        t = t shl ((uval and SEG_MASK) ushr SEG_SHIFT)
        return if ((uval and SIGN_BIT) != 0) (BIAS - t).toShort() else (t - BIAS).toShort()
    }

    /** 16bit PCM -> A-law byte */
    fun linearToAlaw(pcm: Int): Byte {
        var p = pcm.coerceIn(-32768, 32767)
        val mask: Int
        if (p >= 0) mask = 0xD5 else { mask = 0x55; p = -p - 8 }
        val seg = search(p, SEG_END)
        return if (seg >= 8) {
            (0x7F xor mask).toByte()
        } else if (seg == 0) {
            ((p shr 4) xor mask).toByte()
        } else {
            (((seg shl 4) or ((p shr (seg + 3)) and 0xF)) xor mask).toByte()
        }
    }

    /** A-law byte -> 16bit PCM */
    fun alawToLinear(a: Byte): Short {
        var aval = a.toInt() xor 0x55
        var t = (aval and QUANT_MASK) shl 4
        val seg = (aval and SEG_MASK) ushr SEG_SHIFT
        t += if (seg == 0) 8 else (0x100 shl (seg - 1)) + 8
        return if ((aval and SIGN_BIT) != 0) t.toShort() else (-t).toShort()
    }
}

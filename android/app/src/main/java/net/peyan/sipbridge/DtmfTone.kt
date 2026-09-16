package net.peyan.sipbridge

import kotlin.math.PI
import kotlin.math.sin

/**
 * in-band DTMF トーン生成 (Android 非依存・JVM テスト可)。
 * 仕様 (UI-DESIGN §3.1 / T8-P2):
 * - DTMF 二重音 (低群 697/770/852/941 Hz × 高群 1209/1336/1477/1633 Hz) を 120 ms、
 *   続けて無音 80 ms。8 kHz サンプリングで 960 + 640 = 1600 サンプル。
 * - 各正弦波の振幅は -6 dBFS 程度 (フルスケールの約 1/2 = 16383)。
 *   2 波の合成ピークは最大でも 32766 のためクリップしない。
 * - 未対応文字は [toneFor]/[frequenciesFor] が null を返す (呼び出し側は無視する)。
 */
object DtmfTone {
    const val SAMPLE_RATE = 8000
    /** トーン長 120 ms = 960 サンプル。 */
    const val TONE_SAMPLES = SAMPLE_RATE * 120 / 1000
    /** 後続の無音 80 ms = 640 サンプル。 */
    const val SILENCE_SAMPLES = SAMPLE_RATE * 80 / 1000
    /** 1 桁分の総サンプル数 (120ms トーン + 80ms 無音)。 */
    const val TOTAL_SAMPLES = TONE_SAMPLES + SILENCE_SAMPLES
    /** 20 ms フレームのサンプル数 (RtpEngine の送信単位)。 */
    const val FRAME_SAMPLES = SAMPLE_RATE * 20 / 1000

    /** 各トーンの振幅 (-6 dBFS 程度 = 32767 の約 1/2)。 */
    const val AMPLITUDE = 16383.0

    private val LOW = doubleArrayOf(697.0, 770.0, 852.0, 941.0)
    private val HIGH = doubleArrayOf(1209.0, 1336.0, 1477.0, 1633.0)

    /**
     * 桁に対応する (低群, 高群) 周波数。未対応文字は null。
     * 小文字の a〜d も受け付ける (大文字扱い)。
     */
    fun frequenciesFor(digit: Char): Pair<Double, Double>? {
        val rowCol: Pair<Int, Int> = when (digit.uppercaseChar()) {
            '1' -> 0 to 0
            '2' -> 0 to 1
            '3' -> 0 to 2
            'A' -> 0 to 3
            '4' -> 1 to 0
            '5' -> 1 to 1
            '6' -> 1 to 2
            'B' -> 1 to 3
            '7' -> 2 to 0
            '8' -> 2 to 1
            '9' -> 2 to 2
            'C' -> 2 to 3
            '*' -> 3 to 0
            '0' -> 3 to 1
            '#' -> 3 to 2
            'D' -> 3 to 3
            else -> return null
        }
        return LOW[rowCol.first] to HIGH[rowCol.second]
    }

    /**
     * 1 桁分の PCM (16bit, 8kHz mono, [TOTAL_SAMPLES] サンプル)。
     * 前半 [TONE_SAMPLES] が二重音、後半 [SILENCE_SAMPLES] が無音。
     * 未対応文字は null。
     */
    fun toneFor(digit: Char): ShortArray? {
        val (fLow, fHigh) = frequenciesFor(digit) ?: return null
        val out = ShortArray(TOTAL_SAMPLES) // 後半は 0 (無音) のまま
        for (i in 0 until TONE_SAMPLES) {
            val t = i.toDouble() / SAMPLE_RATE
            val s = AMPLITUDE * sin(2.0 * PI * fLow * t) +
                AMPLITUDE * sin(2.0 * PI * fHigh * t)
            out[i] = s.toInt().coerceIn(-32768, 32767).toShort()
        }
        return out
    }

    /** [toneFor] を 20 ms フレーム ([FRAME_SAMPLES] サンプル) に分割する。 */
    fun framesFor(digit: Char): List<ShortArray>? {
        val tone = toneFor(digit) ?: return null
        return tone.toList().chunked(FRAME_SAMPLES) { it.toShortArray() }
    }
}

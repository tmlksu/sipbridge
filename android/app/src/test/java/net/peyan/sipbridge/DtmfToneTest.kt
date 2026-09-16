package net.peyan.sipbridge

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import kotlin.math.abs

/** DtmfTone (in-band DTMF 生成) のテスト。Android 非依存。 */
class DtmfToneTest {

    @Test
    fun `tone length is 120ms plus 80ms silence`() {
        val tone = DtmfTone.toneFor('5')
        assertNotNull(tone)
        // 120ms=960 サンプル + 80ms 無音=640 サンプル
        assertEquals(DtmfTone.TONE_SAMPLES, 960)
        assertEquals(DtmfTone.SILENCE_SAMPLES, 640)
        assertEquals(1600, tone!!.size)
        // 後半 640 サンプルは無音 (全て 0)
        for (i in DtmfTone.TONE_SAMPLES until tone.size) {
            assertEquals("silence at $i", 0, tone[i].toInt())
        }
    }

    @Test
    fun `tone section is audible`() {
        // 全 16 桁 (0-9, *, #, A-D) が無音でないトーンを持つ
        for (d in "123456789*0#ABCD") {
            val tone = DtmfTone.toneFor(d)
            assertNotNull("tone for $d", tone)
            var peak = 0
            for (i in 0 until DtmfTone.TONE_SAMPLES) {
                peak = maxOf(peak, abs(tone!![i].toInt()))
            }
            // -6 dBFS 程度の 2 波合成なので十分大きい (半分以上の振幅を期待)
            assertTrue("peak for $d too small: $peak", peak > 8000)
        }
    }

    @Test
    fun `amplitude never clips`() {
        // 全桁で 16bit フルスケールを超えない (合成ピークは最大 32766)
        for (d in "123456789*0#ABCDabcd") {
            val tone = DtmfTone.toneFor(d)!!
            for (s in tone) {
                val v = s.toInt()
                assertTrue("clip for $d: $v", v in -32767..32767)
            }
        }
    }

    @Test
    fun `unsupported chars are ignored`() {
        assertNull(DtmfTone.toneFor('E'))
        assertNull(DtmfTone.toneFor('x'))
        assertNull(DtmfTone.toneFor(' '))
        assertNull(DtmfTone.toneFor('+'))
        assertNull(DtmfTone.frequenciesFor('?'))
        assertNull(DtmfTone.framesFor('Z'))
    }

    @Test
    fun `lowercase abcd accepted`() {
        assertNotNull(DtmfTone.toneFor('a'))
        assertNotNull(DtmfTone.toneFor('d'))
    }

    @Test
    fun `known frequencies`() {
        // 5 = 770Hz + 1336Hz / 0 = 941Hz + 1336Hz / # = 941Hz + 1477Hz
        assertEquals(770.0 to 1336.0, DtmfTone.frequenciesFor('5'))
        assertEquals(941.0 to 1336.0, DtmfTone.frequenciesFor('0'))
        assertEquals(941.0 to 1477.0, DtmfTone.frequenciesFor('#'))
        assertEquals(697.0 to 1209.0, DtmfTone.frequenciesFor('1'))
    }

    @Test
    fun `frames split into 20ms chunks`() {
        val frames = DtmfTone.framesFor('9')
        assertNotNull(frames)
        // 1600 / 160 = 10 フレーム
        assertEquals(10, frames!!.size)
        frames.forEach { assertEquals(160, it.size) }
    }
}

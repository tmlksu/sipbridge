package net.peyan.sipbridge

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

/** HistoryStore の単体テスト (ファイル I/O は一時ディレクトリで行う)。 */
class HistoryStoreTest {

    @get:Rule
    val tmp = TemporaryFolder()

    private fun store(name: String = "history.json"): HistoryStore =
        HistoryStore(File(tmp.root, name))

    @Test
    fun `add と all は新しい順`() {
        val s = store()
        val e1 = s.add(HistoryDirection.IN, "101", "母屋", 1000L, 60L)
        val e2 = s.add(HistoryDirection.OUT, "102", "", 2000L, 0L)
        val all = s.all()
        assertEquals(2, all.size)
        assertEquals(e2.id, all[0].id)
        assertEquals(e1.id, all[1].id)
        assertEquals(HistoryDirection.OUT, all[0].direction)
        assertEquals("102", all[0].number)
        assertEquals(2000L, all[0].startedAt)
    }

    @Test
    fun `上限200件で古いものから削除`() {
        val s = store()
        repeat(HistoryStore.MAX_ENTRIES + 5) { i ->
            s.add(HistoryDirection.OUT, "num$i", "", i.toLong(), 0L)
        }
        val all = s.all()
        assertEquals(HistoryStore.MAX_ENTRIES, all.size)
        // 最新が先頭、最も古い 5 件 (num0..num4) が落ちている
        assertEquals("num${HistoryStore.MAX_ENTRIES + 4}", all[0].number)
        assertEquals("num5", all.last().number)
        assertTrue(all.none { it.number == "num0" })
    }

    @Test
    fun `missedOnly は不在着信だけ`() {
        val s = store()
        s.add(HistoryDirection.IN, "101", "", 1L, 10L)
        s.add(HistoryDirection.MISSED, "110", "", 2L, 0L)
        s.add(HistoryDirection.OUT, "102", "", 3L, 0L)
        s.add(HistoryDirection.MISSED, "119", "", 4L, 0L)
        val missed = s.missedOnly()
        assertEquals(2, missed.size)
        assertEquals("119", missed[0].number)
        assertEquals("110", missed[1].number)
    }

    @Test
    fun `remove で1件削除`() {
        val s = store()
        val e1 = s.add(HistoryDirection.IN, "101", "", 1L, 0L)
        val e2 = s.add(HistoryDirection.IN, "102", "", 2L, 0L)
        assertTrue(s.remove(e1.id))
        assertFalse(s.remove("no-such-id"))
        val all = s.all()
        assertEquals(1, all.size)
        assertEquals(e2.id, all[0].id)
    }

    @Test
    fun `updateDuration で通話時間を更新`() {
        val s = store()
        val e = s.add(HistoryDirection.IN, "101", "母屋", 1L, 0L)
        assertTrue(s.updateDuration(e.id, 68L))
        assertFalse(s.updateDuration("no-such-id", 10L))
        assertEquals(68L, s.all().first().durationSec)
    }

    @Test
    fun `clear で全消去`() {
        val s = store()
        s.add(HistoryDirection.IN, "101", "", 1L, 0L)
        s.clear()
        assertTrue(s.all().isEmpty())
        assertTrue(s.missedOnly().isEmpty())
    }

    @Test
    fun `別インスタンスでもファイルから読み直す`() {
        val s1 = store()
        s1.add(HistoryDirection.MISSED, "110", "", 1L, 0L)
        val s2 = HistoryStore(File(tmp.root, "history.json"))
        assertEquals(1, s2.all().size)
        assertEquals("110", s2.all()[0].number)
    }
}

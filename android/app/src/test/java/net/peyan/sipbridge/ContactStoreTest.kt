package net.peyan.sipbridge

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

/** ContactStore の単体テスト (ファイル I/O は一時ディレクトリで行う)。 */
class ContactStoreTest {

    @get:Rule
    val tmp = TemporaryFolder()

    private fun store(): ContactStore =
        ContactStore(File(tmp.root, "contacts.json"))

    @Test
    fun `add と all`() {
        val s = store()
        val c = s.add("母屋", "101", ContactGroup.UCHI)
        assertEquals("母屋", c.name)
        assertEquals("101", c.number)
        assertEquals(ContactGroup.UCHI, c.group)
        assertTrue(c.id.isNotBlank())
        val all = s.all()
        assertEquals(1, all.size)
        assertEquals(c, all[0])
    }

    @Test
    fun `update と remove`() {
        val s = store()
        val c = s.add("母屋", "101", ContactGroup.UCHI)
        assertTrue(s.update(c.id, "離れ", "102", ContactGroup.SOTO))
        assertFalse(s.update("no-such-id", "x", "y", ContactGroup.UCHI))
        val updated = s.all().first()
        assertEquals("離れ", updated.name)
        assertEquals("102", updated.number)
        assertEquals(ContactGroup.SOTO, updated.group)
        assertTrue(s.remove(c.id))
        assertFalse(s.remove(c.id))
        assertTrue(s.all().isEmpty())
    }

    @Test
    fun `search は名前部分一致`() {
        val s = store()
        s.add("母屋", "101", ContactGroup.UCHI)
        s.add("離れ", "102", ContactGroup.UCHI)
        s.add("玄関インターホン", "110", ContactGroup.SOTO)
        val hit = s.search("離")
        assertEquals(1, hit.size)
        assertEquals("離れ", hit[0].name)
        // 空クエリは全件
        assertEquals(3, s.search("").size)
        assertEquals(3, s.search("   ").size)
    }

    @Test
    fun `search は番号前方一致 (正規化後)`() {
        val s = store()
        s.add("会社", "03-1234-5678", ContactGroup.SOTO)
        s.add("母屋", "101", ContactGroup.UCHI)
        assertEquals("会社", s.search("03").first().name)
        assertEquals("会社", s.search("031234").first().name)
        assertEquals("母屋", s.search("10").first().name)
        assertTrue(s.search("999").isEmpty())
    }

    @Test
    fun `lookup は空白・ハイフンを除去して比較`() {
        val s = store()
        s.add("会社", "03-1234 5678", ContactGroup.SOTO)
        assertEquals("会社", s.lookup("0312345678"))
        assertEquals("会社", s.lookup("03-1234-5678"))
        assertEquals("会社", s.lookup(" 03 1234 5678 "))
        assertNull(s.lookup("0312345679"))
        assertNull(s.lookup(""))
        assertNull(s.lookup("   "))
    }

    @Test
    fun `normalizePhoneNumber`() {
        assertEquals("0312345678", normalizePhoneNumber("03-1234 5678"))
        assertEquals("101", normalizePhoneNumber(" 1-0-1 "))
        assertEquals("", normalizePhoneNumber(" - "))
    }

    @Test
    fun `別インスタンスでもファイルから読み直す`() {
        val s1 = store()
        s1.add("母屋", "101", ContactGroup.UCHI)
        val s2 = ContactStore(File(tmp.root, "contacts.json"))
        assertEquals("母屋", s2.lookup("101"))
        assertEquals(1, s2.all().size)
    }
}

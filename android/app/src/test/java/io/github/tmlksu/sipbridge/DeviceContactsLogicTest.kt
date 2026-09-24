package io.github.tmlksu.sipbridge

import org.junit.Assert.assertEquals
import org.junit.Test

/** DeviceContacts の Android 非依存ロジック (重複除去・検索フィルタ) の単体テスト。 */
class DeviceContactsLogicTest {

    private fun dc(name: String, number: String, label: String = "") =
        DeviceContact(id = number, name = name, number = number, label = label)

    @Test
    fun `正規化番号の重複は最初の1件だけ残る`() {
        val items = listOf(
            dc("田中 携帯", "090-1234-5678", "携帯"),
            dc("田中 自宅", "090 1234 5678", "自宅"),
            dc("佐藤", "03-1234-5678")
        )
        val deduped = dedupeByNormalizedNumber(items)
        assertEquals(2, deduped.size)
        assertEquals("田中 携帯", deduped[0].name)
        assertEquals("佐藤", deduped[1].name)
    }

    @Test
    fun `空クエリは全件返す`() {
        val items = listOf(dc("田中", "090-1111-2222"), dc("佐藤", "101"))
        assertEquals(items, filterContacts(items, ""))
        assertEquals(items, filterContacts(items, "   "))
    }

    @Test
    fun `名前部分一致で絞る`() {
        val items = listOf(dc("田中太郎", "090-1111-2222"), dc("佐藤", "101"))
        val found = filterContacts(items, "田中")
        assertEquals(1, found.size)
        assertEquals("田中太郎", found[0].name)
    }

    @Test
    fun `上限以下はそのまま返し省略は0`() {
        val items = listOf(dc("田中", "090-1111-2222"), dc("佐藤", "101"))
        val (shown, omitted) = capList(items, 100)
        assertEquals(items, shown)
        assertEquals(0, omitted)
        // ちょうど上限でも省略なし
        val (shown2, omitted2) = capList(items, 2)
        assertEquals(items, shown2)
        assertEquals(0, omitted2)
    }

    @Test
    fun `上限超過は先頭だけ残し省略件数を返す`() {
        val items = listOf(dc("田中", "090-1111-2222"), dc("佐藤", "101"), dc("鈴木", "102"))
        val (shown, omitted) = capList(items, 2)
        assertEquals(listOf(items[0], items[1]), shown)
        assertEquals(1, omitted)
    }

    @Test
    fun `空リストと上限0でも壊れない`() {
        val (shown, omitted) = capList(emptyList<DeviceContact>(), 100)
        assertEquals(emptyList<DeviceContact>(), shown)
        assertEquals(0, omitted)
        val items = listOf(dc("田中", "090-1111-2222"))
        val (shown2, omitted2) = capList(items, 0)
        assertEquals(emptyList<DeviceContact>(), shown2)
        assertEquals(1, omitted2)
    }

    @Test
    fun `番号前方一致は正規化して比べる`() {
        val items = listOf(dc("田中", "090-1111-2222"), dc("佐藤", "03-1234-5678"))
        // ハイフン無しの入力でもヒットする
        assertEquals(listOf(items[0]), filterContacts(items, "0901111"))
        assertEquals(listOf(items[1]), filterContacts(items, "03-1234"))
        // 前方一致なので途中からはヒットしない
        assertEquals(emptyList<DeviceContact>(), filterContacts(items, "1111"))
    }
}

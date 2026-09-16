package net.peyan.sipbridge

import android.content.Context
import java.io.File
import java.util.UUID
import org.json.JSONArray
import org.json.JSONObject

/** 連絡先のグループ。ウチ=内線・家族 / ソト=外線。 */
enum class ContactGroup { UCHI, SOTO }

/** 連絡先 1 件。 */
data class Contact(
    val id: String,
    val name: String,
    val number: String,
    val group: ContactGroup
)

/**
 * 番号比較用の正規化: 空白・ハイフン類を除去する。
 * 全角ハイフン (－) やダッシュ類も除く (端末の入力ゆれ対策)。
 */
fun normalizePhoneNumber(raw: String): String =
    raw.filterNot { it.isWhitespace() || it in HYPHENS }

private val HYPHENS: Set<Char> = setOf(
    '-', '－', // ASCII / 全角ハイフン
    '‐', '‑', '‒', '–', '—', '―', // ダッシュ類
    'ー' // 長音 (番号区切りに使う入力ゆれ対策)
)

/**
 * 連絡先の永続化 (`Context.filesDir/contacts.json`)。
 * Android 非依存 (コンストラクタは [File] を受ける) のため JVM テスト可。
 * 全メソッド同期 (スレッドセーフ)。HistoryStore と同じく毎回読み直す。
 */
class ContactStore(private val file: File) {

    companion object {
        fun fromContext(ctx: Context): ContactStore =
            ContactStore(File(ctx.filesDir, "contacts.json"))
    }

    /** 追加する。作成した連絡先を返す。 */
    @Synchronized
    fun add(name: String, number: String, group: ContactGroup): Contact {
        val c = Contact(
            id = UUID.randomUUID().toString(),
            name = name.trim(),
            number = number.trim(),
            group = group
        )
        val all = load().toMutableList()
        all.add(c)
        save(all)
        return c
    }

    /** 更新する。該当 id があれば true。 */
    @Synchronized
    fun update(id: String, name: String, number: String, group: ContactGroup): Boolean {
        val all = load()
        val idx = all.indexOfFirst { it.id == id }
        if (idx < 0) return false
        val updated = all.toMutableList()
        updated[idx] = Contact(id, name.trim(), number.trim(), group)
        save(updated)
        return true
    }

    /** 削除する。該当 id があれば true。 */
    @Synchronized
    fun remove(id: String): Boolean {
        val all = load()
        val kept = all.filterNot { it.id == id }
        if (kept.size == all.size) return false
        save(kept)
        return true
    }

    /** 全件 (登録順)。 */
    @Synchronized
    fun all(): List<Contact> = load()

    /**
     * 検索する。名前の部分一致、または番号の前方一致 (どちらも正規化後に比較)。
     * query が空なら全件を返す。
     */
    @Synchronized
    fun search(query: String): List<Contact> {
        val q = query.trim()
        if (q.isEmpty()) return load()
        val nq = normalizePhoneNumber(q)
        return load().filter { c ->
            c.name.contains(q) ||
                (nq.isNotEmpty() && normalizePhoneNumber(c.number).startsWith(nq))
        }
    }

    /**
     * 着信時の名前解決用。番号の正規化一致で最初に見つかった名前を返す。
     * 見つからなければ null (呼び出し側で番号表示にフォールバックする)。
     */
    @Synchronized
    fun lookup(number: String): String? {
        val n = normalizePhoneNumber(number)
        if (n.isEmpty()) return null
        return load().firstOrNull { normalizePhoneNumber(it.number) == n }?.name
    }

    // ---- 永続化 (org.json のみ) ----

    private fun load(): List<Contact> {
        val text = runCatching {
            if (!file.exists()) return emptyList()
            file.readText()
        }.getOrNull()?.takeIf { it.isNotBlank() } ?: return emptyList()
        return runCatching {
            val arr = JSONArray(text)
            List(arr.length()) { i -> parse(arr.getJSONObject(i)) }
        }.getOrDefault(emptyList())
    }

    private fun save(all: List<Contact>) {
        runCatching {
            file.parentFile?.mkdirs()
            val arr = JSONArray()
            all.forEach { arr.put(serialize(it)) }
            val tmp = File(file.parentFile, "${file.name}.tmp")
            tmp.writeText(arr.toString())
            if (!tmp.renameTo(file)) {
                file.delete()
                tmp.renameTo(file)
            }
        }
    }

    private fun parse(o: JSONObject): Contact = Contact(
        id = o.optString("id", UUID.randomUUID().toString()),
        name = o.optString("name", ""),
        number = o.optString("number", ""),
        group = runCatching { ContactGroup.valueOf(o.optString("group", "SOTO")) }
            .getOrDefault(ContactGroup.SOTO)
    )

    private fun serialize(c: Contact): JSONObject = JSONObject()
        .put("id", c.id)
        .put("name", c.name)
        .put("number", c.number)
        .put("group", c.group.name)
}

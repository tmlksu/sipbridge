package io.github.tmlksu.sipbridge

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.net.Uri
import android.provider.ContactsContract.CommonDataKinds.Phone
import android.provider.ContactsContract.PhoneLookup
import androidx.core.content.ContextCompat

/**
 * 端末の連絡先との連携 (UI-DESIGN §1.3 v1.2)。権限判定 + クエリのみで UI は持たない。
 *
 * - 取り込み (権限不要): 追加ダイアログの「端末の連絡先から選ぶ」は
 *   `ACTION_PICK` on `Phone.CONTENT_URI` を使い、返り値 URI の一時権限で読む
 *   (このファイルは使わない)。1 人に複数番号があってもシステムのピッカーが
 *   番号単位で選ばせてくれる。
 * - 一覧に混ぜる (任意・既定 OFF): 設定トグル ON + `READ_CONTACTS` 許可のときだけ
 *   [all] で番号ごとに 1 行に展開して「端末」グループに表示する。
 * - 着信時の名前解決: `ContactStore.lookup` → [lookup] の順。
 *
 * [all]/[lookup]/[search] は ContentResolver を叩くため**メインスレッドで呼ばないこと**
 * (呼び出し側がワーカースレッドで呼ぶ)。権限が無い・例外のときは空リスト/null を返す。
 */
data class DeviceContact(
    val id: String,
    val name: String,
    val number: String,
    /** `Phone.getTypeLabel` の表示 (例「携帯」「自宅」)。不明なら空。 */
    val label: String
)

object DeviceContacts {

    /** `READ_CONTACTS` が許可済みか。 */
    fun hasPermission(ctx: Context): Boolean =
        ContextCompat.checkSelfPermission(ctx, Manifest.permission.READ_CONTACTS) ==
            PackageManager.PERMISSION_GRANTED

    /**
     * 端末の連絡先を**番号ごとに 1 件**に展開して返す。名前順ソート。
     * 番号の正規化 ([normalizePhoneNumber]) で重複した番号は除去する (最初の 1 件を残す)。
     * 権限が無い・例外のときは空リスト。
     */
    fun all(ctx: Context): List<DeviceContact> {
        if (!hasPermission(ctx)) return emptyList()
        return try {
            val projection = arrayOf(
                Phone._ID, Phone.DISPLAY_NAME, Phone.NUMBER, Phone.TYPE, Phone.LABEL
            )
            val rows = mutableListOf<DeviceContact>()
            ctx.contentResolver.query(
                Phone.CONTENT_URI, projection, null, null,
                "${Phone.DISPLAY_NAME} COLLATE LOCALIZED ASC"
            )?.use { c ->
                val iId = c.getColumnIndexOrThrow(Phone._ID)
                val iName = c.getColumnIndexOrThrow(Phone.DISPLAY_NAME)
                val iNumber = c.getColumnIndexOrThrow(Phone.NUMBER)
                val iType = c.getColumnIndexOrThrow(Phone.TYPE)
                val iLabel = c.getColumnIndexOrThrow(Phone.LABEL)
                while (c.moveToNext()) {
                    val number = c.getString(iNumber).orEmpty().trim()
                    if (normalizePhoneNumber(number).isEmpty()) continue
                    val name = c.getString(iName).orEmpty().ifBlank { number }
                    val label = Phone.getTypeLabel(
                        ctx.resources, c.getInt(iType), c.getString(iLabel)
                    )?.toString().orEmpty()
                    rows.add(
                        DeviceContact(
                            id = c.getString(iId).orEmpty(),
                            name = name,
                            number = number,
                            label = label
                        )
                    )
                }
            } ?: return emptyList()
            dedupeByNormalizedNumber(rows).sortedBy { it.name }
        } catch (e: Exception) {
            emptyList()
        }
    }

    /**
     * 番号から表示名を 1 件引く (`PhoneLookup.CONTENT_FILTER_URI`)。
     * 権限が無い・見つからない・例外のときは null。
     */
    fun lookup(ctx: Context, number: String): String? {
        if (!hasPermission(ctx)) return null
        if (normalizePhoneNumber(number).isEmpty()) return null
        return try {
            val uri: Uri = Uri.withAppendedPath(
                PhoneLookup.CONTENT_FILTER_URI, Uri.encode(number)
            )
            ctx.contentResolver.query(
                uri, arrayOf(PhoneLookup.DISPLAY_NAME), null, null, null
            )?.use { c ->
                if (c.moveToFirst()) c.getString(0)?.takeIf { it.isNotBlank() } else null
            }
        } catch (e: Exception) {
            null
        }
    }

    /**
     * 端末の連絡先を検索する (名前部分一致 / 番号前方一致。[ContactStore.search] と同じ基準)。
     * query が空なら全件。権限が無い・例外のときは空リスト。
     */
    fun search(ctx: Context, query: String): List<DeviceContact> {
        if (!hasPermission(ctx)) return emptyList()
        return try {
            filterContacts(all(ctx), query)
        } catch (e: Exception) {
            emptyList()
        }
    }
}

/**
 * 正規化番号の重複を除去する (最初の 1 件を残す)。Android 非依存 (JVM テスト可)。
 * 空番号は [DeviceContacts.all] 側で事前に弾くため、ここでは単純に正規化一致で畳む。
 */
internal fun dedupeByNormalizedNumber(items: List<DeviceContact>): List<DeviceContact> {
    val seen = LinkedHashSet<String>()
    return items.filter { seen.add(normalizePhoneNumber(it.number)) }
}

/**
 * 表示上限で切る。戻り値は (表示分, 省略件数)。Android 非依存 (JVM テスト可)。
 * 上限以下ならリストをそのまま返し、省略は 0。
 */
internal fun <T> capList(items: List<T>, max: Int): Pair<List<T>, Int> {
    val m = max.coerceAtLeast(0)
    if (items.size <= m) return items to 0
    return items.subList(0, m) to (items.size - m)
}

/**
 * 名前部分一致 / 番号前方一致で絞る ([ContactStore.search] と同じ基準)。
 * query が空なら全件をそのまま返す。Android 非依存 (JVM テスト可)。
 */
internal fun filterContacts(items: List<DeviceContact>, query: String): List<DeviceContact> {
    val q = query.trim()
    if (q.isEmpty()) return items
    val nq = normalizePhoneNumber(q)
    return items.filter { dc ->
        dc.name.contains(q) ||
            (nq.isNotEmpty() && normalizePhoneNumber(dc.number).startsWith(nq))
    }
}

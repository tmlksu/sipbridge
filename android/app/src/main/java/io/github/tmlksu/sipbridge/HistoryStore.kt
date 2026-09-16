package io.github.tmlksu.sipbridge

import android.content.Context
import java.io.File
import java.util.UUID
import org.json.JSONArray
import org.json.JSONObject

/** 通話履歴の方向。IN=着信応答 / OUT=発信 / MISSED=不在着信。 */
enum class HistoryDirection { IN, OUT, MISSED }

/**
 * 履歴 1 件。name は発生時点の解決名 (不明なら ""。表示時は番号にフォールバックする)。
 * startedAt は epoch ms、durationSec は通話秒数 (未応答なら 0)。
 */
data class HistoryEntry(
    val id: String,
    val direction: HistoryDirection,
    val number: String,
    val name: String,
    val startedAt: Long,
    val durationSec: Long
)

/**
 * 通話履歴の永続化 (`Context.filesDir/history.json`、最大 200 件)。
 * Android 非依存 (コンストラクタは [File] を受ける) のため JVM テスト可。
 * 全メソッド同期 (スレッドセーフ)。毎回ファイルから読み直すため、
 * BridgeService と Fragment が別インスタンスを持っても整合する。
 */
class HistoryStore(private val file: File) {

    companion object {
        /** 最大保持件数。超えた分は古いものから削除する。 */
        const val MAX_ENTRIES = 200

        fun fromContext(ctx: Context): HistoryStore =
            HistoryStore(File(ctx.filesDir, "history.json"))
    }

    /** 追加する (先頭=最新)。上限を超えたら古いものから捨てる。作成したエントリを返す。 */
    @Synchronized
    fun add(
        direction: HistoryDirection,
        number: String,
        name: String,
        startedAt: Long,
        durationSec: Long = 0L
    ): HistoryEntry {
        val entry = HistoryEntry(
            id = UUID.randomUUID().toString(),
            direction = direction,
            number = number,
            name = name,
            startedAt = startedAt,
            durationSec = durationSec.coerceAtLeast(0L)
        )
        val all = load().toMutableList()
        all.add(0, entry)
        save(all.take(MAX_ENTRIES))
        return entry
    }

    /** 全件 (新しい順)。 */
    @Synchronized
    fun all(): List<HistoryEntry> = load()

    /** 不在着信のみ (新しい順)。 */
    @Synchronized
    fun missedOnly(): List<HistoryEntry> =
        load().filter { it.direction == HistoryDirection.MISSED }

    /**
     * 通話時間を更新する (応答済み通話の終了時用)。
     * 該当 id があれば true。
     */
    @Synchronized
    fun updateDuration(id: String, durationSec: Long): Boolean {
        val all = load()
        val idx = all.indexOfFirst { it.id == id }
        if (idx < 0) return false
        val updated = all.toMutableList()
        updated[idx] = all[idx].copy(durationSec = durationSec.coerceAtLeast(0L))
        save(updated)
        return true
    }

    /** 1 件削除する。該当 id があれば true。 */
    @Synchronized
    fun remove(id: String): Boolean {
        val all = load()
        val kept = all.filterNot { it.id == id }
        if (kept.size == all.size) return false
        save(kept)
        return true
    }

    /** 全消去する。 */
    @Synchronized
    fun clear() {
        save(emptyList())
    }

    // ---- 永続化 (org.json のみ) ----

    private fun load(): List<HistoryEntry> {
        val text = runCatching {
            if (!file.exists()) return emptyList()
            file.readText()
        }.getOrNull()?.takeIf { it.isNotBlank() } ?: return emptyList()
        return runCatching {
            val arr = JSONArray(text)
            List(arr.length()) { i -> parse(arr.getJSONObject(i)) }
        }.getOrDefault(emptyList())
    }

    private fun save(all: List<HistoryEntry>) {
        runCatching {
            file.parentFile?.mkdirs()
            val arr = JSONArray()
            all.forEach { arr.put(serialize(it)) }
            // 原子的に書く (tmp → rename)。クラッシュ時の半書き込み防止。
            val tmp = File(file.parentFile, "${file.name}.tmp")
            tmp.writeText(arr.toString())
            if (!tmp.renameTo(file)) {
                file.delete()
                tmp.renameTo(file)
            }
        }
    }

    private fun parse(o: JSONObject): HistoryEntry = HistoryEntry(
        id = o.optString("id", UUID.randomUUID().toString()),
        direction = runCatching { HistoryDirection.valueOf(o.optString("direction", "MISSED")) }
            .getOrDefault(HistoryDirection.MISSED),
        number = o.optString("number", ""),
        name = o.optString("name", ""),
        startedAt = o.optLong("startedAt", 0L),
        durationSec = o.optLong("durationSec", 0L).coerceAtLeast(0L)
    )

    private fun serialize(e: HistoryEntry): JSONObject = JSONObject()
        .put("id", e.id)
        .put("direction", e.direction.name)
        .put("number", e.number)
        .put("name", e.name)
        .put("startedAt", e.startedAt)
        .put("durationSec", e.durationSec)
}

package io.github.tmlksu.sipbridge

import android.os.Bundle
import android.text.InputType
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.RadioButton
import android.widget.RadioGroup
import android.widget.TextView
import android.widget.Toast
import androidx.core.content.ContextCompat
import androidx.fragment.app.Fragment
import androidx.recyclerview.widget.LinearLayoutManager
import androidx.recyclerview.widget.RecyclerView
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import java.text.SimpleDateFormat
import java.util.Calendar
import java.util.Date
import java.util.Locale

/**
 * UI-DESIGN §1.2 履歴。
 * 見出し + 全消去、セグメント (すべて/不在着信)、カード内リスト (RecyclerView)。
 * 行タップ=発信、長押し=削除/連絡先に追加。CallHub の変化 (通話終了) で再読込する。
 */
class HistoryFragment : Fragment(), CallHub.StateListener {

    private lateinit var store: HistoryStore
    private lateinit var contactStore: ContactStore
    private lateinit var adapter: HistoryAdapter
    private lateinit var rvHistory: RecyclerView
    private lateinit var tvEmpty: TextView
    private lateinit var segAll: TextView
    private lateinit var segMissed: TextView
    private var showMissedOnly = false

    override fun onCreateView(
        inflater: LayoutInflater, container: ViewGroup?, savedInstanceState: Bundle?
    ): View = inflater.inflate(R.layout.fragment_history, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        store = HistoryStore.fromContext(requireContext())
        contactStore = ContactStore.fromContext(requireContext())
        rvHistory = view.findViewById(R.id.rvHistory)
        tvEmpty = view.findViewById(R.id.tvEmptyHistory)
        segAll = view.findViewById(R.id.segAll)
        segMissed = view.findViewById(R.id.segMissed)

        adapter = HistoryAdapter(
            onTap = { entry -> DialHelper.dial(this, entry.number) },
            onLongPress = { entry -> showRowMenu(entry) }
        )
        rvHistory.layoutManager = LinearLayoutManager(requireContext())
        rvHistory.adapter = adapter

        segAll.setOnClickListener { setSegment(false) }
        segMissed.setOnClickListener { setSegment(true) }
        view.findViewById<TextView>(R.id.btnClearHistory).setOnClickListener { confirmClear() }
        setSegment(false)
        reload()
    }

    override fun onResume() {
        super.onResume()
        CallHub.addListener(this)
        reload()
    }

    override fun onPause() {
        super.onPause()
        CallHub.removeListener(this)
    }

    /** CallHub の変化 (通話終了など) で再読込する。OkHttp スレッドからも来るため UI スレッドへ。 */
    override fun onChanged() {
        activity?.runOnUiThread { if (isAdded) reload() }
    }

    @Deprecated("DialHelper 経由の権限コールバック用")
    override fun onRequestPermissionsResult(code: Int, perms: Array<out String>, res: IntArray) {
        @Suppress("DEPRECATION")
        super.onRequestPermissionsResult(code, perms, res)
        if (code == DialHelper.REQ_RECORD_AUDIO) {
            DialHelper.onPermissionResult(
                this, res.firstOrNull() == android.content.pm.PackageManager.PERMISSION_GRANTED
            )
        }
    }

    private fun setSegment(missedOnly: Boolean) {
        showMissedOnly = missedOnly
        val ctx = requireContext()
        segAll.setBackgroundResource(if (!missedOnly) R.drawable.segment_sel else 0)
        segMissed.setBackgroundResource(if (missedOnly) R.drawable.segment_sel else 0)
        segAll.setTextColor(
            ContextCompat.getColor(ctx, if (!missedOnly) R.color.nocturne_text else R.color.nocturne_text_dim)
        )
        segMissed.setTextColor(
            ContextCompat.getColor(ctx, if (missedOnly) R.color.nocturne_text else R.color.nocturne_text_dim)
        )
        reload()
    }

    private fun reload() {
        if (!::store.isInitialized) return
        val entries = runCatching {
            if (showMissedOnly) store.missedOnly() else store.all()
        }.getOrDefault(emptyList())
        adapter.submit(entries)
        val empty = entries.isEmpty()
        tvEmpty.visibility = if (empty) View.VISIBLE else View.GONE
        rvHistory.visibility = if (empty) View.GONE else View.VISIBLE
    }

    /** 長押しメニュー: 削除 / 連絡先に追加。 */
    private fun showRowMenu(entry: HistoryEntry) {
        val ctx = requireContext()
        MaterialAlertDialogBuilder(ctx)
            .setItems(
                arrayOf(
                    ctx.getString(R.string.common_delete_item),
                    ctx.getString(R.string.history_menu_add)
                )
            ) { _, which ->
                when (which) {
                    0 -> confirmRemove(entry)
                    1 -> showAddContact(entry)
                }
            }
            .show()
    }

    private fun confirmRemove(entry: HistoryEntry) {
        runCatching { store.remove(entry.id) }
        reload()
    }

    /** 右上の全消去 (確認ダイアログ付き)。 */
    private fun confirmClear() {
        MaterialAlertDialogBuilder(requireContext())
            .setMessage(R.string.history_clear_msg)
            .setPositiveButton(R.string.common_delete) { _, _ ->
                runCatching { store.clear() }
                reload()
            }
            .setNegativeButton(R.string.common_cancel, null)
            .show()
    }

    /** 履歴行からの連絡先追加 (名前入力 + グループ選択)。 */
    private fun showAddContact(entry: HistoryEntry) {
        val ctx = requireContext()
        val density = resources.displayMetrics.density
        val root = LinearLayout(ctx).apply {
            orientation = LinearLayout.VERTICAL
            setPadding((24 * density).toInt(), (8 * density).toInt(), (24 * density).toInt(), 0)
        }
        // 名前が番号と同じ (未解決) なら空欄にする
        val initialName = entry.name.takeIf { it.isNotBlank() && it != entry.number }.orEmpty()
        val etName = EditText(ctx).apply {
            hint = ctx.getString(R.string.history_name_hint)
            setText(initialName)
            inputType = InputType.TYPE_CLASS_TEXT
            setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text))
            setHintTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text_dim))
        }
        root.addView(etName)
        root.addView(TextView(ctx).apply {
            text = entry.number
            setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text_dim))
            textSize = 14f
            setPadding(0, (8 * density).toInt(), 0, (8 * density).toInt())
        })
        val group = RadioGroup(ctx).apply { orientation = RadioGroup.HORIZONTAL }
        val rbUchi = RadioButton(ctx).apply { text = ctx.getString(R.string.group_uchi) }
        val rbSoto = RadioButton(ctx).apply {
            text = ctx.getString(R.string.group_soto)
            isChecked = true
        }
        group.addView(rbUchi)
        group.addView(rbSoto)
        root.addView(group)
        MaterialAlertDialogBuilder(ctx)
            .setTitle(R.string.history_menu_add)
            .setView(root)
            .setPositiveButton(R.string.common_add) { _, _ ->
                val name = etName.text.toString().trim()
                if (name.isEmpty()) {
                    Toast.makeText(ctx, R.string.history_need_name, Toast.LENGTH_SHORT).show()
                    return@setPositiveButton
                }
                runCatching {
                    contactStore.add(
                        name, entry.number,
                        if (rbUchi.isChecked) ContactGroup.UCHI else ContactGroup.SOTO
                    )
                }
                Toast.makeText(ctx, R.string.history_added, Toast.LENGTH_SHORT).show()
            }
            .setNegativeButton(R.string.common_cancel, null)
            .show()
    }

    // ---- リスト ----

    private class HistoryAdapter(
        private val onTap: (HistoryEntry) -> Unit,
        private val onLongPress: (HistoryEntry) -> Unit
    ) : RecyclerView.Adapter<HistoryAdapter.Holder>() {

        private var items: List<HistoryEntry> = emptyList()

        fun submit(entries: List<HistoryEntry>) {
            items = entries
            notifyDataSetChanged()
        }

        class Holder(v: View) : RecyclerView.ViewHolder(v) {
            val row: View = v.findViewById(R.id.rowBody)
            val icon: TextView = v.findViewById(R.id.tvDirIcon)
            val name: TextView = v.findViewById(R.id.tvName)
            val sub: TextView = v.findViewById(R.id.tvSub)
            val time: TextView = v.findViewById(R.id.tvTime)
            val divider: View = v.findViewById(R.id.divider)
        }

        override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): Holder {
            val v = LayoutInflater.from(parent.context)
                .inflate(R.layout.item_history, parent, false)
            return Holder(v)
        }

        override fun getItemCount(): Int = items.size

        override fun onBindViewHolder(h: Holder, position: Int) {
            val e = items[position]
            val ctx = h.itemView.context
            // 方向アイコン円: ↙ 着信 / ↗ 発信 / 不在は nocturne_missed
            val (glyph, colorRes) = when (e.direction) {
                HistoryDirection.IN -> "↙" to R.color.nocturne_accent_text
                HistoryDirection.OUT -> "↗" to R.color.nocturne_accent_text
                HistoryDirection.MISSED -> "↙" to R.color.nocturne_missed
            }
            h.icon.text = glyph
            h.icon.setTextColor(ContextCompat.getColor(ctx, colorRes))
            // 名前 (無ければ番号)。不在着信は名前も missed 色にする。
            h.name.text = e.name.ifBlank {
                e.number.ifBlank { ctx.getString(R.string.history_unknown) }
            }
            h.name.setTextColor(
                ContextCompat.getColor(
                    ctx,
                    if (e.direction == HistoryDirection.MISSED) R.color.nocturne_missed
                    else R.color.nocturne_text
                )
            )
            // 2 行目「着信・101」「不在着信・110」「発信・03-1234-5678」
            val kind = when (e.direction) {
                HistoryDirection.IN -> ctx.getString(R.string.history_kind_in)
                HistoryDirection.OUT -> ctx.getString(R.string.history_kind_out)
                HistoryDirection.MISSED -> ctx.getString(R.string.history_kind_missed)
            }
            h.sub.text = "$kind・${e.number}"
            h.time.text = formatListTime(
                System.currentTimeMillis(), e.startedAt,
                ctx.getString(R.string.history_time_yesterday),
                ctx.getString(R.string.history_time_empty)
            )
            h.divider.visibility = if (position == items.size - 1) View.GONE else View.VISIBLE
            h.row.setOnClickListener { onTap(e) }
            h.row.setOnLongClickListener {
                onLongPress(e)
                true
            }
        }
    }

    companion object {
        /**
         * 右端の時刻表示: 今日=HH:mm / 昨日=「昨日」/ それ以前=M/d。
         * 日付境界は端末のタイムゾーンの Calendar で求める。
         */
        fun formatListTime(
            nowMs: Long,
            ts: Long,
            yesterday: String = "昨日",
            empty: String = "--:--"
        ): String {
            if (ts <= 0) return empty
            val cal = Calendar.getInstance().apply { timeInMillis = nowMs }
            cal.set(Calendar.HOUR_OF_DAY, 0)
            cal.set(Calendar.MINUTE, 0)
            cal.set(Calendar.SECOND, 0)
            cal.set(Calendar.MILLISECOND, 0)
            val startToday = cal.timeInMillis
            cal.add(Calendar.DAY_OF_MONTH, -1)
            val startYesterday = cal.timeInMillis
            val locale = Locale.getDefault()
            return when {
                ts >= startToday -> SimpleDateFormat("HH:mm", locale).format(Date(ts))
                ts >= startYesterday -> yesterday
                else -> SimpleDateFormat("M/d", locale).format(Date(ts))
            }
        }
    }
}

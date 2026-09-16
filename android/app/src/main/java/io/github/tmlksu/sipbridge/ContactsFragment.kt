package io.github.tmlksu.sipbridge

import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.provider.ContactsContract.CommonDataKinds.Phone
import android.text.Editable
import android.text.InputType
import android.text.TextWatcher
import android.view.Gravity
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.EditText
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.RadioButton
import android.widget.RadioGroup
import android.widget.TextView
import android.widget.Toast
import androidx.core.content.ContextCompat
import androidx.fragment.app.Fragment
import androidx.activity.result.contract.ActivityResultContracts
import com.google.android.material.button.MaterialButton
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import java.util.concurrent.Executors

/**
 * UI-DESIGN §1.3 連絡先。
 * 見出し + 検索欄 + グループ見出し「ウチ」「ソト」+ 各カード。
 * 行タップ=発信、長押し=編集/削除、FAB「＋」で追加ダイアログ。
 */
class ContactsFragment : Fragment() {

    private lateinit var store: ContactStore
    private lateinit var container: LinearLayout
    private lateinit var tvEmpty: TextView
    private lateinit var etSearch: EditText

    // ---- 端末の連絡先 (v1.2 §1.3) ----
    /** 設定トグル ON + 権限ありのときだけ表示する「端末」グループの全件キャッシュ。 */
    private var deviceAll: List<DeviceContact> = emptyList()
    private var deviceLoaded = false
    private var deviceLoading = false
    /** バックグラウンド読み込みの世代。古い結果・detach 後の描画を捨てるための連番。 */
    private var loadSeq = 0
    private val deviceExecutor = Executors.newSingleThreadExecutor()
    // 追加/編集ダイアログの欄 (端末ピッカーの結果を流し込む先)
    private var pickName: EditText? = null
    private var pickNumber: EditText? = null
    private var pickSoto: RadioButton? = null
    private var pickIsAdd = true
    /** ダイアログが閉じている間に受け取ったピッカーの結果 (名前 / 番号)。 */
    private var pendingPick: Pair<String, String>? = null

    /**
     * 端末の連絡先ピッカー (`ACTION_PICK` on `Phone.CONTENT_URI`)。
     * 番号単位で選ばせるシステム UI のため、1 人に複数番号があっても選べる。
     * 返り値 URI には一時的な読み取り権限が付くため `READ_CONTACTS` は要らない。
     */
    private val pickPhone =
        registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
            if (result.resultCode != Activity.RESULT_OK) return@registerForActivityResult
            val ctx = context ?: return@registerForActivityResult
            val uri = result.data?.data
            if (uri == null) {
                Toast.makeText(ctx, R.string.contacts_read_fail, Toast.LENGTH_SHORT).show()
                return@registerForActivityResult
            }
            try {
                ctx.contentResolver.query(
                    uri, arrayOf(Phone.DISPLAY_NAME, Phone.NUMBER), null, null, null
                )?.use { c ->
                    if (c.moveToFirst()) {
                        val name = c.getString(0).orEmpty()
                        val number = c.getString(1).orEmpty()
                        if (number.isBlank()) {
                            Toast.makeText(ctx, R.string.contacts_read_fail, Toast.LENGTH_SHORT).show()
                            return@registerForActivityResult
                        }
                        applyPicked(name, number)
                    } else {
                        Toast.makeText(ctx, R.string.contacts_read_fail, Toast.LENGTH_SHORT).show()
                    }
                } ?: Toast.makeText(ctx, R.string.contacts_read_fail, Toast.LENGTH_SHORT).show()
            } catch (e: Exception) {
                Toast.makeText(ctx, R.string.contacts_read_fail, Toast.LENGTH_SHORT).show()
            }
        }

    override fun onCreateView(
        inflater: LayoutInflater, container: ViewGroup?, savedInstanceState: Bundle?
    ): View = inflater.inflate(R.layout.fragment_contacts, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        store = ContactStore.fromContext(requireContext())
        container = view.findViewById(R.id.contactContainer)
        tvEmpty = view.findViewById(R.id.tvEmptyContacts)
        etSearch = view.findViewById(R.id.etSearch)
        etSearch.addTextChangedListener(object : TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, a: Int, b: Int, c: Int) = Unit
            override fun onTextChanged(s: CharSequence?, a: Int, b: Int, c: Int) = Unit
            override fun afterTextChanged(s: Editable?) {
                render()
            }
        })
        view.findViewById<View>(R.id.fabAdd).setOnClickListener { showEditDialog(null) }
        render()
    }

    override fun onResume() {
        super.onResume()
        // 履歴タブの「連絡先に追加」から戻った場合に反映する。
        // 端末側の変更も拾うためキャッシュを捨てて読み直す。
        deviceLoaded = false
        if (::store.isInitialized) render()
    }

    override fun onDestroy() {
        loadSeq++   // 進行中の読み込み結果を捨てる
        deviceExecutor.shutdownNow()
        super.onDestroy()
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

    // ---- 描画 ----

    private fun render() {
        if (!::store.isInitialized || !isAdded) return
        val ctx = context ?: return
        val query = etSearch.text?.toString().orEmpty()
        val all = runCatching { store.search(query) }.getOrDefault(emptyList())
        // 端末の連絡先: トグル ON かつ権限ありのときだけ「端末」グループを出す (既定 OFF)。
        // 読み込みは必ずワーカースレッドで行い、結果をメインスレッドで描画する。
        val showDevice = BridgeConfig.load(ctx).deviceContactsEnabled &&
            DeviceContacts.hasPermission(ctx)
        if (showDevice && !deviceLoaded && !deviceLoading) startDeviceLoad()
        val deviceShown = if (showDevice) filterContacts(deviceAll, query) else emptyList()
        // 空状態テキストは既存のまま (ローカルも端末も 0 件なら従来の文言)
        container.removeAllViews()
        if (all.isEmpty() && deviceShown.isEmpty()) {
            tvEmpty.visibility = View.VISIBLE
            container.addView(tvEmpty)
            return
        }
        tvEmpty.visibility = View.GONE
        val uchi = all.filter { it.group == ContactGroup.UCHI }.sortedBy { it.name }
        val soto = all.filter { it.group == ContactGroup.SOTO }.sortedBy { it.name }
        if (uchi.isNotEmpty()) {
            container.addView(makeGroupLabel(getString(R.string.group_uchi)))
            container.addView(makeCard(uchi))
        }
        if (soto.isNotEmpty()) {
            container.addView(makeGroupLabel(getString(R.string.group_soto)))
            container.addView(makeCard(soto))
        }
        if (deviceShown.isNotEmpty()) {
            container.addView(makeGroupLabel(getString(R.string.group_device)))
            container.addView(makeDeviceCard(deviceShown))
        }
    }

    /** 端末の連絡先をワーカースレッドで読み、メインスレッドで描画する。 */
    private fun startDeviceLoad() {
        val appCtx = requireContext().applicationContext
        deviceLoading = true
        val seq = ++loadSeq
        deviceExecutor.execute {
            val loaded = runCatching { DeviceContacts.all(appCtx) }.getOrDefault(emptyList())
            activity?.runOnUiThread {
                if (!isAdded || seq != loadSeq) return@runOnUiThread
                deviceLoading = false
                deviceAll = loaded
                deviceLoaded = true
                render()
            }
        }
    }

    private fun makeGroupLabel(title: String): TextView {
        val ctx = requireContext()
        val density = resources.displayMetrics.density
        return TextView(ctx).apply {
            text = title
            setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text_dim))
            textSize = 13f
            setPadding(
                (4 * density).toInt(), (12 * density).toInt(),
                (4 * density).toInt(), (4 * density).toInt()
            )
        }
    }

    private fun makeCard(contacts: List<Contact>): LinearLayout {
        val ctx = requireContext()
        val card = LinearLayout(ctx).apply {
            orientation = LinearLayout.VERTICAL
            background = ContextCompat.getDrawable(ctx, R.drawable.card_bg)
            setPadding(0, (4 * densityPx()).toInt(), 0, (4 * densityPx()).toInt())
        }
        contacts.forEachIndexed { i, c ->
            card.addView(makeRow(c))
            if (i != contacts.size - 1) card.addView(makeDivider())
        }
        return card
    }

    private fun densityPx(): Float = resources.displayMetrics.density

    private fun makeDivider(): View {
        val ctx = requireContext()
        val density = densityPx()
        return View(ctx).apply {
            setBackgroundColor(ContextCompat.getColor(ctx, R.color.nocturne_border))
            layoutParams = LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, (1 * density).toInt()
            ).apply {
                marginStart = (64 * density).toInt()
                marginEnd = (12 * density).toInt()
            }
        }
    }

    /** 行: アバター円 / 名前 + 番号 / 受話器アイコン。タップ=発信、長押し=編集/削除。 */
    private fun makeRow(c: Contact): LinearLayout {
        val ctx = requireContext()
        val density = densityPx()
        val row = LinearLayout(ctx).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            // 押下フィードバック (selectableItemBackground をテーマから解決)
            val out = android.util.TypedValue()
            ctx.theme.resolveAttribute(android.R.attr.selectableItemBackground, out, true)
            setBackgroundResource(out.resourceId)
            isClickable = true
            isFocusable = true
            setPadding((12 * density).toInt(), (12 * density).toInt(), (12 * density).toInt(), (12 * density).toInt())
        }
        val avatar = TextView(ctx).apply {
            text = c.name.firstOrNull()?.toString() ?: "?"
            gravity = Gravity.CENTER
            background = ContextCompat.getDrawable(ctx, R.drawable.avatar_circle)
            setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_accent_text))
            textSize = 18f
            layoutParams = LinearLayout.LayoutParams((40 * density).toInt(), (40 * density).toInt())
        }
        row.addView(avatar)
        val middle = LinearLayout(ctx).apply {
            orientation = LinearLayout.VERTICAL
            layoutParams = LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f).apply {
                marginStart = (12 * density).toInt()
            }
        }
        middle.addView(TextView(ctx).apply {
            text = c.name
            setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text))
            textSize = 16f
            maxLines = 1
            ellipsize = android.text.TextUtils.TruncateAt.END
        })
        middle.addView(TextView(ctx).apply {
            text = c.number
            setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text_dim))
            textSize = 13f
            maxLines = 1
            ellipsize = android.text.TextUtils.TruncateAt.END
        })
        row.addView(middle)
        val callIcon = ImageView(ctx).apply {
            setImageResource(android.R.drawable.ic_menu_call)
            setColorFilter(ContextCompat.getColor(ctx, R.color.nocturne_accent))
            layoutParams = LinearLayout.LayoutParams((40 * density).toInt(), (40 * density).toInt())
            setPadding((8 * density).toInt(), (8 * density).toInt(), (8 * density).toInt(), (8 * density).toInt())
            contentDescription = ctx.getString(R.string.contacts_call)
        }
        callIcon.setOnClickListener { DialHelper.dial(this, c.number) }
        row.addView(callIcon)
        row.setOnClickListener { DialHelper.dial(this, c.number) }
        row.setOnLongClickListener {
            showRowMenu(c)
            true
        }
        return row
    }

    /** 端末グループのカード。行はローカル行と同じ体裁で、2 行目は「番号・ラベル」。 */
    private fun makeDeviceCard(contacts: List<DeviceContact>): LinearLayout {
        val ctx = requireContext()
        val card = LinearLayout(ctx).apply {
            orientation = LinearLayout.VERTICAL
            background = ContextCompat.getDrawable(ctx, R.drawable.card_bg)
            setPadding(0, (4 * densityPx()).toInt(), 0, (4 * densityPx()).toInt())
        }
        contacts.forEachIndexed { i, c ->
            card.addView(makeDeviceRow(c))
            if (i != contacts.size - 1) card.addView(makeDivider())
        }
        return card
    }

    /**
     * 端末の行: タップ=発信 ([DialHelper] 経由)、長押し=「連絡先に保存」のみ
     * (編集・削除は出さない。読み取り専用)。
     */
    private fun makeDeviceRow(c: DeviceContact): LinearLayout {
        val ctx = requireContext()
        val density = densityPx()
        val row = LinearLayout(ctx).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            val out = android.util.TypedValue()
            ctx.theme.resolveAttribute(android.R.attr.selectableItemBackground, out, true)
            setBackgroundResource(out.resourceId)
            isClickable = true
            isFocusable = true
            setPadding((12 * density).toInt(), (12 * density).toInt(), (12 * density).toInt(), (12 * density).toInt())
        }
        val avatar = TextView(ctx).apply {
            text = c.name.firstOrNull()?.toString() ?: "?"
            gravity = Gravity.CENTER
            background = ContextCompat.getDrawable(ctx, R.drawable.avatar_circle)
            setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_accent_text))
            textSize = 18f
            layoutParams = LinearLayout.LayoutParams((40 * density).toInt(), (40 * density).toInt())
        }
        row.addView(avatar)
        val middle = LinearLayout(ctx).apply {
            orientation = LinearLayout.VERTICAL
            layoutParams = LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f).apply {
                marginStart = (12 * density).toInt()
            }
        }
        middle.addView(TextView(ctx).apply {
            text = c.name
            setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text))
            textSize = 16f
            maxLines = 1
            ellipsize = android.text.TextUtils.TruncateAt.END
        })
        val sub = if (c.label.isNotBlank()) "${c.number}・${c.label}" else c.number
        middle.addView(TextView(ctx).apply {
            text = sub
            setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text_dim))
            textSize = 13f
            maxLines = 1
            ellipsize = android.text.TextUtils.TruncateAt.END
        })
        row.addView(middle)
        val callIcon = ImageView(ctx).apply {
            setImageResource(android.R.drawable.ic_menu_call)
            setColorFilter(ContextCompat.getColor(ctx, R.color.nocturne_accent))
            layoutParams = LinearLayout.LayoutParams((40 * density).toInt(), (40 * density).toInt())
            setPadding((8 * density).toInt(), (8 * density).toInt(), (8 * density).toInt(), (8 * density).toInt())
            contentDescription = ctx.getString(R.string.contacts_call)
        }
        callIcon.setOnClickListener { DialHelper.dial(this, c.number) }
        row.addView(callIcon)
        row.setOnClickListener { DialHelper.dial(this, c.number) }
        row.setOnLongClickListener {
            showDeviceRowMenu(c)
            true
        }
        return row
    }

    /** 端末の行の長押しメニュー: 「連絡先に保存」(ローカル `ContactStore` へコピー) のみ。 */
    private fun showDeviceRowMenu(c: DeviceContact) {
        val ctx = requireContext()
        MaterialAlertDialogBuilder(ctx)
            .setItems(arrayOf(ctx.getString(R.string.contacts_save_to_local))) { _, _ ->
                runCatching { store.add(c.name, c.number, ContactGroup.SOTO) }
                Toast.makeText(ctx, R.string.contacts_saved, Toast.LENGTH_SHORT).show()
                render()
            }
            .show()
    }

    // ---- 操作 ----

    /** 長押しメニュー: 編集 / 削除。 */
    private fun showRowMenu(c: Contact) {
        val ctx = requireContext()
        MaterialAlertDialogBuilder(ctx)
            .setItems(
                arrayOf(
                    ctx.getString(R.string.common_edit),
                    ctx.getString(R.string.common_delete_item)
                )
            ) { _, which ->
                when (which) {
                    0 -> showEditDialog(c)
                    1 -> confirmRemove(c)
                }
            }
            .show()
    }

    private fun confirmRemove(c: Contact) {
        val ctx = requireContext()
        MaterialAlertDialogBuilder(ctx)
            .setMessage(ctx.getString(R.string.contacts_delete_msg, c.name))
            .setPositiveButton(R.string.common_delete) { _, _ ->
                runCatching { store.remove(c.id) }
                render()
            }
            .setNegativeButton(R.string.common_cancel, null)
            .show()
    }

    /**
     * 追加・編集ダイアログ (名前 / 番号 / グループ)。
     * 先頭に「端末の連絡先から選ぶ」ボタン (連絡先アプリが無い端末では非表示)。
     * @param existing null なら追加、非 null なら編集。
     */
    private fun showEditDialog(existing: Contact?) {
        val ctx = requireContext()
        val density = densityPx()
        val root = LinearLayout(ctx).apply {
            orientation = LinearLayout.VERTICAL
            setPadding((24 * density).toInt(), (8 * density).toInt(), (24 * density).toInt(), 0)
        }
        // 端末の連絡先ピッカーへの導線 (権限不要。番号単位で選べる)。
        // 対応アプリが無ければボタンを出さない。
        if (hasPickApp()) {
            root.addView(MaterialButton(ctx).apply {
                text = ctx.getString(R.string.contacts_pick_from_device)
                setOnClickListener { launchPick() }
            })
        }
        val etName = EditText(ctx).apply {
            hint = ctx.getString(R.string.contacts_name_hint)
            setText(existing?.name.orEmpty())
            inputType = InputType.TYPE_CLASS_TEXT
            setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text))
            setHintTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text_dim))
        }
        val etNumber = EditText(ctx).apply {
            hint = ctx.getString(R.string.contacts_number_hint)
            setText(existing?.number.orEmpty())
            inputType = InputType.TYPE_CLASS_PHONE
            setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text))
            setHintTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text_dim))
        }
        root.addView(etName)
        root.addView(etNumber)
        val group = RadioGroup(ctx).apply { orientation = RadioGroup.HORIZONTAL }
        val rbUchi = RadioButton(ctx).apply {
            text = ctx.getString(R.string.group_uchi)
            isChecked = (existing?.group ?: ContactGroup.SOTO) == ContactGroup.UCHI
        }
        val rbSoto = RadioButton(ctx).apply {
            text = ctx.getString(R.string.group_soto)
            isChecked = (existing?.group ?: ContactGroup.SOTO) == ContactGroup.SOTO
        }
        group.addView(rbUchi)
        group.addView(rbSoto)
        root.addView(group)
        // ピッカーの結果を流し込む先を覚える (ダイアログ表示中のみ有効)
        pickName = etName
        pickNumber = etNumber
        pickSoto = rbSoto
        pickIsAdd = (existing == null)
        // 回転などでダイアログが閉じている間に選ばれた結果を反映する
        pendingPick?.let { (n, num) ->
            etName.setText(n)
            etNumber.setText(num)
            rbSoto.isChecked = true
            pendingPick = null
        }
        MaterialAlertDialogBuilder(ctx)
            .setTitle(
                if (existing == null) R.string.contacts_add_title else R.string.contacts_edit_title
            )
            .setView(root)
            .setPositiveButton(R.string.common_save) { _, _ ->
                val name = etName.text.toString().trim()
                val number = etNumber.text.toString().trim()
                if (name.isEmpty() || number.isEmpty()) {
                    Toast.makeText(ctx, R.string.contacts_need_both, Toast.LENGTH_SHORT).show()
                    return@setPositiveButton
                }
                val g = if (rbUchi.isChecked) ContactGroup.UCHI else ContactGroup.SOTO
                runCatching {
                    if (existing == null) store.add(name, number, g)
                    else store.update(existing.id, name, number, g)
                }
                render()
            }
            .setNegativeButton(R.string.common_cancel, null)
            .setOnDismissListener { clearPickRefs() }
            .show()
    }

    /**
     * ピッカーの結果をダイアログの欄に流し込む。取り込みのグループ既定は「ソト」。
     * 画面回転などでダイアログが閉じている場合 (欄の参照が無い) は、
     * 値を保持したまま追加ダイアログを開き直して流し込む。
     */
    private fun applyPicked(name: String, number: String) {
        val etNumber = pickNumber
        if (etNumber == null) {
            pendingPick = name to number
            showEditDialog(null)
            return
        }
        pickName?.setText(name)
        etNumber.setText(number)
        if (pickIsAdd) pickSoto?.isChecked = true
    }

    /** ダイアログが閉じたら欄の参照を捨てる (View を抱えたままにしない)。 */
    private fun clearPickRefs() {
        pickName = null
        pickNumber = null
        pickSoto = null
    }

    /** 端末の連絡先ピッカーを開く。対応アプリの有無は表示時に判定済み。 */
    private fun launchPick() {
        val intent = Intent(Intent.ACTION_PICK, Phone.CONTENT_URI)
        runCatching { pickPhone.launch(intent) }.onFailure {
            context?.let { ctx ->
                Toast.makeText(ctx, R.string.contacts_read_fail, Toast.LENGTH_SHORT).show()
            }
        }
    }

    /** 端末の連絡先ピッカーに対応するアプリがあるか (`resolveActivity` で事前判定)。 */
    private fun hasPickApp(): Boolean {
        val ctx = context ?: return false
        val intent = Intent(Intent.ACTION_PICK, Phone.CONTENT_URI)
        val pm = ctx.packageManager
        return try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                pm.resolveActivity(intent, PackageManager.ResolveInfoFlags.of(0)) != null
            } else {
                @Suppress("DEPRECATION")
                pm.resolveActivity(intent, 0) != null
            }
        } catch (e: Exception) {
            false
        }
    }
}

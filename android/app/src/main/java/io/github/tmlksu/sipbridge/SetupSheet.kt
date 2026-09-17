package io.github.tmlksu.sipbridge

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.ContextCompat
import com.google.android.material.bottomsheet.BottomSheetDialogFragment
import com.google.android.material.button.MaterialButton

/**
 * まとめて設定シート (UI-DESIGN §6.4)。
 * 設定画面の「セットアップ」カードの「まとめて設定」ボタンと、
 * [MainActivity] の自動表示 (初回起動 / BLOCKING 不足) から開く。
 *
 * 行の順序: マイク → 通知 → 電池の最適化 → 休止の除外 → オーバーレイ → 全画面通知。
 * - ランタイム権限はその場のダイアログ (`RequestMultiplePermissions`)。
 *   2 回拒否 (`shouldShowRequestPermissionRationale` false) のときだけアプリ情報画面へ。
 * - 特別なアクセスは [SystemStatus] の Intent 候補で開き、
 *   戻ってきたら `onResume` で再判定して行を更新する。
 */
class SetupSheet : BottomSheetDialogFragment() {

    private lateinit var rows: LinearLayout

    /** 拒否後の誘導先判定のため、要求中の項目を覚える。 */
    private var pendingItem: Item? = null

    private val permLauncher =
        registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { results ->
            val item = pendingItem
            pendingItem = null
            val ctx = context
            if (ctx != null && item != null && results.values.any { !it }) {
                // 拒否された権限のうち、もうダイアログが出ないものがあればアプリ情報画面へ。
                val deadEnd = results.entries.any { (perm, granted) ->
                    !granted && !shouldShowRequestPermissionRationale(perm)
                }
                if (deadEnd) {
                    SystemStatus.startFirstResolvable(
                        ctx, listOf(SystemStatus.appDetailsIntent(ctx))
                    )
                }
            }
            if (ctx != null && item == Item.TELECOM_ACCOUNT &&
                SystemStatus.hasPermission(ctx, Manifest.permission.CALL_PHONE) &&
                SystemStatus.hasPermission(ctx, Manifest.permission.READ_PHONE_STATE)
            ) {
                // 両方揃ったので有効化画面へ進む (片方だけではティア A が成立しない)。
                SystemStatus.startFirstResolvable(ctx, TelecomCompat.enableAccountIntents(ctx))
            }
            refresh()
        }

    private enum class Item {
        MIC, NOTIFICATIONS, BATTERY, HIBERNATION, OVERLAY, TELECOM_ACCOUNT, FULLSCREEN,
    }

    override fun onCreateView(
        inflater: LayoutInflater, container: ViewGroup?, savedInstanceState: Bundle?
    ): View = inflater.inflate(R.layout.setup_sheet, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        rows = view.findViewById(R.id.setupRows)
        view.findViewById<View>(R.id.btnSetupClose).setOnClickListener { dismiss() }
        refresh()
    }

    override fun onResume() {
        super.onResume()
        // 設定画面から戻ってきたら再判定する (状態が変わらなければ次の候補を案内する)。
        if (::rows.isInitialized) refresh()
    }

    private fun refresh() {
        val ctx = context ?: return
        val status = SystemStatus.read(ctx)
        rows.removeAllViews()
        val inflater = LayoutInflater.from(ctx)
        for (item in Item.entries) {
            // 非対応端末では行自体を出さない (休止・全画面・通話アカウント)。
            if (item == Item.HIBERNATION && status.hibernationExempt == null) continue
            if (item == Item.FULLSCREEN && status.fullScreenIntentAllowed == null) continue
            if (item == Item.TELECOM_ACCOUNT && status.telecomAccountEnabled == null) continue
            rows.addView(makeRow(inflater, item, status))
        }
    }

    private fun isDone(item: Item, s: SystemStatus): Boolean = when (item) {
        Item.MIC -> s.micGranted
        Item.NOTIFICATIONS -> s.notificationsEnabled
        Item.BATTERY -> s.ignoringBatteryOptimizations
        Item.HIBERNATION -> s.hibernationExempt == true
        Item.OVERLAY -> s.overlayGranted
        Item.TELECOM_ACCOUNT -> s.telecomAccountEnabled == true
        Item.FULLSCREEN -> s.fullScreenIntentAllowed == true
    }

    /** マイク・通知は BLOCKING = 必須、他は推奨 (§6.2 の重要度に対応)。 */
    private fun isRequired(item: Item): Boolean =
        item == Item.MIC || item == Item.NOTIFICATIONS

    private fun titleRes(item: Item): Int = when (item) {
        Item.MIC -> R.string.setup_item_mic
        Item.NOTIFICATIONS -> R.string.setup_item_notifications
        Item.BATTERY -> R.string.setup_item_battery
        Item.HIBERNATION -> R.string.setup_item_hibernation
        Item.OVERLAY -> R.string.setup_item_overlay
        Item.TELECOM_ACCOUNT -> R.string.setup_item_telecom
        Item.FULLSCREEN -> R.string.setup_item_fullscreen
    }

    private fun descRes(item: Item): Int = when (item) {
        Item.MIC -> R.string.setup_item_mic_desc
        Item.NOTIFICATIONS -> R.string.setup_item_notifications_desc
        Item.BATTERY -> R.string.setup_item_battery_desc
        Item.HIBERNATION -> R.string.setup_item_hibernation_desc
        Item.OVERLAY -> R.string.setup_item_overlay_desc
        Item.TELECOM_ACCOUNT -> R.string.setup_item_telecom_desc
        Item.FULLSCREEN -> R.string.setup_item_fullscreen_desc
    }

    /** 通話アカウント行の説明。電話の権限不足が原因のときはその旨を出す。 */
    private fun descResFor(item: Item, s: SystemStatus): Int =
        if (item == Item.TELECOM_ACCOUNT &&
            s.telecomAccountEnabled == false &&
            (!s.callPhoneGranted || !s.readPhoneStateGranted)
        ) {
            R.string.setup_item_telecom_desc_need_permission
        } else {
            descRes(item)
        }

    private fun makeRow(inflater: LayoutInflater, item: Item, s: SystemStatus): View {
        val row = inflater.inflate(R.layout.item_setup_row, rows, false)
        val ctx = requireContext()
        row.findViewById<TextView>(R.id.tvSetupItemTitle).text = getString(titleRes(item))
        row.findViewById<TextView>(R.id.tvSetupItemDesc).text = getString(descResFor(item, s))
        val badge = row.findViewById<TextView>(R.id.tvSetupItemBadge)
        val btn = row.findViewById<MaterialButton>(R.id.btnSetupItemAction)
        if (isDone(item, s)) {
            badge.text = getString(R.string.setup_badge_done)
            badge.setTextColor(ctx.getColor(R.color.nocturne_accent_text))
            btn.visibility = View.GONE
        } else {
            badge.text = if (isRequired(item)) getString(R.string.setup_badge_required)
            else getString(R.string.setup_badge_recommended)
            badge.setTextColor(
                ctx.getColor(
                    if (isRequired(item)) R.color.nocturne_missed else R.color.nocturne_text_dim
                )
            )
            btn.visibility = View.VISIBLE
            btn.text = getString(
                if (item == Item.MIC || item == Item.NOTIFICATIONS) R.string.setup_action_allow
                else R.string.setup_action_open
            )
            btn.setOnClickListener { onAction(item) }
        }
        return row
    }

    private fun onAction(item: Item) {
        val ctx = requireContext()
        when (item) {
            Item.MIC -> requestRuntime(listOf(Manifest.permission.RECORD_AUDIO), item)
            Item.NOTIFICATIONS -> {
                // API 33+ で未許可ならまず権限リクエスト。許可済み/それ以外は設定画面へ。
                if (Build.VERSION.SDK_INT >= 33 &&
                    ContextCompat.checkSelfPermission(
                        ctx, Manifest.permission.POST_NOTIFICATIONS
                    ) != PackageManager.PERMISSION_GRANTED
                ) {
                    requestRuntime(listOf(Manifest.permission.POST_NOTIFICATIONS), item)
                } else {
                    openOrToast(SystemStatus.notificationSettingsIntent(ctx))
                }
            }
            Item.BATTERY -> openOrToast(SystemStatus.batteryIntents(ctx))
            Item.HIBERNATION -> openOrToast(SystemStatus.hibernationIntents(ctx))
            Item.OVERLAY -> openOrToast(SystemStatus.overlayIntent(ctx))
            Item.TELECOM_ACCOUNT -> onTelecomAction()
            Item.FULLSCREEN -> openOrToast(SystemStatus.fullscreenIntent(ctx))
        }
    }

    /**
     * 通話アカウント行: CALL_PHONE と READ_PHONE_STATE が両方未許可なら同時に要求し、
     * 両方揃ってから発信アカウント設定 (有効化画面) へ進む。
     * `getCallCapablePhoneAccounts()` には READ_PHONE_STATE が必須で、
     * 片方だけではティア A が成立しない。
     * 未許可でもアプリは従来どおり動く (ティア C に落ちるだけ) ため推奨扱い。
     */
    private fun onTelecomAction() {
        val ctx = requireContext()
        val missing = buildList {
            if (ContextCompat.checkSelfPermission(ctx, Manifest.permission.CALL_PHONE) !=
                PackageManager.PERMISSION_GRANTED
            ) add(Manifest.permission.CALL_PHONE)
            if (ContextCompat.checkSelfPermission(ctx, Manifest.permission.READ_PHONE_STATE) !=
                PackageManager.PERMISSION_GRANTED
            ) add(Manifest.permission.READ_PHONE_STATE)
        }
        if (missing.isNotEmpty()) {
            requestRuntime(missing, Item.TELECOM_ACCOUNT)
        } else {
            openOrToast(TelecomCompat.enableAccountIntents(ctx))
        }
    }

    private fun requestRuntime(perms: List<String>, item: Item) {
        pendingItem = item
        permLauncher.launch(perms.toTypedArray())
    }

    private fun openOrToast(intents: List<android.content.Intent>) {
        val ctx = requireContext()
        if (intents.isEmpty() ||
            !SystemStatus.startFirstResolvable(ctx, intents)
        ) {
            Toast.makeText(ctx, R.string.setup_open_fail, Toast.LENGTH_SHORT).show()
        }
    }
}

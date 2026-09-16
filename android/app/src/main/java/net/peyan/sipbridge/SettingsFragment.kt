package net.peyan.sipbridge

import android.Manifest
import android.app.NotificationManager
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.view.inputmethod.EditorInfo
import android.widget.EditText
import android.widget.TextView
import android.widget.Toast
import androidx.core.content.ContextCompat
import androidx.fragment.app.Fragment
import androidx.activity.result.contract.ActivityResultContracts
import com.google.android.material.button.MaterialButton
import com.google.android.material.button.MaterialButtonToggleGroup
import com.google.android.material.slider.Slider
import com.google.android.material.switchmaterial.SwitchMaterial
import com.google.android.material.textfield.TextInputLayout

/**
 * UI-DESIGN §1.4 設定。
 * - 状態カード / 接続 / SIP アカウント / 動作 / 音声 / 権限 / テスト / フッター。
 * - 接続・SIP アカウントは**インライン入力** (旧: 行タップでダイアログ)。
 *   欄からフォーカスが外れた時点 (または IME の完了) で**その欄だけ**保存する。
 * - 必須項目の相互チェックは保存時に行わない。以前は 1 欄保存するたびに
 *   「relay URL 必須」「Access Client ID か Dev Token 必須」を両方課していたため、
 *   どちらを先に入力しても弾かれて何も保存できないデッドロックになっていた。
 *   未入力は状態カードにまとめて表示し、「開始」を押したときだけ検証する。
 */
class SettingsFragment : Fragment(), CallHub.StateListener {

    private lateinit var tvConnTitle: TextView
    private lateinit var tvConnDetail: TextView
    private lateinit var tvHubStatus: TextView
    private lateinit var tvSetupHint: TextView
    private lateinit var btnStartStop: MaterialButton
    private lateinit var tilRelayUrl: TextInputLayout
    private lateinit var toggleMode: MaterialButtonToggleGroup
    private lateinit var btnModePersistent: MaterialButton
    private lateinit var btnModePush: MaterialButton
    private lateinit var tvModeDesc: TextView
    private lateinit var swAutostart: SwitchMaterial
    private lateinit var swOverlay: SwitchMaterial
    private lateinit var tvOverlayWarn: TextView
    private lateinit var swSpeaker: SwitchMaterial
    private lateinit var swDeviceContacts: SwitchMaterial
    private lateinit var tvMicGain: TextView
    private lateinit var sliderMicGain: Slider
    private lateinit var tvPermOverlay: TextView
    private lateinit var tvPermBattery: TextView
    private lateinit var tvPermNotification: TextView
    private lateinit var tvPermFullscreen: TextView
    private lateinit var tvFooter: TextView

    /** Switch などのプログラム側 setChecked がリスナーを発火させないためのガード。 */
    private var binding = false
    private var pendingStartAction: (() -> Unit)? = null

    /**
     * 端末の連絡先表示トグル用の権限要求。許可されたら ON を保存し、
     * 拒否されたらトグルを OFF に戻す (起動時に勝手に要求はしない)。
     */
    private val requestContactsPermission =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            val ctx = context ?: return@registerForActivityResult
            val cur = BridgeConfig.load(ctx)
            BridgeConfig.save(ctx, cur.copy(deviceContactsEnabled = granted))
            refreshAll()
        }

    /**
     * インライン入力欄 1 つぶんの束縛。いずれも接続に関わる項目なので、
     * Bridge 稼働中かつ設定が揃っているときは保存後に再接続する。
     */
    private class Field(
        val et: EditText,
        val read: (BridgeConfigData) -> String,
        val write: (BridgeConfigData, String) -> BridgeConfigData
    )

    private val fields = mutableListOf<Field>()

    override fun onCreateView(
        inflater: LayoutInflater, container: ViewGroup?, savedInstanceState: Bundle?
    ): View = inflater.inflate(R.layout.fragment_settings, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        tvConnTitle = view.findViewById(R.id.tvConnTitle)
        tvConnDetail = view.findViewById(R.id.tvConnDetail)
        tvHubStatus = view.findViewById(R.id.tvHubStatus)
        tvSetupHint = view.findViewById(R.id.tvSetupHint)
        btnStartStop = view.findViewById(R.id.btnStartStop)
        tilRelayUrl = view.findViewById(R.id.tilRelayUrl)
        toggleMode = view.findViewById(R.id.toggleMode)
        btnModePersistent = view.findViewById(R.id.btnModePersistent)
        btnModePush = view.findViewById(R.id.btnModePush)
        tvModeDesc = view.findViewById(R.id.tvModeDesc)
        swAutostart = view.findViewById(R.id.swAutostart)
        swOverlay = view.findViewById(R.id.swOverlay)
        tvOverlayWarn = view.findViewById(R.id.tvOverlayWarn)
        tvOverlayWarn.setOnClickListener { openOverlaySetting() }
        swSpeaker = view.findViewById(R.id.swSpeaker)
        swDeviceContacts = view.findViewById(R.id.swDeviceContacts)
        tvMicGain = view.findViewById(R.id.tvMicGain)
        sliderMicGain = view.findViewById(R.id.sliderMicGain)
        tvPermOverlay = view.findViewById(R.id.tvPermOverlay)
        tvPermBattery = view.findViewById(R.id.tvPermBattery)
        tvPermNotification = view.findViewById(R.id.tvPermNotification)
        tvPermFullscreen = view.findViewById(R.id.tvPermFullscreen)
        tvFooter = view.findViewById(R.id.tvFooter)

        // スライダー範囲 0.5〜4.5 (0.1 刻み)
        sliderMicGain.valueFrom = 0.5f
        sliderMicGain.valueTo = 4.5f
        sliderMicGain.stepSize = 0.1f

        btnStartStop.setOnClickListener { onStartStop() }

        // 接続カード / SIP アカウントカード: インライン入力。
        // 欄ごとに独立して保存する (相互の必須チェックはしない → デッドロック回避)。
        fields.clear()   // View 再生成時に前回の EditText を持ち越さない
        bindField(view.findViewById(R.id.etRelayUrl), { it.relayUrl }, { c, v -> c.copy(relayUrl = v.trim()) })
        bindField(view.findViewById(R.id.etAccessId), { it.accessClientId }, { c, v -> c.copy(accessClientId = v.trim()) })
        bindField(view.findViewById(R.id.etAccessSecret), { it.accessClientSecret }, { c, v -> c.copy(accessClientSecret = v) })
        bindField(view.findViewById(R.id.etDevToken), { it.devToken }, { c, v -> c.copy(devToken = v) })
        bindField(view.findViewById(R.id.etSipUser), { it.sipUser }, { c, v -> c.copy(sipUser = v.trim()) })
        bindField(view.findViewById(R.id.etSipPassword), { it.sipPassword }, { c, v -> c.copy(sipPassword = v) })
        bindField(view.findViewById(R.id.etSipDisplay), { it.sipDisplay }, { c, v -> c.copy(sipDisplay = v.trim()) })

        // 動作カード: モード切替は保存 + Service 再起動
        toggleMode.addOnButtonCheckedListener { _, checkedId, isChecked ->
            if (!isChecked || binding) return@addOnButtonCheckedListener
            val cur = BridgeConfig.load(requireContext())
            val mode = if (checkedId == R.id.btnModePush) BridgeMode.PUSH else BridgeMode.PERSISTENT
            if (mode != cur.mode) updateAndRestart(cur.copy(mode = mode))
        }
        swAutostart.setOnCheckedChangeListener { _, checked ->
            if (binding) return@setOnCheckedChangeListener
            val cur = BridgeConfig.load(requireContext())
            BridgeConfig.save(requireContext(), cur.copy(autostart = checked))
        }
        swOverlay.setOnCheckedChangeListener { _, checked ->
            if (binding) return@setOnCheckedChangeListener
            val cur = BridgeConfig.load(requireContext())
            BridgeConfig.save(requireContext(), cur.copy(overlayEnabled = checked))
            // ON にした時点で権限が無ければそのまま許可画面へ (着信バブル・
            // 通話中ピルは権限が無いと一切出ないため、ここで気付けるようにする)
            if (checked && !canDrawOverlays()) {
                Toast.makeText(requireContext(), R.string.settings_overlay_warn, Toast.LENGTH_LONG).show()
                openOverlaySetting()
            }
            refreshAll()
        }
        swSpeaker.setOnCheckedChangeListener { _, checked ->
            if (binding) return@setOnCheckedChangeListener
            val cur = BridgeConfig.load(requireContext())
            BridgeConfig.save(requireContext(), cur.copy(speakerOnAnswer = checked))
        }
        swDeviceContacts.setOnCheckedChangeListener { _, checked ->
            if (binding) return@setOnCheckedChangeListener
            val ctx = requireContext()
            if (checked) {
                if (DeviceContacts.hasPermission(ctx)) {
                    BridgeConfig.save(ctx, BridgeConfig.load(ctx).copy(deviceContactsEnabled = true))
                } else {
                    // 許可されるまで ON にしない。権限ダイアログをここでだけ出す。
                    binding = true
                    swDeviceContacts.isChecked = false
                    binding = false
                    requestContactsPermission.launch(Manifest.permission.READ_CONTACTS)
                }
            } else {
                BridgeConfig.save(ctx, BridgeConfig.load(ctx).copy(deviceContactsEnabled = false))
            }
            refreshAll()
        }

        // 音声カード: スライダー確定で保存 (再起動不要)
        sliderMicGain.addOnChangeListener { _, value, fromUser ->
            tvMicGain.text = getString(R.string.settings_gain, value)
            if (fromUser) {
                CallHub.micGain = value
                CallHub.rtp?.micGain = value
            }
        }
        sliderMicGain.addOnSliderTouchListener(object : Slider.OnSliderTouchListener {
            override fun onStartTrackingTouch(slider: Slider) = Unit
            override fun onStopTrackingTouch(slider: Slider) {
                val cur = BridgeConfig.load(requireContext())
                BridgeConfig.save(requireContext(), cur.copy(micGain = slider.value))
            }
        })

        // 権限カード: タップで該当設定画面へ
        view.findViewById<View>(R.id.rowPermOverlay).setOnClickListener { openOverlaySetting() }
        view.findViewById<View>(R.id.rowPermBattery).setOnClickListener { openBatterySetting() }
        view.findViewById<View>(R.id.rowPermNotification).setOnClickListener { openNotificationSetting() }
        view.findViewById<View>(R.id.rowPermFullscreen).setOnClickListener { openFullscreenSetting() }

        // テスト着信 (relay 無しで着信 UI を確認)
        view.findViewById<View>(R.id.btnTestIncoming).setOnClickListener {
            CallHub.from = getString(R.string.settings_test_from)
            CallHub.display = getString(R.string.settings_test_display)
            CallHub.state = CallHub.State.RINGING
            startActivity(Intent(requireContext(), CallActivity::class.java))
        }

        refreshAll()
    }

    override fun onResume() {
        super.onResume()
        CallHub.addListener(this)
        refreshAll()
    }

    override fun onPause() {
        super.onPause()
        // タブ移動・アプリ切り替えでも入力中の欄を取りこぼさない。
        commitFocusedField()
        CallHub.removeListener(this)
    }

    override fun onChanged() {
        activity?.runOnUiThread { refreshAll() }
    }

    // ---- 表示更新 ----

    private fun refreshAll() {
        val ctx = context ?: return
        val cfg = BridgeConfig.load(ctx)
        binding = true
        try {
            // 状態カード (稼働中かどうかは Service の実状態で判定する)
            val running = BridgeService.running
            val status = CallHub.status
            tvConnTitle.text = when {
                !running -> getString(R.string.settings_conn_title_offline)
                CallHub.registered -> getString(R.string.settings_conn_title_online)
                else -> getString(R.string.settings_conn_title_retry)
            }
            val unset = getString(R.string.common_unset)
            val ext = CallHub.extension.ifBlank { cfg.sipUser.ifBlank { unset } }
            tvConnDetail.text = getString(
                R.string.settings_conn_detail, cfg.relayUrl.ifBlank { unset }, ext
            )
            tvHubStatus.text = status
            btnStartStop.text = if (running) getString(R.string.settings_conn_stop)
            else getString(R.string.settings_conn_start)

            // 未入力の必須項目 (接続できない理由) をまとめて表示
            val missing = missingItems(cfg)
            tvSetupHint.text =
                if (missing.isEmpty()) "" else getString(R.string.settings_setup_missing, missing.joinToString("・"))
            tvSetupHint.visibility = if (missing.isEmpty()) View.GONE else View.VISIBLE

            // 接続 / SIP アカウントカード: 保存値を入力欄へ (編集中の欄は触らない)
            fields.forEach { f ->
                if (f.et.hasFocus()) return@forEach
                val v = f.read(cfg)
                if (f.et.text?.toString() != v) f.et.setText(v)
            }

            // 動作カード
            toggleMode.check(if (cfg.mode == BridgeMode.PUSH) R.id.btnModePush else R.id.btnModePersistent)
            tvModeDesc.text = if (cfg.mode == BridgeMode.PUSH) {
                getString(R.string.settings_mode_desc_push)
            } else {
                getString(R.string.settings_mode_desc_persistent)
            }
            swAutostart.isChecked = cfg.autostart
            swOverlay.isChecked = cfg.overlayEnabled
            tvOverlayWarn.visibility =
                if (cfg.overlayEnabled && !canDrawOverlays()) View.VISIBLE else View.GONE
            swSpeaker.isChecked = cfg.speakerOnAnswer
            // システム設定で権限を取り消されていたらトグルも OFF に戻す
            if (cfg.deviceContactsEnabled && !DeviceContacts.hasPermission(ctx)) {
                BridgeConfig.save(ctx, cfg.copy(deviceContactsEnabled = false))
                swDeviceContacts.isChecked = false
            } else {
                swDeviceContacts.isChecked = cfg.deviceContactsEnabled
            }

            // 音声カード
            tvMicGain.text = getString(R.string.settings_gain, cfg.micGain)
            sliderMicGain.value = cfg.micGain.coerceIn(0.5f, 4.5f)

            // 権限カード
            tvPermOverlay.text = if (canDrawOverlays()) getString(R.string.settings_perm_allowed)
            else getString(R.string.settings_perm_denied)
            tvPermBattery.text = if (isBatteryExcluded()) getString(R.string.settings_perm_excluded)
            else getString(R.string.settings_perm_not_excluded)
            tvPermNotification.text = if (areNotificationsEnabled()) getString(R.string.settings_perm_allowed)
            else getString(R.string.settings_perm_denied)
            tvPermFullscreen.text = fullscreenState(ctx)

            // フッター: アプリ版 / relay 版 / device id (先頭 8 桁)
            val appVer = runCatching {
                ctx.packageManager.getPackageInfo(ctx.packageName, 0).versionName
            }.getOrNull() ?: "?"
            val relayVer = CallHub.relayVersion.ifBlank { "?" }
            val dev = cfg.deviceId.take(8).ifBlank { "?" }
            tvFooter.text = getString(R.string.settings_footer, appVer, relayVer, dev)
        } finally {
            binding = false
        }
    }

    // ---- インライン入力: 欄ごとの保存 ----

    /**
     * 入力欄を設定項目に束縛する。保存は「フォーカスが外れたとき」と
     * 「IME の完了キー」の 2 箇所。**この欄以外の必須チェックはしない**
     * (どちらを先に入力しても弾かれるデッドロックを避けるため)。
     */
    private fun bindField(
        et: EditText,
        read: (BridgeConfigData) -> String,
        write: (BridgeConfigData, String) -> BridgeConfigData
    ) {
        val f = Field(et, read, write)
        fields.add(f)
        et.setOnFocusChangeListener { _, hasFocus -> if (!hasFocus) commitField(f) }
        et.setOnEditorActionListener { _, actionId, _ ->
            if (actionId == EditorInfo.IME_ACTION_DONE || actionId == EditorInfo.IME_ACTION_NEXT) {
                et.clearFocus()   // フォーカス喪失リスナで保存される
                true
            } else {
                false
            }
        }
    }

    /** 1 欄ぶんを保存する。変更が無ければ何もしない。 */
    private fun commitField(f: Field) {
        if (binding) return
        val ctx = context ?: return
        val cur = BridgeConfig.load(ctx)
        val next = f.write(cur, f.et.text?.toString() ?: "")
        // relay URL は形式だけその場で知らせる (保存は止めない)
        if (f.et.id == R.id.etRelayUrl) {
            val url = next.relayUrl
            tilRelayUrl.error =
                if (url.isNotBlank() && runCatching { normalizeRelayUrl(url) }.isFailure)
                    getString(R.string.settings_err_url_invalid)
                else null
        }
        if (next == cur) return
        BridgeConfig.save(ctx, next)
        // 稼働中かつ接続に必要な項目が揃っているときだけ張り直す。
        if (BridgeService.running && missingItems(next).isEmpty()) {
            restartBridge(next)
        }
        refreshAll()
    }

    /** フォーカス中の欄があれば確定させる (開始ボタン・画面離脱時)。 */
    private fun commitFocusedField() {
        fields.firstOrNull { it.et.hasFocus() }?.let { it.et.clearFocus() }
    }

    /** 接続に足りない必須項目。空なら開始できる。 */
    private fun missingItems(cfg: BridgeConfigData): List<String> {
        val miss = mutableListOf<String>()
        if (cfg.relayUrl.isBlank()) miss += getString(R.string.settings_missing_relay_url)
        if (cfg.accessClientId.isBlank() && cfg.devToken.isBlank()) {
            miss += getString(R.string.settings_missing_auth)
        } else if (cfg.accessClientId.isNotBlank() && cfg.accessClientSecret.isBlank() &&
            cfg.devToken.isBlank()
        ) {
            miss += getString(R.string.settings_missing_access_secret)
        }
        return miss
    }

    private fun restartBridge(d: BridgeConfigData) {
        val ctx = context ?: return
        CallHub.micGain = d.micGain
        requestMicThen {
            ctx.stopService(Intent(ctx, BridgeService::class.java))
            BridgeService.start(ctx)
            Toast.makeText(
                ctx, ctx.getString(R.string.settings_saved_restart, d.mode), Toast.LENGTH_SHORT
            ).show()
        }
    }

    /** 動作カードの項目 (モードなど) は 1 項目でも接続に関わるので同じ扱い。 */
    private fun updateAndRestart(d: BridgeConfigData) {
        val ctx = requireContext()
        BridgeConfig.save(ctx, d)
        CallHub.micGain = d.micGain
        if (BridgeService.running && missingItems(d).isEmpty()) restartBridge(d)
        refreshAll()
    }

    private fun onStartStop() {
        val ctx = requireContext()
        commitFocusedField()
        if (!BridgeService.running) {
            val missing = missingItems(BridgeConfig.load(ctx))
            if (missing.isNotEmpty()) {
                Toast.makeText(
                    ctx,
                    getString(R.string.settings_err_start_missing, missing.joinToString("・")),
                    Toast.LENGTH_LONG
                ).show()
                refreshAll()
                return
            }
            requestMicThen {
                BridgeService.start(ctx)
                Toast.makeText(ctx, R.string.settings_start, Toast.LENGTH_SHORT).show()
            }
        } else {
            ctx.stopService(Intent(ctx, BridgeService::class.java))
            CallHub.updateStatus("停止中")
        }
        refreshAll()
    }

    private fun requestMicThen(action: () -> Unit) {
        val ctx = requireContext()
        if (ContextCompat.checkSelfPermission(ctx, Manifest.permission.RECORD_AUDIO) ==
            PackageManager.PERMISSION_GRANTED
        ) {
            action()
        } else {
            pendingStartAction = action
            requestPermissions(arrayOf(Manifest.permission.RECORD_AUDIO), 11)
        }
    }

    @Deprecated("use Activity Result API in P2+")
    override fun onRequestPermissionsResult(code: Int, perms: Array<out String>, res: IntArray) {
        @Suppress("DEPRECATION")
        super.onRequestPermissionsResult(code, perms, res)
        if (code == 11) {
            if (res.firstOrNull() == PackageManager.PERMISSION_GRANTED) pendingStartAction?.invoke()
            else Toast.makeText(requireContext(), R.string.common_mic_permission, Toast.LENGTH_LONG).show()
            pendingStartAction = null
        } else if (code == 12) {
            // 通知権限の結果。状態表示だけ更新する。
            refreshAll()
        }
    }

    // ---- 権限の状態と導線 (旧 MainActivity の 2 つ + 通知/全画面を追加) ----

    private fun canDrawOverlays(): Boolean =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) Settings.canDrawOverlays(requireContext())
        else true

    private fun openOverlaySetting() {
        val ctx = requireContext()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M && !Settings.canDrawOverlays(ctx)) {
            startActivity(Intent(Settings.ACTION_MANAGE_OVERLAY_PERMISSION, Uri.parse("package:${ctx.packageName}")))
        } else {
            Toast.makeText(ctx, R.string.settings_perm_overlay_ok, Toast.LENGTH_SHORT).show()
        }
    }

    private fun isBatteryExcluded(): Boolean {
        val pm = requireContext().getSystemService(Context.POWER_SERVICE) as PowerManager
        return Build.VERSION.SDK_INT < Build.VERSION_CODES.M ||
            pm.isIgnoringBatteryOptimizations(requireContext().packageName)
    }

    private fun openBatterySetting() {
        val ctx = requireContext()
        try {
            val pm = ctx.getSystemService(Context.POWER_SERVICE) as PowerManager
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M &&
                !pm.isIgnoringBatteryOptimizations(ctx.packageName)
            ) {
                startActivity(
                    Intent(
                        Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                        Uri.parse("package:${ctx.packageName}")
                    )
                )
            } else {
                Toast.makeText(ctx, R.string.settings_perm_battery_ok, Toast.LENGTH_SHORT).show()
            }
        } catch (e: Exception) {
            Toast.makeText(ctx, ctx.getString(R.string.common_open_settings_fail, e.message), Toast.LENGTH_SHORT).show()
        }
    }

    private fun areNotificationsEnabled(): Boolean {
        val ctx = requireContext()
        val nm = ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (!nm.areNotificationsEnabled()) return false
        if (Build.VERSION.SDK_INT >= 33) {
            return ContextCompat.checkSelfPermission(ctx, Manifest.permission.POST_NOTIFICATIONS) ==
                PackageManager.PERMISSION_GRANTED
        }
        return true
    }

    private fun openNotificationSetting() {
        val ctx = requireContext()
        // API 33+ で未許可ならまず権限リクエスト。許可済み/それ以外は設定画面へ。
        if (Build.VERSION.SDK_INT >= 33 &&
            ContextCompat.checkSelfPermission(ctx, Manifest.permission.POST_NOTIFICATIONS) !=
            PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 12)
            return
        }
        try {
            startActivity(Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS).apply {
                putExtra(Settings.EXTRA_APP_PACKAGE, ctx.packageName)
            })
        } catch (e: Exception) {
            Toast.makeText(ctx, ctx.getString(R.string.common_open_settings_fail, e.message), Toast.LENGTH_SHORT).show()
        }
    }

    private fun fullscreenState(ctx: Context): String {
        if (Build.VERSION.SDK_INT < 34) return ctx.getString(R.string.settings_perm_na)
        val nm = ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        return if (nm.canUseFullScreenIntent()) ctx.getString(R.string.settings_perm_allowed)
        else ctx.getString(R.string.settings_perm_denied)
    }

    private fun openFullscreenSetting() {
        val ctx = requireContext()
        if (Build.VERSION.SDK_INT < 34) {
            Toast.makeText(ctx, R.string.settings_perm_fullscreen_na, Toast.LENGTH_SHORT).show()
            return
        }
        try {
            startActivity(
                Intent(Settings.ACTION_MANAGE_APP_USE_FULL_SCREEN_INTENT).apply {
                    data = Uri.parse("package:${ctx.packageName}")
                }
            )
        } catch (e: Exception) {
            Toast.makeText(ctx, ctx.getString(R.string.common_open_settings_fail, e.message), Toast.LENGTH_SHORT).show()
        }
    }
}

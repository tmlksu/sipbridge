package io.github.tmlksu.sipbridge

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.view.inputmethod.EditorInfo
import android.widget.EditText
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AlertDialog
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
    private lateinit var tvPermMic: TextView
    private lateinit var tvPermHibernation: TextView
    private lateinit var rowPermHibernation: View
    private lateinit var rowTelecom: View
    private lateinit var tvTelecomValue: TextView
    private lateinit var tvTelecomState: TextView
    private lateinit var tvSamsungGuide: TextView
    private lateinit var tvFooter: TextView
    private lateinit var cardSetup: View
    private lateinit var tvSetupTitle: TextView
    private lateinit var tvSetupLines: TextView
    private lateinit var swQuiet: SwitchMaterial

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
            updateConfig(ctx) { it.copy(deviceContactsEnabled = granted) }
            refreshAll()
        }

    /**
     * §6.4 権限カードの「マイク」「通知」行用の権限要求。結果に関わらず表示だけ更新する。
     * 2 回拒否でダイアログが出なくなった場合は SetupSheet 側でアプリ情報画面へ誘導する。
     */
    private val requestRowPermission =
        registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {
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
    ) {
        /**
         * 入力欄に最後に入れた保存値。null = まだ保存値を入れていない (設定を一時的に読めなかった等)。
         * null の欄は保存しない (空欄のまま確定して保存値を "" で上書きしないため, #42)。
         */
        var bound: String? = null
    }

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
        tvPermMic = view.findViewById(R.id.tvPermMic)
        tvPermHibernation = view.findViewById(R.id.tvPermHibernation)
        rowPermHibernation = view.findViewById(R.id.rowPermHibernation)
        rowTelecom = view.findViewById(R.id.rowTelecom)
        tvTelecomValue = view.findViewById(R.id.tvTelecomValue)
        tvTelecomState = view.findViewById(R.id.tvTelecomState)
        rowTelecom.setOnClickListener { openTelecomChoice() }
        tvSamsungGuide = view.findViewById(R.id.tvSamsungGuide)
        tvSamsungGuide.setOnClickListener { openAppDetails() }
        tvFooter = view.findViewById(R.id.tvFooter)
        cardSetup = view.findViewById(R.id.cardSetup)
        tvSetupTitle = view.findViewById(R.id.tvSetupTitle)
        tvSetupLines = view.findViewById(R.id.tvSetupLines)
        view.findViewById<View>(R.id.btnSetupOpen).setOnClickListener { openSetupSheet() }
        swQuiet = view.findViewById(R.id.swQuiet)

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
            val mode = if (checkedId == R.id.btnModePush) BridgeMode.PUSH else BridgeMode.PERSISTENT
            if (mode != BridgeConfig.load(requireContext()).mode) updateAndRestart { it.copy(mode = mode) }
        }
        swAutostart.setOnCheckedChangeListener { _, checked ->
            if (binding) return@setOnCheckedChangeListener
            updateConfig(requireContext()) { it.copy(autostart = checked) }
        }
        swOverlay.setOnCheckedChangeListener { _, checked ->
            if (binding) return@setOnCheckedChangeListener
            updateConfig(requireContext()) { it.copy(overlayEnabled = checked) }
            // ON にした時点で権限が無ければそのまま許可画面へ (着信バブル・
            // 通話中ピルは権限が無いと一切出ないため、ここで気付けるようにする)
            if (checked && !SystemStatus.canDrawOverlays(requireContext())) {
                Toast.makeText(requireContext(), R.string.settings_overlay_warn, Toast.LENGTH_LONG).show()
                openOverlaySetting()
            }
            refreshAll()
        }
        swQuiet.setOnCheckedChangeListener { _, checked ->
            if (binding) return@setOnCheckedChangeListener
            val ctx = requireContext()
            updateConfig(ctx) { it.copy(serviceNotificationQuiet = checked) }
            // §6.5: Service 再起動なしで出し直す (停止中なら何もしない)。
            if (BridgeService.running) runCatching {
                val svc = Intent(ctx, BridgeService::class.java)
                ctx.bindService(svc, object : android.content.ServiceConnection {
                    override fun onServiceConnected(
                        name: android.content.ComponentName?,
                        binder: android.os.IBinder?
                    ) {
                        runCatching {
                            (binder as? BridgeService.LocalBinder)?.service()
                                ?.refreshServiceNotification()
                        }
                        runCatching { ctx.unbindService(this) }
                    }
                    override fun onServiceDisconnected(name: android.content.ComponentName?) = Unit
                }, android.content.Context.BIND_AUTO_CREATE)
            }
            if (checked) showQuietDialog()
            refreshAll()
        }
        swSpeaker.setOnCheckedChangeListener { _, checked ->
            if (binding) return@setOnCheckedChangeListener
            updateConfig(requireContext()) { it.copy(speakerOnAnswer = checked) }
        }
        swDeviceContacts.setOnCheckedChangeListener { _, checked ->
            if (binding) return@setOnCheckedChangeListener
            val ctx = requireContext()
            if (checked) {
                if (DeviceContacts.hasPermission(ctx)) {
                    updateConfig(ctx) { it.copy(deviceContactsEnabled = true) }
                } else {
                    // 許可されるまで ON にしない。権限ダイアログをここでだけ出す。
                    binding = true
                    swDeviceContacts.isChecked = false
                    binding = false
                    requestContactsPermission.launch(Manifest.permission.READ_CONTACTS)
                }
            } else {
                updateConfig(ctx) { it.copy(deviceContactsEnabled = false) }
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
                val gain = slider.value
                updateConfig(requireContext()) { it.copy(micGain = gain) }
            }
        })

        // 権限カード: タップで該当設定画面へ
        view.findViewById<View>(R.id.rowPermOverlay).setOnClickListener { openOverlaySetting() }
        view.findViewById<View>(R.id.rowPermBattery).setOnClickListener { openBatterySetting() }
        view.findViewById<View>(R.id.rowPermNotification).setOnClickListener { openNotificationSetting() }
        view.findViewById<View>(R.id.rowPermFullscreen).setOnClickListener { openFullscreenSetting() }
        view.findViewById<View>(R.id.rowPermMic).setOnClickListener { openMicSetting() }
        view.findViewById<View>(R.id.rowPermHibernation).setOnClickListener { openHibernationSetting() }

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
        // 保存領域に一時的にアクセスできない (#42) ときは既定値で表示を組むが、
        // 入力欄を空で上書きしたり「未入力」を出したりはしない。
        val loaded = BridgeConfig.loadOrNull(ctx)
        val storageUnavailable = loaded == null
        val cfg = loaded ?: BridgeConfigData()
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
            val missing = if (storageUnavailable) emptyList() else missingItems(cfg)
            // 保存済みの relay URL が使えない形 (旧版で保存した平文の LAN 宛てなど) なら理由を出す (#43)。
            // 接続側 (RelayClient) はこの場合、設定が変わるまで再接続しない。
            val savedUrlError = savedRelayUrlError(cfg.relayUrl)
            if (savedUrlError != null && !tilRelayUrl.hasFocus()) tilRelayUrl.error = savedUrlError
            // 暗号化ストアが壊れていて初期化した場合は再設定を促す。揃ったら通知を消す (#42)。
            val resetNotice = !storageUnavailable && BridgeConfig.configResetAt(ctx) > 0L
            if (resetNotice && missing.isEmpty()) BridgeConfig.clearConfigResetNotice(ctx)
            val hints = buildList {
                if (storageUnavailable) add(getString(R.string.settings_warn_storage_unavailable))
                if (resetNotice && missing.isNotEmpty()) add(getString(R.string.settings_warn_config_reset))
                if (missing.isNotEmpty()) {
                    add(getString(R.string.settings_setup_missing, missing.joinToString("・")))
                }
                if (savedUrlError != null) add(savedUrlError)
                // 設定を暗号化できず平文で保存している端末では、その旨も同じ欄に出す (#42)。
                if (BridgeConfig.plainFallback) add(getString(R.string.settings_warn_plain_storage))
            }
            tvSetupHint.text = hints.joinToString("\n")
            tvSetupHint.visibility = if (hints.isEmpty()) View.GONE else View.VISIBLE

            // 接続 / SIP アカウントカード: 保存値を入力欄へ (編集中の欄は触らない)
            fields.forEach { f ->
                if (f.et.hasFocus() || storageUnavailable) return@forEach
                val v = f.read(cfg)
                if (f.et.text?.toString() != v) f.et.setText(v)
                f.bound = v
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
            swSpeaker.isChecked = cfg.speakerOnAnswer
            // システム設定で権限を取り消されていたらトグルも OFF に戻す
            if (cfg.deviceContactsEnabled && !DeviceContacts.hasPermission(ctx)) {
                // refreshAll の中から再帰しないよう、失敗時の Toast/再描画はしない。
                BridgeConfig.update(ctx) { it.copy(deviceContactsEnabled = false) }
                swDeviceContacts.isChecked = false
            } else {
                swDeviceContacts.isChecked = cfg.deviceContactsEnabled
            }

            // 音声カード
            tvMicGain.text = getString(R.string.settings_gain, cfg.micGain)
            sliderMicGain.value = cfg.micGain.coerceIn(0.5f, 4.5f)

            // §6.4 セットアップカード (不足が無ければ GONE)。
            refreshSetupCard(ctx)

            swQuiet.isChecked = cfg.serviceNotificationQuiet

            // 権限カード (判定は SystemStatus に集約。ここでは値だけ見る)。
            val sys = SystemStatus.read(ctx)
            tvPermOverlay.text = if (sys.overlayGranted) getString(R.string.settings_perm_allowed)
            else getString(R.string.settings_perm_denied)
            tvPermBattery.text = if (sys.ignoringBatteryOptimizations) getString(R.string.settings_perm_excluded)
            else getString(R.string.settings_perm_not_excluded)
            tvPermNotification.text = if (sys.notificationsEnabled) getString(R.string.settings_perm_allowed)
            else getString(R.string.settings_perm_denied)
            tvPermFullscreen.text = when (sys.fullScreenIntentAllowed) {
                true -> getString(R.string.settings_perm_allowed)
                false -> getString(R.string.settings_perm_denied)
                null -> getString(R.string.settings_perm_na)
            }
            tvPermMic.text = if (sys.micGranted) getString(R.string.settings_perm_allowed)
            else getString(R.string.settings_perm_denied)
            // 休止機能を持たない端末では行自体を出さない。
            if (sys.hibernationExempt == null) {
                rowPermHibernation.visibility = View.GONE
            } else {
                rowPermHibernation.visibility = View.VISIBLE
                tvPermHibernation.text = if (sys.hibernationExempt) getString(R.string.settings_perm_allowed)
                else getString(R.string.settings_perm_denied)
            }
            // 通話画面 (Telecom 非対応端末では行ごと出さない)。
            refreshTelecomRow(ctx, cfg, sys)
            // Samsung 端末のみ One UI のスリープ案内 (§6.0 B は API では取れないため案内だけ)。
            tvOverlayWarn.visibility =
                if (cfg.overlayEnabled && !sys.overlayGranted) View.VISIBLE else View.GONE
            tvSamsungGuide.visibility = if (sys.isSamsung) View.VISIBLE else View.GONE

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

    /**
     * 設定の一部を変えて保存する ([BridgeConfig.update]: 読込→変換→書込を原子的に行う)。
     * 保存領域に一時的にアクセスできず保存できなかったときは理由を知らせ、表示を読み直す
     * (#42。既定値を元に書いたり平文に書いたりはしない)。
     */
    private fun updateConfig(
        ctx: android.content.Context,
        transform: (BridgeConfigData) -> BridgeConfigData
    ): Boolean {
        val ok = BridgeConfig.update(ctx, transform)
        if (!ok) {
            Toast.makeText(ctx, R.string.settings_warn_storage_unavailable, Toast.LENGTH_LONG).show()
            view?.post { refreshAll() }
        }
        return ok
    }

    /** 保存済みの relay URL が接続に使えない理由 (使えるか空なら null)。 */
    private fun savedRelayUrlError(url: String): String? {
        if (url.isBlank()) return null
        return when (checkRelayUrl(url)) {
            is RelayUrlCheck.Cleartext -> getString(R.string.settings_err_saved_url_cleartext)
            is RelayUrlCheck.Invalid -> getString(R.string.settings_err_url_invalid)
            else -> null
        }
    }

    /** 1 欄ぶんを保存する。変更が無ければ何もしない。 */
    private fun commitField(f: Field) {
        if (binding) return
        val ctx = context ?: return
        val text = f.et.text?.toString() ?: ""
        val bound = f.bound
        if (bound == null) {
            // まだ保存値を入れていない欄 (設定を読めなかった間に表示した欄など) は保存しない。
            refreshAll()
            return
        }
        if (text == bound) {
            // 変更なし。relay URL の欄は、拒否した入力を元に戻した場合に備えて表示だけ直す。
            if (f.et.id == R.id.etRelayUrl) tilRelayUrl.error = savedRelayUrlError(bound)
            return
        }
        // 検証用。保存は下の update で、その時点の保存値に対して行う。
        val next = f.write(BridgeConfig.load(ctx), text)
        // relay URL は形式をその場で知らせる。形式の誤りは保存を止めないが、
        // 平文 (ws:// / http://) でループバック以外を指すものは保存しない (#43。
        // Access トークン・SIP パスワードが平文で流れるため)。
        if (f.et.id == R.id.etRelayUrl) {
            val url = next.relayUrl
            when (val c = if (url.isBlank()) RelayUrlCheck.Empty else checkRelayUrl(url)) {
                is RelayUrlCheck.Cleartext -> {
                    tilRelayUrl.error = getString(
                        if (c.loopbackAllowed) R.string.settings_err_url_cleartext
                        else R.string.settings_err_url_cleartext_release
                    )
                    return
                }
                is RelayUrlCheck.Invalid -> tilRelayUrl.error = getString(R.string.settings_err_url_invalid)
                else -> tilRelayUrl.error = null
            }
        }
        var changed = false
        var saved: BridgeConfigData? = null
        val ok = updateConfig(ctx) { cur ->
            f.write(cur, text).also { changed = it != cur; saved = it }
        }
        val after = saved
        if (ok && after != null) f.bound = f.read(after)
        if (!ok || !changed || after == null) return
        // 稼働中かつ接続に必要な項目が揃っているときだけ張り直す。
        if (BridgeService.running && missingItems(after).isEmpty()) {
            restartBridge(after)
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
    private fun updateAndRestart(transform: (BridgeConfigData) -> BridgeConfigData) {
        val ctx = requireContext()
        var saved: BridgeConfigData? = null
        if (!updateConfig(ctx) { transform(it).also { n -> saved = n } }) return
        val d = saved ?: return
        CallHub.micGain = d.micGain
        if (BridgeService.running && missingItems(d).isEmpty()) restartBridge(d)
        refreshAll()
    }

    private fun onStartStop() {
        val ctx = requireContext()
        commitFocusedField()
        if (!BridgeService.running) {
            val loaded = BridgeConfig.loadOrNull(ctx)
            if (loaded == null) {
                // 保存領域に一時的にアクセスできない (#42)。「未入力」とは出さない。
                Toast.makeText(ctx, R.string.settings_warn_storage_unavailable, Toast.LENGTH_LONG).show()
                refreshAll()
                return
            }
            val missing = missingItems(loaded)
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

    // ---- 権限の導線 (§6.1)。判定は SystemStatus に集約し、ここでは候補の起動だけ行う ----

    private fun openOverlaySetting() {
        val ctx = requireContext()
        if (SystemStatus.canDrawOverlays(ctx)) {
            Toast.makeText(ctx, R.string.settings_perm_overlay_ok, Toast.LENGTH_SHORT).show()
        } else if (!SystemStatus.startFirstResolvable(ctx, SystemStatus.overlayIntent(ctx))) {
            Toast.makeText(ctx, R.string.setup_open_fail, Toast.LENGTH_SHORT).show()
        }
    }

    private fun openBatterySetting() {
        val ctx = requireContext()
        if (SystemStatus.isBatteryExcluded(ctx)) {
            Toast.makeText(ctx, R.string.settings_perm_battery_ok, Toast.LENGTH_SHORT).show()
        } else if (!SystemStatus.startFirstResolvable(ctx, SystemStatus.batteryIntents(ctx))) {
            Toast.makeText(ctx, R.string.setup_open_fail, Toast.LENGTH_SHORT).show()
        }
    }

    private fun openNotificationSetting() {
        val ctx = requireContext()
        // API 33+ で未許可ならまず権限リクエスト。許可済み/それ以外は設定画面へ。
        if (Build.VERSION.SDK_INT >= 33 &&
            ContextCompat.checkSelfPermission(ctx, Manifest.permission.POST_NOTIFICATIONS) !=
            PackageManager.PERMISSION_GRANTED
        ) {
            requestRowPermission.launch(arrayOf(Manifest.permission.POST_NOTIFICATIONS))
            return
        }
        if (!SystemStatus.startFirstResolvable(ctx, SystemStatus.notificationSettingsIntent(ctx))) {
            Toast.makeText(ctx, R.string.setup_open_fail, Toast.LENGTH_SHORT).show()
        }
    }

    private fun openFullscreenSetting() {
        val ctx = requireContext()
        if (Build.VERSION.SDK_INT < 34) {
            Toast.makeText(ctx, R.string.settings_perm_fullscreen_na, Toast.LENGTH_SHORT).show()
            return
        }
        if (!SystemStatus.startFirstResolvable(ctx, SystemStatus.fullscreenIntent(ctx))) {
            Toast.makeText(ctx, R.string.setup_open_fail, Toast.LENGTH_SHORT).show()
        }
    }

    /** §6.4 権限カードの「マイク」行: その場で要求し、結果は表示に反映するだけ。 */
    private fun openMicSetting() {
        val ctx = requireContext()
        if (SystemStatus.hasPermission(ctx, Manifest.permission.RECORD_AUDIO)) {
            Toast.makeText(ctx, R.string.settings_perm_mic_ok, Toast.LENGTH_SHORT).show()
            return
        }
        requestRowPermission.launch(arrayOf(Manifest.permission.RECORD_AUDIO))
    }

    // ---- 通話画面 (Telecom 統合) ----

    /** 通話画面行の表示。Telecom 非対応端末では行ごと出さない。 */
    private fun refreshTelecomRow(ctx: android.content.Context, cfg: BridgeConfigData, sys: SystemStatus) {
        if (!TelecomCompat.hasTelecom(ctx)) {
            rowTelecom.visibility = View.GONE
            tvTelecomState.visibility = View.GONE
            return
        }
        rowTelecom.visibility = View.VISIBLE
        tvTelecomState.visibility = View.VISIBLE
        tvTelecomValue.text = when (cfg.telecomPref) {
            TelecomTierManager.Pref.AUTO -> getString(R.string.settings_telecom_auto)
            TelecomTierManager.Pref.SYSTEM -> getString(R.string.settings_telecom_system)
            TelecomTierManager.Pref.APP -> getString(R.string.settings_telecom_app)
        }
        tvTelecomState.text = when {
            cfg.telecomPref == TelecomTierManager.Pref.APP ->
                getString(R.string.settings_telecom_state_app)
            sys.telecomAccountEnabled == true ->
                getString(R.string.settings_telecom_state_system)
            cfg.telecomPref == TelecomTierManager.Pref.SYSTEM ->
                getString(R.string.settings_telecom_state_app_disabled)
            else -> getString(R.string.settings_telecom_state_app)
        }
    }

    /** 通話画面の方式を選択するダイアログ。選択で保存し `sync` を呼ぶ。 */
    private fun openTelecomChoice() {
        val ctx = requireContext()
        val prefs = TelecomTierManager.Pref.entries.toTypedArray()
        val labels = prefs.map {
            when (it) {
                TelecomTierManager.Pref.AUTO -> getString(R.string.settings_telecom_auto)
                TelecomTierManager.Pref.SYSTEM -> getString(R.string.settings_telecom_system)
                TelecomTierManager.Pref.APP -> getString(R.string.settings_telecom_app)
            }
        }.toTypedArray()
        val cur = BridgeConfig.load(ctx).telecomPref
        AlertDialog.Builder(ctx)
            .setTitle(R.string.settings_row_telecom)
            .setSingleChoiceItems(labels, prefs.indexOf(cur)) { d, which ->
                updateConfig(ctx) { it.copy(telecomPref = prefs[which]) }
                TelecomTierManager.sync(ctx)
                d.dismiss()
                refreshAll()
            }
            .setNegativeButton(R.string.common_cancel, null)
            .show()
    }

    /** §6.4 権限カードの「アプリの休止を無効化」行。 */
    private fun openHibernationSetting() {
        val ctx = requireContext()
        if (!SystemStatus.startFirstResolvable(ctx, SystemStatus.hibernationIntents(ctx))) {
            Toast.makeText(ctx, R.string.setup_open_fail, Toast.LENGTH_SHORT).show()
        }
    }

    private fun openAppDetails() {
        val ctx = requireContext()
        SystemStatus.startFirstResolvable(ctx, listOf(SystemStatus.appDetailsIntent(ctx)))
    }

    // ---- セットアップカード (§6.4) ----

    /**
     * [PushHealth.evaluate] の不足のうち SetupSheet で対処できるものだけを
     * 「あと N 件 + 最大 3 行」で出す。対処できるものが無ければ GONE
     * (PUSH_TOKEN_MISSING / PUSH_REGISTRATION_STALE はシートに項目が無いため数えない)。
     */
    private fun refreshSetupCard(ctx: android.content.Context) {
        val issues = PushHealth.evaluate(System.currentTimeMillis(), PushHealth.buildSnapshot(ctx))
            .filter { PushHealth.isActionable(it.kind) }
        if (issues.isEmpty()) {
            cardSetup.visibility = View.GONE
            return
        }
        cardSetup.visibility = View.VISIBLE
        tvSetupTitle.text = getString(R.string.setup_card_title, issues.size)
        tvSetupLines.text = issues.take(3)
            .joinToString("\n") { HealthCheckReceiver.reasonText(ctx, it.kind) }
    }

    private fun openSetupSheet() {
        runCatching {
            SetupSheet().show(parentFragmentManager, TAG_SETUP_SHEET)
        }
    }

    // ---- 常駐通知の静音化 (§6.5) ----

    /** トグル ON 直後の案内: チャンネル OFF への導線 + 着信は別チャンネルである旨。 */
    private fun showQuietDialog() {
        val ctx = requireContext()
        AlertDialog.Builder(ctx)
            .setTitle(R.string.settings_quiet_dialog_title)
            .setMessage(R.string.settings_quiet_dialog_msg)
            .setPositiveButton(R.string.settings_quiet_dialog_open) { _, _ ->
                SystemStatus.startFirstResolvable(
                    ctx,
                    SystemStatus.channelSettingsIntent(ctx, NotificationHelper.CH_SERVICE_QUIET)
                )
            }
            .setNegativeButton(R.string.common_close, null)
            .show()
    }

    companion object {
        private const val TAG_SETUP_SHEET = "setup_sheet"
    }
}

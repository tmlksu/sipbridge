package io.github.tmlksu.sipbridge

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.content.res.Configuration
import android.os.Bundle
import android.view.Gravity
import android.view.HapticFeedbackConstants
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.Chronometer
import android.widget.GridLayout
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.core.content.ContextCompat
import androidx.fragment.app.Fragment
import com.google.android.material.button.MaterialButton

/**
 * UI-DESIGN §1.1 キーパッド。
 * - 上部左: 状態ピル (登録OK=accent / 未登録=text_dim / 未接続=border)。
 *   上部右: モードチップ (タップで設定タブへ)。
 * - 中央: 入力中の番号 (40sp) + キャプション (内線番号/外線/案内)。
 * - 3x4 の丸キー (0 長押しで +)。最下部: 発信ボタン + 削除 (長押しで全消去)。
 * - 発信中/通話中は発信ボタンの代わりに「通話に戻る」ピル。
 */
class KeypadFragment : Fragment(), CallHub.StateListener {

    private lateinit var tvStatusPill: TextView
    private lateinit var tvModeChip: TextView
    private lateinit var tvInCallPill: Chronometer
    private lateinit var tvNumber: TextView
    private lateinit var tvCaption: TextView
    private lateinit var keypadGrid: GridLayout
    private lateinit var btnCall: MaterialButton
    private lateinit var btnBackToCall: MaterialButton
    private lateinit var btnDelete: MaterialButton

    private val input = StringBuilder()
    private var pendingAction: (() -> Unit)? = null
    /** アプリ内通話中ピルの Chronometer に設定済みの通話開始時刻 (epoch ms)。0 = 停止中。 */
    private var pillStartedAt = 0L

    override fun onCreateView(
        inflater: LayoutInflater, container: ViewGroup?, savedInstanceState: Bundle?
    ): View = inflater.inflate(R.layout.fragment_keypad, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        tvStatusPill = view.findViewById(R.id.tvStatusPill)
        tvModeChip = view.findViewById(R.id.tvModeChip)
        tvInCallPill = view.findViewById(R.id.tvInCallPill)
        tvInCallPill.setOnClickListener {
            startActivity(Intent(requireContext(), CallActivity::class.java))
        }
        tvNumber = view.findViewById(R.id.tvNumber)
        tvCaption = view.findViewById(R.id.tvCaption)
        keypadGrid = view.findViewById(R.id.keypadGrid)
        btnCall = view.findViewById(R.id.btnCall)
        btnBackToCall = view.findViewById(R.id.btnBackToCall)
        btnDelete = view.findViewById(R.id.btnDelete)

        buildKeys()
        tvModeChip.setOnClickListener {
            (activity as? MainActivity)?.selectTab(R.id.nav_settings)
        }
        btnCall.setOnClickListener { dialInput() }
        btnBackToCall.setOnClickListener {
            startActivity(Intent(requireContext(), CallActivity::class.java))
        }
        btnDelete.setOnClickListener {
            if (input.isNotEmpty()) {
                input.deleteCharAt(input.length - 1)
                refreshNumber()
            }
            btnDelete.tapFeedback()
        }
        btnDelete.setOnLongClickListener {
            input.clear()
            refreshNumber()
            btnDelete.performHapticFeedback(HapticFeedbackConstants.LONG_PRESS)
            true
        }
        refreshNumber()
        refreshStatus()
    }

    override fun onResume() {
        super.onResume()
        CallHub.addListener(this)
        refreshStatus()
    }

    override fun onPause() {
        super.onPause()
        CallHub.removeListener(this)
        stopPill()
    }

    override fun onChanged() {
        activity?.runOnUiThread { refreshStatus() }
    }

    // ---- キー組み立て ----

    private data class Key(val digit: String, val sub: String)

    private fun buildKeys() {
        val keys = listOf(
            Key("1", ""), Key("2", "ABC"), Key("3", "DEF"),
            Key("4", "GHI"), Key("5", "JKL"), Key("6", "MNO"),
            Key("7", "PQRS"), Key("8", "TUV"), Key("9", "WXYZ"),
            Key("*", ""), Key("0", "+"), Key("#", "")
        )
        val ctx = requireContext()
        // 寸法は res/values(-land)/dimens.xml。横画面は縦を詰めた角丸キー
        // (背景は drawable-land/circle_key.xml)。
        val keyW = resources.getDimensionPixelSize(R.dimen.keypad_key_width)
        val keyH = keyHeightPx()
        val margin = resources.getDimensionPixelSize(R.dimen.keypad_key_margin)
        // キーが低いとき (横画面の狭い端末) は数字だけにして英字を省く
        val compact = keyH < 52 * resources.displayMetrics.density
        val digitSize = if (compact) 20f else if (keyH < 72 * resources.displayMetrics.density) 24f else 28f
        keys.forEach { k ->
            val cell = LinearLayout(ctx).apply {
                orientation = LinearLayout.VERTICAL
                gravity = Gravity.CENTER
                background = ContextCompat.getDrawable(ctx, R.drawable.circle_key)
                isClickable = true
                isFocusable = true
            }
            val num = TextView(ctx).apply {
                text = k.digit
                setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text))
                textSize = digitSize
                gravity = Gravity.CENTER
            }
            cell.addView(num)
            if (k.sub.isNotEmpty() && !compact) {
                cell.addView(TextView(ctx).apply {
                    text = k.sub
                    setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text_dim))
                    textSize = 9f
                    gravity = Gravity.CENTER
                })
            }
            val lp = GridLayout.LayoutParams().apply {
                width = keyW
                height = keyH
                setMargins(margin, margin, margin, margin)
            }
            cell.layoutParams = lp
            cell.setOnClickListener {
                input.append(k.digit)
                refreshNumber()
                cell.tapFeedback()
            }
            // 0 長押しで「+」
            if (k.digit == "0") {
                cell.setOnLongClickListener {
                    input.append("+")
                    refreshNumber()
                    cell.performHapticFeedback(HapticFeedbackConstants.LONG_PRESS)
                    true
                }
            }
            keypadGrid.addView(cell)
        }
    }

    /**
     * キー 1 個の高さ。縦画面は dimens どおり (72dp)。
     * 横画面は端末ごとに縦が足りない (Echo Show 5 = 利用可能 h321dp) ため、
     * 画面高から 4 行ぶんを割り付ける (状態ピルと上下 padding に約 64dp)。
     * 40dp を下限、72dp の丸キーに近づけすぎない 56dp を上限にする。
     */
    private fun keyHeightPx(): Int {
        if (resources.configuration.orientation != Configuration.ORIENTATION_LANDSCAPE) {
            return resources.getDimensionPixelSize(R.dimen.keypad_key_height)
        }
        val density = resources.displayMetrics.density
        val marginDp = resources.getDimensionPixelSize(R.dimen.keypad_key_margin) / density
        val availDp = resources.configuration.screenHeightDp - 64f
        val hDp = (availDp / 4f - marginDp * 2f).coerceIn(40f, 56f)
        return (hDp * density).toInt()
    }

    // ---- 表示更新 ----

    private fun refreshNumber() {
        tvNumber.text = input.toString()
        // P3: 入力中番号のキャプション (連絡先一致 → 名前、2〜4 桁 → 内線番号、それ以外 → 外線)
        val match: String? = if (input.isNotEmpty()) {
            val ctx = context
            if (ctx != null) runCatching {
                ContactStore.fromContext(ctx).lookup(input.toString())
            }.getOrNull() else null
        } else null
        tvCaption.text = when {
            input.isEmpty() -> getString(R.string.keypad_hint_empty)
            !match.isNullOrBlank() -> match
            input.length in 2..4 && input.all { it.isDigit() } -> getString(R.string.keypad_caption_extension)
            else -> getString(R.string.keypad_caption_external)
        }
    }

    private fun refreshStatus() {
        val ctx = context ?: return
        val registered = CallHub.registered
        val ext = CallHub.extension
        val status = CallHub.status
        // ドット色: 登録OK=accent / 未登録=text_dim / 未接続=border
        val (label, dotColor) = when {
            registered -> {
                val e = if (ext.isNotBlank()) getString(R.string.keypad_status_ext, ext) else ""
                getString(R.string.keypad_status_registered, e) to R.color.nocturne_accent
            }
            status.contains("未設定") || status.contains("停止") || status.contains("未接続") ->
                getString(R.string.keypad_status_offline) to R.color.nocturne_border
            else -> getString(R.string.keypad_status_unregistered) to R.color.nocturne_text_dim
        }
        tvStatusPill.text = label
        tvStatusPill.setTextColor(ContextCompat.getColor(ctx, dotColor))
        val mode = BridgeConfig.load(ctx).mode
        tvModeChip.text = if (mode == BridgeMode.PUSH) getString(R.string.keypad_mode_push)
        else getString(R.string.keypad_mode_persistent)
        // 発信中/通話中は「通話に戻る」ピル (§3.2 の入口。P2 で通話画面に遷移)
        val inCall = CallHub.state != CallHub.State.IDLE
        btnCall.visibility = if (inCall) View.GONE else View.VISIBLE
        btnDelete.visibility = if (inCall) View.GONE else View.VISIBLE
        btnBackToCall.visibility = if (inCall) View.VISIBLE else View.GONE
        // アプリ内通話中ピル: 通話中のみ上部右に「📞 mm:ss」(タップで通話画面)
        if (CallHub.state == CallHub.State.IN_CALL) {
            tvInCallPill.visibility = View.VISIBLE
            startPill()
        } else {
            tvInCallPill.visibility = View.GONE
            stopPill()
        }
    }

    /**
     * ピルの Chronometer を通話開始時刻に合わせて動かす。refreshStatus は状態変化のたびに
     * 呼ばれるため、開始時刻が変わったとき (resume で relay の startedAt に置き換わった等) だけ
     * base を設定し直す。
     */
    private fun startPill() {
        val start = CallHub.callStartedAt
        if (start == pillStartedAt && start != 0L) return
        pillStartedAt = start
        tvInCallPill.base = CallHub.callStartedElapsedRealtime()
        tvInCallPill.start()
    }

    private fun stopPill() {
        tvInCallPill.stop()
        pillStartedAt = 0L
    }

    // ---- 発信 ----

    private fun dialInput() {
        val to = input.toString().trim()
        if (to.isBlank()) {
            Toast.makeText(requireContext(), R.string.keypad_need_number, Toast.LENGTH_SHORT).show()
            return
        }
        requestMicThen {
            requireContext().startService(
                Intent(requireContext(), BridgeService::class.java)
                    .setAction(BridgeService.ACT_DIAL)
                    .putExtra(BridgeService.EXTRA_TO, to)
            )
        }
    }

    private fun requestMicThen(action: () -> Unit) {
        val ctx = requireContext()
        if (ContextCompat.checkSelfPermission(ctx, Manifest.permission.RECORD_AUDIO) ==
            PackageManager.PERMISSION_GRANTED
        ) {
            action()
        } else {
            pendingAction = action
            requestPermissions(arrayOf(Manifest.permission.RECORD_AUDIO), 10)
        }
    }

    @Deprecated("use Activity Result API in P2+")
    override fun onRequestPermissionsResult(code: Int, perms: Array<out String>, res: IntArray) {
        @Suppress("DEPRECATION")
        super.onRequestPermissionsResult(code, perms, res)
        if (code == 10 && res.firstOrNull() == PackageManager.PERMISSION_GRANTED) {
            pendingAction?.invoke()
        } else if (code == 10) {
            Toast.makeText(requireContext(), R.string.common_mic_permission, Toast.LENGTH_LONG).show()
        }
        pendingAction = null
    }
}

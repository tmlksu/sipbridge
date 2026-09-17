package io.github.tmlksu.sipbridge

import android.content.Context
import android.content.Intent
import android.content.res.ColorStateList
import android.graphics.Rect
import android.media.AudioManager
import android.media.Ringtone
import android.media.RingtoneManager
import android.media.ToneGenerator
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.PowerManager
import android.os.VibrationEffect
import android.os.Vibrator
import android.view.Gravity
import android.view.View
import android.view.WindowManager
import android.widget.GridLayout
import android.widget.LinearLayout
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.updatePadding
import com.google.android.material.bottomsheet.BottomSheetDialog
import com.google.android.material.button.MaterialButton

/**
 * UI-DESIGN §2/§3 の通話画面。旧 IncomingCallActivity を置換する。
 * 着信中 / 発信中 (呼出中) / 通話中 / 終了確認 / DTMF シートを 1 Activity で状態遷移する。
 * 真実は [CallHub] (state / outgoing / session) が持ち、本画面は表示に徹する。
 *
 * - 共通: キャプション「SIP BRIDGE・着信/発信/通話中」、アバター円 (1 文字目)、名前、番号、状態行。
 * - 状態行の「•••」は 3 点の点滅アニメ (Handler)。
 * - ロック画面上表示・画面点灯・KEEP_SCREEN_ON・着信音/バイブは旧画面から移植。
 *   ロック解除は求めない (認証なしでロック画面上のまま応答・通話できる)。
 * - 通話中 (スピーカー OFF) は近接センサーで画面を消す (耳に当てた状態の誤タップ防止)。
 * - 「縮小」ボタンは持たない。ホーム操作で離れれば Service が通話中ピルを出す。
 */
class CallActivity : AppCompatActivity(), CallHub.StateListener {

    private lateinit var tvCaption: TextView
    private lateinit var tvAvatar: TextView
    private lateinit var tvName: TextView
    private lateinit var tvNumber: TextView
    private lateinit var tvState: TextView
    private lateinit var tvEndToast: TextView
    private lateinit var layoutIncoming: LinearLayout
    private lateinit var layoutOutgoing: LinearLayout
    private lateinit var layoutIncall: LinearLayout
    private lateinit var btnMute: MaterialButton
    private lateinit var btnSpeaker: MaterialButton

    private var ringtone: Ringtone? = null
    private var vibrator: Vibrator? = null
    private var toneGenerator: ToneGenerator? = null
    private var dtmfSheet: BottomSheetDialog? = null
    private var tvDtmfInput: TextView? = null
    private val dtmfInput = StringBuilder()

    private val handler = Handler(Looper.getMainLooper())
    /** ドットアニメの位相 (0〜3)。通話中タイマーと共通の tick で更新する。 */
    private var dotsPhase = 0
    /** 通話開始を観測してからの経過秒 (終了表示用に保持)。 */
    private var lastElapsedSec = 0
    /** 一度でも RINGING/IN_CALL を表示したか (IDLE 直 finish と終了表示の区別用)。 */
    private var wasEverActive = false
    /** 終了表示 (1.5 秒) に入ったか。 */
    private var endShown = false
    /** 耳に当てている間だけ画面を消す近接ロック (通話中・スピーカー OFF のとき保持)。 */
    private var proximityLock: PowerManager.WakeLock? = null

    private val tick = object : Runnable {
        override fun run() {
            refreshStateLine()
            handler.postDelayed(this, 400)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // ティア A の呼では OS 標準画面が出ているため、自前 UI は誤って開かれても畳む。
        if (CallHub.tier == CallTier.MANAGED) {
            finish()
            return
        }
        // 通話中・着信中は画面を点けっぱなし (終了でActivityが閉じれば自動解除)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        // ロック画面上・画面ON
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O_MR1) {
            setShowWhenLocked(true)
            setTurnScreenOn(true)
        } else {
            @Suppress("DEPRECATION")
            window.addFlags(
                WindowManager.LayoutParams.FLAG_SHOW_WHEN_LOCKED or
                    WindowManager.LayoutParams.FLAG_TURN_SCREEN_ON or
                        WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON
            )
        }
        // ロック解除 (requestDismissKeyguard) は要求しない。
        // セキュアロック中に呼ぶと通話画面より先に PIN/生体の認証 UI が出てしまうため、
        // showWhenLocked のままロック画面の上で応答・通話できるようにする (標準の電話アプリと同じ挙動)。
        setContentView(R.layout.activity_call)
        applyWindowInsets()

        tvCaption = findViewById(R.id.tvCaption)
        tvAvatar = findViewById(R.id.tvAvatar)
        tvName = findViewById(R.id.tvName)
        tvNumber = findViewById(R.id.tvNumber)
        tvState = findViewById(R.id.tvState)
        tvEndToast = findViewById(R.id.tvEndToast)
        layoutIncoming = findViewById(R.id.layoutIncoming)
        layoutOutgoing = findViewById(R.id.layoutOutgoing)
        layoutIncall = findViewById(R.id.layoutIncall)
        btnMute = findViewById(R.id.btnMute)
        btnSpeaker = findViewById(R.id.btnSpeaker)

        findViewById<MaterialButton>(R.id.btnAnswer).setOnClickListener {
            stopRinging()
            sendServiceAction(BridgeService.ACT_ANSWER)
            refresh()
        }
        findViewById<MaterialButton>(R.id.btnReject).setOnClickListener {
            stopRinging()
            // 着信拒否に確認は出さない (即時)
            sendServiceAction(BridgeService.ACT_REJECT)
            finish()
        }
        findViewById<MaterialButton>(R.id.btnDisconnect).setOnClickListener {
            stopRinging()
            // 発信呼出中の切断に確認は出さない (即時)
            sendServiceAction(BridgeService.ACT_HANGUP)
            finish()
        }
        btnMute.setOnClickListener {
            val rtp = CallHub.rtp
            if (rtp != null) {
                rtp.muted = !rtp.muted
                paintToggle(btnMute, rtp.muted)
            }
        }
        findViewById<MaterialButton>(R.id.btnKeypad).setOnClickListener { showDtmfSheet() }
        btnSpeaker.setOnClickListener {
            val am = getSystemService(Context.AUDIO_SERVICE) as AudioManager
            val next = !AudioRoute.isSpeakerOn(am)
            AudioRoute.setSpeaker(am, next)
            paintToggle(btnSpeaker, AudioRoute.isSpeakerOn(am))
            // スピーカー中は耳に当てないので近接ロックを外す
            updateProximityLock()
        }
        // 保留 / 通話を追加 / 連絡先は v1 では無効 (レイアウト側で enabled=false, alpha=0.35)
        findViewById<MaterialButton>(R.id.btnEnd).setOnClickListener { onEndPressed() }
        refresh()
    }

    override fun onNewIntent(intent: Intent?) {
        super.onNewIntent(intent)
        refresh()
    }

    override fun onResume() {
        super.onResume()
        CallHub.addListener(this)
        // 実着信・発信・通話時のみ Service へ前面通知 (テスト表示では送らない)。
        // 発信直後は relay の ringing 前で session が null のため outgoing も見る
        // (でないと復帰しても通話中ピルが消えない)。
        if (CallHub.session != null || CallHub.outgoing) {
            runCatching {
                sendServiceAction(BridgeService.ACT_UI_SHOWN)
            }
        }
        handler.post(tick)
        refresh()
    }

    override fun onPause() {
        super.onPause()
        CallHub.removeListener(this)
        handler.removeCallbacks(tick)
        if (CallHub.session != null || CallHub.outgoing) {
            runCatching {
                sendServiceAction(BridgeService.ACT_UI_HIDDEN)
            }
        }
        if (CallHub.state == CallHub.State.IDLE) stopRinging()
        // 画面を離れたら近接ロックは持たない (画面が消えたままになるのを防ぐ)
        releaseProximityLock()
    }

    override fun onChanged() {
        runOnUiThread {
            if (CallHub.tier == CallTier.MANAGED) {
                finish()
                return@runOnUiThread
            }
            refresh()
        }
    }

    private fun sendServiceAction(action: String) {
        startService(Intent(this, BridgeService::class.java).setAction(action))
    }

    // ---- 表示更新 ----

    private fun refresh() {
        if (endShown) return
        val from = CallHub.from
        val display = CallHub.display
        val name = when {
            display.isNotBlank() -> display
            from.isNotBlank() -> from
            else -> getString(R.string.call_unknown)
        }
        tvName.text = name
        tvNumber.text = from
        // アバター円 = 表示名 or 番号の 1 文字目
        tvAvatar.text = (display.ifBlank { from }.firstOrNull()?.toString() ?: "?")
        when (CallHub.state) {
            CallHub.State.RINGING -> {
                wasEverActive = true
                if (CallHub.outgoing) {
                    tvCaption.text = getString(R.string.call_caption_outgoing)
                    layoutIncoming.visibility = View.GONE
                    layoutOutgoing.visibility = View.VISIBLE
                    layoutIncall.visibility = View.GONE
                } else {
                    tvCaption.text = getString(R.string.call_caption_incoming)
                    layoutIncoming.visibility = View.VISIBLE
                    layoutOutgoing.visibility = View.GONE
                    layoutIncall.visibility = View.GONE
                    startRinging()
                }
                refreshStateLine()
            }
            CallHub.State.IN_CALL -> {
                wasEverActive = true
                stopRinging()
                tvCaption.text = getString(R.string.call_caption_incall)
                layoutIncoming.visibility = View.GONE
                layoutOutgoing.visibility = View.GONE
                layoutIncall.visibility = View.VISIBLE
                // トグル状態を実態に合わせる
                paintToggle(btnMute, CallHub.rtp?.muted == true)
                val am = getSystemService(Context.AUDIO_SERVICE) as AudioManager
                paintToggle(btnSpeaker, AudioRoute.isSpeakerOn(am))
                refreshStateLine()
            }
            CallHub.State.IDLE -> {
                stopRinging()
                if (wasEverActive) showEndThenFinish() else finish()
            }
        }
        updateProximityLock()
    }

    /**
     * システムバーのインセットを画面全体の padding にする
     * (targetSdk 35 は edge-to-edge 強制。下端の操作ボタンがジェスチャーバーに
     * 重ならないようにする)。
     */
    private fun applyWindowInsets() {
        val root = findViewById<View>(R.id.rootCall)
        val basePadding = Rect(root.paddingLeft, root.paddingTop, root.paddingRight, root.paddingBottom)
        ViewCompat.setOnApplyWindowInsetsListener(root) { v, insets ->
            val bars = insets.getInsets(
                WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout()
            )
            v.updatePadding(
                left = basePadding.left + bars.left,
                top = basePadding.top + bars.top,
                right = basePadding.right + bars.right,
                bottom = basePadding.bottom + bars.bottom
            )
            WindowInsetsCompat.CONSUMED
        }
        ViewCompat.requestApplyInsets(root)
    }

    /**
     * 通話中かつスピーカー OFF のときだけ近接センサーで画面を消す。
     * (耳に当てている間の誤タップ防止。スピーカー中・着信/呼出中は保持しない)
     */
    private fun updateProximityLock() {
        val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
        val am = getSystemService(Context.AUDIO_SERVICE) as AudioManager
        val want = !isFinishing && !endShown &&
            CallHub.state == CallHub.State.IN_CALL && !AudioRoute.isSpeakerOn(am)
        if (want) {
            if (proximityLock == null) {
                if (!pm.isWakeLockLevelSupported(PowerManager.PROXIMITY_SCREEN_OFF_WAKE_LOCK)) return
                proximityLock = pm.newWakeLock(
                    PowerManager.PROXIMITY_SCREEN_OFF_WAKE_LOCK, "SipBridge:proximity"
                )
            }
            proximityLock?.let { if (!it.isHeld) runCatching { it.acquire(60 * 60 * 1000L) } }
        } else {
            releaseProximityLock()
        }
    }

    private fun releaseProximityLock() {
        proximityLock?.let { if (it.isHeld) runCatching { it.release() } }
    }

    /**
     * 状態行の更新 (tick 400ms ごと + refresh 時)。
     * - 着信中 / 呼出中: 「•••」の 3 点点滅アニメ (1〜3 点を循環)。
     * - 呼出中は 183 early media 時に「呼出中 (相手側応答音)」。
     * - 通話中: 経過時間 mm:ss。
     */
    private fun refreshStateLine() {
        if (endShown) return
        dotsPhase = (dotsPhase + 1) % 4
        val dots = "•".repeat(dotsPhase + 1)
        when (CallHub.state) {
            CallHub.State.RINGING -> {
                tvState.text = if (CallHub.outgoing) {
                    if (CallHub.earlyMedia) getString(R.string.call_state_outgoing_early, dots)
                    else getString(R.string.call_state_outgoing, dots)
                } else {
                    getString(R.string.call_state_incoming, dots)
                }
            }
            CallHub.State.IN_CALL -> {
                val start = CallHub.callStartedAt
                val sec = if (start > 0) ((System.currentTimeMillis() - start) / 1000).toInt().coerceAtLeast(0) else lastElapsedSec
                lastElapsedSec = sec
                tvState.text = "%02d:%02d".format(sec / 60, sec % 60)
            }
            CallHub.State.IDLE -> Unit
        }
    }

    /** 通話終了後「通話終了 mm:ss」を 1.5 秒表示して閉じる。 */
    private fun showEndThenFinish() {
        if (endShown || isFinishing) return
        endShown = true
        handler.removeCallbacks(tick)
        stopRinging()
        dtmfSheet?.dismiss()
        layoutIncoming.visibility = View.GONE
        layoutOutgoing.visibility = View.GONE
        layoutIncall.visibility = View.GONE
        val mmss = "%02d:%02d".format(lastElapsedSec / 60, lastElapsedSec % 60)
        tvEndToast.text = getString(R.string.call_end_toast, mmss)
        tvEndToast.visibility = View.VISIBLE
        handler.postDelayed({ if (!isFinishing) finish() }, 1500)
    }

    // ---- 終了 ----

    /**
     * 終了は**確認を出さず即時**に切る (一般的な通話アプリと同じ挙動)。
     * 通話中は終了表示 (onChanged→IDLE) を出すため finish() はしない。
     */
    private fun onEndPressed() {
        sendServiceAction(BridgeService.ACT_HANGUP)
        if (CallHub.state != CallHub.State.IN_CALL) finish()
    }

    // ---- DTMF シート (§3.1) ----

    private fun showDtmfSheet() {
        dtmfSheet?.dismiss()
        val ctx = this
        val density = resources.displayMetrics.density
        val sheet = BottomSheetDialog(ctx)
        val root = LinearLayout(ctx).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            setPadding((16 * density).toInt(), (16 * density).toInt(), (16 * density).toInt(), (16 * density).toInt())
        }
        tvDtmfInput = TextView(ctx).apply {
            text = dtmfInput.toString()
            setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text))
            textSize = 24f
            gravity = Gravity.CENTER
            minHeight = (48 * density).toInt()
        }
        root.addView(tvDtmfInput)
        val grid = GridLayout(ctx).apply {
            columnCount = 3
            rowCount = 4
        }
        val keys = listOf("1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "0", "#")
        val size = (72 * density).toInt()
        val margin = (6 * density).toInt()
        keys.forEach { k ->
            val b = MaterialButton(ctx).apply {
                text = k
                textSize = 24f
                setBackgroundColor(ContextCompat.getColor(ctx, R.color.nocturne_surface_hi))
                setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text))
                cornerRadius = size / 2
                setOnClickListener { it.tapFeedback(); onDtmfKey(k[0]) }
            }
            grid.addView(b, GridLayout.LayoutParams().apply {
                width = size
                height = size
                setMargins(margin, margin, margin, margin)
            })
        }
        root.addView(grid)
        root.addView(MaterialButton(ctx).apply {
            text = getString(R.string.common_close)
            setBackgroundColor(ContextCompat.getColor(ctx, R.color.nocturne_surface_hi))
            setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text))
            setOnClickListener { sheet.dismiss() }
        })
        // ロック画面の上に通話画面を出したまま (= ロック解除なし) でもキーパッドを使えるようにする。
        // Dialog は Activity とは別ウィンドウなので showWhenLocked が効かず、フラグを個別に立てる。
        sheet.window?.addFlags(WindowManager.LayoutParams.FLAG_SHOW_WHEN_LOCKED)
        sheet.setContentView(root)
        sheet.setOnDismissListener { dtmfSheet = null }
        dtmfSheet = sheet
        sheet.show()
    }

    /**
     * DTMF 1 キー: 入力済み文字列に足し、in-band で送り、短い操作音を鳴らす。
     * 操作音はローカル確認用 (ToneGenerator)。送出は RtpEngine.sendDtmf。
     */
    private fun onDtmfKey(digit: Char) {
        dtmfInput.append(digit)
        tvDtmfInput?.text = dtmfInput.toString()
        CallHub.rtp?.sendDtmf(digit)
        val toneType = when (digit) {
            '1' -> ToneGenerator.TONE_DTMF_1
            '2' -> ToneGenerator.TONE_DTMF_2
            '3' -> ToneGenerator.TONE_DTMF_3
            '4' -> ToneGenerator.TONE_DTMF_4
            '5' -> ToneGenerator.TONE_DTMF_5
            '6' -> ToneGenerator.TONE_DTMF_6
            '7' -> ToneGenerator.TONE_DTMF_7
            '8' -> ToneGenerator.TONE_DTMF_8
            '9' -> ToneGenerator.TONE_DTMF_9
            '0' -> ToneGenerator.TONE_DTMF_0
            '*' -> ToneGenerator.TONE_DTMF_S
            '#' -> ToneGenerator.TONE_DTMF_P
            else -> -1
        }
        if (toneType >= 0) {
            runCatching {
                if (toneGenerator == null) {
                    toneGenerator = ToneGenerator(AudioManager.STREAM_VOICE_CALL, 80)
                }
                toneGenerator?.startTone(toneType, 120)
            }
        }
    }

    /** ミュート/スピーカーの ON/OFF 塗り (ON で accent_soft)。 */
    private fun paintToggle(btn: MaterialButton, on: Boolean) {
        val color = if (on) R.color.nocturne_accent_soft else R.color.nocturne_surface_hi
        btn.backgroundTintList = ColorStateList.valueOf(ContextCompat.getColor(this, color))
    }

    // ---- 着信音・バイブ (旧 IncomingCallActivity から移植) ----

    private fun startRinging() {
        if (ringtone?.isPlaying == true) return
        try {
            val uri = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_RINGTONE)
            ringtone = RingtoneManager.getRingtone(applicationContext, uri)?.apply {
                if (!isPlaying) play()
            }
        } catch (e: Exception) { /* ignore */ }
        try {
            vibrator = getSystemService(Context.VIBRATOR_SERVICE) as Vibrator
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                vibrator?.vibrate(
                    VibrationEffect.createWaveform(longArrayOf(0, 800, 400, 800), 0)
                )
            } else {
                @Suppress("DEPRECATION") vibrator?.vibrate(longArrayOf(0, 800, 400, 800), 0)
            }
        } catch (e: Exception) { /* ignore */ }
    }

    private fun stopRinging() {
        runCatching { ringtone?.stop() }
        ringtone = null
        runCatching { vibrator?.cancel() }
    }

    override fun onDestroy() {
        releaseProximityLock()
        proximityLock = null
        stopRinging()
        handler.removeCallbacksAndMessages(null)
        dtmfSheet?.dismiss()
        dtmfSheet = null
        runCatching { toneGenerator?.release() }
        toneGenerator = null
        super.onDestroy()
    }
}

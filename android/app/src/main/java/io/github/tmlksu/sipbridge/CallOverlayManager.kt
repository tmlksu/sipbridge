package io.github.tmlksu.sipbridge

import android.annotation.SuppressLint
import android.content.Context
import android.content.Intent
import android.graphics.PixelFormat
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.view.Gravity
import android.view.MotionEvent
import android.view.WindowManager
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView
import androidx.core.content.ContextCompat

/**
 * オーバーレイ: 他アプリ前面の着信バブル (応答/拒否) と通話中ピル (UI-DESIGN §3.2)。
 *
 * - 着信バブル: [show]/[hide] (既存)。
 * - 通話中ピル「📞 mm:ss」: [showInCallPill]/[hideInCallPill]。
 *   nocturne_surface 地に accent 系文字、右上配置、1 秒更新、タップで通話画面に戻る、
 *   ドラッグで移動可。BridgeService が「縮小」で表示し、復帰/通話終了で隠す。
 *   [hide] は両方を隠す (通話終了時の取り残し防止)。
 */
class CallOverlayManager(private val ctx: Context) {
    private var wm: WindowManager? = null
    private var view: LinearLayout? = null

    // ---- 通話中ピル ----
    private var pillView: TextView? = null
    private var pillParams: WindowManager.LayoutParams? = null
    private val pillHandler = Handler(Looper.getMainLooper())
    private var pillStart = 0L
    private var pillTick: Runnable? = null

    fun canDraw(): Boolean =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) Settings.canDrawOverlays(ctx) else true

    @SuppressLint("ClickableViewAccessibility")
    fun show(from: String, onAnswer: () -> Unit, onReject: () -> Unit) {
        if (!canDraw()) return
        hide()
        wm = ctx.getSystemService(Context.WINDOW_SERVICE) as WindowManager
        val layout = LinearLayout(ctx).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(0xDD222222.toInt())
            setPadding(32, 24, 32, 24)
        }
        layout.addView(TextView(ctx).apply {
            text = ctx.getString(R.string.overlay_incoming, from)
            setTextColor(0xFFFFFFFF.toInt())
            textSize = 18f
        })
        val row = LinearLayout(ctx).apply { orientation = LinearLayout.HORIZONTAL }
        val btnOk = Button(ctx).apply { text = ctx.getString(R.string.overlay_answer) }
        val btnNg = Button(ctx).apply { text = ctx.getString(R.string.overlay_reject) }
        btnOk.setOnClickListener { onAnswer() }
        btnNg.setOnClickListener { onReject() }
        row.addView(btnOk, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
        row.addView(btnNg, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
        layout.addView(row)
        val params = WindowManager.LayoutParams(
            WindowManager.LayoutParams.WRAP_CONTENT,
            WindowManager.LayoutParams.WRAP_CONTENT,
            overlayType(),
            WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or
                WindowManager.LayoutParams.FLAG_SHOW_WHEN_LOCKED or
                WindowManager.LayoutParams.FLAG_TURN_SCREEN_ON or
                WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON,
            PixelFormat.TRANSLUCENT
        ).apply { gravity = Gravity.BOTTOM or Gravity.CENTER_HORIZONTAL; y = 80 }
        view = layout
        runCatching { wm?.addView(layout, params) }
    }

    /**
     * 通話中ピルを表示する。既存ピルがあれば作り直す。
     * @param startedAt 通話開始時刻 (System.currentTimeMillis)。
     *   **0 以下なら発信呼出中**とみなし、経過時間ではなく「📞 呼出中」を出す。
     * @param onTap ピルタップ時 (CallActivity に戻る)。
     */
    @SuppressLint("ClickableViewAccessibility")
    fun showInCallPill(startedAt: Long, onTap: () -> Unit) {
        if (!canDraw()) return
        hideInCallPill()
        if (wm == null) wm = ctx.getSystemService(Context.WINDOW_SERVICE) as WindowManager
        pillStart = startedAt
        val density = ctx.resources.displayMetrics.density
        val tv = TextView(ctx).apply {
            // 他アプリの上でも視認できるよう accent 塗り + 太字 (UI-DESIGN §3.2)
            background = ContextCompat.getDrawable(ctx, R.drawable.pill_call)
            setTextColor(ContextCompat.getColor(ctx, R.color.nocturne_text))
            textSize = 17f
            setTypeface(typeface, android.graphics.Typeface.BOLD)
            elevation = 8 * density
            setPadding((20 * density).toInt(), (12 * density).toInt(), (20 * density).toInt(), (12 * density).toInt())
        }
        val params = WindowManager.LayoutParams(
            WindowManager.LayoutParams.WRAP_CONTENT,
            WindowManager.LayoutParams.WRAP_CONTENT,
            overlayType(),
            WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or
                WindowManager.LayoutParams.FLAG_SHOW_WHEN_LOCKED or
                WindowManager.LayoutParams.FLAG_TURN_SCREEN_ON,
            PixelFormat.TRANSLUCENT
        ).apply {
            gravity = Gravity.TOP or Gravity.END
            x = (16 * density).toInt()
            y = (80 * density).toInt()
        }
        pillParams = params
        // タップとドラッグの区別 (10dp 以上動いたらドラッグ)
        val touchSlop = (10 * density)
        var downX = 0f
        var downY = 0f
        var baseX = 0
        var baseY = 0
        var dragging = false
        tv.setOnTouchListener { _, ev ->
            when (ev.action) {
                MotionEvent.ACTION_DOWN -> {
                    downX = ev.rawX
                    downY = ev.rawY
                    baseX = params.x
                    baseY = params.y
                    dragging = false
                    true
                }
                MotionEvent.ACTION_MOVE -> {
                    val dx = ev.rawX - downX
                    val dy = ev.rawY - downY
                    if (!dragging && kotlin.math.hypot(dx, dy) > touchSlop) dragging = true
                    if (dragging) {
                        // END 重力では x が右からの距離になるため逆向きに補正する
                        params.x = baseX - dx.toInt()
                        params.y = baseY + dy.toInt()
                        runCatching { wm?.updateViewLayout(tv, params) }
                    }
                    true
                }
                MotionEvent.ACTION_UP -> {
                    if (!dragging) onTap()
                    true
                }
                else -> false
            }
        }
        pillView = tv
        updatePillText(tv)
        runCatching { wm?.addView(tv, params) }
        val tick = object : Runnable {
            override fun run() {
                val v = pillView ?: return
                updatePillText(v)
                pillHandler.postDelayed(this, 1000)
            }
        }
        pillTick = tick
        pillHandler.postDelayed(tick, 1000)
    }

    private fun updatePillText(tv: TextView) {
        if (pillStart <= 0) {
            // 発信呼出中 (まだ通話が始まっていない)
            tv.text = "📞 ${ctx.getString(R.string.overlay_calling)}"
            return
        }
        val s = ((System.currentTimeMillis() - pillStart) / 1000).toInt().coerceAtLeast(0)
        tv.text = "📞 %02d:%02d".format(s / 60, s % 60)
    }

    fun hideInCallPill() {
        pillTick?.let { pillHandler.removeCallbacks(it) }
        pillTick = null
        runCatching { pillView?.let { wm?.removeView(it) } }
        pillView = null
        pillParams = null
    }

    fun hide() {
        hideInCallPill()
        runCatching { view?.let { wm?.removeView(it) } }
        view = null
    }

    private fun overlayType(): Int =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O)
            WindowManager.LayoutParams.TYPE_APPLICATION_OVERLAY
        else
            @Suppress("DEPRECATION") WindowManager.LayoutParams.TYPE_PHONE

    /** CallActivity に戻るための共通インテント。 */
    companion object {
        fun callActivityIntent(ctx: Context): Intent =
            Intent(ctx, CallActivity::class.java).apply {
                flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP
            }
    }
}

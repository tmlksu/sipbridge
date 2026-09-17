package io.github.tmlksu.sipbridge.probe

import android.app.Activity
import android.content.Intent
import android.graphics.Color
import android.graphics.Typeface
import android.net.Uri
import android.os.Bundle
import android.provider.Settings
import android.text.InputType
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView

/**
 * Telecom 統合の実機検証を **端末の画面だけで** 回すための画面 (probe ビルド専用)。
 *
 * adb が繋がらない端末 (手で APK を入れた S25 など) 用。applicationId は
 * `io.github.tmlksu.sipbridge.probe` なので、本番の sipbridge を消さずに同居できる。
 *
 * 手順:
 *   1. [登録 managed] → 2. [通話アカウント設定] で OS の設定を開き sipbridge probe を ON
 *   → 3. [状態] で enabled=true を確認 → 4. [着信テスト] で標準の着信画面が出るか見る
 *   → 5. [発信 標準経路] で内線番号がどう正規化されるかを見る → 6. [解除]
 */
class ProbeActivity : Activity(), TelecomProbe.Sink {

    private lateinit var logView: TextView
    private lateinit var scroll: ScrollView
    private lateinit var numberInput: EditText

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val pad = dp(12)
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(pad, pad, pad, pad)
        }

        root.addView(TextView(this).apply {
            text = "sipbridge Telecom プローブ"
            setTypeface(null, Typeface.BOLD)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 18f)
        })
        root.addView(TextView(this).apply {
            text = "本番アプリとは別 ID。検証が済んだらアンインストールしてください。"
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 12f)
            setPadding(0, dp(2), 0, dp(8))
        })

        numberInput = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_PHONE
            setText("2104")
            hint = "テストに使う内線番号"
        }
        root.addView(numberInput)

        val grid = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        root.addView(grid)

        fun row(vararg buttons: Pair<String, () -> Unit>) {
            val r = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
            for ((label, action) in buttons) {
                r.addView(Button(this).apply {
                    text = label
                    setTextSize(TypedValue.COMPLEX_UNIT_SP, 13f)
                    layoutParams = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)
                    setOnClickListener { runCatching { action() }.onFailure { TelecomProbe.w("UI 例外 $it") } }
                })
            }
            grid.addView(r)
        }

        row("1. 登録 managed" to { TelecomProbe.register(this, "managed") },
            "1b. 登録 self" to { TelecomProbe.register(this, "self") })
        row("2. 通話アカウント設定" to { openPhoneAccountSettings() },
            "3. 状態" to { TelecomProbe.status(this) })
        row("4. 着信 managed" to { TelecomProbe.incoming(this, "managed", num()) },
            "4b. 着信 self" to { TelecomProbe.incoming(this, "self", num()) })
        row("5. 発信 アプリから" to { TelecomProbe.outgoing(this, "managed", num()) },
            "5b. 発信 標準経路" to { dialViaSystem() })
        row("切断" to { TelecomProbe.hangup() },
            "6. 解除" to { TelecomProbe.unregister(this) })
        row("連絡先アプリを開く" to { openContacts() },
            "ログ消去" to { TelecomProbe.clearLog() })

        logView = TextView(this).apply {
            typeface = Typeface.MONOSPACE
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 10f)
            setTextIsSelectable(true)
            setBackgroundColor(Color.parseColor("#11000000"))
            setPadding(dp(6), dp(6), dp(6), dp(6))
        }
        scroll = ScrollView(this).apply {
            addView(logView)
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f
            )
        }
        root.addView(scroll)

        setContentView(root, ViewGroup.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        logView.gravity = Gravity.START
        ensureCallPhonePermission()
    }

    /** ACTION_CALL / placeCall に必要。probe ビルドは adb grant できない端末向けなので自分で要求する。 */
    private fun ensureCallPhonePermission() {
        if (checkSelfPermission(android.Manifest.permission.CALL_PHONE) !=
            android.content.pm.PackageManager.PERMISSION_GRANTED
        ) {
            runCatching { requestPermissions(arrayOf(android.Manifest.permission.CALL_PHONE), 1) }
        }
    }

    override fun onResume() {
        super.onResume()
        TelecomProbe.sink = this
        onLog(TelecomProbe.snapshot())
    }

    override fun onPause() {
        super.onPause()
        if (TelecomProbe.sink === this) TelecomProbe.sink = null
    }

    override fun onLog(all: String) {
        logView.text = all
        scroll.post { scroll.fullScroll(View.FOCUS_DOWN) }
    }

    private fun num(): String = numberInput.text.toString().trim().ifEmpty { "2104" }

    /** OS の「通話アカウント」設定を開く。機種によって場所が違うので候補を順に試す。 */
    private fun openPhoneAccountSettings() {
        val candidates = listOf(
            Intent("android.telecom.action.CHANGE_PHONE_ACCOUNTS"),
            Intent(Settings.ACTION_SETTINGS).setClassName(
                "com.android.server.telecom",
                "com.android.server.telecom.settings.EnableAccountPreferenceActivity"
            ),
            Intent("android.settings.ACTION_PHONE_ACCOUNT_SETTINGS"),
            Intent(Settings.ACTION_SETTINGS)
        )
        for (i in candidates) {
            if (runCatching { startActivity(i); true }.getOrDefault(false)) {
                TelecomProbe.i("STATE 設定を開いた: ${i.action ?: i.component}")
                return
            }
        }
        TelecomProbe.w("RESULT 通話アカウント設定を開けなかった (手動で 設定 > 通話 > 通話アカウント)")
    }

    /**
     * ★本命の検証: 標準の電話経路 (ACTION_CALL) で発信し、
     * `onCreateOutgoingConnection` に届く番号が書き換わっていないかを見る。
     */
    private fun dialViaSystem() {
        val to = num()
        TelecomProbe.expect(to)
        val i = Intent(Intent.ACTION_CALL, Uri.fromParts("tel", to, null))
        val r = runCatching { startActivity(i) }
        if (r.isFailure) {
            TelecomProbe.w("RESULT ACTION_CALL 失敗 ${r.exceptionOrNull()} " +
                "(CALL_PHONE 権限が未許可なら 設定 > アプリ > 権限 で許可)")
        }
    }

    /** 連絡先アプリから内線を掛けたときの経路 (アカウント選択ダイアログ) を見るため。 */
    private fun openContacts() {
        val i = Intent(Intent.ACTION_VIEW).setData(
            Uri.parse("content://com.android.contacts/contacts")
        )
        if (runCatching { startActivity(i); true }.getOrDefault(false)) return
        TelecomProbe.w("RESULT 連絡先アプリを開けなかった")
    }

    private fun dp(v: Int): Int = (v * resources.displayMetrics.density).toInt()
}

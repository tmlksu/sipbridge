package io.github.tmlksu.sipbridge

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import androidx.core.content.ContextCompat

/**
 * debug ビルドのみの adb 設定投入口 (Echo Show など入力しづらい端末の初期設定用)。
 * release には含めない (`src/debug` 配下 + debug manifest の receiver)。
 *
 * **DUMP 権限で保護しており、adb shell からのみ届く** (debug manifest の
 * `android:permission="android.permission.DUMP"`)。exported だが DUMP は第三者アプリが
 * 取得できないため、他アプリから relay URL や認証情報を書き換えたり発信させたりはできない。
 * 暗黙ブロードキャストは届かないので、コンポーネントを `-n` で明示すること。
 *
 * 例:
 * ```
 * adb shell am broadcast -n io.github.tmlksu.sipbridge/.DebugConfigReceiver \
 *   -a io.github.tmlksu.sipbridge.DEBUG_SET_CONFIG \
 *   --es relayUrl wss://sip.example.com --es sipUser 101 --es sipPassword xxx \
 *   --es restart true
 * ```
 *
 * relayUrl は設定画面と同じ検証 ([checkRelayUrl]) を通す。平文 (ws:// / http://) は
 * ループバック (127.0.0.1 / localhost。adb reverse 用。debug の network_security_config と同じ)
 * 以外では受け付けず、保存しない。
 *
 * extras は全て任意。受け取ったキーだけ上書きして保存する。
 * 文字列 extras: relayUrl, accessClientId, accessClientSecret, devToken,
 * sipUser, sipPassword, sipDisplay, mode (PERSISTENT|PUSH), micGain,
 * overlayEnabled, autostart, speakerOnAnswer (true/false),
 * deviceContactsEnabled (true/false), telecomPref (AUTO|SYSTEM|APP),
 * restart (true なら BridgeService を再起動)。
 * boolean extra として渡された場合 (ez) も受け付ける。
 */
class DebugConfigReceiver : BroadcastReceiver() {

    companion object {
        const val ACTION = "io.github.tmlksu.sipbridge.DEBUG_SET_CONFIG"
        /**
         * E2E 用の通話操作 (adb から応答するため。BridgeService の ACT_* を起動する)。
         * extras: action=answer|reject|hangup|dial, to=<番号> (dial のみ)。
         * 例: `adb shell am broadcast -n io.github.tmlksu.sipbridge/.DebugConfigReceiver
         *   -a io.github.tmlksu.sipbridge.DEBUG_CALL_ACTION --es action answer`
         */
        const val ACTION_CALL = "io.github.tmlksu.sipbridge.DEBUG_CALL_ACTION"
        private const val TAG = "DebugConfig"
    }

    override fun onReceive(ctx: Context, intent: Intent) {
        if (intent.action == ACTION_CALL) {
            onCallAction(ctx, intent)
            return
        }
        if (intent.action != ACTION) return
        // 読込→変換→書込を BridgeConfig.update で原子的に行う。保存領域に一時的に
        // アクセスできないときは何も書かない (#42: 既定値での上書き防止)。
        var changed = false
        val ok = BridgeConfig.update(ctx.applicationContext) { cur ->
            applyExtras(cur, intent).also { changed = it != cur }
        }
        if (!ok) {
            Log.w(TAG, "config storage unavailable; DEBUG_SET_CONFIG ignored")
        } else if (changed) {
            // 通話画面の方式を変えたら PhoneAccount の登録/解除を追随させる。
            runCatching { TelecomTierManager.sync(ctx.applicationContext) }
            Log.i(TAG, "config updated via adb")
        }
        if (boolExtra(intent, "restart") == true) {
            ctx.applicationContext.stopService(Intent(ctx.applicationContext, BridgeService::class.java))
            BridgeService.start(ctx.applicationContext)
            Log.i(TAG, "BridgeService restarted via adb")
        }
    }

    /** intent の extras のうち渡されたキーだけ [cur] に上書きした値を返す。 */
    private fun applyExtras(cur: BridgeConfigData, intent: Intent): BridgeConfigData {
        var d = cur
        intent.getStringExtra("relayUrl")?.let {
            val url = it.trim()
            val check = checkRelayUrl(url)
            if (url.isEmpty() || check is RelayUrlCheck.Ok) {
                d = d.copy(relayUrl = url)
            } else {
                // 設定画面と同じく、平文の非ループバック宛てなどは保存しない。
                Log.w(TAG, "relayUrl rejected: ${check.message}")
            }
        }
        intent.getStringExtra("accessClientId")?.let { d = d.copy(accessClientId = it.trim()) }
        intent.getStringExtra("accessClientSecret")?.let { d = d.copy(accessClientSecret = it) }
        intent.getStringExtra("devToken")?.let { d = d.copy(devToken = it) }
        intent.getStringExtra("sipUser")?.let { d = d.copy(sipUser = it.trim()) }
        intent.getStringExtra("sipPassword")?.let { d = d.copy(sipPassword = it) }
        intent.getStringExtra("sipDisplay")?.let { d = d.copy(sipDisplay = it.trim()) }
        intent.getStringExtra("mode")?.let {
            runCatching { BridgeMode.valueOf(it.trim()) }.onSuccess { m ->
                d = d.copy(mode = m)
            }
        }
        intent.getStringExtra("micGain")?.let {
            it.toFloatOrNull()?.let { g -> d = d.copy(micGain = g) }
        }
        boolExtra(intent, "overlayEnabled")?.let { d = d.copy(overlayEnabled = it) }
        boolExtra(intent, "autostart")?.let { d = d.copy(autostart = it) }
        boolExtra(intent, "speakerOnAnswer")?.let { d = d.copy(speakerOnAnswer = it) }
        boolExtra(intent, "deviceContactsEnabled")?.let { d = d.copy(deviceContactsEnabled = it) }
        // 通話画面の方式 (AUTO|SYSTEM|APP)。ティア落ちの E2E (ops/telecom-e2e.sh) から使う。
        intent.getStringExtra("telecomPref")?.let {
            runCatching { TelecomTierManager.Pref.valueOf(it.trim().uppercase()) }.onSuccess { p ->
                d = d.copy(telecomPref = p)
            }
        }
        return d
    }

    /** DEBUG_CALL_ACTION: 通話操作を BridgeService に投げる (E2E スクリプト用)。 */
    private fun onCallAction(ctx: Context, intent: Intent) {
        val app = ctx.applicationContext
        val svc = when (intent.getStringExtra("action")?.lowercase()) {
            "answer" -> Intent(app, BridgeService::class.java).setAction(BridgeService.ACT_ANSWER)
            "reject" -> Intent(app, BridgeService::class.java).setAction(BridgeService.ACT_REJECT)
            "hangup" -> Intent(app, BridgeService::class.java).setAction(BridgeService.ACT_HANGUP)
            "dial" -> Intent(app, BridgeService::class.java).setAction(BridgeService.ACT_DIAL)
                .putExtra(BridgeService.EXTRA_TO, intent.getStringExtra("to").orEmpty())
            else -> {
                Log.w(TAG, "unknown DEBUG_CALL_ACTION: ${intent.getStringExtra("action")}")
                return
            }
        }
        runCatching { ContextCompat.startForegroundService(app, svc) }
            .onFailure { runCatching { app.startService(svc) } }
        Log.i(TAG, "call action via adb: ${intent.getStringExtra("action")}")
    }

    /** 文字列 "true"/"false" または boolean extra を読む。キーが無ければ null。 */
    private fun boolExtra(intent: Intent, key: String): Boolean? {
        if (!intent.hasExtra(key)) return null
        intent.getStringExtra(key)?.let {
            return it.equals("true", ignoreCase = true)
        }
        return runCatching { intent.getBooleanExtra(key, false) }.getOrNull()
    }
}

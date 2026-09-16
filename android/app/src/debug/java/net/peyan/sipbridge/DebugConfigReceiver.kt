package net.peyan.sipbridge

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import androidx.core.content.ContextCompat

/**
 * debug ビルドのみの adb 設定投入口 (Echo Show など入力しづらい端末の初期設定用)。
 * release には含めない (`src/debug` 配下 + debug manifest の receiver)。
 *
 * 例:
 * ```
 * adb shell am broadcast -a net.peyan.sipbridge.DEBUG_SET_CONFIG \
 *   --es relayUrl wss://sip.example.com --es sipUser 101 --es sipPassword xxx \
 *   --es restart true
 * ```
 *
 * extras は全て任意。受け取ったキーだけ上書きして保存する。
 * 文字列 extras: relayUrl, accessClientId, accessClientSecret, devToken,
 * sipUser, sipPassword, sipDisplay, mode (PERSISTENT|PUSH), micGain,
 * overlayEnabled, autostart, speakerOnAnswer (true/false),
 * deviceContactsEnabled (true/false),
 * restart (true なら BridgeService を再起動)。
 * boolean extra として渡された場合 (ez) も受け付ける。
 */
class DebugConfigReceiver : BroadcastReceiver() {

    companion object {
        const val ACTION = "net.peyan.sipbridge.DEBUG_SET_CONFIG"
        /**
         * E2E 用の通話操作 (adb から応答するため。BridgeService の ACT_* を起動する)。
         * extras: action=answer|reject|hangup|dial, to=<番号> (dial のみ)。
         * 例: `adb shell am broadcast -a net.peyan.sipbridge.DEBUG_CALL_ACTION --es action answer`
         */
        const val ACTION_CALL = "net.peyan.sipbridge.DEBUG_CALL_ACTION"
        private const val TAG = "DebugConfig"
    }

    override fun onReceive(ctx: Context, intent: Intent) {
        if (intent.action == ACTION_CALL) {
            onCallAction(ctx, intent)
            return
        }
        if (intent.action != ACTION) return
        val cur = BridgeConfig.load(ctx.applicationContext)
        var d = cur
        var touched = false

        intent.getStringExtra("relayUrl")?.let { d = d.copy(relayUrl = it.trim()); touched = true }
        intent.getStringExtra("accessClientId")?.let { d = d.copy(accessClientId = it.trim()); touched = true }
        intent.getStringExtra("accessClientSecret")?.let { d = d.copy(accessClientSecret = it); touched = true }
        intent.getStringExtra("devToken")?.let { d = d.copy(devToken = it); touched = true }
        intent.getStringExtra("sipUser")?.let { d = d.copy(sipUser = it.trim()); touched = true }
        intent.getStringExtra("sipPassword")?.let { d = d.copy(sipPassword = it); touched = true }
        intent.getStringExtra("sipDisplay")?.let { d = d.copy(sipDisplay = it.trim()); touched = true }
        intent.getStringExtra("mode")?.let {
            runCatching { BridgeMode.valueOf(it.trim()) }.onSuccess { m ->
                d = d.copy(mode = m); touched = true
            }
        }
        intent.getStringExtra("micGain")?.let {
            it.toFloatOrNull()?.let { g -> d = d.copy(micGain = g); touched = true }
        }
        boolExtra(intent, "overlayEnabled")?.let { d = d.copy(overlayEnabled = it); touched = true }
        boolExtra(intent, "autostart")?.let { d = d.copy(autostart = it); touched = true }
        boolExtra(intent, "speakerOnAnswer")?.let { d = d.copy(speakerOnAnswer = it); touched = true }
        boolExtra(intent, "deviceContactsEnabled")?.let { d = d.copy(deviceContactsEnabled = it); touched = true }

        if (touched) {
            BridgeConfig.save(ctx.applicationContext, d)
            Log.i(TAG, "config updated via adb")
        }
        if (boolExtra(intent, "restart") == true) {
            ctx.applicationContext.stopService(Intent(ctx.applicationContext, BridgeService::class.java))
            BridgeService.start(ctx.applicationContext)
            Log.i(TAG, "BridgeService restarted via adb")
        }
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

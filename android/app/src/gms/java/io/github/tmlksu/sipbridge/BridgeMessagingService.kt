package io.github.tmlksu.sipbridge

import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.ServiceConnection
import android.os.Build
import android.os.IBinder
import android.util.Log
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage

/**
 * gms flavor の FCM 受信口。foss flavor には含めない
 * (このファイルは `src/gms` 配下のため、foss ビルドにはコンパイルされない)。
 *
 * - 着信 data message (`type=incoming`) を受けたら [BridgeService] を
 *   [BridgeService.ACT_WAKE_INCOMING] で起動する。接続後の `hello.call`
 *   から着信 UI が出る (PUSH モードの起床経路)。
 * - トークン更新 (`onNewToken`) を [BridgeService.setPushToken] に配送する。
 *   relay は接続確立後の `hello` で `register_push` を送り、トークンを永続化する。
 */
class BridgeMessagingService : FirebaseMessagingService() {

    companion object {
        private const val TAG = "BridgeFCM"
        private const val PREFS = "sipbridge_gms"
        private const val KEY_LAST_TOKEN = "last_fcm_token"
        /** onMessageReceived が ACT_WAKE_INCOMING に添える参考情報 (UI は hello.call を正とする)。 */
        const val EXTRA_CALL_ID = "callId"
        const val EXTRA_FROM = "from"
        const val EXTRA_DISPLAY = "display"
    }

    override fun onMessageReceived(msg: RemoteMessage) {
        val data = msg.data
        if (data["type"] != "incoming") {
            Log.i(TAG, "incoming 以外 (${data["type"]}) のため無視")
            return
        }
        // PUSH 待機中の BridgeService はロックを持っていない (issue #19)。この関数を抜けて
        // FCM の wake lock が切れてから、サービスが接続前にロックを取るまでの間に
        // CPU が眠らないよう、サービスを起こす前 (deliverToken より前) に橋渡しの
        // wake lock を取っておく (サービスが自分のロックを取るか、下の起床 Intent の
        // 処理を終えた時点で放す。最長でも 30 秒で切れる)。
        BridgeService.holdWakeBridge(this)
        val caller = data["caller"] ?: data["from"]
        Log.i(TAG, "着信 push 受信: callId=${data["callId"]} from=$caller")
        // §6.2 到達性の記録: push が届いた時刻。
        PushHealth.markPushReceived(this)
        // bind 失敗などで届いていないトークンがあれば、この機会に再配送する。
        lastToken()?.let { deliverToken(it) }
        val wake = Intent(this, BridgeService::class.java).apply {
            action = BridgeService.ACT_WAKE_INCOMING
            data["callId"]?.let { putExtra(EXTRA_CALL_ID, it) }
            caller?.let { putExtra(EXTRA_FROM, it) }
            data["display"]?.let { putExtra(EXTRA_DISPLAY, it) }
            putExtra(BridgeService.EXTRA_WAKE_BRIDGE, true)
        }
        runCatching {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) startForegroundService(wake)
            else startService(wake)
        }.onFailure { Log.w(TAG, "BridgeService 起動失敗", it) }
    }

    override fun onNewToken(token: String) {
        Log.i(TAG, "FCM トークン更新 (len=${token.length})")
        getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            .edit().putString(KEY_LAST_TOKEN, token).apply()
        deliverToken(token)
    }

    private fun lastToken(): String? =
        getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            .getString(KEY_LAST_TOKEN, null)?.takeIf { it.isNotBlank() }

    /**
     * トークンを起動中の [BridgeService] の [BridgeService.setPushToken] に配送する。
     * main 側 (foss 共通) の変更なしに bind 経由で呼ぶ。
     */
    private fun deliverToken(token: String) {
        val svc = Intent(this, BridgeService::class.java)
        runCatching {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) startForegroundService(svc)
            else startService(svc)
        }
        val conn = object : ServiceConnection {
            override fun onServiceConnected(name: ComponentName?, binder: IBinder?) {
                runCatching {
                    (binder as? BridgeService.LocalBinder)?.service()?.setPushToken(token)
                        ?: error("binder が LocalBinder ではない")
                    Log.i(TAG, "setPushToken へ配送")
                }.onFailure { Log.w(TAG, "setPushToken 配送失敗", it) }
                runCatching { unbindService(this) }
            }
            override fun onServiceDisconnected(name: ComponentName?) = Unit
        }
        val bound = runCatching {
            bindService(svc, conn, Context.BIND_AUTO_CREATE)
        }.getOrDefault(false)
        if (!bound) Log.w(TAG, "bind 失敗。次回 push 受信時に再送する")
    }
}

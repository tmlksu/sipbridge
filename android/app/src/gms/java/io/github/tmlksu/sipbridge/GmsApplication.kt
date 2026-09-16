package io.github.tmlksu.sipbridge

import android.app.Application
import android.content.Context
import android.util.Log
import com.google.firebase.messaging.FirebaseMessaging

/**
 * gms flavor の Application (main には無い。gms の manifest が `android:name` で指す)。
 *
 * FCM トークンは「更新されたとき」に `onNewToken` で降ってくるだけなので、
 * 新規インストール直後やデータ消去後は自動では取得されないことがある
 * (実機 P780 で確認)。トークンが無いと PUSH モードは relay に `register_push` を
 * 送れず着信 push を受けられないため、プロセス起動時に明示的に `getToken()` を呼ぶ。
 * 取得できたら prefs に保存して [BridgeService] を起こす。接続 → hello →
 * `register_push` は既存経路 ([BridgeService.setPushToken]) が行う。
 */
class GmsApplication : Application() {

    override fun onCreate() {
        super.onCreate()
        // Firebase 未初期化 (google-services.json 不一致など) でも落とさない。
        runCatching {
            FirebaseMessaging.getInstance().token
                .addOnSuccessListener { token -> onToken(token) }
                .addOnFailureListener { e -> Log.w(TAG, "FCM トークン取得失敗", e) }
        }.onFailure { Log.w(TAG, "FirebaseMessaging を初期化できない", it) }
    }

    private fun onToken(token: String) {
        Log.i(TAG, "FCM トークン取得 (len=${token.length})")
        getSharedPreferences(BridgeService.FCM_PREFS, Context.MODE_PRIVATE)
            .edit().putString(BridgeService.FCM_TOKEN_KEY, token).apply()
        runCatching { BridgeService.start(this) }
            .onFailure { Log.w(TAG, "BridgeService 起動失敗", it) }
    }

    companion object {
        private const val TAG = "SipBridgeGms"
    }
}

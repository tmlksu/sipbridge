package io.github.tmlksu.sipbridge

import android.app.Activity
import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.util.Log

/**
 * 通話開始時に一瞬だけ出す透明な画面 (R13)。
 *
 * Android 11 以降、バックグラウンドから起動された前面サービスはマイクを使えない
 * (録音は無音になり、API 34+ では microphone 型での前面化自体が SecurityException)。
 * 見えている画面からサービスを起動し直すと、その起動にはマイクの許可 (while-in-use) が付く。
 * ここでは [BridgeService.ACT_PROMOTE_MIC] を送ってすぐ閉じるだけ。
 * バックグラウンドからこの画面を出せるのは、通話中に Telecom が接続サービスを
 * bind している間と、「他のアプリの上に表示」を許可している場合。
 */
class MicPromoteActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val i = Intent(this, BridgeService::class.java).setAction(BridgeService.ACT_PROMOTE_MIC)
        runCatching {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) startForegroundService(i) else startService(i)
        }.onFailure { Log.w("MicPromoteActivity", "サービスに届かない", it) }
    }

    override fun onResume() {
        super.onResume()
        finish()
        @Suppress("DEPRECATION")
        overridePendingTransition(0, 0)
    }
}

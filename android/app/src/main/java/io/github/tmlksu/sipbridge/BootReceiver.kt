package io.github.tmlksu.sipbridge

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

class BootReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        val a = intent.action ?: return
        if (a == Intent.ACTION_BOOT_COMPLETED || a == Intent.ACTION_LOCKED_BOOT_COMPLETED) {
            // §6.3 定期チェックの再登録 (autostart OFF でも OS 設定の監視は続ける)。
            HealthCheckReceiver.schedule(ctx.applicationContext)
            if (BridgeConfig.load(ctx).autostart) {
                BridgeService.start(ctx.applicationContext)
            }
        }
    }
}

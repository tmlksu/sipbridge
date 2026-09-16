package io.github.tmlksu.sipbridge

import android.app.AlarmManager
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/**
 * 1 日 1 回の定期チェック (UI-DESIGN §6.3)。
 * `BridgeService.onCreate` と [BootReceiver] から [schedule] で登録する
 * (`setInexactRepeating`。exact alarm は使わない = 権限不要)。
 *
 * 発火時:
 * 1. PUSH モードでトークン登録が 7 日以上前なら `BridgeService` を起こして
 *    再接続 → `register_push` を送り直す (§6.0 の E/F 対策。
 *    再登録のためだけの接続は既存の idle 猶予で自動切断される)。
 * 2. [PushHealth.evaluate] して BLOCKING/WARN があれば通知を 1 本出す
 *    (チャンネル `sipbridge_health`。タップで設定タブ)。
 * 3. 同一 kind の通知は 3 日に 1 回まで。全部解消したら通知をキャンセルする。
 *
 * 注意: 休止済みのアプリではアラーム自体が発火しない。この通知は
 * 「休止する前に気付かせる」ためのもので、恒久対策は休止除外トグル (§6.4)。
 */
class HealthCheckReceiver : BroadcastReceiver() {

    override fun onReceive(ctx: Context, intent: Intent) {
        val app = ctx.applicationContext
        val now = System.currentTimeMillis()
        val cfg = BridgeConfig.load(app)
        val times = PushHealth.readTimes(app)

        // 手順 1: PUSH の再登録 (明示アクションで起こし、同一プロセスで登録済みでも再登録する)。
        if (PushHealth.needsReregister(now, cfg.mode, times.lastPushRegisteredAt)) {
            runCatching { BridgeService.startReregister(app) }
        }

        // 手順 2・3: 判定と通知。
        val issues = PushHealth.evaluate(now, PushHealth.buildSnapshot(app))
        val nm = app.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (issues.isEmpty()) {
            runCatching { nm.cancel(NotificationHelper.ID_HEALTH) }
            PushHealth.clearWarn(app)
            return
        }
        val (lastWarnAt, lastWarnKind) = PushHealth.getLastWarn(app)
        if (!PushHealth.shouldNotify(now, issues, lastWarnAt, lastWarnKind)) return
        PushHealth.setLastWarn(app, now, issues.first().kind)

        val hasBlocking = issues.any { it.severity == PushHealth.Severity.BLOCKING }
        val (title, text) = when {
            !hasBlocking && issues.first().kind == PushHealth.Kind.HIBERNATION_SOON ->
                app.getString(R.string.health_title_hibernation_soon) to
                    app.getString(R.string.health_text_hibernation_soon)
            hasBlocking ->
                app.getString(R.string.health_title_blocking) to
                    reasonText(app, issues.first().kind)
            else ->
                app.getString(R.string.health_title_check) to
                    issues.take(2).joinToString("\n") { reasonText(app, it.kind) }
        }
        runCatching {
            nm.notify(
                NotificationHelper.ID_HEALTH,
                NotificationHelper.healthIssueNotification(app, title, text)
            )
        }
    }

    companion object {
        fun schedule(ctx: Context) {
            // PUSH モードでは push 起床のたびにプロセスが再生成され onCreate → schedule が
            // 繰り返されるため、無条件に登録し直すと「今から 1 日後」へリセットされ続けて
            // アラームが永久に発火しない。既に登録済みなら何もしない。
            // 再起動後はアラームも PendingIntent も消えているため BootReceiver 経由では登録される。
            val probe = Intent(ctx.applicationContext, HealthCheckReceiver::class.java)
            val existing = PendingIntent.getBroadcast(
                ctx.applicationContext, 0, probe,
                PendingIntent.FLAG_NO_CREATE or PendingIntent.FLAG_IMMUTABLE
            )
            if (existing != null) return
            val am = ctx.getSystemService(Context.ALARM_SERVICE) as? AlarmManager ?: return
            val pi = PendingIntent.getBroadcast(
                ctx.applicationContext, 0,
                Intent(ctx.applicationContext, HealthCheckReceiver::class.java),
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
            )
            runCatching {
                am.setInexactRepeating(
                    AlarmManager.RTC_WAKEUP,
                    System.currentTimeMillis() + AlarmManager.INTERVAL_DAY,
                    AlarmManager.INTERVAL_DAY,
                    pi
                )
            }
        }

        /** kind ごとの理由文 (通知本文・セットアップカードで共用)。 */
        fun reasonText(ctx: Context, kind: PushHealth.Kind): String = when (kind) {
            PushHealth.Kind.NOTIFICATIONS_OFF ->
                ctx.getString(R.string.health_reason_notifications_off)
            PushHealth.Kind.MIC_DENIED ->
                ctx.getString(R.string.health_reason_mic_denied)
            PushHealth.Kind.PUSH_TOKEN_MISSING ->
                ctx.getString(R.string.health_reason_push_token_missing)
            PushHealth.Kind.PUSH_REGISTRATION_STALE ->
                ctx.getString(R.string.health_reason_push_registration_stale)
            PushHealth.Kind.HIBERNATION_SOON ->
                ctx.getString(R.string.health_reason_hibernation_soon)
            PushHealth.Kind.BATTERY_OPTIMIZED ->
                ctx.getString(R.string.health_reason_battery_optimized)
            PushHealth.Kind.OVERLAY_DENIED ->
                ctx.getString(R.string.health_reason_overlay_denied)
            PushHealth.Kind.FULLSCREEN_DENIED ->
                ctx.getString(R.string.health_reason_fullscreen_denied)
        }
    }
}

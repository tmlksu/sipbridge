package io.github.tmlksu.sipbridge

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build
import androidx.core.app.NotificationCompat

object NotificationHelper {
    const val CH_SERVICE = "sipbridge_service"
    /** §6.5 静音用の常駐通知チャンネル (IMPORTANCE_MIN)。作成後の importance は変えられないため別保持。 */
    const val CH_SERVICE_QUIET = "sipbridge_service_quiet"
    const val CH_INCOMING = "sipbridge_incoming"
    /** §6.3 定期チェックの警告通知チャンネル (IMPORTANCE_DEFAULT)。 */
    const val CH_HEALTH = "sipbridge_health"
    const val ID_SERVICE = 1001
    const val ID_INCOMING = 1002
    const val ID_HEALTH = 1003

    fun ensureChannels(ctx: Context) {
        val nm = ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            nm.createNotificationChannel(
                NotificationChannel(
                    CH_SERVICE,
                    ctx.getString(R.string.notif_ch_service),
                    NotificationManager.IMPORTANCE_LOW
                )
            )
            nm.createNotificationChannel(
                NotificationChannel(
                    CH_SERVICE_QUIET,
                    ctx.getString(R.string.notif_ch_service_quiet),
                    NotificationManager.IMPORTANCE_MIN
                )
            )
            // 着信チャンネルの挙動は一切変えない (IMPORTANCE_HIGH + DND バイパス維持)。
            nm.createNotificationChannel(
                NotificationChannel(
                    CH_INCOMING,
                    ctx.getString(R.string.notif_ch_incoming),
                    NotificationManager.IMPORTANCE_HIGH
                ).apply {
                    setBypassDnd(true)
                    lockscreenVisibility = Notification.VISIBILITY_PUBLIC
                }
            )
            nm.createNotificationChannel(
                NotificationChannel(
                    CH_HEALTH,
                    ctx.getString(R.string.notif_ch_health),
                    NotificationManager.IMPORTANCE_DEFAULT
                )
            )
        }
    }

    /**
     * 常駐通知 (UI-DESIGN §2.1)。
     * - PERSISTENT: 「relay に接続中・内線 2104」/「常時接続／常駐通知 (消去不可)」
     * - PUSH 待機 (未接続): 「待機中 (PUSH 起床)」
     *
     * @param pushIdle PUSH モードで WSS 未接続 (起床待ち) のとき true。
     * @param extension 内線番号 (hello.account)。空なら内線部分を省略する。
     */
    fun serviceNotification(
        ctx: Context, pushIdle: Boolean, extension: String, quiet: Boolean = false
    ): Notification {
        val pi = PendingIntent.getActivity(
            ctx, 0, Intent(ctx, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        val (title, text) = if (pushIdle) {
            ctx.getString(R.string.notif_service_push_title) to
                ctx.getString(R.string.notif_service_push_text)
        } else {
            val t = if (extension.isNotBlank()) ctx.getString(R.string.notif_service_title_ext, extension)
            else ctx.getString(R.string.notif_service_title)
            t to ctx.getString(R.string.notif_service_text)
        }
        val b = NotificationCompat.Builder(ctx, if (quiet) CH_SERVICE_QUIET else CH_SERVICE)
            .setSmallIcon(android.R.drawable.ic_menu_call)
            .setContentTitle(title)
            .setContentText(text)
            .setContentIntent(pi)
            .setOngoing(true)
        if (quiet) {
            // §6.5: ステータスバーのアイコンを消し、シェード最下部に折りたたむ。
            b.setSilent(true)
                .setShowWhen(false)
                .setPriority(NotificationCompat.PRIORITY_MIN)
        }
        return b.build()
    }

    /**
     * 通話中の常駐通知 (UI-DESIGN §3.2 の代替表示。オーバーレイ権限が無い場合)。
     * タイトル「通話中」+ ヘッダの経過時間 (chronometer)。タップで通話画面に戻る。
     */
    fun inCallNotification(ctx: Context, startedAtEpochMs: Long, quiet: Boolean = false): Notification {
        val tap = PendingIntent.getActivity(
            ctx, 4, CallOverlayManager.callActivityIntent(ctx),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        // 経過時間は SystemUI 側の chronometer に任せる (#23)。when = 通話開始の epoch ms
        // (CallHub.callStartedAt と同じ基準)。これで毎秒の notify() 再発行が不要になる。
        val start = if (startedAtEpochMs > 0) startedAtEpochMs else System.currentTimeMillis()
        val b = NotificationCompat.Builder(ctx, if (quiet) CH_SERVICE_QUIET else CH_SERVICE)
            .setSmallIcon(android.R.drawable.ic_menu_call)
            .setContentTitle(ctx.getString(R.string.notif_incall_title))
            .setContentText(ctx.getString(R.string.notif_incall_text))
            .setContentIntent(tap)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setWhen(start)
            .setUsesChronometer(true)
            .setChronometerCountDown(false)
        // quiet でも chronometer は表示される (showWhen とは独立) ので setShowWhen(false) は残す
        if (quiet) b.setSilent(true).setShowWhen(false).setPriority(NotificationCompat.PRIORITY_MIN)
        return b.build()
    }

    /** 発信呼出中の常駐通知 (オーバーレイ権限が無いときの通話中ピル代替)。 */
    fun outgoingNotification(ctx: Context, to: String, quiet: Boolean = false): Notification {
        val tap = PendingIntent.getActivity(
            ctx, 5, CallOverlayManager.callActivityIntent(ctx),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        val b = NotificationCompat.Builder(ctx, if (quiet) CH_SERVICE_QUIET else CH_SERVICE)
            .setSmallIcon(android.R.drawable.ic_menu_call)
            .setContentTitle(ctx.getString(R.string.notif_outgoing_title))
            .setContentText(ctx.getString(R.string.notif_outgoing_text, to))
            .setContentIntent(tap)
            .setOngoing(true)
        if (quiet) b.setSilent(true).setShowWhen(false).setPriority(NotificationCompat.PRIORITY_MIN)
        return b.build()
    }

    /**
     * §6.3 定期チェックの警告通知 (1 本。タップで設定タブ)。
     * ongoing=false、着信チャンネルとは別なので常駐通知の静音化の影響を受けない。
     */
    fun healthIssueNotification(ctx: Context, title: String, text: String): Notification {
        val tap = PendingIntent.getActivity(
            ctx, 6, MainActivity.settingsIntent(ctx),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        return NotificationCompat.Builder(ctx, CH_HEALTH)
            .setSmallIcon(android.R.drawable.ic_menu_call)
            .setContentTitle(title)
            .setContentText(text)
            .setStyle(NotificationCompat.BigTextStyle().bigText(text))
            .setContentIntent(tap)
            .setAutoCancel(true)
            .setOngoing(false)
            .build()
    }

    /**
     * 着信通知 (UI-DESIGN §2.1)。
     * タイトル「着信中・<表示名 or 番号>」、本文「<番号> から (relay 経由)」、
     * アクション「拒否」「応答」(応答は accent 色)。
     */
    fun incomingNotification(ctx: Context, from: String, display: String = ""): Notification {
        val full = Intent(ctx, CallActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP
        }
        val fullPi = PendingIntent.getActivity(
            ctx, 1, full, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        val answer = PendingIntent.getService(
            ctx, 2, Intent(ctx, BridgeService::class.java).setAction(BridgeService.ACT_ANSWER),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        val reject = PendingIntent.getService(
            ctx, 3, Intent(ctx, BridgeService::class.java).setAction(BridgeService.ACT_REJECT),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        val name = display.ifBlank { from.ifBlank { ctx.getString(R.string.call_unknown) } }
        return NotificationCompat.Builder(ctx, CH_INCOMING)
            .setSmallIcon(android.R.drawable.ic_menu_call)
            .setColor(ctx.getColor(R.color.nocturne_accent))
            .setContentTitle(ctx.getString(R.string.notif_incoming_title, name))
            .setContentText(ctx.getString(R.string.notif_incoming_text, from))
            .setPriority(NotificationCompat.PRIORITY_MAX)
            .setCategory(NotificationCompat.CATEGORY_CALL)
            .setFullScreenIntent(fullPi, true)
            .setOngoing(true)
            .setAutoCancel(false)
            .addAction(
                android.R.drawable.ic_menu_close_clear_cancel,
                ctx.getString(R.string.notif_action_reject), reject
            )
            .addAction(
                android.R.drawable.ic_menu_call,
                ctx.getString(R.string.notif_action_answer), answer
            )
            .build()
    }
}

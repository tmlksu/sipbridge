package io.github.tmlksu.sipbridge

import android.Manifest
import android.app.NotificationManager
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.PowerManager
import android.provider.Settings
import androidx.core.content.ContextCompat

/**
 * OS 状態のスナップショット (UI-DESIGN §6.1)。
 *
 * PrefixDialer の `SystemStatus` と同じ方針: **Android API の呼び出しをこのファイルに閉じ込め**、
 * UI ([SettingsFragment]/[SetupSheet]) は値だけを見る。設定画面に散らばっていた
 * `canDrawOverlays` / `isBatteryExcluded` / `areNotificationsEnabled` /
 * `fullscreenState` の判定はここに集約した (重複実装は残さない)。
 *
 * 非対応は `null` で返し、UI 側は行自体を出さない:
 * - [hibernationExempt]: API 30 未満、または休止設定画面が解決できない端末
 *   (Echo Show 5 = LineageOS 18.1/API 30/GApps 無し。休止機能自体が無い可能性)。
 * - [fullScreenIntentAllowed]: API 34 未満。
 */
data class SystemStatus(
    /** RECORD_AUDIO が許可済みか。 */
    val micGranted: Boolean = false,
    /**
     * 着信を表示できるか (権限 + [NotificationManager.areNotificationsEnabled] +
     * 着信チャンネルが OFF でない)。§6.5: 常駐チャンネルを意図的に切った状態は
     * 「未許可」にしないため、判定対象は着信チャンネルだけ。
     */
    val notificationsEnabled: Boolean = false,
    /** 他アプリの上に表示できるか。 */
    val overlayGranted: Boolean = false,
    /** 全画面通知が使えるか (API 34+)。対象外は null。 */
    val fullScreenIntentAllowed: Boolean? = null,
    /** 電池の最適化から除外されているか。 */
    val ignoringBatteryOptimizations: Boolean = false,
    /** 未使用アプリの休止から除外されているか。非対応端末は null。 */
    val hibernationExempt: Boolean? = null,
    /** gms ビルドかつ FCM トークンを保持しているか。foss では常に false。 */
    val pushTokenPresent: Boolean = false,
    /** Samsung 製か (One UI のスリープ案内を出すため)。 */
    val isSamsung: Boolean = false,
    /** CALL_PHONE が許可済みか。 */
    val callPhoneGranted: Boolean = false,
    /** READ_PHONE_STATE が許可済みか (managed 有効化の判定に必須)。 */
    val readPhoneStateGranted: Boolean = false,
    /** 通話アカウントが有効か。Telecom 非対応端末や `telecomPref == APP` では null (行を出さない)。
     *  READ_PHONE_STATE 未許可のときは判定不能だが行は出したいので false を返す
     *  (null にすると行が消えて詰む)。 */
    val telecomAccountEnabled: Boolean? = null,
) {
    companion object {

        fun read(ctx: Context): SystemStatus = SystemStatus(
            micGranted = hasPermission(ctx, Manifest.permission.RECORD_AUDIO),
            notificationsEnabled = areNotificationsEnabled(ctx),
            overlayGranted = canDrawOverlays(ctx),
            fullScreenIntentAllowed = fullScreenIntentAllowed(ctx),
            ignoringBatteryOptimizations = isBatteryExcluded(ctx),
            hibernationExempt = hibernationExempt(ctx),
            pushTokenPresent = hasPushToken(ctx),
            isSamsung = Build.MANUFACTURER.equals("samsung", ignoreCase = true),
            callPhoneGranted = hasPermission(ctx, Manifest.permission.CALL_PHONE),
            readPhoneStateGranted = hasPermission(ctx, Manifest.permission.READ_PHONE_STATE),
            telecomAccountEnabled = telecomAccountEnabled(ctx),
        )

        fun hasPermission(ctx: Context, permission: String): Boolean =
            ContextCompat.checkSelfPermission(ctx, permission) == PackageManager.PERMISSION_GRANTED

        fun canDrawOverlays(ctx: Context): Boolean =
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) Settings.canDrawOverlays(ctx)
            else true

        fun isBatteryExcluded(ctx: Context): Boolean {
            if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M) return true
            val pm = ctx.getSystemService(Context.POWER_SERVICE) as? PowerManager
                ?: return false
            return runCatching { pm.isIgnoringBatteryOptimizations(ctx.packageName) }
                .getOrDefault(false)
        }

        fun areNotificationsEnabled(ctx: Context): Boolean {
            val nm = ctx.getSystemService(Context.NOTIFICATION_SERVICE) as? NotificationManager
                ?: return false
            if (!nm.areNotificationsEnabled()) return false
            if (Build.VERSION.SDK_INT >= 33 &&
                !hasPermission(ctx, Manifest.permission.POST_NOTIFICATIONS)
            ) return false
            // 着信チャンネルが OFF だと着信は表示されない (常駐チャンネルは判定対象外)。
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                val ch = runCatching { nm.getNotificationChannel(NotificationHelper.CH_INCOMING) }
                    .getOrNull()
                if (ch != null && ch.importance == NotificationManager.IMPORTANCE_NONE) return false
            }
            return true
        }

        fun fullScreenIntentAllowed(ctx: Context): Boolean? {
            if (Build.VERSION.SDK_INT < 34) return null
            val nm = ctx.getSystemService(Context.NOTIFICATION_SERVICE) as? NotificationManager
                ?: return false
            return runCatching { nm.canUseFullScreenIntent() }.getOrDefault(false)
        }

        /**
         * 未使用アプリの休止 (auto-revoke) から除外されているか。
         * API 30 未満、または休止設定画面 ([autoRevokeIntent]) が解決できない端末
         * (機能を持たない) では null。appDetails は常に解決できるためゲートには使わない。
         */
        fun hibernationExempt(ctx: Context): Boolean? {
            if (Build.VERSION.SDK_INT < 30) return null
            if (autoRevokeIntent(ctx).resolveActivity(ctx.packageManager) == null) return null
            return runCatching { ctx.packageManager.isAutoRevokeWhitelisted() }.getOrNull()
        }

        /**
         * 通話アカウント (managed) が有効か。Telecom 非対応端末や
         * `telecomPref == APP` (アプリ独自に固定) では null を返す (行を出さない)。
         * `getCallCapablePhoneAccounts()` には READ_PHONE_STATE (ランタイム権限) が
         * 必須のため、未許可のときは判定不能だが行は出したいので false を返す。
         */
        fun telecomAccountEnabled(ctx: Context): Boolean? = telecomAccountEnabledFrom(
            hasTelecom = TelecomCompat.hasTelecom(ctx),
            prefIsApp = runCatching { BridgeConfig.load(ctx).telecomPref }.getOrNull() ==
                TelecomTierManager.Pref.APP,
            readPhoneStateGranted = hasPermission(ctx, Manifest.permission.READ_PHONE_STATE),
            managedEnabled = TelecomCompat.isManagedEnabled(ctx),
        )

        /**
         * [telecomAccountEnabled] の判定本体 (純関数。JVM テスト対象)。
         * READ_PHONE_STATE 未許可では `isManagedEnabled()` が常に false になるため、
         * Telecom 呼び出しの前に false で打ち切る (原因が権限だと分かるようにするため)。
         */
        fun telecomAccountEnabledFrom(
            hasTelecom: Boolean,
            prefIsApp: Boolean,
            readPhoneStateGranted: Boolean,
            managedEnabled: Boolean,
        ): Boolean? {
            if (!hasTelecom) return null
            if (prefIsApp) return null
            if (!readPhoneStateGranted) return false
            return managedEnabled
        }

        /** gms かつ FCM トークン保持か。main ソースセットなので Firebase API には触らない。 */
        fun hasPushToken(ctx: Context): Boolean {
            if (BuildConfig.FLAVOR != "gms") return false
            return runCatching {
                ctx.getSharedPreferences(BridgeService.FCM_PREFS, Context.MODE_PRIVATE)
                    .getString(BridgeService.FCM_TOKEN_KEY, null)?.isNotBlank() == true
            }.getOrDefault(false)
        }

        // ---- 設定画面へ飛ぶ Intent の候補 (先頭から resolveActivity が通るものを起動する) ----

        /**
         * 電池の最適化の除外へ誘導する候補。先頭はその場で許可を求めるダイアログ。
         * 本アプリは `REQUEST_IGNORE_BATTERY_OPTIMIZATIONS` を宣言済み (manifest) なので
         * 出せるが、端末によっては解決できないためフォールバックを必ず持つ
         * (PrefixDialer の教訓: 未宣言だと S25 で無反応になる)。
         */
        fun batteryIntents(ctx: Context): List<Intent> = listOf(
            Intent(
                Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                Uri.parse("package:${ctx.packageName}")
            ),
            Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS),
            appDetailsIntent(ctx),
        )

        /** 休止の除外へ誘導する候補。API 30+ かつ解決できる端末でのみ使うこと。 */
        fun autoRevokeIntent(ctx: Context): Intent =
            Intent(Intent.ACTION_AUTO_REVOKE_PERMISSIONS).apply {
                data = Uri.parse("package:${ctx.packageName}")
            }

        fun hibernationIntents(ctx: Context): List<Intent> = listOf(
            autoRevokeIntent(ctx),
            appDetailsIntent(ctx),
        )

        fun overlayIntent(ctx: Context): List<Intent> =
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
                listOf(
                    Intent(
                        Settings.ACTION_MANAGE_OVERLAY_PERMISSION,
                        Uri.parse("package:${ctx.packageName}")
                    )
                )
            } else {
                emptyList()
            }

        fun notificationSettingsIntent(ctx: Context): List<Intent> = listOf(
            Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS).apply {
                putExtra(Settings.EXTRA_APP_PACKAGE, ctx.packageName)
            },
            appDetailsIntent(ctx),
        )

        /** 通知チャンネル個別の設定画面 (§6.5 の静音チャンネル案内用)。 */
        fun channelSettingsIntent(ctx: Context, channelId: String): List<Intent> = listOf(
            Intent(Settings.ACTION_CHANNEL_NOTIFICATION_SETTINGS).apply {
                putExtra(Settings.EXTRA_APP_PACKAGE, ctx.packageName)
                putExtra(Settings.EXTRA_CHANNEL_ID, channelId)
            },
            appDetailsIntent(ctx),
        )

        fun fullscreenIntent(ctx: Context): List<Intent> =
            if (Build.VERSION.SDK_INT >= 34) {
                listOf(
                    Intent(Settings.ACTION_MANAGE_APP_USE_FULL_SCREEN_INTENT).apply {
                        data = Uri.parse("package:${ctx.packageName}")
                    },
                    appDetailsIntent(ctx),
                )
            } else {
                emptyList()
            }

        fun appDetailsIntent(ctx: Context): Intent =
            Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS)
                .setData(Uri.parse("package:${ctx.packageName}"))

        /** 候補のうち解決できるものがあるか。 */
        fun isResolvable(ctx: Context, intents: List<Intent>): Boolean =
            intents.any { it.resolveActivity(ctx.packageManager) != null }

        /**
         * 候補を先頭から試し、起動できた最初のもので true。
         * 解決できても起動に失敗する端末に備え、例外時は次を試す。
         */
        fun startFirstResolvable(ctx: Context, intents: List<Intent>): Boolean {
            for (i in intents) {
                if (i.resolveActivity(ctx.packageManager) == null) continue
                if (runCatching { ctx.startActivity(i) }.isSuccess) return true
            }
            return false
        }
    }
}

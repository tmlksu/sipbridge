package io.github.tmlksu.sipbridge

import android.content.Context
import android.content.SharedPreferences
import android.util.Log
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import java.util.UUID

/** 接続モード。PERSISTENT=常時WSS (Echo Show用) / PUSH=FCM起床・用済み切断 (S25用)。 */
enum class BridgeMode { PERSISTENT, PUSH }

data class BridgeConfigData(
    val relayUrl: String = "",
    val accessClientId: String = "",
    val accessClientSecret: String = "",
    val devToken: String = "",
    val mode: BridgeMode = BridgeMode.PERSISTENT,
    val micGain: Float = 2.0f,
    val overlayEnabled: Boolean = true,
    val autostart: Boolean = true,
    /** SIP アカウント (内線)。relay がこの資格情報で Asterisk に登録する。 */
    val sipUser: String = "",
    val sipPassword: String = "",
    val sipDisplay: String = "",
    /** 応答時にスピーカー出力にする (既定 OFF。一般的な通話アプリに合わせ受話口を使う)。
     *  受話口を持たない端末 (Echo Show など) では [AudioRoute.hasEarpiece] が false のため
     *  設定に関わらずスピーカーへ出す。 */
    val speakerOnAnswer: Boolean = false,
    /** 端末の連絡先を連絡先タブに混ぜて表示する (既定 OFF。ON で READ_CONTACTS を要求)。 */
    val deviceContactsEnabled: Boolean = false,
    /** §6.5 常駐通知を静音チャンネルに出す (既定 OFF)。着信チャンネルは影響を受けない。 */
    val serviceNotificationQuiet: Boolean = false,
    /** relay の X-Device-Id。初回生成し端末に固定。 */
    val deviceId: String = "",
    /** 通話画面の方式 (自動 / OS 標準 / アプリ独自)。既定 AUTO。 */
    val telecomPref: TelecomTierManager.Pref = TelecomTierManager.Pref.AUTO,
    /** 学習した上限ティア。ティア落ちのたびに下がる。既定 MANAGED。 */
    val telecomMaxTier: CallTier = CallTier.MANAGED,
    /** 学習をリセットする条件の指紋。 */
    val telecomEnvFingerprint: String = "",
)

/**
 * 設定の読み書き。パスワード類 (Access Secret / Dev Token) を含むため
 * EncryptedSharedPreferences に保存する。暗号化に失敗した端末では
 * 平文 SharedPreferences にフォールバックする (動作優先)。
 */
object BridgeConfig {
    private const val TAG = "BridgeConfig"
    private const val ENC_PREF = "sipbridge_enc"
    private const val PLAIN_PREF = "sipbridge"

    fun load(ctx: Context): BridgeConfigData {
        val p = prefs(ctx)
        val mode = try {
            BridgeMode.valueOf(p.getString("mode", BridgeMode.PERSISTENT.name)!!)
        } catch (e: Exception) {
            BridgeMode.PERSISTENT
        }
        // enum は name で保存し、未知の値は既定に落とす。
        val telecomPref = TelecomTierManager.prefFromName(p.getString("telecomPref", null))
        val telecomMaxTier =
            TelecomTierManager.tierFromName(p.getString("telecomMaxTier", null))
        var deviceId = p.getString("deviceId", "") ?: ""
        if (deviceId.isBlank()) {
            deviceId = UUID.randomUUID().toString()
            runCatching { p.edit().putString("deviceId", deviceId).apply() }
        }
        return BridgeConfigData(
            relayUrl = p.getString("relayUrl", "") ?: "",
            accessClientId = p.getString("accessClientId", "") ?: "",
            accessClientSecret = p.getString("accessClientSecret", "") ?: "",
            devToken = p.getString("devToken", "") ?: "",
            mode = mode,
            micGain = p.getFloat("micGain", 2.0f).let { if (it <= 0) 2.0f else it },
            overlayEnabled = p.getBoolean("overlayEnabled", true),
            autostart = p.getBoolean("autostart", true),
            sipUser = p.getString("sipUser", "") ?: "",
            sipPassword = p.getString("sipPassword", "") ?: "",
            sipDisplay = p.getString("sipDisplay", "") ?: "",
            speakerOnAnswer = p.getBoolean("speakerOnAnswer", false),
            deviceContactsEnabled = p.getBoolean("deviceContactsEnabled", false),
            serviceNotificationQuiet = p.getBoolean("serviceNotificationQuiet", false),
            deviceId = deviceId,
            telecomPref = telecomPref,
            telecomMaxTier = telecomMaxTier,
            telecomEnvFingerprint = p.getString("telecomEnvFingerprint", "") ?: ""
        )
    }

    fun save(ctx: Context, d: BridgeConfigData) {
        // deviceId は端末固定。空で保存しようとしたら既存値を維持する。
        val keepId = d.deviceId.ifBlank { load(ctx).deviceId }
        prefs(ctx).edit()
            .putString("relayUrl", d.relayUrl.trim())
            .putString("accessClientId", d.accessClientId.trim())
            .putString("accessClientSecret", d.accessClientSecret)
            .putString("devToken", d.devToken)
            .putString("mode", d.mode.name)
            .putFloat("micGain", d.micGain)
            .putBoolean("overlayEnabled", d.overlayEnabled)
            .putBoolean("autostart", d.autostart)
            .putString("sipUser", d.sipUser.trim())
            .putString("sipPassword", d.sipPassword)
            .putString("sipDisplay", d.sipDisplay.trim())
            .putBoolean("speakerOnAnswer", d.speakerOnAnswer)
            .putBoolean("deviceContactsEnabled", d.deviceContactsEnabled)
            .putBoolean("serviceNotificationQuiet", d.serviceNotificationQuiet)
            .putString("deviceId", keepId.ifBlank { UUID.randomUUID().toString() })
            .putString("telecomPref", d.telecomPref.name)
            .putString("telecomMaxTier", d.telecomMaxTier.name)
            .putString("telecomEnvFingerprint", d.telecomEnvFingerprint)
            .apply()
    }

    private fun prefs(ctx: Context): SharedPreferences {
        runCatching {
            val masterKey = MasterKey.Builder(ctx)
                .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
                .build()
            return EncryptedSharedPreferences.create(
                ctx,
                ENC_PREF,
                masterKey,
                EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
                EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM
            )
        }.onFailure {
            Log.w(TAG, "encrypted prefs unavailable, fallback to plain: ${it.message}")
        }
        return ctx.getSharedPreferences(PLAIN_PREF, Context.MODE_PRIVATE)
    }
}

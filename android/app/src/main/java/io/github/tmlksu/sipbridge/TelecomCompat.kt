package io.github.tmlksu.sipbridge

import android.content.Context
import android.content.Intent
import android.graphics.drawable.Icon
import android.net.Uri
import android.os.Bundle
import android.provider.Settings
import android.telecom.DisconnectCause
import android.telecom.PhoneAccount
import android.telecom.PhoneAccountHandle
import android.telecom.TelecomManager
import android.util.Log

/**
 * Telecom API の呼び出しをこのファイルに閉じ込める (`SystemStatus` と同じ方針)。
 * UI とサービスはここが返す値だけを見る。**例外は全部ここで握り潰し、boolean/null で返す。**
 *
 * 有効化判定は `callCapablePhoneAccounts.contains(handle)` で行う。
 * `getPhoneAccount(handle)?.isEnabled` は Samsung で必ず null になるため使ってはいけない。
 */
object TelecomCompat {
    private const val TAG = "TelecomCompat"
    const val ACCOUNT_ID_MANAGED = "sipbridge"
    const val ACCOUNT_ID_SELF = "sipbridge-self"

    /** 自前キー: relay の callId (addNewIncomingCall の extras に入れる)。 */
    const val EXTRA_CALL_ID = "sipbridge.CALL_ID"
    /** 自前キー: アプリ起点の発信 (placeCall の EXTRA_OUTGOING_CALL_EXTRAS に入れる)。 */
    const val EXTRA_FROM_APP = "sipbridge.FROM_APP"

    fun handle(ctx: Context, id: String): PhoneAccountHandle =
        PhoneAccountHandle(
            android.content.ComponentName(ctx, SipConnectionService::class.java), id
        )

    private fun telecom(ctx: Context): TelecomManager? =
        runCatching { ctx.getSystemService(Context.TELECOM_SERVICE) as? TelecomManager }
            .getOrNull()

    /** Telecom の feature 名。API 30 で `connectionservice` → `telecom` に置き換わった。
     *  定数ではなく文字列で持つ (前者は非推奨、後者は API 30 追加で minSdk 29 より新しい)。 */
    private const val FEATURE_TELECOM = "android.software.telecom"
    private const val FEATURE_CONNECTION_SERVICE = "android.software.connectionservice"

    /**
     * 端末に Telecom があるか。`TelecomManager != null` かつ Telecom の feature 持ちを見る。
     *
     * Echo Show 5 は `com.android.server.telecom` パッケージだけ持っているため
     * `getSystemService(TELECOM_SERVICE)` が非 null になり得る (telephony /
     * InCallService / 既定ダイヤラーは無い)。feature 要求でこれを弾く。
     *
     * **feature 名は 2 つとも見ること**。実測 (2026-09-17):
     * - Galaxy S25 (Android 16 / One UI 8) … `telecom` のみ (**`connectionservice` 無し**)
     * - TINNO P780 (Android 11)           … `connectionservice` あり
     * - Echo Show 5 (LineageOS 18.1)      … **どちらも無し**
     *
     * 旧名だけを見ると S25 で PhoneAccount が登録されず、ティア C から一生出られない
     * (v1.5 で実際にそうなった)。
     */
    fun hasTelecom(ctx: Context): Boolean = runCatching {
        telecom(ctx) != null && ctx.packageManager.let {
            it.hasSystemFeature(FEATURE_TELECOM) || it.hasSystemFeature(FEATURE_CONNECTION_SERVICE)
        }
    }.getOrDefault(false)

    /** managed アカウントを登録する。成功で true。 */
    fun registerManaged(ctx: Context, label: String, shortDesc: String): Boolean {
        val tm = telecom(ctx) ?: return false
        return runCatching {
            // setAddress の番号は設定の内線番号、未設定なら "sipbridge"。
            val ext = runCatching { BridgeConfig.load(ctx).sipUser.ifBlank { "sipbridge" } }
                .getOrDefault("sipbridge")
            val account = PhoneAccount.builder(handle(ctx, ACCOUNT_ID_MANAGED), label)
                .setCapabilities(PhoneAccount.CAPABILITY_CALL_PROVIDER)
                .addSupportedUriScheme(PhoneAccount.SCHEME_TEL)
                .addSupportedUriScheme(PhoneAccount.SCHEME_SIP)
                .setAddress(Uri.fromParts(PhoneAccount.SCHEME_TEL, ext, null))
                .setShortDescription(shortDesc)
                .setIcon(Icon.createWithResource(ctx, R.mipmap.ic_launcher))
                .build()
            tm.registerPhoneAccount(account)
            true
        }.onFailure { Log.w(TAG, "registerManaged failed", it) }.getOrDefault(false)
    }

    /** self-managed アカウントを登録する。 */
    fun registerSelfManaged(ctx: Context, label: String, shortDesc: String): Boolean {
        val tm = telecom(ctx) ?: return false
        return runCatching {
            val account = PhoneAccount.builder(handle(ctx, ACCOUNT_ID_SELF), label)
                .setCapabilities(PhoneAccount.CAPABILITY_SELF_MANAGED)
                .addSupportedUriScheme(PhoneAccount.SCHEME_TEL)
                .addSupportedUriScheme(PhoneAccount.SCHEME_SIP)
                .setShortDescription(shortDesc)
                .setIcon(Icon.createWithResource(ctx, R.mipmap.ic_launcher))
                .build()
            tm.registerPhoneAccount(account)
            true
        }.onFailure { Log.w(TAG, "registerSelfManaged failed", it) }.getOrDefault(false)
    }

    fun unregister(ctx: Context, id: String) {
        val tm = telecom(ctx) ?: return
        runCatching { tm.unregisterPhoneAccount(handle(ctx, id)) }
            .onFailure { Log.w(TAG, "unregister failed: $id", it) }
    }

    fun unregisterAll(ctx: Context) {
        unregister(ctx, ACCOUNT_ID_MANAGED)
        unregister(ctx, ACCOUNT_ID_SELF)
    }

    /**
     * managed が**今**使えるか。
     * `getPhoneAccount(handle)?.isEnabled` は Samsung で必ず null になるため使わない。
     */
    fun isManagedEnabled(ctx: Context): Boolean {
        val tm = telecom(ctx) ?: return false
        return runCatching { tm.callCapablePhoneAccounts.contains(handle(ctx, ACCOUNT_ID_MANAGED)) }
            .onFailure { Log.w(TAG, "isManagedEnabled failed", it) }
            .getOrDefault(false)
    }

    /**
     * managed アカウントが登録だけされているか (有効化の案内を出すかの判定用)。
     * Samsung では getPhoneAccount が常に null のため、有効化済みの判定には使えない。
     * 有効化済みなら true を返すよう isManagedEnabled と OR する。
     */
    fun isManagedRegistered(ctx: Context): Boolean {
        if (isManagedEnabled(ctx)) return true
        val tm = telecom(ctx) ?: return false
        return runCatching { tm.getPhoneAccount(handle(ctx, ACCOUNT_ID_MANAGED)) != null }
            .getOrDefault(false)
    }

    fun isSelfManagedUsable(ctx: Context, incoming: Boolean): Boolean {
        val tm = telecom(ctx) ?: return false
        val h = handle(ctx, ACCOUNT_ID_SELF)
        return runCatching {
            if (incoming) tm.isIncomingCallPermitted(h) else tm.isOutgoingCallPermitted(h)
        }.onFailure { Log.w(TAG, "isSelfManagedUsable failed", it) }.getOrDefault(false)
    }

    /** 発信アカウント設定への Intent 候補。先頭が AOSP 標準の有効化画面。 */
    @Suppress("UNUSED_PARAMETER")
    fun enableAccountIntents(ctx: Context): List<Intent> = listOf(
        Intent("android.telecom.action.CHANGE_PHONE_ACCOUNTS"),
        Intent(Settings.ACTION_SETTINGS),
    )

    fun addNewIncomingCall(ctx: Context, id: String, extras: Bundle): Boolean {
        val tm = telecom(ctx) ?: return false
        return runCatching {
            tm.addNewIncomingCall(handle(ctx, id), extras)
            true
        }.onFailure { Log.w(TAG, "addNewIncomingCall failed", it) }.getOrDefault(false)
    }

    fun placeCall(ctx: Context, number: String, extras: Bundle): Boolean {
        val tm = telecom(ctx) ?: return false
        return runCatching {
            tm.placeCall(Uri.fromParts(PhoneAccount.SCHEME_TEL, number, null), extras)
            true
        }.onFailure { Log.w(TAG, "placeCall failed", it) }.getOrDefault(false)
    }

    /**
     * relay の終了理由 → [DisconnectCause] の対応表 (純関数)。
     * `bye`→REMOTE, `cancel`→CANCELED, `reject`→BUSY, `timeout`→MISSED,
     * `answered_elsewhere`→ANSWERED_ELSEWHERE, その他→UNKNOWN。
     */
    fun disconnectCauseFor(reason: String): Int = when (reason) {
        "bye" -> DisconnectCause.REMOTE
        "cancel" -> DisconnectCause.CANCELED
        "reject" -> DisconnectCause.BUSY
        "timeout" -> DisconnectCause.MISSED
        "answered_elsewhere" -> DisconnectCause.ANSWERED_ELSEWHERE
        else -> DisconnectCause.UNKNOWN
    }

    /**
     * 発信番号の取り出し (純関数)。番号は加工しない。
     * `tel:` なら schemeSpecificPart、`sip:` なら `@` の前、その他はそのまま。
     */
    fun numberFrom(uri: String): String {
        val s = uri.substringAfter(":")
        return s.substringBefore("@")
    }
}

package io.github.tmlksu.sipbridge

import android.content.Context
import android.os.Build
import android.util.Log

/** 通話画面のティア。呼ごとにセットアップ時点で確定し、通話中は変えない。 */
enum class CallTier { MANAGED, SELF_MANAGED, LEGACY }

/**
 * ティアの決定と、端末ごとの学習 (sticky degradation)。
 *
 * F-Droid 配布で段階リリースができないため、端末ごとに黙って賢くなる:
 * ティア落ちが起きたら [BridgeConfig.telecomMaxTier] に記録し、以後その端末では
 * そのティアを既定にする。アプリ更新・OS 変化を検知したら再探索する。
 */
object TelecomTierManager {
    private const val TAG = "TelecomTierManager"

    /** 設定の手動上書き。AUTO が既定。 */
    enum class Pref { AUTO, SYSTEM, APP }

    /** 起動時・設定変更時に呼ぶ。必要なアカウントを登録/解除する。 */
    fun sync(ctx: Context) {
        runCatching {
            resetIfEnvironmentChanged(ctx)
            val cfg = BridgeConfig.load(ctx)
            if (cfg.telecomPref == Pref.APP) {
                TelecomCompat.unregisterAll(ctx)
                return
            }
            if (!TelecomCompat.hasTelecom(ctx)) return
            val label = runCatching {
                ctx.getString(R.string.telecom_account_label)
            }.getOrDefault("SIP Bridge")
            val desc = runCatching {
                ctx.getString(R.string.telecom_account_desc)
            }.getOrDefault("")
            TelecomCompat.registerManaged(ctx, label, desc)
            TelecomCompat.registerSelfManaged(ctx, label, desc)
        }.onFailure { Log.w(TAG, "sync failed", it) }
    }

    /** この呼で使うティアを決める。 */
    fun decide(ctx: Context, incoming: Boolean): CallTier = runCatching {
        val cfg = BridgeConfig.load(ctx)
        decideFrom(
            pref = cfg.telecomPref,
            hasTelecom = TelecomCompat.hasTelecom(ctx),
            maxTier = cfg.telecomMaxTier,
            managedOk = TelecomCompat.isManagedEnabled(ctx),
            selfOk = TelecomCompat.isSelfManagedUsable(ctx, incoming),
        )
    }.getOrDefault(CallTier.LEGACY)

    /**
     * [decide] の判定本体 (純関数。JVM テスト対象)。
     * 端末依存部分は引数に切り出してある。
     * `Pref.SYSTEM` (ユーザーの明示選択) は学習した上限 (`maxTier`) を無視し、
     * 使えるなら MANAGED を選ぶ。使えなければ SELF_MANAGED → LEGACY と落ちる。
     */
    fun decideFrom(
        pref: Pref,
        hasTelecom: Boolean,
        maxTier: CallTier,
        managedOk: Boolean,
        selfOk: Boolean,
    ): CallTier {
        if (pref == Pref.APP) return CallTier.LEGACY
        if (!hasTelecom) return CallTier.LEGACY
        if (pref == Pref.SYSTEM) {
            if (managedOk) return CallTier.MANAGED
            if (selfOk) return CallTier.SELF_MANAGED
            return CallTier.LEGACY
        }
        if (maxTier == CallTier.LEGACY) return CallTier.LEGACY
        if (managedOk && maxTier == CallTier.MANAGED) return CallTier.MANAGED
        if (selfOk) return CallTier.SELF_MANAGED
        return CallTier.LEGACY
    }

    /** ティア落ちを記録する (以後この端末ではそのティアを既定にする)。上げることはしない。 */
    fun recordDegrade(ctx: Context, from: CallTier, to: CallTier, reason: String) {
        runCatching {
            Log.i(TAG, "degrade $from -> $to ($reason)")
            val cfg = BridgeConfig.load(ctx)
            // ordinal: MANAGED(0) < SELF_MANAGED(1) < LEGACY(2)。下がる方向だけ記録する。
            if (to.ordinal > cfg.telecomMaxTier.ordinal) {
                BridgeConfig.save(ctx, cfg.copy(telecomMaxTier = to))
            }
        }.onFailure { Log.w(TAG, "recordDegrade failed", it) }
    }

    /**
     * アプリ更新・OS 変化・**前提条件の変化**を検知したら学習を捨てて再探索する。
     *
     * 前提条件 (通話アカウントの有効化・電話権限) を指紋に含めるのが重要。
     * 含めないと「アカウントが無効だったせいでティア落ち → 学習に LEGACY が残る →
     * ユーザーが有効化しても学習が効いたままティア C」という詰みが起きる
     * ([decideFrom] は `maxTier == LEGACY` を無条件で LEGACY にするため)。
     * ユーザーが足りないものを足したら、その場でもう一度上のティアを試す。
     */
    fun resetIfEnvironmentChanged(ctx: Context) {
        runCatching {
            // 前提条件のスナップショット。どれかが変わったら学習をやり直す。
            val caps = listOf(
                TelecomCompat.hasTelecom(ctx),
                TelecomCompat.isManagedEnabled(ctx),
                SystemStatus.hasPermission(ctx, android.Manifest.permission.READ_PHONE_STATE),
                SystemStatus.hasPermission(ctx, android.Manifest.permission.CALL_PHONE),
            ).joinToString("") { if (it) "1" else "0" }
            val current =
                "${BuildConfig.VERSION_CODE}/${Build.VERSION.SDK_INT}/" +
                    "${Build.FINGERPRINT.hashCode()}/$caps"
            val cfg = BridgeConfig.load(ctx)
            if (cfg.telecomEnvFingerprint != current) {
                Log.i(TAG, "environment changed, reset learned tier")
                BridgeConfig.save(
                    ctx,
                    cfg.copy(
                        telecomMaxTier = CallTier.MANAGED,
                        telecomEnvFingerprint = current
                    )
                )
            }
        }.onFailure { Log.w(TAG, "resetIfEnvironmentChanged failed", it) }
    }

    /** enum 復元 (純関数)。未知の値は既定に落とす。 */
    fun prefFromName(name: String?): Pref =
        runCatching { Pref.valueOf(requireNotNull(name)) }.getOrDefault(Pref.AUTO)

    /** enum 復元 (純関数)。未知の値は既定に落とす。 */
    fun tierFromName(name: String?): CallTier =
        runCatching { CallTier.valueOf(requireNotNull(name)) }.getOrDefault(CallTier.MANAGED)
}

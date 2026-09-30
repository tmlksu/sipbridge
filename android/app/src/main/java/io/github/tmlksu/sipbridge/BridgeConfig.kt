package io.github.tmlksu.sipbridge

import android.content.Context
import android.content.SharedPreferences
import android.os.Looper
import android.os.SystemClock
import android.os.UserManager
import android.util.Log
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import java.io.File
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
    /**
     * 実際の保存領域から読んだ値か (永続化しない。copy で引き継がれる)。
     * [BridgeConfig.load] が保存領域を使えないときに返す既定値は false なので、
     * それを元にした値を [BridgeConfig.save] しても書かれない (#42: 既定値での上書き防止)。
     * 新規セットアップ (保存領域から読んだ空の設定) は true。
     */
    val fromStore: Boolean = false,
)

/**
 * 設定の読み書き。パスワード類 (Access Secret / Dev Token / SIP パスワード) を含むため
 * EncryptedSharedPreferences に保存する (#42)。
 *
 * 開けなかったときの扱い (判定は [EncStoreResolver]、原因の分類は [PrefsFailure]):
 * - 一時障害: **データは消さない**。短い間隔で数回再試行し、駄目なら「一時的に使えない」
 *   ([temporarilyUnavailable]) として、load は null / 既定値、save は**書かない** (平文にも書かない)。
 *   直後の [TRANSIENT_COOLDOWN_MS] は Keystore を叩かずに即「一時的に使えない」を返す。
 * - 壊れている: 別々のプロセス起動で 2 回続いたときだけ、壊れたファイルを
 *   `<名前>.broken-<時刻>.xml` に退避し、新しい鍵 alias・新しいファイル名で作り直す
 *   (元の鍵は消さない)。設定は初期状態になるので [configResetAt] で再設定を促す。
 * - 作り直した新しい世代でも壊れている: この端末は暗号化できないとみなし、平文で動く
 *   ([plainFallback]。meta に残し、以後は退避・世代追加をしない)。
 *
 * 既存ユーザーの通常経路は従来どおり `sipbridge_enc` + 既定のマスターキーを読む。
 * 平文 `sipbridge` からの移行は、旧版 (平文フォールバックがあった版) の値と、
 * 平文フォールバック端末が暗号化に戻れたときのためだけに残している。
 * バックアップ・端末間移行の対象からは外している (res/xml/data_extraction_rules.xml, #48)。
 */
object BridgeConfig {
    private const val TAG = "BridgeConfig"
    /** 世代 0 (既存ユーザー) の暗号化ファイル名。 */
    private const val ENC_PREF = "sipbridge_enc"
    private const val PLAIN_PREF = "sipbridge"
    /** 暗号化ストアの世代・破損カウンタ・初期化の記録 (平文。秘密は入れない)。 */
    private const val META_PREF = "sipbridge_meta"
    private const val META_GEN = "encGen"
    private const val META_CORRUPT_STREAK = "corruptStreak"
    private const val META_CORRUPT_TOKEN = "corruptToken"
    private const val META_CORRUPT_FIRST_AT = "corruptFirstAt"
    private const val META_CORRUPT_ATTEMPTS = "corruptAttempts"
    private const val META_PLAIN_FALLBACK = "plainFallback"
    private const val META_RESET_AT = "configResetAt"
    /** 接続モードの写し (秘密ではない)。設定を一時的に読めない間のモード判定用。 */
    private const val META_MODE = "mode"

    /** 一時障害の再試行間隔。メインスレッドから呼ばれる (設定画面など) ときは合計を小さくする。 */
    private val RETRY_DELAYS_MAIN_MS = longArrayOf(100, 200)
    private val RETRY_DELAYS_BG_MS = longArrayOf(250, 500, 1000)
    /** 一時障害の直後は、この間 Keystore を試さずに即「一時的に使えない」を返す。 */
    const val TRANSIENT_COOLDOWN_MS = 10_000L

    /** このプロセスの識別子。壊れ判定を「別々の起動で 2 回」数えるのに使う。 */
    private val PROCESS_TOKEN = UUID.randomUUID().toString()

    /** 世代ごとの暗号化ファイル名。世代 0 は従来の名前、世代 1 は `sipbridge_enc2`。 */
    internal fun encPrefName(gen: Int): String = if (gen <= 0) ENC_PREF else "$ENC_PREF${gen + 1}"

    /** 世代ごとのマスターキー alias。世代 0 は従来の既定 alias。 */
    internal fun masterKeyAlias(gen: Int): String =
        if (gen <= 0) MasterKey.DEFAULT_MASTER_KEY_ALIAS else "sipbridge_master_key${gen + 1}"

    /**
     * 作り直しても暗号化できず、平文 SharedPreferences で動いているか。
     * true なら設定画面に「この端末では暗号化できない」警告を出す。
     */
    @Volatile
    var plainFallback: Boolean = false
        private set

    /**
     * 設定の保存領域に一時的にアクセスできない (Keystore の一時障害など)。
     * この間 load は既定値 ([loadOrNull] は null)、save は何もしない。次に開けたら false に戻る。
     */
    @Volatile
    var temporarilyUnavailable: Boolean = false
        private set

    /** 開けた prefs (暗号化、または確定した平文フォールバック)。 */
    @Volatile
    private var cached: SharedPreferences? = null

    /** この時刻までは一時障害の直後として Keystore を試さない。 */
    @Volatile
    private var transientUntil = 0L

    private sealed class Store {
        /** 暗号化 (または確定した平文フォールバック)。キャッシュ済み。 */
        class Ready(val prefs: SharedPreferences) : Store()
        /** 一時障害のため今は読み書きできない。キャッシュしない。 */
        object Unavailable : Store()
        /** ユーザーのロック解除前 (資格情報で暗号化された領域が読めない)。 */
        object Locked : Store()
    }

    /**
     * 設定を読む。保存領域が使えない (ロック解除前・一時障害) ときは既定値。
     * 注意: 既定値は autostart=true などを含む。directBootAware を付けてロック解除前に
     * 起動され得るようにする場合は、呼び出し側で [loadOrNull] を使い null を区別すること。
     */
    fun load(ctx: Context): BridgeConfigData = loadOrNull(ctx) ?: BridgeConfigData()

    /**
     * 設定を読む。保存領域が使えない (ロック解除前・一時障害) ときは null。
     * [ignoreCooldown] が true なら一時障害直後の待ち時間中でも開き直しを試す
     * (FCM 起床など、数秒以内に読めないと着信を取りこぼす経路用)。
     */
    fun loadOrNull(ctx: Context, ignoreCooldown: Boolean = false): BridgeConfigData? {
        val p = (openStore(ctx, ignoreCooldown) as? Store.Ready)?.prefs ?: return null
        return synchronized(this) { readFrom(p) }.also { mirrorMode(ctx, it.mode) }
    }

    /**
     * 最後に読めた (または書いた) 接続モード。sipbridge_meta の写しから読む (#42)。
     * 設定を一時的に読めない間のモード判定用。記録が無ければ null。
     */
    fun lastKnownMode(ctx: Context): BridgeMode? =
        runCatching { meta(ctx).getString(META_MODE, null)?.let { BridgeMode.valueOf(it) } }.getOrNull()

    /** 接続モードを sipbridge_meta に写す (値が変わったときだけ書く)。 */
    private fun mirrorMode(ctx: Context, mode: BridgeMode) {
        runCatching {
            val m = meta(ctx)
            if (m.getString(META_MODE, null) != mode.name) m.edit().putString(META_MODE, mode.name).apply()
        }
    }

    /** [p] から設定を読む (deviceId が無ければ生成して保存する)。 */
    private fun readFrom(p: SharedPreferences): BridgeConfigData {
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
            telecomEnvFingerprint = p.getString("telecomEnvFingerprint", "") ?: "",
            fromStore = true,
        )
    }

    /**
     * 読込→変換→書込を 1 つのロックの中で行う (#42)。設定の一部だけ変えるときはこれを使う。
     * 保存領域が使えない (ロック解除前・一時障害) ときは何もせず false
     * (既定値を元に書いて本物の設定を上書きしないため。平文にも書かない)。
     * [transform] が同じ値を返したら書き込まずに true。
     * 別スレッドの load→copy→save と後勝ちで上書きし合うこともない。
     */
    fun update(ctx: Context, transform: (BridgeConfigData) -> BridgeConfigData): Boolean {
        val p = (openStore(ctx) as? Store.Ready)?.prefs ?: run {
            Log.w(TAG, "config storage unavailable: not updated")
            return false
        }
        synchronized(this) {
            val cur = readFrom(p)
            val next = transform(cur)
            if (next != cur) write(p, next)
            mirrorMode(ctx, next.mode)
            return true
        }
    }

    /**
     * 設定を丸ごと保存する。保存領域が使えない (ロック解除前・一時障害) とき、または [d] が
     * 保存領域から読んだ値に由来しない ([BridgeConfigData.fromStore] が false。保存領域を
     * 使えなかったときの既定値など) ときは**何も書かず** false。
     * 一部の項目だけ変えるときは [update] を使うこと。
     */
    @Deprecated(
        "load→copy→save は別スレッドの変更を上書きし得る。読込→変換→書込を原子的に行う update を使う",
        ReplaceWith("BridgeConfig.update(ctx) { it.copy(/* 変更する項目 */) }")
    )
    fun save(ctx: Context, d: BridgeConfigData): Boolean {
        if (!d.fromStore) {
            Log.w(TAG, "refuse to save config not read from storage (defaults)")
            return false
        }
        val p = (openStore(ctx) as? Store.Ready)?.prefs ?: run {
            Log.w(TAG, "config storage unavailable: not saved")
            return false
        }
        synchronized(this) { write(p, d) }
        mirrorMode(ctx, d.mode)
        return true
    }

    /** ロック保持中に呼ぶ。 */
    private fun write(p: SharedPreferences, d: BridgeConfigData) {
        // deviceId は端末固定。空で保存しようとしたら既存値を維持する。
        val keepId = d.deviceId.ifBlank { p.getString("deviceId", "") ?: "" }
        p.edit()
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

    /**
     * 暗号化ストアが壊れていたため初期化した時刻 (ms)。0 なら初期化していない。
     * 設定画面が「再設定してください」を出すのに使う。
     */
    fun configResetAt(ctx: Context): Long =
        runCatching { meta(ctx).getLong(META_RESET_AT, 0L) }.getOrDefault(0L)

    /** 再設定が済んだら初期化の通知を消す。 */
    fun clearConfigResetNotice(ctx: Context) {
        runCatching { meta(ctx).edit().remove(META_RESET_AT).apply() }
    }

    private fun meta(ctx: Context): SharedPreferences =
        (ctx.applicationContext ?: ctx).getSharedPreferences(META_PREF, Context.MODE_PRIVATE)

    private fun plain(ctx: Context): SharedPreferences =
        ctx.getSharedPreferences(PLAIN_PREF, Context.MODE_PRIVATE)

    private fun readMeta(ctx: Context): EncMetaState {
        val m = meta(ctx)
        return EncMetaState(
            gen = m.getInt(META_GEN, 0),
            corruptStreak = m.getInt(META_CORRUPT_STREAK, 0),
            corruptToken = m.getString(META_CORRUPT_TOKEN, "") ?: "",
            corruptFirstAt = m.getLong(META_CORRUPT_FIRST_AT, 0L),
            corruptAttempts = m.getInt(META_CORRUPT_ATTEMPTS, 0),
            plainFallback = m.getBoolean(META_PLAIN_FALLBACK, false),
            resetAt = m.getLong(META_RESET_AT, 0L),
        )
    }

    /** meta を同期で書く (世代の切り替えは、プロセスが落ちても失われないようにする)。 */
    @Suppress("ApplySharedPref")
    private fun writeMeta(ctx: Context, s: EncMetaState): Boolean =
        runCatching {
            val e = meta(ctx).edit()
                .putInt(META_GEN, s.gen)
                .putInt(META_CORRUPT_STREAK, s.corruptStreak)
                .putString(META_CORRUPT_TOKEN, s.corruptToken)
                .putLong(META_CORRUPT_FIRST_AT, s.corruptFirstAt)
                .putInt(META_CORRUPT_ATTEMPTS, s.corruptAttempts)
                .putBoolean(META_PLAIN_FALLBACK, s.plainFallback)
            if (s.resetAt > 0L) e.putLong(META_RESET_AT, s.resetAt) else e.remove(META_RESET_AT)
            e.commit()
        }.getOrDefault(false)

    /**
     * プロセスに 1 つの判定器 (同じプロセス内の「10 分以上」判定を elapsedRealtime で持つため)。
     * ロック保持中に使う。
     */
    private var resolverInstance: EncStoreResolver<SharedPreferences>? = null

    private fun resolver(app: Context): EncStoreResolver<SharedPreferences> =
        resolverInstance ?: newResolver(app).also { resolverInstance = it }

    private fun newResolver(app: Context) = EncStoreResolver(
        processToken = PROCESS_TOKEN,
        readMeta = { readMeta(app) },
        writeMeta = { writeMeta(app, it) },
        open = { gen -> openEncrypted(app, gen) },
        quarantine = { gen -> quarantine(app, encPrefName(gen)) },
        elapsed = { SystemClock.elapsedRealtime() },
        log = { Log.w(TAG, it) },
    )

    /**
     * 設定の保存先を決める。
     * - ロック解除前は何もせず [Store.Locked] (Direct Boot 中に誤判定して退避やキャッシュをしない)。
     * - 一時障害の直後 ([transientUntil] まで) は Keystore を試さず [Store.Unavailable]。
     * - 試行は 1 回ずつロックを取り、再試行の sleep はロックの外で行う。
     */
    private fun openStore(ctx: Context, ignoreCooldown: Boolean = false): Store {
        cached?.let { return Store.Ready(it) }
        val app = ctx.applicationContext ?: ctx
        if (!isUserUnlocked(app)) return Store.Locked
        if (!ignoreCooldown && System.currentTimeMillis() < transientUntil) return unavailable()
        val delays =
            if (Looper.myLooper() == Looper.getMainLooper()) RETRY_DELAYS_MAIN_MS else RETRY_DELAYS_BG_MS
        var sleeps = 0
        while (true) {
            val outcome = synchronized(this) {
                cached?.let { return Store.Ready(it) }
                when (val o = resolver(app).attempt()) {
                    is EncStoreResolver.Outcome.Encrypted -> return ready(app, o.prefs)
                    EncStoreResolver.Outcome.PlainFallback -> return fallbackToPlain(app)
                    else -> o
                }
            }
            if (outcome !is EncStoreResolver.Outcome.Retryable || sleeps >= delays.size) break
            runCatching { Thread.sleep(delays[sleeps]) }
            sleeps++
        }
        transientUntil = System.currentTimeMillis() + TRANSIENT_COOLDOWN_MS
        return unavailable()
    }

    /** ロック保持中に呼ぶ。 */
    private fun ready(app: Context, enc: SharedPreferences): Store {
        migratePlainToEncrypted(app, enc)
        cached = enc
        transientUntil = 0L
        temporarilyUnavailable = false
        plainFallback = false
        return Store.Ready(enc)
    }

    /** ロック保持中に呼ぶ。 */
    private fun fallbackToPlain(app: Context): Store {
        Log.e(TAG, "encrypted prefs unusable on this device; using PLAIN prefs")
        val p = plain(app)
        cached = p
        temporarilyUnavailable = false
        plainFallback = true
        return Store.Ready(p)
    }

    private fun unavailable(): Store {
        temporarilyUnavailable = true
        return Store.Unavailable
    }

    private fun openEncrypted(ctx: Context, gen: Int): SharedPreferences {
        val masterKey = MasterKey.Builder(ctx, masterKeyAlias(gen))
            .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
            .build()
        return EncryptedSharedPreferences.create(
            ctx,
            encPrefName(gen),
            masterKey,
            EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
            EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM
        ).also {
            // 鍵が合わないファイルは create では通り読み出しで落ちることがあるため、ここで 1 回読む。
            it.all
        }
    }

    /**
     * 壊れた暗号化ファイルを `<名前>.broken-<時刻>.xml` に退避する (消さない)。
     * 退避したファイルは自動では消さない (android/DESIGN.md §8)。
     */
    private fun quarantine(ctx: Context, name: String) {
        runCatching {
            val dir = File(ctx.applicationInfo.dataDir, "shared_prefs")
            val src = File(dir, "$name.xml")
            if (!src.exists()) return
            val dst = File(dir, "$name.broken-${System.currentTimeMillis()}.xml")
            if (src.renameTo(dst)) Log.w(TAG, "quarantined $name.xml -> ${dst.name}")
            else Log.w(TAG, "quarantine rename failed: $name.xml")
            // SharedPreferences の書き込み途中の退避 (.bak) も動かす。残すと次に同名で開いたとき
            // 壊れた内容が復元されうる。
            val bak = File(dir, "$name.xml.bak")
            if (bak.exists() && !bak.renameTo(File(dir, "${dst.name}.bak"))) {
                Log.w(TAG, "quarantine rename failed: $name.xml.bak")
            }
        }.onFailure { Log.w(TAG, "quarantine failed: ${PrefsFailure.describe(it)}") }
    }

    private fun isUserUnlocked(ctx: Context): Boolean =
        runCatching { ctx.getSystemService(UserManager::class.java)?.isUserUnlocked }
            .getOrNull() ?: true

    /**
     * 平文 `sipbridge` に値が残っていれば暗号化側へ移し、平文ファイルを消す。
     * 対象は旧版 (暗号化失敗時に平文へ書いていた版) の値と、平文フォールバック端末が
     * 暗号化に戻れたときの値だけ (現行版は一時障害中に平文へ書かない)。
     * 暗号化側に既にあるキーは暗号化側を優先する。
     */
    private fun migratePlainToEncrypted(ctx: Context, enc: SharedPreferences) {
        runCatching {
            val plain = plain(ctx)
            val entries = plain.all
            if (entries.isEmpty()) return
            val e = enc.edit()
            var moved = 0
            for ((k, v) in entries) {
                if (enc.contains(k)) continue
                when (v) {
                    is String -> e.putString(k, v)
                    is Boolean -> e.putBoolean(k, v)
                    is Float -> e.putFloat(k, v)
                    is Int -> e.putInt(k, v)
                    is Long -> e.putLong(k, v)
                    else -> continue
                }
                moved++
            }
            // 暗号化側への書き込みを確定させてから平文を消す。
            if (e.commit()) {
                // 直後にファイルを消すため同期で書く (apply だと消した後に空ファイルが書き戻される)。
                @Suppress("ApplySharedPref")
                plain.edit().clear().commit()
                ctx.deleteSharedPreferences(PLAIN_PREF)
                Log.i(TAG, "migrated $moved plain entries to encrypted prefs")
            }
        }.onFailure { Log.w(TAG, "plain -> encrypted migration failed: ${PrefsFailure.describe(it)}") }
    }
}

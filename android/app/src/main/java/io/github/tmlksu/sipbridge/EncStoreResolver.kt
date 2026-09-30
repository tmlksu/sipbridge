package io.github.tmlksu.sipbridge

/**
 * 暗号化設定ストアの世代・破損カウンタの記録 (`sipbridge_meta`、平文。秘密は入れない)。
 *
 * @property gen 使用中の暗号化ストアの世代 (0 = 従来の `sipbridge_enc` + 既定のマスターキー)。
 * @property corruptStreak 「壊れている」判定が別々のプロセス起動で何回続いたか。
 * @property corruptToken 最後に [corruptStreak] を数えたプロセスの識別子 (同じプロセスでは数え直さない)。
 * @property corruptFirstAt [corruptToken] のプロセスで最初に壊れ判定になった時刻 (壁時計 ms。記録用)。
 *   0 = 無し。同じプロセス内の経過時間の判定には使わない (時計の変更に影響されないよう
 *   [EncStoreResolver] がプロセス内で elapsedRealtime を持つ)。
 * @property corruptAttempts [corruptToken] のプロセスで続けて壊れ判定になった回数
 *   (途中で一時障害が挟まったら 0 に戻す。「全て CORRUPT」の判定用)。
 * @property plainFallback 作り直した新しいストアでも壊れていた → この端末では暗号化できない。
 * @property resetAt 壊れたストアを退避して初期化した時刻 (ms)。0 なら未初期化 (設定画面の通知用)。
 */
data class EncMetaState(
    val gen: Int = 0,
    val corruptStreak: Int = 0,
    val corruptToken: String = "",
    val corruptFirstAt: Long = 0L,
    val corruptAttempts: Int = 0,
    val plainFallback: Boolean = false,
    val resetAt: Long = 0L,
) {
    /** 破損カウンタが全て初期値か。 */
    val corruptClear: Boolean
        get() = corruptStreak == 0 && corruptToken.isEmpty() && corruptFirstAt == 0L && corruptAttempts == 0

    fun clearCorrupt(): EncMetaState =
        copy(corruptStreak = 0, corruptToken = "", corruptFirstAt = 0L, corruptAttempts = 0)
}

/**
 * 暗号化設定ストアを開く 1 回ぶんの判定 (#42)。Android 依存なし (JVM 単体テスト可)。
 * 再試行の間隔・キャッシュ・ロックは呼び出し側 ([BridgeConfig]) が持つ。
 *
 * - 開けた → [Outcome.Encrypted]。破損カウンタが残っていれば初期値に戻す (値が変わるときだけ書く)。
 * - 一時障害 / 不明 → [Outcome.Retryable]。何も消さない (同じプロセスの連続破損回数だけ 0 に戻す)。
 * - 壊れている → 次のどちらかを満たすまでは [Outcome.NotNow] (一時障害と同じ扱い):
 *   - 別々のプロセス起動で [CORRUPT_CONFIRM_STREAK] 回続いた
 *   - 同じプロセスで最初の破損判定から [CORRUPT_CONFIRM_ELAPSED_MS] 以上経ち、その間
 *     [CORRUPT_CONFIRM_ATTEMPTS] 回以上試して全て壊れ判定だった
 *     (PERSISTENT で何週間も再起動しない端末が、保存も接続もできないまま止まらないように)
 *   満たしたら meta を**先に**書いて (失敗したら何もしない) から壊れたファイルを退避し、
 *   新しい世代 (新しい鍵 alias・ファイル名) で作り直す。
 * - 作り直した新しい世代でも壊れている → meta に plainFallback を残して [Outcome.PlainFallback]。
 *   以後の起動では退避・世代追加をせず、現世代を 1 回試して駄目なら平文のまま。
 */
class EncStoreResolver<P : Any>(
    /** このプロセスの識別子 (起動ごとに変わる値)。 */
    private val processToken: String,
    private val readMeta: () -> EncMetaState,
    /** meta を同期で書く。成功したら true。 */
    private val writeMeta: (EncMetaState) -> Boolean,
    /** 世代 [gen] の暗号化ストアを開く (失敗したら例外)。 */
    private val open: (gen: Int) -> P,
    /** 世代 [gen] の壊れたファイルを退避する (消さない)。 */
    private val quarantine: (gen: Int) -> Unit,
    private val now: () -> Long = System::currentTimeMillis,
    /**
     * 単調増加の時計 (ms)。同じプロセス内の「最初の壊れ判定から 10 分以上」の判定に使う
     * (Android では SystemClock.elapsedRealtime。壁時計の変更に影響されない)。
     */
    private val elapsed: () -> Long = { System.nanoTime() / 1_000_000L },
    private val classify: (Throwable) -> PrefsFailure = { PrefsFailure.classify(it) },
    private val log: (String) -> Unit = {},
) {
    sealed class Outcome<out P> {
        data class Encrypted<P>(val prefs: P) : Outcome<P>()
        /** 一時障害 (または不明)。間隔を空けて再試行してよい。 */
        data class Retryable(val kind: PrefsFailure) : Outcome<Nothing>()
        /** 今は開けない (壊れ判定の確定待ちなど)。すぐに再試行しても変わらない。 */
        object NotNow : Outcome<Nothing>()
        /** この端末では暗号化できない。平文で動く。 */
        object PlainFallback : Outcome<Nothing>()
    }

    companion object {
        /** 壊れ判定を確定させるのに必要な、別々のプロセス起動での連続回数。 */
        const val CORRUPT_CONFIRM_STREAK = 2
        /** 同じプロセス内で確定させる場合の、最初の破損判定からの経過時間。 */
        const val CORRUPT_CONFIRM_ELAPSED_MS = 10 * 60 * 1000L
        /** 同じプロセス内で確定させる場合の、続けて壊れ判定になった試行回数。 */
        const val CORRUPT_CONFIRM_ATTEMPTS = 3
    }

    /**
     * このプロセスで続けて壊れ判定になった最初の時点 ([elapsed] の値)。-1 = 無し。
     * 同じインスタンスをプロセス内で使い回す前提 (BridgeConfig がそうしている)。
     */
    private var firstCorruptElapsed = -1L

    fun attempt(): Outcome<P> {
        val m = readMeta()
        if (m.plainFallback) {
            // 既に「暗号化できない」と確定した端末。退避・世代追加はせず、現世代を 1 回だけ試す
            // (OS 更新などで直っていれば暗号化に戻る)。
            // 前提: 現世代のファイルは、確定時に作り直した直後の世代で、作り直しも開けなかったため
            // 中身は空 (または存在しない)。平文で動いていた間の値は呼び出し側
            // (BridgeConfig.migratePlainToEncrypted) が「暗号化側に無いキーだけ」移すので、
            // 空のストアには平文の値がそのまま全部移る。
            return runCatching { open(m.gen) }.fold(
                onSuccess = {
                    writeMeta(m.copy(plainFallback = false))
                    log("encrypted prefs available again (gen=${m.gen}); leave plain fallback")
                    Outcome.Encrypted(it)
                },
                onFailure = { Outcome.PlainFallback }
            )
        }

        val err = try {
            val p = open(m.gen)
            firstCorruptElapsed = -1L
            if (!m.corruptClear) writeMeta(m.clearCorrupt())
            return Outcome.Encrypted(p)
        } catch (t: Throwable) {
            t
        }
        val kind = classify(err)
        log("open encrypted prefs failed (gen=${m.gen} kind=$kind): ${PrefsFailure.describe(err)}")
        val t = now()
        if (kind != PrefsFailure.CORRUPT) {
            // 「全て壊れ判定」の連続が途切れた。起動をまたぐ回数 (streak) はそのまま。
            firstCorruptElapsed = -1L
            if (m.corruptAttempts != 0 || m.corruptFirstAt != 0L) {
                writeMeta(m.copy(corruptAttempts = 0, corruptFirstAt = 0L))
            }
            return Outcome.Retryable(kind)
        }

        // 壊れている判定。起動をまたぐ回数は同じプロセス内では 1 回しか数えない。
        val sameProcess = m.corruptToken == processToken
        val streak = if (sameProcess) m.corruptStreak else m.corruptStreak + 1
        val attempts = if (sameProcess) m.corruptAttempts + 1 else 1
        val continuing = sameProcess && m.corruptAttempts > 0
        val firstAt = if (continuing && m.corruptFirstAt > 0L) m.corruptFirstAt else t
        val el = elapsed()
        if (!continuing || firstCorruptElapsed < 0L) firstCorruptElapsed = el
        val counted = m.copy(
            corruptStreak = streak, corruptToken = processToken,
            corruptFirstAt = firstAt, corruptAttempts = attempts,
        )
        if (counted != m) writeMeta(counted)
        val confirmedAcrossLaunches = streak >= CORRUPT_CONFIRM_STREAK
        val confirmedInProcess =
            attempts >= CORRUPT_CONFIRM_ATTEMPTS && el - firstCorruptElapsed >= CORRUPT_CONFIRM_ELAPSED_MS
        if (!confirmedAcrossLaunches && !confirmedInProcess) {
            log("corrupt suspected (streak=$streak attempts=$attempts); keep data and retry later")
            return Outcome.NotNow
        }

        // 確定: meta を先に書き、書けたときだけ退避して新しい世代で作り直す。
        val next = m.gen + 1
        val nm = counted.clearCorrupt().copy(gen = next, resetAt = t)
        if (!writeMeta(nm)) {
            log("meta commit failed; skip quarantine")
            return Outcome.NotNow
        }
        firstCorruptElapsed = -1L
        quarantine(m.gen)
        log("encrypted prefs corrupt (gen=${m.gen}); recreate as gen=$next")
        val err2 = try {
            return Outcome.Encrypted(open(next))
        } catch (e: Throwable) {
            e
        }
        val kind2 = classify(err2)
        log("open recreated prefs failed (gen=$next kind=$kind2): ${PrefsFailure.describe(err2)}")
        if (kind2 != PrefsFailure.CORRUPT) return Outcome.NotNow
        // 作り直した新しいストアでも壊れている → この端末では暗号化できない。以後は世代を増やさない。
        writeMeta(nm.copy(plainFallback = true))
        return Outcome.PlainFallback
    }
}

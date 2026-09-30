package io.github.tmlksu.sipbridge

import java.security.KeyStoreException
import javax.crypto.AEADBadTagException
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** #42: 暗号化設定ストアを開くときの状態遷移 (退避・世代・平文フォールバック)。 */
class EncStoreResolverTest {

    /** sipbridge_meta と暗号化ストアの偽物。プロセスをまたいで共有する。 */
    private class Device {
        var meta = EncMetaState()
        var metaWrites = 0
        var metaWritable = true
        /** 世代ごとの「開いたときの結果」。null なら開ける。 */
        val failures = mutableMapOf<Int, () -> Throwable>()
        val quarantined = mutableListOf<Int>()
        /** 退避した時点の meta (meta を先に書いたかの確認用)。 */
        val metaAtQuarantine = mutableListOf<EncMetaState>()
        /** 壁時計 (記録用) と単調時計 (同じプロセス内の経過判定用)。 */
        var clock = 1_000L
        var elapsedClock = 50_000L
        fun advance(ms: Long) { clock += ms; elapsedClock += ms }

        fun resolver(processToken: String) = EncStoreResolver(
            processToken = processToken,
            readMeta = { meta },
            writeMeta = {
                if (metaWritable) {
                    meta = it; metaWrites++; true
                } else {
                    false
                }
            },
            open = { gen -> failures[gen]?.let { throw it() } ?: "enc$gen" },
            quarantine = { gen -> quarantined += gen; metaAtQuarantine += meta },
            now = { clock },
            elapsed = { elapsedClock },
            classify = { PrefsFailure.classify(it) { null } },
        )
    }

    private val corrupt: () -> Throwable = { AEADBadTagException("tag mismatch") }
    private val transient: () -> Throwable = { KeyStoreException("System error") }

    @Test
    fun `healthy store opens without touching meta`() {
        val d = Device()
        assertEquals(EncStoreResolver.Outcome.Encrypted("enc0"), d.resolver("p1").attempt())
        assertEquals(0, d.metaWrites)
    }

    @Test
    fun `transient failure never quarantines or touches meta`() {
        val d = Device()
        d.failures[0] = transient
        repeat(3) { i ->
            val o = d.resolver("p$i").attempt()
            assertEquals(EncStoreResolver.Outcome.Retryable(PrefsFailure.TRANSIENT), o)
        }
        assertTrue(d.quarantined.isEmpty())
        assertEquals(0, d.metaWrites)
        assertEquals(0, d.meta.gen)
    }

    @Test
    fun `corrupt within the same process in a short time is never quarantined`() {
        val d = Device()
        d.failures[0] = corrupt
        val r = d.resolver("p1")
        repeat(5) {
            assertEquals(EncStoreResolver.Outcome.NotNow, r.attempt())
            d.advance(10_000L)   // 10 秒おき (合計 50 秒 < 10 分)
        }
        assertEquals(1, d.meta.corruptStreak)   // 起動をまたぐ回数は同じプロセスでは 1 回だけ
        assertEquals(5, d.meta.corruptAttempts)
        assertEquals(1_000L, d.meta.corruptFirstAt)
        assertTrue(d.quarantined.isEmpty())
        assertEquals(0, d.meta.gen)
    }

    @Test
    fun `corrupt in one long-running process for 10 minutes and 3 attempts is confirmed`() {
        val d = Device()
        d.failures[0] = corrupt
        val r = d.resolver("persistent")
        assertEquals(EncStoreResolver.Outcome.NotNow, r.attempt())          // t=0, 1 回目
        d.advance(5 * 60_000L)
        assertEquals(EncStoreResolver.Outcome.NotNow, r.attempt())          // t=5 分, 2 回目
        d.advance(5 * 60_000L)
        // t=10 分・3 回目: 10 分以上かつ 3 回以上、全て壊れ判定 → 確定して作り直す
        assertEquals(EncStoreResolver.Outcome.Encrypted("enc1"), r.attempt())
        assertEquals(listOf(0), d.quarantined)
        assertEquals(1, d.meta.gen)
        assertTrue(d.meta.corruptClear)
    }

    @Test
    fun `wall clock jump does not confirm in-process corruption`() {
        // 壁時計が 1 日進んでも (時刻合わせなど)、単調時計で 10 分経っていなければ確定しない
        val d = Device()
        d.failures[0] = corrupt
        val r = d.resolver("p")
        repeat(3) {
            assertEquals(EncStoreResolver.Outcome.NotNow, r.attempt())
            d.clock += 24 * 60 * 60_000L
            d.elapsedClock += 10_000L
        }
        assertTrue(d.quarantined.isEmpty())
        assertEquals(3, d.meta.corruptAttempts)
    }

    @Test
    fun `elapsed time alone or attempts alone does not confirm`() {
        // 10 分経っても 2 回しか試していない
        val d = Device()
        d.failures[0] = corrupt
        val r = d.resolver("p")
        r.attempt()
        d.advance(20 * 60_000L)
        assertEquals(EncStoreResolver.Outcome.NotNow, r.attempt())
        assertTrue(d.quarantined.isEmpty())
        // 3 回試したが 10 分経っていない
        val d2 = Device()
        d2.failures[0] = corrupt
        val r2 = d2.resolver("p")
        repeat(3) { r2.attempt(); d2.advance(60_000L) }
        assertTrue(d2.quarantined.isEmpty())
    }

    @Test
    fun `transient failure in between restarts the in-process corrupt run`() {
        val d = Device()
        d.failures[0] = corrupt
        val r = d.resolver("p")
        r.attempt()
        d.advance(5 * 60_000L)
        r.attempt()
        d.failures[0] = transient
        d.advance(1_000L)
        assertEquals(EncStoreResolver.Outcome.Retryable(PrefsFailure.TRANSIENT), r.attempt())
        assertEquals(0, d.meta.corruptAttempts)
        assertEquals(1, d.meta.corruptStreak)   // 起動をまたぐ回数は残す
        d.failures[0] = corrupt
        d.advance(5 * 60_000L)
        // 最初の壊れ判定から 10 分以上だが、一時障害で連続が途切れたので数え直し (1 回目)
        assertEquals(EncStoreResolver.Outcome.NotNow, r.attempt())
        assertEquals(1, d.meta.corruptAttempts)
        assertTrue(d.quarantined.isEmpty())
    }

    @Test
    fun `corrupt on two separate launches quarantines and recreates`() {
        val d = Device()
        d.failures[0] = corrupt
        assertEquals(EncStoreResolver.Outcome.NotNow, d.resolver("launch1").attempt())
        val o = d.resolver("launch2").attempt()
        assertEquals(EncStoreResolver.Outcome.Encrypted("enc1"), o)
        assertEquals(listOf(0), d.quarantined)
        assertEquals(1, d.meta.gen)
        assertEquals(0, d.meta.corruptStreak)
        assertEquals(1_000L, d.meta.resetAt)
        assertFalse(d.meta.plainFallback)
        // meta (新しい世代) を先に書いてから退避している
        assertEquals(1, d.metaAtQuarantine.single().gen)
    }

    @Test
    fun `success between corrupt launches resets the streak`() {
        val d = Device()
        d.failures[0] = corrupt
        d.resolver("launch1").attempt()
        assertEquals(1, d.meta.corruptStreak)
        d.failures.remove(0)
        assertEquals(EncStoreResolver.Outcome.Encrypted("enc0"), d.resolver("launch2").attempt())
        assertEquals(0, d.meta.corruptStreak)
        val writes = d.metaWrites
        d.resolver("launch3").attempt()
        assertEquals(writes, d.metaWrites)   // 既に 0 なら書かない
        d.failures[0] = corrupt
        assertEquals(EncStoreResolver.Outcome.NotNow, d.resolver("launch4").attempt())
        assertTrue(d.quarantined.isEmpty())
    }

    @Test
    fun `quarantine is skipped when meta cannot be committed`() {
        val d = Device()
        d.failures[0] = corrupt
        d.resolver("launch1").attempt()
        d.metaWritable = false
        assertEquals(EncStoreResolver.Outcome.NotNow, d.resolver("launch2").attempt())
        assertTrue(d.quarantined.isEmpty())
        assertEquals(0, d.meta.gen)
    }

    @Test
    fun `recreated store also corrupt falls back to plain and stops adding generations`() {
        val d = Device()
        d.failures[0] = corrupt
        d.failures[1] = corrupt
        d.resolver("launch1").attempt()
        assertEquals(EncStoreResolver.Outcome.PlainFallback, d.resolver("launch2").attempt())
        assertTrue(d.meta.plainFallback)
        assertEquals(1, d.meta.gen)
        // 以後の起動では退避も世代追加もしない
        repeat(3) { i ->
            assertEquals(EncStoreResolver.Outcome.PlainFallback, d.resolver("later$i").attempt())
        }
        assertEquals(listOf(0), d.quarantined)
        assertEquals(1, d.meta.gen)
    }

    @Test
    fun `plain fallback device returns to encryption when the store opens again`() {
        val d = Device()
        d.meta = EncMetaState(gen = 1, plainFallback = true)
        assertEquals(EncStoreResolver.Outcome.Encrypted("enc1"), d.resolver("p").attempt())
        assertFalse(d.meta.plainFallback)
        assertEquals(1, d.meta.gen)
    }

    @Test
    fun `transient failure of the recreated store is not treated as plain fallback`() {
        val d = Device()
        d.failures[0] = corrupt
        d.failures[1] = transient
        d.resolver("launch1").attempt()
        assertEquals(EncStoreResolver.Outcome.NotNow, d.resolver("launch2").attempt())
        assertFalse(d.meta.plainFallback)
        assertEquals(1, d.meta.gen)
        // 次の起動は新しい世代を開き直す (世代は増えない)
        d.failures.remove(1)
        assertEquals(EncStoreResolver.Outcome.Encrypted("enc1"), d.resolver("launch3").attempt())
        assertEquals(1, d.meta.gen)
    }
}

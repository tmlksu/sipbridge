package io.github.tmlksu.sipbridge

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * TelecomTierManager.decideFrom の判定表と enum 復元の JVM テスト。
 * decideFrom は端末依存部分を引数に切り出した純関数のため Robolectric 不要。
 */
class TelecomTierManagerTest {

    @Test
    fun `pref APP forces legacy`() {
        assertEquals(
            CallTier.LEGACY,
            TelecomTierManager.decideFrom(
                pref = TelecomTierManager.Pref.APP,
                hasTelecom = true,
                maxTier = CallTier.MANAGED,
                managedOk = true,
                selfOk = true,
            )
        )
    }

    @Test
    fun `no telecom forces legacy`() {
        assertEquals(
            CallTier.LEGACY,
            TelecomTierManager.decideFrom(
                pref = TelecomTierManager.Pref.AUTO,
                hasTelecom = false,
                maxTier = CallTier.MANAGED,
                managedOk = true,
                selfOk = true,
            )
        )
    }

    @Test
    fun `learned max tier caps managed`() {
        // 学習で SELF_MANAGED に落ちた端末では managed が使えても上がらない。
        assertEquals(
            CallTier.SELF_MANAGED,
            TelecomTierManager.decideFrom(
                pref = TelecomTierManager.Pref.AUTO,
                hasTelecom = true,
                maxTier = CallTier.SELF_MANAGED,
                managedOk = true,
                selfOk = true,
            )
        )
    }

    @Test
    fun `learned legacy forces legacy`() {
        assertEquals(
            CallTier.LEGACY,
            TelecomTierManager.decideFrom(
                pref = TelecomTierManager.Pref.AUTO,
                hasTelecom = true,
                maxTier = CallTier.LEGACY,
                managedOk = true,
                selfOk = true,
            )
        )
    }

    @Test
    fun `managed wins when usable`() {
        assertEquals(
            CallTier.MANAGED,
            TelecomTierManager.decideFrom(
                pref = TelecomTierManager.Pref.SYSTEM,
                hasTelecom = true,
                maxTier = CallTier.MANAGED,
                managedOk = true,
                selfOk = true,
            )
        )
    }

    @Test
    fun `system ignores learned cap and picks managed`() {
        // ユーザーが明示的に OS 標準を選んだ以上、学習より優先する。
        assertEquals(
            CallTier.MANAGED,
            TelecomTierManager.decideFrom(
                pref = TelecomTierManager.Pref.SYSTEM,
                hasTelecom = true,
                maxTier = CallTier.LEGACY,
                managedOk = true,
                selfOk = false,
            )
        )
    }

    @Test
    fun `system without managed falls back to self managed`() {
        assertEquals(
            CallTier.SELF_MANAGED,
            TelecomTierManager.decideFrom(
                pref = TelecomTierManager.Pref.SYSTEM,
                hasTelecom = true,
                maxTier = CallTier.LEGACY,
                managedOk = false,
                selfOk = true,
            )
        )
    }

    @Test
    fun `system without anything falls to legacy`() {
        assertEquals(
            CallTier.LEGACY,
            TelecomTierManager.decideFrom(
                pref = TelecomTierManager.Pref.SYSTEM,
                hasTelecom = true,
                maxTier = CallTier.LEGACY,
                managedOk = false,
                selfOk = false,
            )
        )
    }

    @Test
    fun `system without telecom forces legacy`() {
        assertEquals(
            CallTier.LEGACY,
            TelecomTierManager.decideFrom(
                pref = TelecomTierManager.Pref.SYSTEM,
                hasTelecom = false,
                maxTier = CallTier.MANAGED,
                managedOk = true,
                selfOk = true,
            )
        )
    }

    @Test
    fun `self only falls to self managed`() {
        assertEquals(
            CallTier.SELF_MANAGED,
            TelecomTierManager.decideFrom(
                pref = TelecomTierManager.Pref.AUTO,
                hasTelecom = true,
                maxTier = CallTier.MANAGED,
                managedOk = false,
                selfOk = true,
            )
        )
    }

    @Test
    fun `nothing usable falls to legacy`() {
        assertEquals(
            CallTier.LEGACY,
            TelecomTierManager.decideFrom(
                pref = TelecomTierManager.Pref.AUTO,
                hasTelecom = true,
                maxTier = CallTier.MANAGED,
                managedOk = false,
                selfOk = false,
            )
        )
    }

    @Test
    fun `unknown pref name falls back to AUTO`() {
        assertEquals(
            TelecomTierManager.Pref.AUTO,
            TelecomTierManager.prefFromName("SOMETHING_NEW")
        )
        assertEquals(
            TelecomTierManager.Pref.AUTO,
            TelecomTierManager.prefFromName(null)
        )
    }

    @Test
    fun `known pref names round trip`() {
        for (p in TelecomTierManager.Pref.entries) {
            assertEquals(p, TelecomTierManager.prefFromName(p.name))
        }
    }

    @Test
    fun `unknown tier name falls back to MANAGED`() {
        assertEquals(
            CallTier.MANAGED,
            TelecomTierManager.tierFromName("SOMETHING_NEW")
        )
        assertEquals(
            CallTier.MANAGED,
            TelecomTierManager.tierFromName(null)
        )
    }

    @Test
    fun `known tier names round trip`() {
        for (t in CallTier.entries) {
            assertEquals(t, TelecomTierManager.tierFromName(t.name))
        }
    }
}

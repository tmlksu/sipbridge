package io.github.tmlksu.sipbridge

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PowerLockPolicy の JVM テスト (issue #19)。
 * PUSH モード待機中にロックを持ち続けないこと、PERSISTENT は現状維持であることを確かめる。
 */
class PowerLockPolicyTest {

    @Test
    fun `PERSISTENT は状態に関わらず常に保持する`() {
        for (callIdle in listOf(true, false)) {
            for (wanted in listOf(true, false)) {
                assertTrue(
                    "callIdle=$callIdle wanted=$wanted",
                    PowerLockPolicy.shouldHold(BridgeMode.PERSISTENT, callIdle, wanted)
                )
            }
        }
    }

    @Test
    fun `PUSH の待機中 (未接続・通話なし) は解放する`() {
        assertFalse(PowerLockPolicy.shouldHold(BridgeMode.PUSH, callIdle = true, connectionWanted = false))
    }

    @Test
    fun `PUSH の接続中 (FCM 起床・発信・register_push) は保持する`() {
        assertTrue(PowerLockPolicy.shouldHold(BridgeMode.PUSH, callIdle = true, connectionWanted = true))
    }

    @Test
    fun `PUSH の通話中は接続要求の有無に関わらず保持する`() {
        assertTrue(PowerLockPolicy.shouldHold(BridgeMode.PUSH, callIdle = false, connectionWanted = true))
        assertTrue(PowerLockPolicy.shouldHold(BridgeMode.PUSH, callIdle = false, connectionWanted = false))
    }
}

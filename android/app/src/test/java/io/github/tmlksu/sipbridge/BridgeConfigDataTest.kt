package io.github.tmlksu.sipbridge

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** #42: 保存領域を使えなかったときの既定値が save で書かれないための印 (fromStore)。 */
class BridgeConfigDataTest {

    @Test
    fun `defaults are not from store and copy keeps the flag`() {
        val defaults = BridgeConfigData()
        assertFalse(defaults.fromStore)
        // 既定値を元に一部だけ変えた値も「保存領域由来ではない」まま (save が拒否する)
        assertFalse(defaults.copy(relayUrl = "wss://relay.example.com").fromStore)
        // 保存領域から読んだ値 (新規セットアップの空設定を含む) は copy しても true のまま
        val stored = BridgeConfigData(fromStore = true)
        assertTrue(stored.copy(sipUser = "2104").fromStore)
    }
}

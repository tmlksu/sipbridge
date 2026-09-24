package io.github.tmlksu.sipbridge

/**
 * wake lock / Wi-Fi ロックを保持するかの判定 (issue #19)。Android API に依存しない純ロジックで、
 * JVM テストで検証する。実際の acquire/release は [BridgeService.updatePowerLocks] が行う。
 *
 * - PERSISTENT: 常に保持する (Echo Show の常時接続。issue #7 の対策で wake lock を取り直し続ける)。
 * - PUSH: 接続を張ろうとしている/張っている間 (オンデマンド接続から idle 切断まで) と、
 *   通話中だけ保持する。待機中は解放し、端末が deep sleep できるようにする
 *   (着信は FCM の high-priority メッセージが端末を起こす)。
 */
object PowerLockPolicy {

    /**
     * ロックを保持すべきか。
     * [callIdle] は `CallHub.state == IDLE`、[connectionWanted] は relay への接続を
     * 要求している (ensureConnected / connectFresh から PUSH idle 切断まで) か。
     */
    fun shouldHold(mode: BridgeMode, callIdle: Boolean, connectionWanted: Boolean): Boolean =
        when (mode) {
            BridgeMode.PERSISTENT -> true
            BridgeMode.PUSH -> connectionWanted || !callIdle
        }
}

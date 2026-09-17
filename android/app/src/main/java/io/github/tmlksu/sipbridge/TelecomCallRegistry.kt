package io.github.tmlksu.sipbridge

import android.util.Log

/**
 * 生成された [SipConnection] を 1 個だけ保持する軽い singleton
 * (同時通話は relay が 1 本しか扱わないため 1 個で足りる)。
 *
 * `connection == null` (= ティア C の呼) のときは全部 no-op。
 * `BridgeService` の `CallHub` 更新点から必ず呼ぶ。
 */
object TelecomCallRegistry {
    private const val TAG = "TelecomCallRegistry"

    @Volatile var connection: SipConnection? = null

    /**
     * `addNewIncomingCall` / `placeCall` の結果待ちのコールバック。
     * `BridgeService` がウォッチドッグと一緒に登録し、
     * `SipConnectionService` が `onCreate...Connection` / `...Failed` で呼ぶ。
     */
    @Volatile var onCreated: (() -> Unit)? = null
    @Volatile var onCreateFailed: (() -> Unit)? = null

    fun setRinging() {
        runCatching { connection?.setRinging() }
            .onFailure { Log.w(TAG, "setRinging failed", it) }
    }

    fun setDialing() {
        runCatching { connection?.setDialing() }
            .onFailure { Log.w(TAG, "setDialing failed", it) }
    }

    fun setActive() {
        runCatching { connection?.setActive() }
            .onFailure { Log.w(TAG, "setActive failed", it) }
    }

    fun setDisconnected(cause: Int) {
        val c = connection ?: return
        runCatching { c.setDisconnected(android.telecom.DisconnectCause(cause)) }
            .onFailure { Log.w(TAG, "setDisconnected failed", it) }
    }

    /**
     * `setDisconnected` の後は必ず `destroy()`。忘れると OS の通話画面が残り続ける。
     * 保留中の生成コールバックも捨てる。
     */
    fun clear() {
        onCreated = null
        onCreateFailed = null
        val c = connection
        connection = null
        if (c == null) return
        runCatching { c.destroy() }.onFailure { Log.w(TAG, "destroy failed", it) }
    }
}

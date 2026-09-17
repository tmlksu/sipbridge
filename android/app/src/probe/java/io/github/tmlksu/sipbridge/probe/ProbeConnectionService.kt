package io.github.tmlksu.sipbridge.probe

import android.os.Build
import android.telecom.Connection
import android.telecom.ConnectionRequest
import android.telecom.ConnectionService
import android.telecom.DisconnectCause
import android.telecom.PhoneAccountHandle
import android.util.Log

/**
 * Telecom 統合の実機検証用 ConnectionService (debug ビルド専用)。
 *
 * 音声は一切扱わない。Telecom から何がどのタイミングで呼ばれるか、
 * 発信番号がどう書き換わるかを logcat に出すだけの観測器。
 * 本実装 (`SipConnectionService`) はここで得た結果をもとに書く。
 */
class ProbeConnectionService : ConnectionService() {

    companion object {
        const val TAG = TelecomProbe.TAG
    }

    override fun onCreateIncomingConnection(
        handle: PhoneAccountHandle?, request: ConnectionRequest?
    ): Connection {
        val ms = TelecomProbe.markIncomingConnected()
        TelecomProbe.i("RESULT incoming.onCreateIncomingConnection OK elapsedMs=$ms " +
            "accountId=${handle?.id} address=${request?.address} " +
            "extras=${TelecomProbe.dumpExtras(request)}")
        return newConnection(handle, request, incoming = true)
    }

    override fun onCreateIncomingConnectionFailed(
        handle: PhoneAccountHandle?, request: ConnectionRequest?
    ) {
        val ms = TelecomProbe.markIncomingConnected()
        TelecomProbe.w("RESULT incoming.onCreateIncomingConnectionFailed elapsedMs=$ms " +
            "accountId=${handle?.id} address=${request?.address}")
    }

    override fun onCreateOutgoingConnection(
        handle: PhoneAccountHandle?, request: ConnectionRequest?
    ): Connection {
        val ms = TelecomProbe.markOutgoingConnected()
        // ★本命: ダイヤラー/Telecom が番号をどう正規化したかを見る
        TelecomProbe.i("RESULT outgoing.onCreateOutgoingConnection OK elapsedMs=$ms " +
            "accountId=${handle?.id} address=${request?.address} " +
            "requestedAddress=${TelecomProbe.lastRequestedTo} " +
            "extras=${TelecomProbe.dumpExtras(request)}")
        return newConnection(handle, request, incoming = false)
    }

    override fun onCreateOutgoingConnectionFailed(
        handle: PhoneAccountHandle?, request: ConnectionRequest?
    ) {
        val ms = TelecomProbe.markOutgoingConnected()
        TelecomProbe.w("RESULT outgoing.onCreateOutgoingConnectionFailed elapsedMs=$ms " +
            "accountId=${handle?.id} address=${request?.address}")
    }

    private fun newConnection(
        handle: PhoneAccountHandle?, request: ConnectionRequest?, incoming: Boolean
    ): Connection {
        val selfManaged = handle?.id == TelecomProbe.ID_SELF
        val c = object : Connection() {
            override fun onAnswer() { TelecomProbe.i("EVENT onAnswer"); setActive() }
            override fun onAnswer(videoState: Int) { TelecomProbe.i("EVENT onAnswer(v=$videoState)"); setActive() }
            override fun onReject() { TelecomProbe.i("EVENT onReject"); disconnect(DisconnectCause.REJECTED) }
            override fun onDisconnect() { TelecomProbe.i("EVENT onDisconnect"); disconnect(DisconnectCause.LOCAL) }
            override fun onAbort() { TelecomProbe.i("EVENT onAbort"); disconnect(DisconnectCause.CANCELED) }
            override fun onHold() { TelecomProbe.i("EVENT onHold"); setOnHold() }
            override fun onUnhold() { TelecomProbe.i("EVENT onUnhold"); setActive() }
            override fun onPlayDtmfTone(c: Char) { TelecomProbe.i("EVENT onPlayDtmfTone $c") }
            override fun onStopDtmfTone() { TelecomProbe.i("EVENT onStopDtmfTone") }
            override fun onShowIncomingCallUi() { TelecomProbe.i("EVENT onShowIncomingCallUi (self-managed)") }
            override fun onCallAudioStateChanged(state: android.telecom.CallAudioState?) {
                TelecomProbe.i("EVENT onCallAudioStateChanged $state")
            }
            override fun onStateChanged(state: Int) {
                TelecomProbe.i("EVENT onStateChanged ${stateToString(state)}")
            }

            private fun disconnect(cause: Int) {
                setDisconnected(DisconnectCause(cause))
                destroy()
                TelecomProbe.onConnectionGone()
            }
        }
        c.setAddress(request?.address, android.telecom.TelecomManager.PRESENTATION_ALLOWED)
        c.setCallerDisplayName(
            TelecomProbe.lastDisplayName, android.telecom.TelecomManager.PRESENTATION_ALLOWED
        )
        c.audioModeIsVoip = true
        var props = 0
        if (selfManaged) props = props or Connection.PROPERTY_SELF_MANAGED
        if (props != 0) c.connectionProperties = props
        c.connectionCapabilities = Connection.CAPABILITY_MUTE or
            Connection.CAPABILITY_SUPPORT_HOLD or Connection.CAPABILITY_HOLD
        if (incoming) c.setRinging() else c.setDialing()
        TelecomProbe.currentConnection = c
        TelecomProbe.i("STATE connection created selfManaged=$selfManaged sdk=${Build.VERSION.SDK_INT}")
        return c
    }
}

package io.github.tmlksu.sipbridge

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.telecom.CallAudioState
import android.telecom.Connection
import android.telecom.ConnectionRequest
import android.telecom.ConnectionService
import android.telecom.DisconnectCause
import android.telecom.PhoneAccountHandle
import android.telecom.TelecomManager
import android.util.Log

/**
 * Telecom 統合の `ConnectionService`。`SipConnection` は**薄い殻**で、状態は持たず、
 * 操作を `BridgeService` に転送し、表示を `CallHub` から受ける。
 *
 * `tel:` の intent-filter は持たない。`CAPABILITY_HOLD` は宣言しない
 * (relay に保留が無い。宣言すると保留できるふりになる)。
 */
class SipConnectionService : ConnectionService() {

    companion object {
        private const val TAG = "SipConnectionService"
    }

    override fun onCreateIncomingConnection(
        handle: PhoneAccountHandle?, request: ConnectionRequest?
    ): Connection {
        Log.i(TAG, "onCreateIncomingConnection accountId=${handle?.id}")
        val c = newConnection(handle, request)
        // ウォッチドッグ発火後に遅れて届いた呼 (= もう誰も待っていない) は登録せず、
        // その場で切る。OS の着信画面と自前 CallActivity の二重表示を防ぐ。
        if (TelecomCallRegistry.onCreated == null) {
            Log.i(TAG, "stray incoming connection, disconnect immediately")
            runCatching { c.setDisconnected(DisconnectCause(DisconnectCause.CANCELED)) }
            runCatching { c.destroy() }
            return c
        }
        runCatching { TelecomCallRegistry.onCreated?.invoke() }
        TelecomCallRegistry.onCreated = null
        TelecomCallRegistry.onCreateFailed = null
        TelecomCallRegistry.connection = c
        c.setRinging()
        return c
    }

    override fun onCreateIncomingConnectionFailed(
        handle: PhoneAccountHandle?, request: ConnectionRequest?
    ) {
        Log.w(TAG, "onCreateIncomingConnectionFailed accountId=${handle?.id}")
        runCatching { TelecomCallRegistry.onCreateFailed?.invoke() }
        TelecomCallRegistry.onCreated = null
        TelecomCallRegistry.onCreateFailed = null
    }

    override fun onCreateOutgoingConnection(
        handle: PhoneAccountHandle?, request: ConnectionRequest?
    ): Connection {
        // 番号は加工しない。tel: なら schemeSpecificPart、sip: なら @ の前を取る。
        val raw = request?.address?.toString().orEmpty()
        val to = TelecomCompat.numberFrom(raw)
        Log.i(TAG, "onCreateOutgoingConnection accountId=${handle?.id} to=$to")
        val c = newConnection(handle, request)
        // アプリ起点 (dialFromAnywhere → placeCall) かを extras で見分ける。
        // placeCall 時に EXTRA_OUTGOING_CALL_EXTRAS の下に入れた EXTRA_FROM_APP は、
        // 機種により request.extras 直下かネストのどちらかで届くため両方見る。
        // 標準ダイヤラー起点には EXTRA_FROM_APP が無く、ウォッチドッグも arm されて
        // いないので従来どおり処理を続ける (ここで弾くとティア A の目玉機能が死ぬ)。
        val reqExtras = request?.extras
        val fromApp = (reqExtras?.getBoolean(TelecomCompat.EXTRA_FROM_APP, false) == true) ||
            (reqExtras?.getBundle(TelecomManager.EXTRA_OUTGOING_CALL_EXTRAS)
                ?.getBoolean(TelecomCompat.EXTRA_FROM_APP, false) == true)
        if (fromApp && TelecomCallRegistry.onCreated == null) {
            // ウォッチドッグ発火後に遅れて届いた呼。進行中のティア C 発信を壊さないよう即切り。
            Log.i(TAG, "stray app-originated outgoing connection, disconnect immediately")
            runCatching { c.setDisconnected(DisconnectCause(DisconnectCause.CANCELED)) }
            runCatching { c.destroy() }
            return c
        }
        runCatching { TelecomCallRegistry.onCreated?.invoke() }
        TelecomCallRegistry.onCreated = null
        TelecomCallRegistry.onCreateFailed = null
        TelecomCallRegistry.connection = c
        c.setDialing()
        // 続きは BridgeService が dialViaRelay(to, showUi=false) として実行する。
        runCatching {
            val app = applicationContext
            // 標準ダイヤラー起点の発信ではこれが最初の入口になる (dialFromAnywhere を通らない)。
            // BridgeService がティアを取り違えないよう、どちらのアカウントで来たかを渡す。
            val tier = if (handle?.id == TelecomCompat.ACCOUNT_ID_SELF) CallTier.SELF_MANAGED
            else CallTier.MANAGED
            val i = Intent(app, BridgeService::class.java)
                .setAction(BridgeService.ACT_DIAL_TELECOM)
                .putExtra(BridgeService.EXTRA_TO, to)
                .putExtra(BridgeService.EXTRA_TELECOM_TIER, tier.name)
            app.startService(i)
        }.onFailure { Log.w(TAG, "ACT_DIAL_TELECOM failed", it) }
        return c
    }

    override fun onCreateOutgoingConnectionFailed(
        handle: PhoneAccountHandle?, request: ConnectionRequest?
    ) {
        Log.w(TAG, "onCreateOutgoingConnectionFailed accountId=${handle?.id}")
        runCatching { TelecomCallRegistry.onCreateFailed?.invoke() }
        TelecomCallRegistry.onCreated = null
        TelecomCallRegistry.onCreateFailed = null
    }

    private fun newConnection(
        handle: PhoneAccountHandle?, request: ConnectionRequest?
    ): SipConnection {
        val c = SipConnection(applicationContext)
        val number = TelecomCompat.numberFrom(request?.address?.toString().orEmpty())
        val display = CallHub.display.ifBlank { number }
        runCatching {
            c.setAddress(
                Uri.fromParts("tel", number, null), TelecomManager.PRESENTATION_ALLOWED
            )
            c.setCallerDisplayName(display, TelecomManager.PRESENTATION_ALLOWED)
        }.onFailure { Log.w(TAG, "setAddress/displayName failed", it) }
        c.audioModeIsVoip = true
        c.connectionCapabilities = Connection.CAPABILITY_MUTE
        if (handle?.id == TelecomCompat.ACCOUNT_ID_SELF) {
            c.connectionProperties = Connection.PROPERTY_SELF_MANAGED
        }
        // 初期状態は呼び出し側で setRinging/setDialing する。
        return c
    }
}

/** 状態を持たない薄い殻。操作は [BridgeService] へ、表示は [CallHub] から。 */
class SipConnection(private val appCtx: Context) : Connection() {

    companion object {
        private const val TAG = "SipConnection"
    }

    private fun send(action: String, extras: Bundle? = null) {
        runCatching {
            val i = Intent(appCtx, BridgeService::class.java).setAction(action)
            if (extras != null) i.putExtras(extras)
            appCtx.startService(i)
        }.onFailure { Log.w(TAG, "send $action failed", it) }
    }

    override fun onAnswer() {
        Log.i(TAG, "onAnswer")
        send(BridgeService.ACT_ANSWER)
    }

    override fun onAnswer(videoState: Int) {
        Log.i(TAG, "onAnswer videoState=$videoState")
        send(BridgeService.ACT_ANSWER)
    }

    override fun onReject() {
        Log.i(TAG, "onReject")
        send(BridgeService.ACT_REJECT)
    }

    override fun onReject(rejectReason: Int) {
        Log.i(TAG, "onReject reason=$rejectReason")
        send(BridgeService.ACT_REJECT)
    }

    override fun onDisconnect() {
        Log.i(TAG, "onDisconnect")
        send(BridgeService.ACT_HANGUP)
    }

    override fun onAbort() {
        Log.i(TAG, "onAbort")
        send(BridgeService.ACT_HANGUP)
    }

    /** in-band DTMF。relay には送らない。 */
    override fun onPlayDtmfTone(c: Char) {
        runCatching { CallHub.rtp?.sendDtmf(c) }
            .onFailure { Log.w(TAG, "sendDtmf failed", it) }
    }

    /** in-band は 120ms 固定長のため何もしない。 */
    override fun onStopDtmfTone() = Unit

    override fun onCallAudioStateChanged(state: CallAudioState) {
        // ティア A/B では音声経路は Telecom が管理するため、ミュートの反映だけにし、
        // AudioRoute には一切触らない。API 31+ の setCommunicationDevice(EARPIECE) で
        // Telecom の経路選択 (Bluetooth 等) を上書きしてしまう恐れがあるため。
        runCatching {
            CallHub.rtp?.muted = state.isMuted
        }.onFailure { Log.w(TAG, "onCallAudioStateChanged failed", it) }
    }

    /** 着信音は Telecom 側のため何もしない。 */
    override fun onSilence() = Unit

    override fun onStateChanged(state: Int) {
        Log.i(TAG, "onStateChanged state=$state")
    }

    /** self-managed のみ: 自前の着信 UI を出す。 */
    override fun onShowIncomingCallUi() {
        Log.i(TAG, "onShowIncomingCallUi")
        send(BridgeService.ACT_SHOW_INCOMING_UI)
    }
}

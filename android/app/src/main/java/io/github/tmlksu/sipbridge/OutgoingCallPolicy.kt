package io.github.tmlksu.sipbridge

/**
 * 発信の成否判定 (R5)。Android API に一切依存しない純ロジックで、JVM テストで検証する。
 * 実際の状態遷移は [BridgeService] が行い、ここでは「どうすべきか」だけを返す。
 */
object OutgoingCallPolicy {

    /**
     * 発信失敗とみなす relay の error コード (docs/PROTOCOL.md「エラーコード一覧」)。
     * どちらも `dial` への応答で、relay が呼を作らなかったことを意味する。
     * `account_failed` / `account_password_mismatch` は `sip_account` への応答で、
     * 以前の結び付けのまま `dial` 自体は成立しうるため含めない
     * (畳むと relay 側の呼だけが相手を鳴らし続ける)。結び付けが本当に無ければ
     * `dial` に `no_account` が返るので、そちらで畳まれる。
     */
    val OUTGOING_FAILURE_CODES: Set<String> = setOf("dial_failed", "no_account")

    /** dial を送って (または pendingDial に積んで) から callId が付くまでの猶予。 */
    const val OUTGOING_WATCHDOG_MS = 30_000L

    /**
     * `error` 受信時に発信中の表示を畳むか。
     * 発信中 (outgoing かつ RINGING) に発信失敗系コードを受けたら true。
     */
    fun shouldFailOutgoingOnError(code: String, outgoing: Boolean, ringing: Boolean): Boolean =
        outgoing && ringing && code in OUTGOING_FAILURE_CODES

    /**
     * `dial` をすぐ送らず pendingDial に積むか。
     * 未接続のときは従来どおり積む。接続済みでも最初の hello を処理する前
     * (sip_account 未送信) は積み、hello 処理後に送る。
     */
    fun shouldQueueDialForHello(connected: Boolean, helloProcessed: Boolean): Boolean =
        !connected || !helloProcessed

    /**
     * 発信ウォッチドッグの発火時に発信を終了させるか。
     * dial 後 [OUTGOING_WATCHDOG_MS] 経っても callId が空
     * (ringing/answered 未受信) のままなら true。
     */
    fun shouldTimeoutOutgoing(outgoing: Boolean, ringing: Boolean, hasCallId: Boolean): Boolean =
        outgoing && ringing && !hasCallId
}

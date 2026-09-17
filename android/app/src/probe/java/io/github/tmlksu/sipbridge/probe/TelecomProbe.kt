package io.github.tmlksu.sipbridge.probe

import android.content.ComponentName
import android.content.Context
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.telecom.Connection
import android.telecom.ConnectionRequest
import android.telecom.PhoneAccount
import android.telecom.PhoneAccountHandle
import android.telecom.TelecomManager
import android.util.Log

/**
 * Telecom 統合の実機検証 (debug ビルド専用)。
 *
 * 目的 (DESIGN 検討メモの「実装前に潰すべき検証」):
 *  1. `CAPABILITY_CALL_PROVIDER` の PhoneAccount を登録できるか / 有効化されるか
 *  2. 標準ダイヤラー経由の発信で `onCreateOutgoingConnection` に**どんな文字列で**届くか
 *     (Samsung 等が内線番号を正規化して壊さないか)
 *  3. `addNewIncomingCall` で標準の着信画面が出るか、何 ms で `onCreate...` が来るか
 *     (= フォールバック用ウォッチドッグの閾値決め)
 *  4. self-managed でも同じ経路が動くか (ティア B の下見)
 *
 * 結果はすべて logcat タグ [TAG] に `RESULT` / `STATE` / `EVENT` 接頭辞で出す。
 */
object TelecomProbe {

    const val TAG = "TelecomProbe"

    /** 画面 (ProbeActivity) へログを流すための出口。adb が無い端末でも結果を読めるようにする。 */
    interface Sink { fun onLog(all: String) }

    @Volatile var sink: Sink? = null
    private val lines = ArrayDeque<String>()

    fun i(msg: String) { Log.i(TAG, msg); append(msg) }
    fun w(msg: String) { Log.w(TAG, msg); append("! " + msg) }

    @Synchronized private fun append(msg: String) {
        lines.addLast(msg)
        while (lines.size > 200) lines.removeFirst()
        val all = lines.joinToString("\n")
        Handler(Looper.getMainLooper()).post { sink?.onLog(all) }
    }

    @Synchronized fun clearLog() { lines.clear(); sink?.onLog("") }

    @Synchronized fun snapshot(): String = lines.joinToString("\n")

    const val ID_MANAGED = "probe-managed"
    const val ID_SELF = "probe-self"

    /** 直近に発信要求した番号 (正規化前)。onCreateOutgoingConnection での比較用。 */
    @Volatile var lastRequestedTo: String = ""
    @Volatile var lastDisplayName: String = "プローブ"
    @Volatile var currentConnection: Connection? = null

    private val main = Handler(Looper.getMainLooper())
    private var incomingT0 = 0L
    private var outgoingT0 = 0L
    private var incomingWatchdog: Runnable? = null
    private var outgoingWatchdog: Runnable? = null

    /** ウォッチドッグ待ち時間。本実装の閾値をここで決めるため広めに取る。 */
    private const val WATCHDOG_MS = 5000L

    private fun tm(ctx: Context) =
        ctx.getSystemService(Context.TELECOM_SERVICE) as TelecomManager

    fun handle(ctx: Context, id: String) = PhoneAccountHandle(
        ComponentName(ctx.packageName, ProbeConnectionService::class.java.name), id
    )

    // ---- 1. 登録 ----

    /** @param mode "managed" (CALL_PROVIDER) または "self" (SELF_MANAGED)。 */
    fun register(ctx: Context, mode: String) {
        val id = if (mode == "self") ID_SELF else ID_MANAGED
        val h = handle(ctx, id)
        val label = if (mode == "self") "sipbridge probe (self)" else "sipbridge probe"
        val builder = PhoneAccount.builder(h, label)
            .setShortDescription("Telecom 検証用")
            .addSupportedUriScheme(PhoneAccount.SCHEME_TEL)
            .addSupportedUriScheme(PhoneAccount.SCHEME_SIP)
        if (mode == "self") {
            builder.setCapabilities(PhoneAccount.CAPABILITY_SELF_MANAGED)
        } else {
            builder.setCapabilities(PhoneAccount.CAPABILITY_CALL_PROVIDER)
            // 内線をそのまま扱うため、番号ではなくラベルだけの口として登録する
            builder.setAddress(Uri.fromParts(PhoneAccount.SCHEME_TEL, "sipbridge", null))
        }
        val r = runCatching { tm(ctx).registerPhoneAccount(builder.build()) }
        if (r.isFailure) {
            w("RESULT register.$mode FAILED ${r.exceptionOrNull()}")
            return
        }
        i("RESULT register.$mode OK handle=$h")
        status(ctx)
    }

    fun unregister(ctx: Context) {
        for (id in listOf(ID_MANAGED, ID_SELF)) {
            val r = runCatching { tm(ctx).unregisterPhoneAccount(handle(ctx, id)) }
            i("RESULT unregister.$id ${if (r.isSuccess) "OK" else "FAILED ${r.exceptionOrNull()}"}")
        }
    }

    /** 登録状態・有効化状態・発着信が許可されているかをまとめて出す。 */
    fun status(ctx: Context) {
        val t = tm(ctx)
        i("STATE sdk=${Build.VERSION.SDK_INT} manufacturer=${Build.MANUFACTURER} model=${Build.MODEL}")
        for (id in listOf(ID_MANAGED, ID_SELF)) {
            val h = handle(ctx, id)
            val acct = runCatching { t.getPhoneAccount(h) }.getOrNull()
            if (acct == null) {
                i("RESULT status.$id NOT_REGISTERED")
                continue
            }
            val inOk = runCatching { t.isIncomingCallPermitted(h) }.getOrNull()
            val outOk = runCatching { t.isOutgoingCallPermitted(h) }.getOrNull()
            i("RESULT status.$id registered=true enabled=${acct.isEnabled} " +
                "caps=0x${Integer.toHexString(acct.capabilities)} " +
                "schemes=${acct.supportedUriSchemes} " +
                "incomingPermitted=$inOk outgoingPermitted=$outOk")
        }
        val def = runCatching { t.getDefaultOutgoingPhoneAccount(PhoneAccount.SCHEME_TEL) }
            .getOrElse { "N/A (${it.javaClass.simpleName})" }
        i("RESULT status.defaultOutgoingAccount(tel)=$def")
        val all = runCatching { t.callCapablePhoneAccounts }.getOrElse { emptyList() }
        i("RESULT status.callCapableAccounts=${all.size} $all")
    }

    // ---- 3. 着信 ----

    fun incoming(ctx: Context, mode: String, from: String) {
        val id = if (mode == "self") ID_SELF else ID_MANAGED
        val h = handle(ctx, id)
        val t = tm(ctx)
        val permitted = runCatching { t.isIncomingCallPermitted(h) }.getOrNull()
        val extras = Bundle().apply {
            putParcelable(
                TelecomManager.EXTRA_INCOMING_CALL_ADDRESS,
                Uri.fromParts(PhoneAccount.SCHEME_TEL, from, null)
            )
            putString(TelecomManager.EXTRA_CALL_DISCONNECT_MESSAGE, null)
        }
        incomingT0 = System.currentTimeMillis()
        armIncomingWatchdog()
        val r = runCatching { t.addNewIncomingCall(h, extras) }
        i("RESULT incoming.addNewIncomingCall.$mode " +
            "${if (r.isSuccess) "returned" else "THREW ${r.exceptionOrNull()}"} " +
            "from=$from incomingPermitted=$permitted")
        if (r.isFailure) cancelIncomingWatchdog()
    }

    // ---- 2. 発信 ----

    fun outgoing(ctx: Context, mode: String, to: String) {
        val id = if (mode == "self") ID_SELF else ID_MANAGED
        val h = handle(ctx, id)
        val t = tm(ctx)
        lastRequestedTo = to
        val extras = Bundle().apply {
            putParcelable(TelecomManager.EXTRA_PHONE_ACCOUNT_HANDLE, h)
        }
        outgoingT0 = System.currentTimeMillis()
        armOutgoingWatchdog()
        val r = runCatching {
            t.placeCall(Uri.fromParts(PhoneAccount.SCHEME_TEL, to, null), extras)
        }
        i("RESULT outgoing.placeCall.$mode " +
            "${if (r.isSuccess) "returned" else "THREW ${r.exceptionOrNull()}"} to=$to")
        if (r.isFailure) cancelOutgoingWatchdog()
    }

    /**
     * 標準ダイヤラー経由の発信を記録するための印。
     * `am start -a android.intent.action.CALL -d tel:2104` の直前に呼んでおくと、
     * `onCreateOutgoingConnection` のログで元番号と比較できる。
     */
    fun expect(to: String) {
        lastRequestedTo = to
        outgoingT0 = System.currentTimeMillis()
        armOutgoingWatchdog()
        i("STATE expect outgoing to=$to (外部ダイヤラー経由を待つ)")
    }

    fun hangup() {
        val c = currentConnection
        if (c == null) { i("RESULT hangup NO_CONNECTION"); return }
        c.setDisconnected(android.telecom.DisconnectCause(android.telecom.DisconnectCause.LOCAL))
        c.destroy()
        onConnectionGone()
        i("RESULT hangup OK")
    }

    // ---- ウォッチドッグ (本実装のフォールバック閾値を決めるための計測) ----

    private fun armIncomingWatchdog() {
        cancelIncomingWatchdog()
        val r = Runnable {
            w("RESULT incoming.onCreateIncomingConnection TIMEOUT " +
                "(${WATCHDOG_MS}ms 以内に来なかった → この端末では沈黙失敗する)")
        }
        incomingWatchdog = r
        main.postDelayed(r, WATCHDOG_MS)
    }

    private fun cancelIncomingWatchdog() {
        incomingWatchdog?.let { main.removeCallbacks(it) }
        incomingWatchdog = null
    }

    private fun armOutgoingWatchdog() {
        cancelOutgoingWatchdog()
        val r = Runnable {
            w("RESULT outgoing.onCreateOutgoingConnection TIMEOUT (${WATCHDOG_MS}ms)")
        }
        outgoingWatchdog = r
        main.postDelayed(r, WATCHDOG_MS)
    }

    private fun cancelOutgoingWatchdog() {
        outgoingWatchdog?.let { main.removeCallbacks(it) }
        outgoingWatchdog = null
    }

    fun markIncomingConnected(): Long {
        cancelIncomingWatchdog()
        return if (incomingT0 == 0L) -1 else System.currentTimeMillis() - incomingT0
    }

    fun markOutgoingConnected(): Long {
        cancelOutgoingWatchdog()
        return if (outgoingT0 == 0L) -1 else System.currentTimeMillis() - outgoingT0
    }

    fun onConnectionGone() { currentConnection = null }

    fun dumpExtras(req: ConnectionRequest?): String {
        val b = req?.extras ?: return "{}"
        return runCatching { b.keySet().joinToString(",") { "$it=${b.get(it)}" } }
            .getOrDefault("<unreadable>")
    }
}

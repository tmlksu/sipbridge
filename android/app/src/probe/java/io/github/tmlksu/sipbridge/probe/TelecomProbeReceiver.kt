package io.github.tmlksu.sipbridge.probe

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log

/**
 * [TelecomProbe] の adb 操作口 (debug ビルド専用)。
 *
 * ```
 * adb shell am broadcast -n io.github.tmlksu.sipbridge/.probe.TelecomProbeReceiver \
 *   -a io.github.tmlksu.sipbridge.PROBE --es cmd register --es mode managed
 * ```
 * cmd: register | unregister | status | incoming | outgoing | expect | hangup
 * mode: managed (既定) | self
 * from / to: 番号 (既定 2104)
 */
class TelecomProbeReceiver : BroadcastReceiver() {

    companion object {
        const val ACTION = "io.github.tmlksu.sipbridge.PROBE"
    }

    override fun onReceive(ctx: Context, intent: Intent) {
        if (intent.action != ACTION) return
        val app = ctx.applicationContext
        val cmd = intent.getStringExtra("cmd").orEmpty()
        val mode = intent.getStringExtra("mode") ?: "managed"
        val num = intent.getStringExtra("to") ?: intent.getStringExtra("from") ?: "2104"
        TelecomProbe.i("---- CMD $cmd mode=$mode num=$num ----")
        when (cmd) {
            "register" -> TelecomProbe.register(app, mode)
            "unregister" -> TelecomProbe.unregister(app)
            "status" -> TelecomProbe.status(app)
            "incoming" -> TelecomProbe.incoming(app, mode, num)
            "outgoing" -> TelecomProbe.outgoing(app, mode, num)
            "expect" -> TelecomProbe.expect(num)
            "hangup" -> TelecomProbe.hangup()
            else -> TelecomProbe.w("RESULT unknown cmd=$cmd")
        }
    }
}

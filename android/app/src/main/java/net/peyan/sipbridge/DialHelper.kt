package net.peyan.sipbridge

import android.Manifest
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.view.HapticFeedbackConstants
import android.view.View
import android.widget.Toast
import androidx.core.content.ContextCompat
import androidx.fragment.app.Fragment
import java.lang.ref.WeakReference
import java.util.WeakHashMap

/**
 * キー押下の触覚フィードバック (UI-DESIGN §1.1/§3.1)。
 * 端末の触覚フィードバック設定が OFF なら鳴らない
 * (`FLAG_IGNORE_GLOBAL_SETTING` は使わない)。キーパッド・⌫・DTMF シートの
 * キーでのみ使い、他のボタンでは振動させない。
 */
fun View.tapFeedback() {
    performHapticFeedback(HapticFeedbackConstants.KEYBOARD_TAP)
}

/**
 * 履歴・連絡先タブからの発信の共通処理。
 * マイク権限を確認し、[BridgeService.ACT_DIAL] を投げる
 * (発信画面への遷移は BridgeService が行う)。
 */
object DialHelper {
    const val REQ_RECORD_AUDIO = 101

    private val pending = WeakHashMap<Fragment, String>()

    /** 指定番号へ発信する (権限が無ければ要求し、許可後に発信する)。 */
    fun dial(fragment: Fragment, number: String) {
        val dest = number.trim()
        if (dest.isEmpty()) return
        val ctx = fragment.requireContext()
        if (ContextCompat.checkSelfPermission(ctx, Manifest.permission.RECORD_AUDIO) ==
            PackageManager.PERMISSION_GRANTED
        ) {
            startDial(ctx, dest)
        } else {
            pending[fragment] = dest
            fragment.requestPermissions(arrayOf(Manifest.permission.RECORD_AUDIO), REQ_RECORD_AUDIO)
        }
    }

    /** [dial] で権限要求した Fragment は onRequestPermissionsResult から呼ぶこと。 */
    fun onPermissionResult(fragment: Fragment, granted: Boolean) {
        val dest = pending.remove(fragment) ?: return
        if (granted) {
            startDial(fragment.requireContext(), dest)
        } else {
            // Fragment が detach 済みの可能性に備える
            runCatching {
                val ref = WeakReference(fragment.requireContext())
                Toast.makeText(ref.get(), ref.get()?.getString(R.string.common_mic_permission), Toast.LENGTH_LONG).show()
            }
        }
    }

    private fun startDial(ctx: Context, dest: String) {
        ctx.startService(
            Intent(ctx, BridgeService::class.java)
                .setAction(BridgeService.ACT_DIAL)
                .putExtra(BridgeService.EXTRA_TO, dest)
        )
    }
}

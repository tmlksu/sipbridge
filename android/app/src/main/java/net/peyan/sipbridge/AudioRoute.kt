package net.peyan.sipbridge

import android.media.AudioDeviceInfo
import android.media.AudioManager
import android.os.Build
import android.util.Log

/**
 * 通話中の音声出力先 (スピーカー / 受話口) の切り替え。
 *
 * API 31+ では `AudioManager.isSpeakerphoneOn` が非推奨で、特に Android 13/14/15 では
 * false に戻しても受話口へ戻らない端末がある (S25 でスピーカーから戻らない事象)。
 * そのため 31+ は `setCommunicationDevice()` で明示的にデバイスを選び、
 * 取得できない端末 (受話口が無い Echo Show など) だけ旧 API にフォールバックする。
 */
object AudioRoute {
    private const val TAG = "AudioRoute"

    fun isSpeakerOn(am: AudioManager): Boolean {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            am.communicationDevice?.let { return it.type == AudioDeviceInfo.TYPE_BUILTIN_SPEAKER }
        }
        @Suppress("DEPRECATION")
        return am.isSpeakerphoneOn
    }

    /** スピーカー出力の ON/OFF。実際に適用できたかを返す。 */
    fun setSpeaker(am: AudioManager, on: Boolean): Boolean {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            val want = if (on) AudioDeviceInfo.TYPE_BUILTIN_SPEAKER
            else AudioDeviceInfo.TYPE_BUILTIN_EARPIECE
            val dev = runCatching {
                am.availableCommunicationDevices.firstOrNull { it.type == want }
            }.getOrNull()
            if (dev != null && runCatching { am.setCommunicationDevice(dev) }.getOrDefault(false)) {
                return true
            }
            // 受話口が無い端末などは選択を解除して既定経路に戻す
            if (!on) runCatching { am.clearCommunicationDevice() }
                .onFailure { Log.w(TAG, "clearCommunicationDevice 失敗: ${it.message}") }
        }
        @Suppress("DEPRECATION")
        return runCatching { am.isSpeakerphoneOn = on; true }.getOrDefault(false)
    }

    /** 通話終了時: 選択したデバイスを解除して通常モードに戻す。 */
    fun clear(am: AudioManager) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            runCatching { am.clearCommunicationDevice() }
        }
    }

    /**
     * 受話口 (内蔵イヤピース) を持つか。応答時の経路決定に使う。
     * 受話口が無い端末 (Echo Show など) で受話口へ回すと無音になるため、
     * false のときは設定に関わらずスピーカーへ出す。
     * 例外は握り潰して false を返す。判定不能な古い API では受話口あり
     * とみなして設定に従う (true)。
     */
    fun hasEarpiece(am: AudioManager): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M) return true
        return try {
            am.getDevices(AudioManager.GET_DEVICES_OUTPUTS)
                .any { it.type == AudioDeviceInfo.TYPE_BUILTIN_EARPIECE }
        } catch (e: Exception) {
            // 判定不能時は「受話口あり」とみなして設定 (speakerOnAnswer) に従う
            Log.w(TAG, "hasEarpiece 判定失敗: ${e.message}")
            true
        }
    }
}

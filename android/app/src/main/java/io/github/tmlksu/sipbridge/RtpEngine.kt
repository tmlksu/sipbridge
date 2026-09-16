package io.github.tmlksu.sipbridge

import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.AudioTrack
import android.media.MediaRecorder
import android.media.audiofx.AcousticEchoCanceler
import android.media.audiofx.NoiseSuppressor
import android.util.Log
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.atomic.AtomicBoolean

/**
 * RTP 音声エンジン (G.711 PCMU/PCMA, 8kHz mono, ptime 20ms)。
 * EchoSIP の UDP 送受信部を撤去し、WebSocket バイナリ送受に差し替えたもの。
 *
 * - 送信: マイク → G.711 → [RtpPacket.build] → [MediaSink.send] (RelayClient.sendRtp)。
 * - 受信: [onRtpReceived] (RelayClient のバイナリコールバック) → RTP 解析 →
 *   G.711 デコード → [JitterBuffer] (5 フレーム開始/上限 20) → AudioTrack。
 * - RTP ヘッダの生成・解析は [RtpPacket] に分離 (JVM テスト可)。
 */
class RtpEngine(
    @Volatile var payloadType: Int = 0, // 0=PCMU 8=PCMA
    @Volatile var micGain: Float = 2.0f
) {
    /** WS バイナリ送信口。RelayClient.sendRtp をそのまま渡す想定。 */
    fun interface MediaSink {
        fun send(rtp: ByteArray)
    }

    companion object {
        private const val TAG = "RtpEngine"
        private const val SAMPLE_RATE = 8000
        private const val FRAME_SAMPLES = 160 // 20ms
    }

    var sink: MediaSink? = null
    private val running = AtomicBoolean(false)
    @Volatile var muted = false

    private var audioTrack: AudioTrack? = null
    private var audioRecord: AudioRecord? = null
    private var aec: AcousticEchoCanceler? = null
    private var ns: NoiseSuppressor? = null
    private var seq = (0..30000).random()
    private var timestamp = (0..100000).random()
    private val ssrc = (0..Int.MAX_VALUE).random()

    private val jitter = JitterBuffer(warmupFrames = 5, maxFrames = 20)
    private val playQueue = LinkedBlockingQueue<ShortArray>()
    @Volatile private var playWarmed = false
    /** DTMF 送出キュー (20 ms PCM フレーム)。送信スレッドがマイクの代わりに送る (置換)。 */
    private val dtmfQueue = LinkedBlockingQueue<ShortArray>()

    fun start() {
        if (running.getAndSet(true)) return
        jitter.clear()
        playQueue.clear()
        dtmfQueue.clear()
        playWarmed = false

        // 再生側
        val minOut = AudioTrack.getMinBufferSize(
            SAMPLE_RATE, AudioFormat.CHANNEL_OUT_MONO, AudioFormat.ENCODING_PCM_16BIT
        ).coerceAtLeast(FRAME_SAMPLES * 4)
        audioTrack = AudioTrack.Builder()
            .setAudioAttributes(
                AudioAttributes.Builder()
                    .setUsage(AudioAttributes.USAGE_VOICE_COMMUNICATION)
                    .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH)
                    .build()
            )
            .setAudioFormat(
                AudioFormat.Builder()
                    .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                    .setSampleRate(SAMPLE_RATE)
                    .setChannelMask(AudioFormat.CHANNEL_OUT_MONO)
                    .build()
            )
            .setBufferSizeInBytes(minOut * 4)
            .setTransferMode(AudioTrack.MODE_STREAM)
            .build()
        audioTrack?.play()

        // 録音側 (VOICE_COMMUNICATIONでHW AECを狙う)
        val minIn = AudioRecord.getMinBufferSize(
            SAMPLE_RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT
        ).coerceAtLeast(FRAME_SAMPLES * 4)
        audioRecord = AudioRecord(
            MediaRecorder.AudioSource.VOICE_COMMUNICATION,
            SAMPLE_RATE, AudioFormat.CHANNEL_IN_MONO,
            AudioFormat.ENCODING_PCM_16BIT, minIn * 4
        )
        try {
            val sid = audioRecord?.audioSessionId ?: 0
            if (AcousticEchoCanceler.isAvailable()) {
                aec = AcousticEchoCanceler.create(sid)?.apply { enabled = true }
            }
            if (NoiseSuppressor.isAvailable()) {
                ns = NoiseSuppressor.create(sid)?.apply { enabled = true }
            }
        } catch (e: Exception) {
            Log.w(TAG, "AEC/NS unavailable: ${e.message}")
        }
        audioRecord?.startRecording()

        // 再生スレッド (ジッタバッファから取り出して書き込む)
        Thread(playLoop(), "rtp-play").apply { isDaemon = true; start() }
        // 送信スレッド (20ms周期でマイク→RTP→sink)
        Thread(sendLoop(), "rtp-send").apply { isDaemon = true; start() }
    }

    fun stop() {
        running.set(false)
        runCatching { Thread.sleep(60) }
        runCatching { audioRecord?.stop(); audioRecord?.release() }
        runCatching { audioTrack?.stop(); audioTrack?.release() }
        runCatching { aec?.release(); ns?.release() }
        audioRecord = null; audioTrack = null
        jitter.clear()
        playQueue.clear()
    }

    /**
     * in-band DTMF を送る (UI-DESIGN §3.1)。
     * [DtmfTone] の 120ms トーン + 80ms 無音を 20ms フレームに分けてキューに積み、
     * 送信スレッドがマイクフレームの代わりに (ミックスではなく置換で) 送る。
     * ミュート中でも送る (トーン自体が置換されるため)。未対応文字は無視する。
     */
    fun sendDtmf(digit: Char) {
        if (!running.get()) return
        val frames = DtmfTone.framesFor(digit) ?: return
        frames.forEach { dtmfQueue.offer(it) }
        // 溜まりすぎは古い方を捨てる (10 桁分 = 100 フレームを上限の目安に)
        while (dtmfQueue.size > 100) dtmfQueue.poll()
    }

    /** DTMF 送出待ちのフレーム数 (テスト用)。 */
    fun pendingDtmfFrames(): Int = dtmfQueue.size

    /**
     * WS バイナリフレーム受信口。RelayClient の Listener.onRtpReceived から呼ぶ。
     * パケット生成・解析ロジックは EchoSIP から継承 (ヘッダ 12B)。
     */
    fun onRtpReceived(packet: ByteArray) {
        if (!running.get()) return
        val parsed = RtpPacket.parse(packet) ?: return
        val n = parsed.payload.size
        if (n == 0) return
        val pcm = ShortArray(n)
        // 送信側 PT と食い違う場合は受信パケット側を優先 (relay は PT を書き換えない)
        val isPcmu = parsed.payloadType != 8
        for (i in 0 until n) {
            pcm[i] = if (isPcmu) G711.ulawToLinear(parsed.payload[i])
            else G711.alawToLinear(parsed.payload[i])
        }
        jitter.offer(pcm)
        jitter.pollReady()?.let { playQueue.offer(it) }
            ?: run {
                // ウォームアップ前は無音で埋めない (playLoop 側で無音挿入)
            }
        // 溜まりすぎは捨てる (JitterBuffer 側でも上限あり。二重の安全策)
        while (playQueue.size > 20) playQueue.poll()
    }

    private fun playLoop(): Runnable = Runnable {
        val silence = ShortArray(FRAME_SAMPLES)
        while (running.get()) {
            try {
                val frame = playQueue.poll(40, java.util.concurrent.TimeUnit.MILLISECONDS)
                if (frame != null) {
                    playWarmed = true
                    audioTrack?.write(frame, 0, frame.size)
                } else if (playWarmed) {
                    // 無音時は微小無音を書く (アンダーラン防止)
                    runCatching { audioTrack?.write(silence, 0, silence.size) }
                }
            } catch (e: Exception) {
                if (running.get()) Log.w(TAG, "play: ${e.message}")
            }
        }
    }

    private fun sendLoop(): Runnable = Runnable {
        val pcmBuf = ShortArray(FRAME_SAMPLES)
        var nextT = System.nanoTime()
        while (running.get()) {
            try {
                // DTMF 送出待ちがあればマイクの代わりに送る (置換。ミュートの影響を受けない)。
                val dtmfFrame = dtmfQueue.poll()
                val read: Int
                if (dtmfFrame != null) {
                    read = dtmfFrame.size.coerceAtMost(FRAME_SAMPLES)
                    dtmfFrame.copyInto(pcmBuf, 0, 0, read)
                    sendPcmFrame(pcmBuf, read, encodeMuted = false)
                } else {
                    read = audioRecord?.read(pcmBuf, 0, FRAME_SAMPLES) ?: 0
                    if (read > 0) sendPcmFrame(pcmBuf, read, encodeMuted = true)
                }
                nextT += 20_000_000L
                val sleepMs = (nextT - System.nanoTime()) / 1_000_000L
                if (sleepMs > 0) Thread.sleep(sleepMs) else nextT = System.nanoTime()
            } catch (e: Exception) {
                if (running.get()) Log.w(TAG, "send: ${e.message}")
            }
        }
    }

    /** PCM フレームを G.711 エンコードして RTP で送る。 */
    private fun sendPcmFrame(pcm: ShortArray, count: Int, encodeMuted: Boolean) {
        val gain = micGain
        val pay = ByteArray(count)
        val isPcmu = payloadType != 8
        for (i in 0 until count) {
            var s = (pcm[i] * gain).toInt().coerceIn(-32768, 32767)
            if (encodeMuted && muted) s = 0
            pay[i] = if (isPcmu) G711.linearToUlaw(s) else G711.linearToAlaw(s)
        }
        val pkt = RtpPacket.build(
            sequence = seq,
            timestamp = timestamp.toLong(),
            ssrc = ssrc.toLong(),
            payloadType = payloadType,
            payload = pay
        )
        runCatching { sink?.send(pkt) }
        seq = (seq + 1) and 0xFFFF
        timestamp += count
    }

}

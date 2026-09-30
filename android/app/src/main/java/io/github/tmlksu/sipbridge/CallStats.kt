package io.github.tmlksu.sipbridge

/**
 * 通話品質の計測 (issue #26, docs/QUALITY_STATS.md)。Android 依存なし (JVM テスト可)。
 *
 * 大原則: アプリを重くしない。既存スレッドのパケット処理から整数演算を呼ぶだけで、
 * スレッド・タイマー・パケットごとのアロケーションは足さない。
 */

/**
 * 下り RTP の受信統計。書き込みは受信スレッド (OkHttp の読み取りスレッド) の
 * [onPacket] のみ。読み出しは通話終了時 (別スレッド) なので各値は @Volatile にしておく
 * (厳密な一貫性は不要。数値の目安が取れればよい)。
 *
 * - gaps: 期待 seq より先に進んだ分 (欠けた数)。16bit ラップ対応。
 * - reorder: 期待 seq より前 (逆行・重複)。
 * - jitter: RFC 3550 §6.4.1 の interarrival jitter (RTP ts 単位で保持、ms で返す)。
 * - maxGap / stall*: 連続する 2 パケットの到着間隔 (ns) の最大値と閾値超え回数。
 * - SSRC が変わった、または seq が [RESYNC_SEQ] 以上飛んだ (前後とも) ときは
 *   相手側のストリーム切替とみなし、欠け・逆行・jitter を数えずに基準を取り直す。
 */
class RtpRxStats(private val clockRate: Int = 8000) {
    companion object {
        /** これ以上の seq の飛びはストリームの取り直しとみなす (8 kHz / 20 ms で 60 秒分)。 */
        const val RESYNC_SEQ = 3000
        private const val NS_PER_MS = 1_000_000L
        const val STALL100_NS = 100 * NS_PER_MS
        const val STALL200_NS = 200 * NS_PER_MS
        const val STALL500_NS = 500 * NS_PER_MS
    }

    @Volatile var pkts: Long = 0; private set
    @Volatile var gaps: Long = 0; private set
    @Volatile var reorder: Long = 0; private set
    @Volatile var maxGapNs: Long = 0; private set
    @Volatile var stall100: Long = 0; private set
    @Volatile var stall200: Long = 0; private set
    @Volatile var stall500: Long = 0; private set
    /** RFC 3550 の J (RTP ts 単位)。 */
    @Volatile private var jitterTs: Double = 0.0

    private var started = false
    /** 次に期待する seq (最大 seq + 1)。 */
    private var expectedSeq = 0
    private var lastSsrc = 0L
    private var lastTs = 0L
    private var lastArrivalNs = 0L

    val jitterMs: Int get() = Math.round(jitterTs * 1000.0 / clockRate).toInt()
    val maxGapMs: Long get() = maxGapNs / NS_PER_MS

    /** 受信 1 パケットごとに呼ぶ。[ts] は RTP timestamp (32bit 符号なし)、[arrivalNs] は単調時計。 */
    fun onPacket(seq: Int, ts: Long, ssrc: Long, arrivalNs: Long) {
        pkts++
        if (!started) {
            started = true
            resync(seq, ts, ssrc, arrivalNs)
            return
        }
        // 到着間隔 (seq に関係なく、到着順で見る)
        val gapNs = arrivalNs - lastArrivalNs
        if (gapNs > maxGapNs) maxGapNs = gapNs
        if (gapNs > STALL100_NS) {
            stall100++
            if (gapNs > STALL200_NS) {
                stall200++
                if (gapNs > STALL500_NS) stall500++
            }
        }

        val delta = (seq - expectedSeq) and 0xFFFF
        val forward = delta < 0x8000
        val jump = if (forward) delta else 0x10000 - delta
        if (ssrc != lastSsrc || jump >= RESYNC_SEQ) {
            resync(seq, ts, ssrc, arrivalNs)
            return
        }
        if (forward) {
            gaps += delta
            expectedSeq = (seq + 1) and 0xFFFF
        } else {
            reorder++
        }

        // RFC 3550: D(i,j) = (Rj - Ri) - (Sj - Si)、J += (|D| - J) / 16
        val arrivalDiffTs = gapNs.toDouble() * clockRate / 1_000_000_000.0
        val tsDiff = (ts - lastTs).toInt() // 32bit ラップを符号付き差分に
        val d = arrivalDiffTs - tsDiff
        jitterTs += (Math.abs(d) - jitterTs) / 16.0
        lastTs = ts
        lastArrivalNs = arrivalNs
    }

    private fun resync(seq: Int, ts: Long, ssrc: Long, arrivalNs: Long) {
        expectedSeq = (seq + 1) and 0xFFFF
        lastSsrc = ssrc
        lastTs = ts
        lastArrivalNs = arrivalNs
    }
}

/**
 * 通話中の下り途絶監視 (QUALITY_STATS.md「付随: 通話中の下り途絶監視」)。
 *
 * 下り RTP の最終到着から [thresholdNs] 経ったら 1 回だけ張り直しを要求する。
 * - 受信スレッドが [onRx]、再生スレッド (20 ms ループ) が [poll] を呼ぶ。周期処理は足さない。
 * - まだ 1 パケットも受けていない (応答直後のウォームアップ前) 間は発動しない。
 * - 途絶 1 回につき 1 回: 発動は「どの最終到着時刻に対して発動したか」で記録し、
 *   次の RTP 受信で最終到着時刻が変わるまで再発動しない (保留などで相手が RTP を
 *   止め続けても、張り直しは連続 1 回で止まる)。
 * - [onConnected] (WS の onOpen = 接続し直し) からも [thresholdNs] の猶予を取る
 *   (網切替などの別経路で張り直した直後、resume 後の最初の RTP を待たずに
 *   新しい接続を切ってしまわないように)。猶予は発動を遅らせるだけで、回数の判定には使わない。
 * - [act] が false (未接続で張り直せなかった等) を返したら発動済みにしない。
 */
class RxStallMonitor(private val thresholdNs: Long = DEFAULT_THRESHOLD_NS) {
    companion object {
        const val DEFAULT_THRESHOLD_NS = 3_000_000_000L
    }

    /** 最終到着時刻 (単調時計 ns)。0 = 未受信。受信スレッドのみ書く。 */
    @Volatile private var lastRxNs = 0L
    /** 接続し直した時刻。0 = なし。 */
    @Volatile private var connectedNs = 0L
    /** 発動済みの最終到着時刻。再生スレッドのみ書く。 */
    private var firedForRxNs = 0L
    /** 張り直した回数 (rx.reconnects)。 */
    @Volatile var reconnects: Int = 0; private set

    fun onRx(nowNs: Long) {
        // 0 は「未受信」の印なので避ける (単調時計が 0 ちょうどのことは実際には無い)。
        lastRxNs = if (nowNs == 0L) 1L else nowNs
    }

    fun onConnected(nowNs: Long) {
        connectedNs = nowNs
    }

    /** 再生スレッドから毎ループ呼ぶ。張り直しを要求したら true。 */
    fun poll(nowNs: Long, act: () -> Boolean): Boolean {
        val last = lastRxNs
        if (last == 0L || last == firedForRxNs) return false
        val ref = maxOf(last, connectedNs)
        if (nowNs - ref < thresholdNs) return false
        if (!act()) return false
        firedForRxNs = last
        reconnects++
        return true
    }
}

/** 通話終了時に送る `call_stats` の中身 (スキーマは RelayProtocol.buildCallStats)。 */
data class CallStatsReport(
    val callId: String,
    val durMs: Long,
    val net: String,
    val rxPkts: Long,
    val rxGaps: Long,
    val rxReorder: Long,
    val rxJitterMs: Int,
    val rxMaxGapMs: Long,
    val rxStall100: Long,
    val rxStall200: Long,
    val rxStall500: Long,
    val rxReconnects: Int,
    val jbUnderrun: Long,
    val jbOverflow: Long,
    val playUnderrun: Int,
    val txPkts: Long,
    val txDrop: Long,
    val txLost: Long,
    val txLateMs: Long,
    val rttStartMs: Long,
    val rttEndMs: Long
)

/** 通話中の RTT 計測 (JSON ping/pong を開始直後・終了直前に 1 回ずつ)。スレッド間は volatile で足りる。 */
class CallRtt {
    @Volatile private var startPingTs = -1L
    @Volatile private var endPingTs = -1L
    @Volatile var startMs = -1L; private set
    @Volatile var endMs = -1L; private set

    fun markStartPing(ts: Long) { startPingTs = ts }
    fun markEndPing(ts: Long) { endPingTs = ts }
    val endPingSent: Boolean get() = endPingTs >= 0

    /** pong を受けたら呼ぶ。この通話の ping への応答なら true。 */
    fun onPong(ts: Long, nowMs: Long): Boolean {
        if (ts < 0) return false
        val rtt = nowMs - ts
        if (rtt < 0 || rtt > 60_000) return false
        return when (ts) {
            startPingTs -> { if (startMs < 0) startMs = rtt; true }
            endPingTs -> { if (endMs < 0) endMs = rtt; true }
            else -> false
        }
    }
}

/**
 * `hello.relayVersion` が [min] 以上か (semver の MAJOR.MINOR.PATCH 比較)。
 * 先頭の `v` は許し、`-pre` 付きは同じ番号の正式版より小さいとみなす。`+build` は無視。
 * パースできなければ false (送らない側に倒す)。
 */
fun relayVersionAtLeast(version: String, min: String): Boolean {
    val v = parseSemver(version) ?: return false
    val m = parseSemver(min) ?: return false
    for (i in 0 until 3) {
        if (v.first[i] != m.first[i]) return v.first[i] > m.first[i]
    }
    // 同じ番号: プレリリースは正式版より前
    return !(v.second && !m.second)
}

private fun parseSemver(s: String): Pair<IntArray, Boolean>? {
    var t = s.trim().removePrefix("v")
    t = t.substringBefore('+')
    val pre = t.contains('-')
    t = t.substringBefore('-')
    val parts = t.split('.')
    if (parts.size != 3) return null
    val nums = IntArray(3)
    for (i in 0 until 3) {
        val p = parts[i]
        if (p.isEmpty() || !p.all { it in '0'..'9' }) return null
        nums[i] = p.toIntOrNull() ?: return null
    }
    return nums to pre
}

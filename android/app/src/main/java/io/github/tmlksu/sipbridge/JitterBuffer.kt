package io.github.tmlksu.sipbridge

/**
 * 端末側ジッタバッファ。PROTOCOL.md の推奨どおり 5 フレーム (100 ms) で
 * 再生開始し、上限 20 フレームを超えた分は古い方から捨てる。
 * PCM 20 ms フレーム (ShortArray) を想定した汎用 FIFO。Android 依存なし。
 */
class JitterBuffer(
    private val warmupFrames: Int = 5,
    private val maxFrames: Int = 20
) {
    private val queue: ArrayDeque<ShortArray> = ArrayDeque()
    private var warmed = false

    /** 追加。満杯時は古いフレームを捨て、捨てた数を返す。 */
    @Synchronized
    fun offer(frame: ShortArray): Int {
        queue.addLast(frame)
        var dropped = 0
        while (queue.size > maxFrames) {
            queue.removeFirst()
            dropped++
        }
        if (!warmed && queue.size >= warmupFrames) warmed = true
        return dropped
    }

    /**
     * 再生可能なフレームを 1 つ取り出す。ウォームアップ前は null
     * (呼び出し側は無音で埋める)。ウォームアップ後はキューが空でも
     * warmed は維持する (無音挿入の判断は呼び出し側)。
     */
    @Synchronized
    fun pollReady(): ShortArray? {
        if (!warmed) return null
        return queue.removeFirstOrNull()
    }

    @Synchronized fun size(): Int = queue.size
    @Synchronized fun isWarmed(): Boolean = warmed

    @Synchronized
    fun clear() {
        queue.clear()
        warmed = false
    }
}

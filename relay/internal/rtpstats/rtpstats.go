// Package rtpstats は RTP 受信側の品質計測 (docs/QUALITY_STATS.md) を行う。
//
// 受信パケット数・seq の欠け・逆行/重複・RFC 3550 の interarrival jitter・
// 到着間隔の最大値・到着間隔が 100/200/500 ms を超えた回数 (stall) を数える。
//
// RTP ホットパス (20 ms ごと) で呼ばれるため、ロックもアロケーションも使わない。
// 全フィールドは atomic で、書き手は通常 1 つの受信 goroutine、読み手は通話終了時の
// Snapshot である。勝者の再接続が重なる短い間は書き手が 2 つになりうるが、
// その場合も data race にはならず、計測値が 1〜2 パケット分ずれるだけである。
package rtpstats

import (
	"encoding/binary"
	"log/slog"
	"math"
	"sync/atomic"
	"time"
)

// nsPerTick は RTP タイムスタンプ 1 単位あたりのナノ秒 (G.711 = 8 kHz)。
const nsPerTick = int64(time.Second) / 8000

// stall の閾値。
const (
	stall100 = 100 * time.Millisecond
	stall200 = 200 * time.Millisecond
	stall500 = 500 * time.Millisecond
)

// epoch は到着時刻の基準である。time.Since は monotonic 時計を使う。
var epoch = time.Now()

// Stats は 1 方向・1 通話分の RTP 受信統計である。ゼロ値で使える。
type Stats struct {
	pkts     atomic.Uint64
	gaps     atomic.Uint64
	reorder  atomic.Uint64
	stall100 atomic.Uint64
	stall200 atomic.Uint64
	stall500 atomic.Uint64
	maxGapNs atomic.Int64
	// jitter16 は RFC 3550 A.8 の整数実装どおり 16 倍した jitter (単位 ns)。
	jitter16 atomic.Int64

	started atomic.Bool
	lastArr atomic.Int64  // 直前パケットの到着時刻 (epoch からの ns)
	lastTs  atomic.Uint32 // 直前に到着したパケットの RTP ts (到着順)
	highSeq atomic.Uint32 // これまでの最大 seq (16 bit)
	ssrc    atomic.Uint32
}

// Observe は受信した RTP パケット 1 つを計測に加える (到着時刻は現在)。
func (s *Stats) Observe(pkt []byte) {
	s.ObserveAt(pkt, time.Since(epoch))
}

// ObserveAt は到着時刻 arr (単調増加する任意の基準からの経過) を指定して計測に加える。
// RTP ヘッダとして短すぎる・バージョンが 2 でないパケットは数えない。
func (s *Stats) ObserveAt(pkt []byte, arr time.Duration) {
	if len(pkt) < 12 || pkt[0]>>6 != 2 {
		return
	}
	seq := binary.BigEndian.Uint16(pkt[2:4])
	ts := binary.BigEndian.Uint32(pkt[4:8])
	ssrc := binary.BigEndian.Uint32(pkt[8:12])
	now := int64(arr)

	s.pkts.Add(1)
	started := s.started.Load()
	prevArr := s.lastArr.Swap(now)
	prevTs := s.lastTs.Swap(ts)

	if started {
		gap := now - prevArr
		if gap > s.maxGapNs.Load() {
			s.maxGapNs.Store(gap)
		}
		if gap > int64(stall100) {
			s.stall100.Add(1)
		}
		if gap > int64(stall200) {
			s.stall200.Add(1)
		}
		if gap > int64(stall500) {
			s.stall500.Add(1)
		}
	}

	if !started || s.ssrc.Load() != ssrc {
		// 最初のパケット、または送信元の作り直し (SSRC 変更) では seq/ts の
		// 基準を取り直す (欠けや jitter として数えない)。
		s.ssrc.Store(ssrc)
		s.highSeq.Store(uint32(seq))
		s.started.Store(true)
		return
	}

	// seq: 16 bit の差を符号付きで見る (折り返し対応)。
	switch d := int16(seq - uint16(s.highSeq.Load())); {
	case d > 0:
		if d > 1 {
			s.gaps.Add(uint64(d - 1))
		}
		s.highSeq.Store(uint32(seq))
	default:
		s.reorder.Add(1) // 逆行・重複
	}

	// RFC 3550 6.4.1: D = (Rj - Ri) - (Sj - Si)、J += (|D| - J) / 16。
	// ts の差は int32 で見て折り返しに対応する。
	dns := (now - prevArr) - int64(int32(ts-prevTs))*nsPerTick
	if dns < 0 {
		dns = -dns
	}
	j := s.jitter16.Load()
	s.jitter16.Store(j + dns - ((j + 8) >> 4))
}

// Pkts は受信パケット数である。
func (s *Stats) Pkts() uint64 { return s.pkts.Load() }

// Snapshot は計測値の写しである (ログ・JSON 用)。
type Snapshot struct {
	Pkts     uint64  `json:"pkts"`
	Gaps     uint64  `json:"gaps"`
	Reorder  uint64  `json:"reorder"`
	JitterMs float64 `json:"jitterMs"`
	MaxGapMs int64   `json:"maxGapMs"`
	Stall100 uint64  `json:"stall100"`
	Stall200 uint64  `json:"stall200"`
	Stall500 uint64  `json:"stall500"`
}

// Snapshot は現在の計測値を返す。
func (s *Stats) Snapshot() Snapshot {
	j := float64(s.jitter16.Load()) / 16 / float64(time.Millisecond)
	return Snapshot{
		Pkts:     s.pkts.Load(),
		Gaps:     s.gaps.Load(),
		Reorder:  s.reorder.Load(),
		JitterMs: math.Round(j*10) / 10,
		MaxGapMs: s.maxGapNs.Load() / int64(time.Millisecond),
		Stall100: s.stall100.Load(),
		Stall200: s.stall200.Load(),
		Stall500: s.stall500.Load(),
	}
}

// Add は複数の計測 (例: 早期メディアと本通話の別パイプ) を合算する。
// jitter と maxGap は大きい方を取る。
func (a Snapshot) Add(b Snapshot) Snapshot {
	a.Pkts += b.Pkts
	a.Gaps += b.Gaps
	a.Reorder += b.Reorder
	a.JitterMs = math.Max(a.JitterMs, b.JitterMs)
	if b.MaxGapMs > a.MaxGapMs {
		a.MaxGapMs = b.MaxGapMs
	}
	a.Stall100 += b.Stall100
	a.Stall200 += b.Stall200
	a.Stall500 += b.Stall500
	return a
}

// LogValue は slog のグループとして展開する (up.pkts=… の形になる)。
func (a Snapshot) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Uint64("pkts", a.Pkts),
		slog.Uint64("gaps", a.Gaps),
		slog.Uint64("reorder", a.Reorder),
		slog.Float64("jitterMs", a.JitterMs),
		slog.Int64("maxGapMs", a.MaxGapMs),
		slog.Uint64("stall100", a.Stall100),
		slog.Uint64("stall200", a.Stall200),
		slog.Uint64("stall500", a.Stall500),
	)
}

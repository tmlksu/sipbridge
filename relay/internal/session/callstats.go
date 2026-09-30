package session

import (
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tmlksu/sipbridge/relay/internal/call"
	"github.com/tmlksu/sipbridge/relay/internal/proto"
	"github.com/tmlksu/sipbridge/relay/internal/rtpstats"
)

// 通話品質ログ (docs/QUALITY_STATS.md)。
//
// 1 通話 1 行の `call_stats` を出す。アプリの call_stats を受けた時点
// (通話終了前に届いたら終了時点)、または通話終了から CallStatsWait (既定 5 秒)
// 以内に届かなければ relay 側の計測だけで出す。同じ通話を二重には出さない。
// 番号・表示名などの PII は出さない (callId, account=内線, dev=デバイス ID のみ)。

// DefaultCallStatsWait は通話終了からアプリの call_stats を待つ上限である。
const DefaultCallStatsWait = 5 * time.Second

// mediaStatser は受信統計を持つ MediaPipe (sipbackend / fakebackend) である。
type mediaStatser interface {
	RTPStats() rtpstats.Snapshot
	DroppedPackets() uint64
}

// astSnapshot は Asterisk→relay 区間の計測である。
type astSnapshot struct {
	rtp   rtpstats.Snapshot
	qdrop uint64 // パイプの受信キュー溢れ (relay 内でメディアポンプが追いつかない)
}

func (a astSnapshot) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Uint64("pkts", a.rtp.Pkts),
		slog.Uint64("gaps", a.rtp.Gaps),
		slog.Uint64("reorder", a.rtp.Reorder),
		slog.Float64("jitterMs", a.rtp.JitterMs),
		slog.Uint64("qdrop", a.qdrop),
	)
}

// callRecord は 1 通話分の計測である。
type callRecord struct {
	// RTP ホットパスから書く (atomic のみ、ロック無し)。
	up     rtpstats.Stats // app→relay 上り (handleBinary)
	txDrop atomic.Uint64  // relay→app 下りの送信キュー満による破棄 (メディアポンプ)

	// 以下は callStatsLog.mu で保護する。
	key     string
	callID  string
	account string
	dev     string
	start   time.Time
	pipes   []call.MediaPipe
	ended   bool
	dur     time.Duration
	upSnap  rtpstats.Snapshot
	ast     astSnapshot
	txSnap  uint64
	app     json.RawMessage // アプリの call_stats (t/callId を除いたもの)
	net     string
	timer   *time.Timer
	written bool
}

// callStatsLog は通話ごとの計測を集めてログに出す。ロックは mu だけで、
// mu を保持したまま group/hub のロックを取らない (葉のロック)。
type callStatsLog struct {
	log  *slog.Logger
	wait time.Duration

	mu   sync.Mutex
	recs map[string]*callRecord // account + "\x00" + callId → 記録
}

func newCallStatsLog(log *slog.Logger, wait time.Duration) *callStatsLog {
	if wait <= 0 {
		wait = DefaultCallStatsWait
	}
	return &callStatsLog{log: log, wait: wait, recs: make(map[string]*callRecord)}
}

func statsKey(account, callID string) string { return account + "\x00" + callID }

// begin はメディア開始時に通話の記録を作る (同じ通話なら既存を返し、パイプを足す)。
// 同じ account の終了していない別の記録は、EvEnded を取りこぼしたものとして終了させる。
func (l *callStatsLog) begin(account, callID, dev string, pipe call.MediaPipe) *callRecord {
	key := statsKey(account, callID)
	var flush []*callRecord
	l.mu.Lock()
	rec := l.recs[key]
	if rec == nil {
		for _, r := range l.recs {
			if r.account == account && !r.ended {
				if l.endLocked(r, "") {
					flush = append(flush, r)
				}
			}
		}
		rec = &callRecord{key: key, callID: callID, account: account, dev: dev, start: time.Now()}
		l.recs[key] = rec
	}
	if pipe != nil && !containsPipe(rec.pipes, pipe) {
		rec.pipes = append(rec.pipes, pipe)
	}
	l.mu.Unlock()
	for _, r := range flush {
		l.write(r)
	}
	return rec
}

func containsPipe(ps []call.MediaPipe, p call.MediaPipe) bool {
	for _, q := range ps {
		if q == p {
			return true
		}
	}
	return false
}

// end は通話終了を記録する。アプリの call_stats が既に届いていれば即座に出し、
// 無ければ wait 後に relay 分だけで出す。
func (l *callStatsLog) end(rec *callRecord, dev string) {
	if rec == nil {
		return
	}
	l.mu.Lock()
	now := l.endLocked(rec, dev)
	l.mu.Unlock()
	if now {
		l.write(rec)
	}
}

// endLocked は終了時点の計測を確定する。戻り値 true なら呼び出し側が
// (ロック解放後に) write する。false なら待ちタイマを張った / 既に終了済み。
func (l *callStatsLog) endLocked(rec *callRecord, dev string) bool {
	if rec.ended {
		return false
	}
	rec.ended = true
	if dev != "" {
		rec.dev = dev
	}
	rec.dur = time.Since(rec.start)
	rec.upSnap = rec.up.Snapshot()
	rec.txSnap = rec.txDrop.Load()
	for _, p := range rec.pipes {
		if ms, ok := p.(mediaStatser); ok {
			rec.ast.rtp = rec.ast.rtp.Add(ms.RTPStats())
			rec.ast.qdrop += ms.DroppedPackets()
		}
	}
	rec.pipes = nil
	if rec.app != nil {
		return l.takeLocked(rec)
	}
	rec.timer = time.AfterFunc(l.wait, func() {
		l.mu.Lock()
		ok := l.takeLocked(rec)
		l.mu.Unlock()
		if ok {
			l.write(rec)
		}
	})
	return false
}

// takeLocked は記録を一覧から外し、まだ出していなければ true を返す (二重出力防止)。
func (l *callStatsLog) takeLocked(rec *callRecord) bool {
	if rec.written {
		return false
	}
	rec.written = true
	if rec.timer != nil {
		rec.timer.Stop()
	}
	if l.recs[rec.key] == rec {
		delete(l.recs, rec.key)
	}
	return true
}

// deliver はアプリの call_stats を受け取る。応答は返さない。
func (l *callStatsLog) deliver(account, dev string, m *proto.CallStats) {
	if len(m.Raw) > proto.MaxCallStatsSize {
		l.log.Warn("call_stats が大きすぎるため破棄", "device", dev, "size", len(m.Raw))
		return
	}
	app, net, ok := sanitizeAppStats(m.Raw)
	if !ok {
		l.log.Warn("call_stats の解析に失敗したため破棄", "device", dev)
		return
	}
	l.mu.Lock()
	rec := l.recs[statsKey(account, m.CallID)]
	if rec == nil || m.CallID == "" {
		l.mu.Unlock()
		// 記録の無い通話 (relay 再起動・待ち超過後の遅着・旧 callId)。
		// 本来の call_stats 行と区別し、集計で二重に数えないよう別名で出す。
		l.log.Info("call_stats_orphan", "callId", m.CallID, "account", account,
			"dev", dev, "net", net, "app", app)
		return
	}
	if rec.app != nil {
		l.mu.Unlock()
		l.log.Debug("call_stats の重複を無視", "callId", m.CallID, "device", dev)
		return
	}
	rec.app = app
	rec.net = net
	now := rec.ended && l.takeLocked(rec)
	l.mu.Unlock()
	if now {
		l.write(rec)
	}
}

// write は 1 通話 1 行を出す。rec は一覧から外れて確定済みなのでロック不要。
func (l *callStatsLog) write(rec *callRecord) {
	attrs := []any{
		"callId", rec.callID,
		"account", rec.account,
		"dev", rec.dev,
		"dur", rec.dur.Milliseconds(),
	}
	if rec.net != "" {
		attrs = append(attrs, "net", rec.net)
	}
	attrs = append(attrs,
		"up", rec.upSnap,
		"ast", rec.ast,
		"txDrop", rec.txSnap,
	)
	if rec.app != nil {
		attrs = append(attrs, "app", rec.app)
	}
	l.log.Info("call_stats", attrs...)
}

// sanitizeAppStats はアプリの call_stats から t/callId を除いた JSON (キー順は整列、
// 空白なし) と net を返す。JSON オブジェクトでなければ ok=false。
// 中身のスキーマは解釈しない (アプリ側の項目追加に強くするため)。
func sanitizeAppStats(raw []byte) (app json.RawMessage, net string, ok bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, "", false
	}
	delete(m, "t")
	delete(m, "callId")
	if v, has := m["net"]; has {
		var s string
		if json.Unmarshal(v, &s) == nil {
			switch s {
			case "wifi", "cellular", "other":
				net = s
			}
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, "", false
	}
	return out, net, true
}

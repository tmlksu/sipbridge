// Package fakebackend はテスト用の call.Backend 実装である。
// 外部から InjectIncoming() で着信を起こせ、Answer 後のパイプは
// Send したパケットをそのまま Recv に返す (エコー)。
package fakebackend

import (
	"context"
	"fmt"
	"sync"

	"github.com/tmlksu/sipbridge/relay/internal/call"
	"github.com/tmlksu/sipbridge/relay/internal/rtpstats"
)

// Fake は外部操作で駆動する疑似 SIP バックエンドである。
type Fake struct {
	mu    sync.Mutex
	ev    chan<- call.Event
	calls map[string]*fakeCall
	seq   int

	regOK     bool // Start で上げる EvRegistered.OK
	regDetail string
	// DialAnsweredAfter は Dial 後に EvAnswered を上げるまでの遅延。
	// 0 なら即時に Ringing→Answered を上げる。
}

type fakeCall struct {
	state string // "ringing" | "active" | "ended"
	pt    int
}

// New は Fake を作る (Start で登録成功を上げる)。
func New() *Fake {
	return &Fake{calls: make(map[string]*fakeCall), regOK: true, regDetail: "fake: registered"}
}

// NewUnregistered は登録失敗状態の Fake を作る
// (パスワード変更の受理条件などのテスト用)。
func NewUnregistered() *Fake {
	return &Fake{calls: make(map[string]*fakeCall), regOK: false, regDetail: "fake: 401 unauthorized"}
}

// Start は登録イベントを上げ、ctx 終了まで待つ。
func (f *Fake) Start(ctx context.Context, ev chan<- call.Event) error {
	f.mu.Lock()
	f.ev = ev
	ok, detail := f.regOK, f.regDetail
	f.mu.Unlock()
	select {
	case ev <- call.EvRegistered{OK: ok, Detail: detail}:
	default:
	}
	go func() {
		<-ctx.Done()
	}()
	return nil
}

// InjectIncoming は外部から着信を起こし、callId を返す。
func (f *Fake) InjectIncoming(from, display string, pt int) string {
	f.mu.Lock()
	f.seq++
	id := fmt.Sprintf("fake-%d", f.seq)
	f.calls[id] = &fakeCall{state: "ringing", pt: pt}
	ev := f.ev
	f.mu.Unlock()
	if ev != nil {
		ev <- call.EvIncoming{CallID: id, From: from, Display: display, PT: pt}
	}
	return id
}

// InjectRemoteHangup は相手側からの切断を起こす。
func (f *Fake) InjectRemoteHangup(callID string) {
	f.mu.Lock()
	c, ok := f.calls[callID]
	if ok && c.state != "ended" {
		c.state = "ended"
	}
	ev := f.ev
	f.mu.Unlock()
	if ok && ev != nil {
		ev <- call.EvEnded{CallID: callID, Reason: "bye", Code: 200}
	}
}

// Answer はパイプを返し、通話を active にする。
func (f *Fake) Answer(callID string, pt int) (call.MediaPipe, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.calls[callID]
	if !ok || c.state != "ringing" {
		return nil, fmt.Errorf("応答できる着信 %q が無い", callID)
	}
	c.state = "active"
	if pt != 0 {
		c.pt = pt
	}
	return newEchoPipe(), nil
}

// Reject は着信を拒否する (イベントは Manager が発行するため上げない)。
func (f *Fake) Reject(callID string, code int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.calls[callID]
	if !ok || c.state != "ringing" {
		return fmt.Errorf("拒否できる着信 %q が無い", callID)
	}
	c.state = "ended"
	return nil
}

// Hangup は通話を切断する (イベントは Manager が発行するため上げない)。
func (f *Fake) Hangup(callID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.calls[callID]
	if !ok || c.state == "ended" {
		return fmt.Errorf("通話 %q が無い", callID)
	}
	c.state = "ended"
	return nil
}

// Dial は発信し、非同期に Ringing→Answered を上げる。
func (f *Fake) Dial(to string) (string, error) {
	f.mu.Lock()
	f.seq++
	id := fmt.Sprintf("fake-out-%d", f.seq)
	f.calls[id] = &fakeCall{state: "ringing", pt: 0}
	ev := f.ev
	f.mu.Unlock()
	if ev == nil {
		return id, nil
	}
	go func() {
		ev <- call.EvRinging{CallID: id, Early: false}
		f.mu.Lock()
		if c, ok := f.calls[id]; ok && c.state == "ringing" {
			c.state = "active"
			c.pt = 0
		}
		f.mu.Unlock()
		ev <- call.EvAnswered{CallID: id, PT: 0, Pipe: newEchoPipe()}
	}()
	return id, nil
}

// echoPipe は Send されたパケットを Recv にそのまま返す。
// 返したパケットは「Asterisk から受けた RTP」として統計に数える
// (sipbackend の rtpPipe と同じ RTPStats/DroppedPackets を持つ)。
type echoPipe struct {
	mu      sync.Mutex
	ch      chan []byte
	closed  bool
	stats   rtpstats.Stats
	dropped uint64 // mu で保護
}

func newEchoPipe() *echoPipe {
	return &echoPipe{ch: make(chan []byte, 50)}
}

func (p *echoPipe) Send(rtp []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("パイプは閉じている")
	}
	cp := append([]byte(nil), rtp...)
	p.stats.Observe(cp)
	select {
	case p.ch <- cp:
	default:
		// 溢れたら古い方を捨てて入れ直す。
		select {
		case <-p.ch:
			p.dropped++
		default:
		}
		select {
		case p.ch <- cp:
		default:
		}
	}
	return nil
}

func (p *echoPipe) Recv() <-chan []byte { return p.ch }

// RTPStats はエコーしたパケットの統計である。
func (p *echoPipe) RTPStats() rtpstats.Snapshot { return p.stats.Snapshot() }

// DroppedPackets は受信キュー溢れで捨てたパケット数である。
func (p *echoPipe) DroppedPackets() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dropped
}

func (p *echoPipe) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.ch)
	}
	return nil
}

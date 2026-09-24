// Package call は単一通話の状態機械と Backend インタフェースを定義する。
// 状態遷移は docs/PROTOCOL.md に従う:
//
//	IDLE --INVITE--> RINGING_IN --answer--> ACTIVE --BYE/hangup--> IDLE
//	                 |--reject/timeout(25s)/CANCEL--> IDLE
//	IDLE --dial----> RINGING_OUT --200--> ACTIVE
//	                 |--hangup(CANCEL)/4xx-6xx--> IDLE
package call

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// 状態。
type State string

const (
	StateIdle       State = "idle"
	StateRingingIn  State = "ringing_in"
	StateRingingOut State = "ringing_out"
	StateActive     State = "active"
)

// 既定の無応答タイムアウト (着信から 25 秒で 480)。
const DefaultNoAnswerTimeout = 25 * time.Second

// MediaPipe は RTP の送受パイプである。
type MediaPipe interface {
	Send(rtp []byte) error
	Recv() <-chan []byte
	Close() error
}

// Backend は SIP 側 (Asterisk) とのやり取りを抽象化する。
// T1 では fakebackend、T2 で sipbackend が実装する。
type Backend interface {
	Start(ctx context.Context, ev chan<- Event) error // Event: Registered/Incoming/Ended/Answered/Ringing
	Answer(callID string, pt int) (MediaPipe, error)  // 200 OK を送り RTP パイプを返す
	Reject(callID string, code int) error
	Hangup(callID string) error
	Dial(to string) (callID string, err error)
}

// ---- Backend からのイベント ----

// Event は Backend が上げるイベントの印インタフェースである。
type Event interface{ callEvent() }

// EvRegistered は REGISTER 状態変化である。
type EvRegistered struct {
	OK     bool
	Detail string
}

func (EvRegistered) callEvent() {}

// EvIncoming は着信 (INVITE 受信、100/180 応答済み) である。
type EvIncoming struct {
	CallID  string
	From    string
	Display string
	PT      int
}

func (EvIncoming) callEvent() {}

// EvRinging は発信が 1xx を受けたことである。
type EvRinging struct {
	CallID string
	Early  bool      // 183 でメディアあり
	Pipe   MediaPipe // Early=true のとき早期メディア用パイプ (任意)
}

func (EvRinging) callEvent() {}

// EvAnswered は発信が 200 OK を受けたことである。
// 着信の answer 確認は Manager.Answer が直接 EvAnswered を発行するため、
// Backend は着信応答でこれを上げてはならない。
type EvAnswered struct {
	CallID string
	PT     int
	Pipe   MediaPipe // 発信時: INVITE の SDP で確保した RTP パイプ (必須)
}

func (EvAnswered) callEvent() {}

// EvEnded は相手側からの通話終了 (BYE/CANCEL/4xx-6xx) である。
type EvEnded struct {
	CallID string
	Reason string // bye/cancel/reject/timeout/error
	Code   int
}

func (EvEnded) callEvent() {}

// CallView は現在の通話のスナップショットである。
type CallView struct {
	CallID    string
	Direction string // "in" | "out"
	State     string // "ringing" | "active"
	From      string
	Display   string
	PT        int
	StartedAt int64 // Unix 秒
}

// Manager は状態機械を保持し、Backend とアプリ側 (session) をつなぐ。
// 公開イベントは Events() から受け取る。Manager 自身が出すイベントは
// EvRegistered / EvIncoming / EvRinging / EvAnswered / EvEnded である。
type Manager struct {
	be Backend

	NoAnswerTimeout time.Duration

	mu         sync.Mutex
	state      State
	cur        *CallView
	pipe       MediaPipe
	registered bool
	regDetail  string

	events        chan Event
	noAnswerTimer *time.Timer
}

// NewManager は Manager を作る。
func NewManager(be Backend) *Manager {
	return &Manager{
		be:              be,
		state:           StateIdle,
		NoAnswerTimeout: DefaultNoAnswerTimeout,
		events:          make(chan Event, 32),
	}
}

// Events は公開イベントの受信チャネルである。
func (m *Manager) Events() <-chan Event { return m.events }

// Start は Backend を起動し、イベントポンプを開始する。
func (m *Manager) Start(ctx context.Context) error {
	beCh := make(chan Event, 32)
	if err := m.be.Start(ctx, beCh); err != nil {
		return err
	}
	go m.pump(ctx, beCh)
	return nil
}

func (m *Manager) emit(ev Event) {
	select {
	case m.events <- ev:
	default:
		// 受信者が詰まっていても状態機械は止めない。イベント欠落は
		// session 側の hello 同期で補える。
	}
}

func (m *Manager) pump(ctx context.Context, beCh <-chan Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-beCh:
			m.handleBackend(ev)
		}
	}
}

// handleBackend は Backend イベントで状態を遷移させる。
func (m *Manager) handleBackend(ev Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch e := ev.(type) {
	case EvRegistered:
		m.registered = e.OK
		m.regDetail = e.Detail
		m.emit(e)
	case EvIncoming:
		if m.state != StateIdle {
			// 単一通話スコープのため 2 本目は 486 で断る。
			_ = m.be.Reject(e.CallID, 486)
			return
		}
		m.state = StateRingingIn
		m.cur = &CallView{
			CallID: e.CallID, Direction: "in", State: "ringing",
			From: e.From, Display: e.Display, PT: e.PT,
			StartedAt: time.Now().Unix(),
		}
		m.armNoAnswerLocked(e.CallID)
		m.emit(e)
	case EvRinging:
		if m.state != StateRingingOut || m.cur == nil || m.cur.CallID != e.CallID {
			return
		}
		if e.Early && e.Pipe != nil && m.pipe == nil {
			m.pipe = e.Pipe // 早期メディア (183) を勝者へ流せるようにする
		}
		m.emit(e)
	case EvAnswered:
		if m.state != StateRingingOut || m.cur == nil || m.cur.CallID != e.CallID {
			return
		}
		if e.Pipe != nil && e.Pipe != m.pipe {
			m.closePipeLocked()
			m.pipe = e.Pipe
		}
		m.state = StateActive
		m.cur.State = "active"
		m.cur.PT = e.PT
		m.emit(e)
	case EvEnded:
		if m.cur == nil || m.cur.CallID != e.CallID {
			return // 既に自発終了済み
		}
		m.closePipeLocked()
		m.stopNoAnswerLocked()
		m.state = StateIdle
		m.cur = nil
		m.emit(e)
	}
}

func (m *Manager) armNoAnswerLocked(callID string) {
	m.stopNoAnswerLocked()
	timeout := m.NoAnswerTimeout
	if timeout <= 0 {
		timeout = DefaultNoAnswerTimeout
	}
	m.noAnswerTimer = time.AfterFunc(timeout, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.state != StateRingingIn || m.cur == nil || m.cur.CallID != callID {
			return
		}
		_ = m.be.Reject(callID, 480)
		m.state = StateIdle
		ended := EvEnded{CallID: callID, Reason: "timeout", Code: 480}
		m.cur = nil
		m.emit(ended)
	})
}

func (m *Manager) stopNoAnswerLocked() {
	if m.noAnswerTimer != nil {
		m.noAnswerTimer.Stop()
		m.noAnswerTimer = nil
	}
}

func (m *Manager) closePipeLocked() {
	if m.pipe != nil {
		_ = m.pipe.Close()
		m.pipe = nil
	}
}

// Answer は着信に応答し、RTP パイプを確保する。成功時は EvAnswered を発行する。
func (m *Manager) Answer(callID string, pt int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != StateRingingIn || m.cur == nil || m.cur.CallID != callID {
		return fmt.Errorf("応答できる着信 %q が無い", callID)
	}
	if pt == 0 {
		pt = m.cur.PT
	}
	pipe, err := m.be.Answer(callID, pt)
	if err != nil {
		return err
	}
	m.stopNoAnswerLocked()
	m.pipe = pipe
	m.state = StateActive
	m.cur.State = "active"
	m.cur.PT = pt
	m.emit(EvAnswered{CallID: callID, PT: pt})
	return nil
}

// Reject は着信を拒否する。
func (m *Manager) Reject(callID string, code int) error {
	if code == 0 {
		code = 486
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != StateRingingIn || m.cur == nil || m.cur.CallID != callID {
		return fmt.Errorf("拒否できる着信 %q が無い", callID)
	}
	if err := m.be.Reject(callID, code); err != nil {
		return err
	}
	m.stopNoAnswerLocked()
	m.state = StateIdle
	m.cur = nil
	m.emit(EvEnded{CallID: callID, Reason: "reject", Code: code})
	return nil
}

// Hangup は通話 (ACTIVE) または発信 (RINGING_OUT) を切断する。
func (m *Manager) Hangup(callID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil || m.cur.CallID != callID {
		return fmt.Errorf("通話 %q が無い", callID)
	}
	reason, code := "bye", 200
	if m.state == StateRingingOut {
		reason = "cancel"
	}
	if m.state != StateActive && m.state != StateRingingOut {
		return fmt.Errorf("通話 %q は切断できる状態ではない", callID)
	}
	if err := m.be.Hangup(callID); err != nil {
		return err
	}
	m.closePipeLocked()
	m.stopNoAnswerLocked()
	m.state = StateIdle
	m.cur = nil
	m.emit(EvEnded{CallID: callID, Reason: reason, Code: code})
	return nil
}

// hangupInternal は WS 切断タイムアウトなど内部要因での切断である。
// reason は ended.reason としてアプリに通知される。
func (m *Manager) hangupInternal(reason string, code int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil || (m.state != StateActive && m.state != StateRingingOut && m.state != StateRingingIn) {
		return
	}
	callID := m.cur.CallID
	if m.state == StateRingingIn {
		_ = m.be.Reject(callID, code)
	} else {
		_ = m.be.Hangup(callID)
	}
	m.closePipeLocked()
	m.stopNoAnswerLocked()
	m.state = StateIdle
	m.cur = nil
	m.emit(EvEnded{CallID: callID, Reason: reason, Code: code})
}

// HangupTimeout は内部タイムアウトによる切断 (reason="timeout") である。
func (m *Manager) HangupTimeout() {
	m.hangupInternal("timeout", 480)
}

// HangupTimeoutIf は現在の呼が callID のときだけ HangupTimeout する。
// 別の呼 (猶予中に前の呼が終わり、次の着信が来た等) を巻き込まないため。
func (m *Manager) HangupTimeoutIf(callID string) {
	m.mu.Lock()
	match := m.cur != nil && m.cur.CallID == callID
	m.mu.Unlock()
	if match {
		m.hangupInternal("timeout", 480)
	}
}

// Dial は発信する。callID は EvRinging/EvAnswered で追跡する。
func (m *Manager) Dial(to string) (string, error) {
	if to == "" {
		return "", fmt.Errorf("発信先が空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != StateIdle {
		return "", fmt.Errorf("通話中のため発信できない")
	}
	callID, err := m.be.Dial(to)
	if err != nil {
		return "", err
	}
	m.state = StateRingingOut
	m.cur = &CallView{
		CallID: callID, Direction: "out", State: "ringing",
		From: to, StartedAt: time.Now().Unix(),
	}
	return callID, nil
}

// Current は現在の通話の複製を返す。無ければ nil。
func (m *Manager) Current() *CallView {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return nil
	}
	cp := *m.cur
	return &cp
}

// Pipe は確保済みの RTP パイプを返す。無ければ nil。
func (m *Manager) Pipe() MediaPipe {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pipe
}

// Registered は REGISTER 状態を返す。
func (m *Manager) Registered() (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.registered, m.regDetail
}

// State は現在の状態を返す (テスト用)。
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

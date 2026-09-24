package call_test

import (
	"context"
	"testing"
	"time"

	"github.com/tmlksu/sipbridge/relay/internal/call"
	"github.com/tmlksu/sipbridge/relay/internal/fakebackend"
)

func newManager(t *testing.T) (*call.Manager, *fakebackend.Fake) {
	t.Helper()
	fb := fakebackend.New()
	m := call.NewManager(fb)
	m.NoAnswerTimeout = 300 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start 失敗: %v", err)
	}
	// 登録イベントを消費する。
	select {
	case ev := <-m.Events():
		if _, ok := ev.(call.EvRegistered); !ok {
			t.Fatalf("最初のイベントが Registered ではない: %T", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Registered が来ない")
	}
	if ok, _ := m.Registered(); !ok {
		t.Fatalf("登録状態になっていない")
	}
	return m, fb
}

func nextEvent(t *testing.T, m *call.Manager) call.Event {
	t.Helper()
	select {
	case ev := <-m.Events():
		return ev
	case <-time.After(2 * time.Second):
		t.Fatalf("イベントが来ない")
		return nil
	}
}

func TestIncomingAnswerHangup(t *testing.T) {
	m, fb := newManager(t)
	id := fb.InjectIncoming("102", "Bob", 0)
	if ev := nextEvent(t, m); ev.(call.EvIncoming).CallID != id {
		t.Fatalf("Incoming の callId が不一致: %+v", ev)
	}
	if m.State() != call.StateRingingIn {
		t.Fatalf("state = %q", m.State())
	}
	if cur := m.Current(); cur == nil || cur.Direction != "in" || cur.State != "ringing" {
		t.Fatalf("Current が不正: %+v", cur)
	}
	if err := m.Answer(id, 0); err != nil {
		t.Fatalf("Answer 失敗: %v", err)
	}
	if ev := nextEvent(t, m); ev.(call.EvAnswered).CallID != id {
		t.Fatalf("Answered の callId が不一致: %+v", ev)
	}
	if m.State() != call.StateActive {
		t.Fatalf("state = %q", m.State())
	}
	if m.Pipe() == nil {
		t.Fatalf("パイプが無い")
	}
	if err := m.Hangup(id); err != nil {
		t.Fatalf("Hangup 失敗: %v", err)
	}
	ev := nextEvent(t, m)
	ended, ok := ev.(call.EvEnded)
	if !ok || ended.Reason != "bye" {
		t.Fatalf("Ended が不正: %+v", ev)
	}
	if m.State() != call.StateIdle || m.Current() != nil {
		t.Fatalf("IDLE に戻っていない")
	}
}

func TestReject(t *testing.T) {
	m, fb := newManager(t)
	id := fb.InjectIncoming("102", "", 8)
	_ = nextEvent(t, m)
	if err := m.Reject(id, 0); err != nil {
		t.Fatalf("Reject 失敗: %v", err)
	}
	ev := nextEvent(t, m)
	if ended, ok := ev.(call.EvEnded); !ok || ended.Reason != "reject" || ended.Code != 486 {
		t.Fatalf("Ended が不正: %+v", ev)
	}
}

func TestNoAnswerTimeout(t *testing.T) {
	m, fb := newManager(t)
	id := fb.InjectIncoming("102", "", 0)
	_ = nextEvent(t, m)
	ev := nextEvent(t, m)
	ended, ok := ev.(call.EvEnded)
	if !ok || ended.CallID != id || ended.Reason != "timeout" || ended.Code != 480 {
		t.Fatalf("タイムアウト Ended が不正: %+v", ev)
	}
	if m.State() != call.StateIdle {
		t.Fatalf("state = %q", m.State())
	}
}

func TestRemoteHangup(t *testing.T) {
	m, fb := newManager(t)
	id := fb.InjectIncoming("102", "", 0)
	_ = nextEvent(t, m)
	if err := m.Answer(id, 0); err != nil {
		t.Fatalf("Answer 失敗: %v", err)
	}
	_ = nextEvent(t, m)
	fb.InjectRemoteHangup(id)
	ev := nextEvent(t, m)
	if ended, ok := ev.(call.EvEnded); !ok || ended.Reason != "bye" {
		t.Fatalf("Ended が不正: %+v", ev)
	}
}

func TestSecondIncomingRejected(t *testing.T) {
	m, fb := newManager(t)
	id1 := fb.InjectIncoming("102", "", 0)
	_ = nextEvent(t, m)
	_ = fb.InjectIncoming("103", "", 0) // 2 本目は 486
	if cur := m.Current(); cur == nil || cur.CallID != id1 {
		t.Fatalf("1 本目が維持されていない: %+v", cur)
	}
}

func TestDialFlow(t *testing.T) {
	m, _ := newManager(t)
	id, err := m.Dial("102")
	if err != nil {
		t.Fatalf("Dial 失敗: %v", err)
	}
	if m.State() != call.StateRingingOut {
		t.Fatalf("state = %q", m.State())
	}
	if ev := nextEvent(t, m); ev.(call.EvRinging).CallID != id {
		t.Fatalf("Ringing が不正: %+v", ev)
	}
	if ev := nextEvent(t, m); ev.(call.EvAnswered).CallID != id {
		t.Fatalf("Answered が不正: %+v", ev)
	}
	if m.State() != call.StateActive {
		t.Fatalf("state = %q", m.State())
	}
	if err := m.Hangup(id); err != nil {
		t.Fatalf("Hangup 失敗: %v", err)
	}
	ev := nextEvent(t, m)
	if ended, ok := ev.(call.EvEnded); !ok || ended.Reason != "bye" {
		t.Fatalf("通話中の Hangup は bye: %+v", ev)
	}
}

func TestAnswerUnknownCall(t *testing.T) {
	m, _ := newManager(t)
	if err := m.Answer("nope", 0); err == nil {
		t.Errorf("未知の着信への Answer は失敗するはず")
	}
	if err := m.Hangup("nope"); err == nil {
		t.Errorf("未知の通話への Hangup は失敗するはず")
	}
	if _, err := m.Dial(""); err == nil {
		t.Errorf("空の発信先は失敗するはず")
	}
}

// 受信者が居ないときの取りこぼしが記録されること (#5)。
func TestEmitRecordsDroppedEvents(t *testing.T) {
	m, _ := newManager(t)
	// Events() を誰も読まないので、バッファを超えた分は捨てられる。
	for i := 0; i < call.EventQueueSize+3; i++ {
		m.EmitForTest(call.EvRegistered{OK: true})
	}
	// Backend の登録イベントが先にバッファへ入る分だけ多くなり得る。
	if got := m.DroppedEvents(); got < 3 {
		t.Fatalf("DroppedEvents = %d, want >= 3", got)
	}
}

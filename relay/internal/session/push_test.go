package session_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tmlksu/sipbridge/relay/internal/proto"
	"github.com/tmlksu/sipbridge/relay/internal/push"
)

// recordingPusher は Send の呼び出しを記録する検証用 Pusher である。
type recordingPusher struct {
	mu    sync.Mutex
	calls []pushCall
	ch    chan pushCall
}

type pushCall struct {
	tokens  []string
	payload push.Payload
}

func newRecordingPusher() *recordingPusher {
	return &recordingPusher{ch: make(chan pushCall, 8)}
}

func (p *recordingPusher) Send(_ context.Context, tokens []string, pl push.Payload) error {
	c := pushCall{tokens: append([]string(nil), tokens...), payload: pl}
	p.mu.Lock()
	p.calls = append(p.calls, c)
	p.mu.Unlock()
	select {
	case p.ch <- c:
	default:
	}
	return nil
}

// count は記録済みの Send 呼び出し回数である。
func (p *recordingPusher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

// wait は Send が呼ばれるまで待つ (Send は非同期実行のため)。
func (p *recordingPusher) wait(t *testing.T, d time.Duration) pushCall {
	t.Helper()
	select {
	case c := <-p.ch:
		return c
	case <-time.After(d):
		t.Fatalf("push が %v 以内に送られない", d)
		return pushCall{}
	}
}

// registerPush は register_push を送り、ping/pong で処理完了を待つ。
func registerPush(t *testing.T, ws *websocket.Conn, token string, ts int64) {
	t.Helper()
	writeJSON(t, ws, &proto.RegisterPush{T: proto.TRegisterPush, Provider: "fcm", Token: token})
	writeJSON(t, ws, &proto.Ping{T: proto.TPing, Ts: ts})
	pong, ok := readJSON(t, ws).(*proto.Pong)
	if !ok || pong.Ts != ts {
		t.Fatalf("pong が不正: %+v", pong)
	}
}

// waitSessions は WS 接続数が want になるまで待つ (切断処理は非同期のため)。
func waitSessions(t *testing.T, fx *fixture, want int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fx.hub.SessionCount() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("接続数が %d にならない (現在 %d)", want, fx.hub.SessionCount())
}

// TestPushOnlyToOfflineDevices は着信 push が
// 「登録済みかつ WS 未接続」のデバイスだけに送られることを確認する。
// dev-A は push 登録後に切断、dev-B は接続したまま。
func TestPushOnlyToOfflineDevices(t *testing.T) {
	rp := newRecordingPusher()
	fx := newFixtureWithPusher(t, rp)

	a := dial(t, fx.url, "dev-A")
	if _, ok := readJSON(t, a).(*proto.Hello); !ok {
		t.Fatalf("A に hello が来ない")
	}
	registerPush(t, a, "tok-A", 1)
	_ = a.Close(websocket.StatusNormalClosure, "")

	b := dial(t, fx.url, "dev-B")
	defer b.Close(websocket.StatusNormalClosure, "")
	if _, ok := readJSON(t, b).(*proto.Hello); !ok {
		t.Fatalf("B に hello が来ない")
	}
	registerPush(t, b, "tok-B", 2)
	waitSessions(t, fx, 1, 3*time.Second) // A の切断が反映されるまで

	fx.fb(t).InjectIncoming("102", "Bob", 0)
	inc, ok := readJSON(t, b).(*proto.Incoming)
	if !ok {
		t.Fatalf("B に incoming が来ない")
	}

	c := rp.wait(t, 3*time.Second)
	if len(c.tokens) != 1 || c.tokens[0] != "tok-A" {
		t.Fatalf("push 先が不正: %v (tok-A のみのはず)", c.tokens)
	}
	if c.payload.Type != "incoming" || c.payload.CallID != inc.CallID ||
		c.payload.From != "102" || c.payload.Display != "Bob" {
		t.Fatalf("payload が不正: %+v", c.payload)
	}
}

// TestNoPushWhenAllDevicesOnline は全登録デバイスが接続中なら
// push を送らないことを確認する。
func TestNoPushWhenAllDevicesOnline(t *testing.T) {
	rp := newRecordingPusher()
	fx := newFixtureWithPusher(t, rp)

	a := dial(t, fx.url, "dev-A")
	defer a.Close(websocket.StatusNormalClosure, "")
	if _, ok := readJSON(t, a).(*proto.Hello); !ok {
		t.Fatalf("A に hello が来ない")
	}
	registerPush(t, a, "tok-A", 1)

	fx.fb(t).InjectIncoming("102", "Bob", 0)
	if _, ok := readJSON(t, a).(*proto.Incoming); !ok {
		t.Fatalf("A に incoming が来ない")
	}

	select {
	case c := <-rp.ch:
		t.Fatalf("接続中デバイスに push が送られた: %v", c.tokens)
	case <-time.After(300 * time.Millisecond):
	}
	if n := rp.count(); n != 0 {
		t.Fatalf("Send 呼び出し回数 = %d (0 のはず)", n)
	}
}

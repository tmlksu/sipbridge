package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tmlksu/sipbridge/relay/internal/auth"
	"github.com/tmlksu/sipbridge/relay/internal/call"
	"github.com/tmlksu/sipbridge/relay/internal/fakebackend"
	"github.com/tmlksu/sipbridge/relay/internal/proto"
	"github.com/tmlksu/sipbridge/relay/internal/push"
	"github.com/tmlksu/sipbridge/relay/internal/session"
	"github.com/tmlksu/sipbridge/relay/internal/state"
)

// TestMuxAuthAndHello は実配線 (認証→WS→hello) の確認である。
func TestMuxAuthAndHello(t *testing.T) {
	factory := func(user, password, display string) (call.Backend, error) {
		return fakebackend.New(), nil
	}
	store, _ := state.New("")
	hub := session.NewHub(factory, push.Noop{}, store, session.Config{
		Version: "test", DefaultAccount: "101", DefaultPassword: "pw",
	}, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := hub.Run(ctx); err != nil {
		t.Fatalf("hub.Run 失敗: %v", err)
	}
	mux := buildMux(auth.NewTokenAuth("x"), hub)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if code := get(t, srv.URL+"/healthz"); code != 200 {
		t.Errorf("/healthz = %d", code)
	}
	if code := get(t, srv.URL+"/v1/session"); code != 401 {
		t.Errorf("認証なし /v1/session = %d (401 のはず)", code)
	}

	wsURL := "ws://" + srv.Listener.Addr().String() + "/v1/session"
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	ws, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": {"Bearer x"},
			"X-Device-Id":   {"dev-1"},
		},
	})
	if err != nil {
		t.Fatalf("WS 接続失敗: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	typ, data, err := ws.Read(readCtx)
	if err != nil || typ != websocket.MessageText {
		t.Fatalf("hello 受信失敗: %v %v", typ, err)
	}
	msg, err := proto.Decode(data)
	if err != nil {
		t.Fatalf("Decode 失敗: %v", err)
	}
	hello, ok := msg.(*proto.Hello)
	if !ok || hello.Extension != "101" || hello.Account != "101" {
		t.Fatalf("hello が不正: %+v", msg)
	}
	if hello.RelayVersion != "test" {
		t.Errorf("relayVersion = %q", hello.RelayVersion)
	}
}

func get(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // テスト用
	if err != nil {
		t.Fatalf("GET %s 失敗: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

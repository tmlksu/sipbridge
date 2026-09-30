package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

// fixedAuth は常に指定の principal で認証を通す (principal の受け渡し確認用)。
type fixedAuth struct{ principal string }

func (a fixedAuth) Authenticate(*http.Request) (string, error) { return a.principal, nil }
func (a fixedAuth) Mode() string                               { return "test" }

// TestMuxPassesPrincipal は Authenticate の principal が ServeWS に届き、
// DEVICE_BINDING=enforce で記録と異なる principal が 409 device_binding_mismatch、
// principal の無い接続 (cf-access 相当) が 401 になることを確認する。
// また認証失敗の応答本文に失敗理由を載せないことを確認する。
func TestMuxPassesPrincipal(t *testing.T) {
	factory := func(user, password, display string) (call.Backend, error) {
		return fakebackend.New(), nil
	}
	store, _ := state.New("")
	if err := store.SetDevicePush("dev-1", state.Push{Provider: "fcm", Token: "tok"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PinDevicePrincipal("dev-1", "cn:bob.access"); err != nil {
		t.Fatal(err)
	}
	hub := session.NewHub(factory, push.Noop{}, store, session.Config{
		Version: "test", DeviceBinding: session.DeviceBindingEnforce, RequirePrincipal: true,
	}, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := hub.Run(ctx); err != nil {
		t.Fatalf("hub.Run 失敗: %v", err)
	}
	wsURL := func(srv *httptest.Server) string { return "ws://" + srv.Listener.Addr().String() + "/v1/session" }
	dialDev := func(srv *httptest.Server) (*websocket.Conn, *http.Response, error) {
		dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dcancel()
		return websocket.Dial(dctx, wsURL(srv), &websocket.DialOptions{
			HTTPHeader: http.Header{"X-Device-Id": {"dev-1"}},
		})
	}

	alice := httptest.NewServer(buildMux(fixedAuth{"cn:alice.access"}, hub))
	defer alice.Close()
	_, resp, err := dialDev(alice)
	if err == nil || resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("別 principal は 409 のはず: err=%v resp=%v", err, resp)
	}
	if b, _ := io.ReadAll(resp.Body); strings.TrimSpace(string(b)) != session.DeviceBindingMismatchBody {
		t.Errorf("409 の本文 = %q (device_binding_mismatch のはず)", b)
	}
	anon := httptest.NewServer(buildMux(fixedAuth{""}, hub))
	defer anon.Close()
	if _, resp, err := dialDev(anon); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("principal の無い JWT (enforce) は 401 のはず: err=%v resp=%v", err, resp)
	}
	bob := httptest.NewServer(buildMux(fixedAuth{"cn:bob.access"}, hub))
	defer bob.Close()
	ws, _, err := dialDev(bob)
	if err != nil {
		t.Fatalf("記録どおりの principal が拒否された: %v", err)
	}
	_ = ws.Close(websocket.StatusNormalClosure, "")

	tok := httptest.NewServer(buildMux(auth.NewTokenAuth("x"), hub))
	defer tok.Close()
	resp, err = http.Get(tok.URL + "/v1/session") //nolint:gosec,noctx // テスト用
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || strings.TrimSpace(string(body)) != "unauthorized" {
		t.Errorf("認証失敗の応答が不正: %d %q", resp.StatusCode, body)
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

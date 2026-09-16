package push_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"

	"github.com/tmlksu/sipbridge/relay/internal/push"
)

// fakeFCM は FCM HTTP v1 を模擬するサーバである。
// トークンごとに成功/UNREGISTERED/500 を返し、受けたリクエストを記録する。
type fakeFCM struct {
	t        *testing.T
	mu       sync.Mutex
	requests []recordedRequest
}

type recordedRequest struct {
	auth     string
	path     string
	token    string
	data     map[string]string
	priority string
	ttl      string
}

func (f *fakeFCM) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Message struct {
			Token   string            `json:"token"`
			Data    map[string]string `json:"data"`
			Android *struct {
				Priority string `json:"priority"`
				TTL      string `json:"ttl"`
			} `json:"android"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	rec := recordedRequest{
		auth:  r.Header.Get("Authorization"),
		path:  r.URL.Path,
		token: req.Message.Token,
		data:  req.Message.Data,
	}
	if req.Message.Android != nil {
		rec.priority = req.Message.Android.Priority
		rec.ttl = req.Message.Android.TTL
	}
	f.mu.Lock()
	f.requests = append(f.requests, rec)
	f.mu.Unlock()

	switch req.Message.Token {
	case "gone-token":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Requested entity was not found.",` +
			`"status":"UNREGISTERED","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError",` +
			`"errorCode":"UNREGISTERED"}]}}`))
	case "broken-token":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":500,"message":"Internal error.","status":"INTERNAL"}}`))
	default:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"projects/demo-proj/messages/12345"}`))
	}
}

func (f *fakeFCM) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeFCM) last() recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

// fakeRemover は push.TokenRemover のテスト実装である
// (本番は internal/state.Store が実装する)。
type fakeRemover struct {
	mu     sync.Mutex
	tokens map[string]bool
}

func newFakeRemover(tokens ...string) *fakeRemover {
	r := &fakeRemover{tokens: make(map[string]bool, len(tokens))}
	for _, t := range tokens {
		r.tokens[t] = true
	}
	return r
}

func (r *fakeRemover) RemoveToken(token string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tokens, token)
	return nil
}

func (r *fakeRemover) has(token string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tokens[token]
}

// newTestFCM は静的 OAuth2 トークンと httptest エンドポイントで FCM を作る。
func newTestFCM(t *testing.T, fake *fakeFCM, store push.TokenRemover) (*push.FCM, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)
	fcm, err := push.NewFCM(push.FCMConfig{
		ProjectID:   "demo-proj",
		Endpoint:    srv.URL,
		TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-access-token"}),
		Store:       store,
	})
	if err != nil {
		t.Fatalf("NewFCM 失敗: %v", err)
	}
	return fcm, srv
}

func TestNewFCMValidation(t *testing.T) {
	if _, err := push.NewFCM(push.FCMConfig{}); err == nil {
		t.Errorf("ProjectID 空でもエラーにならない")
	}
	if _, err := push.NewFCM(push.FCMConfig{ProjectID: "p"}); err == nil {
		t.Errorf("認証情報なしでもエラーにならない")
	}
	if _, err := push.NewFCM(push.FCMConfig{
		ProjectID:          "p",
		ServiceAccountFile: "/nonexistent/sa.json",
	}); err == nil {
		t.Errorf("存在しないサービスアカウントでもエラーにならない")
	}
	if _, err := push.NewFCM(push.FCMConfig{
		ProjectID:          "p",
		ServiceAccountJSON: []byte("not-json"),
	}); err == nil {
		t.Errorf("不正なサービスアカウント JSON でもエラーにならない")
	}
}

func TestFCMSendSuccess(t *testing.T) {
	rm := newFakeRemover("ok-token")
	fake := &fakeFCM{t: t}
	fcm, _ := newTestFCM(t, fake, rm)

	p := push.Payload{Type: "incoming", CallID: "c1", From: "09011112222", Display: "テスト"}
	if err := fcm.Send(context.Background(), []string{"ok-token"}, p); err != nil {
		t.Fatalf("Send 失敗: %v", err)
	}
	if got := fake.count(); got != 1 {
		t.Fatalf("リクエスト数 = %d (1 のはず)", got)
	}
	rec := fake.last()
	if rec.auth != "Bearer test-access-token" {
		t.Errorf("Authorization = %q", rec.auth)
	}
	if rec.path != "/v1/projects/demo-proj/messages:send" {
		t.Errorf("パス = %q", rec.path)
	}
	if rec.token != "ok-token" {
		t.Errorf("token = %q", rec.token)
	}
	for k, want := range map[string]string{
		"type": "incoming", "callId": "c1", "caller": "09011112222", "display": "テスト",
	} {
		if rec.data[k] != want {
			t.Errorf("data[%q] = %q (%q のはず)", k, rec.data[k], want)
		}
	}
	if rec.priority != "HIGH" {
		t.Errorf("android priority = %q (HIGH のはず)", rec.priority)
	}
	if rec.ttl != "30s" {
		t.Errorf("android ttl = %q (30s のはず)", rec.ttl)
	}
	// 成功トークンは残る。
	if !rm.has("ok-token") {
		t.Errorf("成功トークンが削除されている")
	}
}

func TestFCMSendUnregisteredRemovesToken(t *testing.T) {
	rm := newFakeRemover("gone-token")
	fake := &fakeFCM{t: t}
	fcm, _ := newTestFCM(t, fake, rm)

	// UNREGISTERED は削除してエラーにしない (呼び出し側は通常継続する)。
	if err := fcm.Send(context.Background(), []string{"gone-token"},
		push.Payload{Type: "incoming", CallID: "c9"}); err != nil {
		t.Fatalf("UNREGISTERED で Send がエラーを返した: %v", err)
	}
	if rm.has("gone-token") {
		t.Errorf("UNREGISTERED トークンが残っている")
	}
	if got := fake.count(); got != 1 {
		t.Errorf("リクエスト数 = %d (1 のはず)", got)
	}
}

func TestFCMSendMixed(t *testing.T) {
	rm := newFakeRemover("gone-token", "ok-token")
	fake := &fakeFCM{t: t}
	fcm, _ := newTestFCM(t, fake, rm)

	if err := fcm.Send(context.Background(), []string{"gone-token", "ok-token", ""},
		push.Payload{Type: "incoming", CallID: "c2"}); err != nil {
		t.Fatalf("Send 失敗: %v", err)
	}
	if rm.has("gone-token") {
		t.Errorf("無効トークンが残っている")
	}
	if !rm.has("ok-token") {
		t.Errorf("有効トークンが消えている")
	}
	if got := fake.count(); got != 2 {
		t.Errorf("リクエスト数 = %d (空トークンを除き 2 のはず)", got)
	}
}

func TestFCMSendServerError(t *testing.T) {
	rm := newFakeRemover("broken-token")
	fake := &fakeFCM{t: t}
	fcm, _ := newTestFCM(t, fake, rm)

	if err := fcm.Send(context.Background(), []string{"broken-token"},
		push.Payload{Type: "incoming"}); err == nil {
		t.Errorf("500 でもエラーにならない")
	} else if !strings.Contains(err.Error(), "500") {
		t.Errorf("500 がエラー文に含まれない: %v", err)
	}
	// 500 は無効トークンではないので残る。
	if !rm.has("broken-token") {
		t.Errorf("500 でトークンが消えている (UNREGISTERED のみ削除のはず)")
	}
}

func TestFCMSendEmpty(t *testing.T) {
	fake := &fakeFCM{t: t}
	fcm, _ := newTestFCM(t, fake, newFakeRemover())
	if err := fcm.Send(context.Background(), nil, push.Payload{Type: "incoming"}); err != nil {
		t.Fatalf("空トークンでエラー: %v", err)
	}
	if got := fake.count(); got != 0 {
		t.Errorf("空トークンでリクエストが発生した: %d", got)
	}
}

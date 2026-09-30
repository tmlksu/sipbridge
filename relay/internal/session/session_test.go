package session_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tmlksu/sipbridge/relay/internal/call"
	"github.com/tmlksu/sipbridge/relay/internal/fakebackend"
	"github.com/tmlksu/sipbridge/relay/internal/proto"
	"github.com/tmlksu/sipbridge/relay/internal/push"
	"github.com/tmlksu/sipbridge/relay/internal/session"
	"github.com/tmlksu/sipbridge/relay/internal/state"
)

// fixture はテスト用の Hub 一式である。BackendFactory は account ごとに
// fakebackend を作り、テストから取り出せるよう記録する。
type fixture struct {
	mu    sync.Mutex
	fakes map[string]*fakebackend.Fake // account → 最新の fakebackend
	made  map[string]int               // account → Backend 生成回数
	// unregistered に入れた account は登録失敗状態の Backend を作る。
	unregistered map[string]bool

	hub   *session.Hub
	srv   *httptest.Server
	url   string
	store *state.Store
}

// defaultTestAccount は既定アカウント (SIP_USER 相当) のテスト値である。
const (
	defaultTestAccount  = "101"
	defaultTestPassword = "pw101"
)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWithPusher(t, push.Noop{})
}

// newFixtureWithPusher は Pusher を差し替えられる版である。
func newFixtureWithPusher(t *testing.T, pusher push.Pusher) *fixture {
	t.Helper()
	fx := newFixtureCfg(t, pusher, nil, session.Config{
		DefaultAccount:  defaultTestAccount,
		DefaultPassword: defaultTestPassword,
	})
	fx.waitRegistered(t, defaultTestAccount)
	return fx
}

// newMultiFixture は既定アカウント無し (端末が sip_account を送る) の Hub である。
func newMultiFixture(t *testing.T, store *state.Store) *fixture {
	t.Helper()
	return newFixtureCfg(t, push.Noop{}, store, session.Config{})
}

func newFixtureCfg(t *testing.T, pusher push.Pusher, store *state.Store, cfg session.Config) *fixture {
	t.Helper()
	return newFixtureLog(t, pusher, store, cfg, slog.Default())
}

// newFixtureLog はログ出力先を差し替えられる版である (call_stats の検証用)。
func newFixtureLog(t *testing.T, pusher push.Pusher, store *state.Store, cfg session.Config, log *slog.Logger) *fixture {
	t.Helper()
	if store == nil {
		var err error
		if store, err = state.New(""); err != nil {
			t.Fatalf("state.New 失敗: %v", err)
		}
	}
	fx := &fixture{
		fakes:        make(map[string]*fakebackend.Fake),
		made:         make(map[string]int),
		unregistered: make(map[string]bool),
		store:        store,
	}
	factory := func(user, password, display string) (call.Backend, error) {
		fx.mu.Lock()
		fb := fakebackend.New()
		if fx.unregistered[user] {
			fb = fakebackend.NewUnregistered()
		}
		fx.fakes[user] = fb
		fx.made[user]++
		fx.mu.Unlock()
		return fb, nil
	}
	cfg.Version = "test"
	if cfg.PingInterval == 0 {
		cfg.PingInterval = 30 * time.Second
	}
	if cfg.ResumeTimeout == 0 {
		cfg.ResumeTimeout = 200 * time.Millisecond
	}
	hub := session.NewHub(factory, pusher, store, cfg, log)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := hub.Run(ctx); err != nil {
		t.Fatalf("hub.Run 失敗: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(hub.ServeWS))
	t.Cleanup(srv.Close)
	fx.hub = hub
	fx.srv = srv
	fx.url = "ws://" + srv.Listener.Addr().String() + "/v1/session"
	return fx
}

// backend は account の fakebackend を返す (生成されるまで待つ)。
func (fx *fixture) backend(t *testing.T, account string) *fakebackend.Fake {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		fx.mu.Lock()
		fb := fx.fakes[account]
		fx.mu.Unlock()
		if fb != nil {
			return fb
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("account %q の Backend が作られない", account)
	return nil
}

// backendCount は account の Backend 生成回数である (作り直しの確認用)。
func (fx *fixture) backendCount(account string) int {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return fx.made[account]
}

// fb は既定アカウントの fakebackend である。
func (fx *fixture) fb(t *testing.T) *fakebackend.Fake {
	t.Helper()
	return fx.backend(t, defaultTestAccount)
}

// waitRegistered は account が REGISTER 済みになるまで待つ
// (fakebackend の EvRegistered は非同期に処理されるため)。
func (fx *fixture) waitRegistered(t *testing.T, account string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fx.hub.AccountRegistered(account) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("account %q が登録済みにならない", account)
}

func dial(t *testing.T, url, device string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opts := &websocket.DialOptions{HTTPHeader: http.Header{"X-Device-Id": {device}}}
	ws, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		t.Fatalf("WS 接続失敗: %v", err)
	}
	return ws
}

func readMsg(t *testing.T, ws *websocket.Conn) (websocket.MessageType, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("WS 読み取り失敗: %v", err)
	}
	return typ, data
}

func readJSON(t *testing.T, ws *websocket.Conn) any {
	t.Helper()
	typ, data := readMsg(t, ws)
	if typ != websocket.MessageText {
		t.Fatalf("テキストフレームのはずが %v", typ)
	}
	msg, err := proto.Decode(data)
	if err != nil {
		t.Fatalf("Decode 失敗: %v", err)
	}
	return msg
}

func writeJSON(t *testing.T, ws *websocket.Conn, v any) {
	t.Helper()
	data, err := proto.Encode(v)
	if err != nil {
		t.Fatalf("Encode 失敗: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("WS 書き込み失敗: %v", err)
	}
}

// TestFullCallFlow は受け入れの結合シナリオである:
// 接続 → hello → InjectIncoming → incoming → answer → answered →
// バイナリ往復 (エコー) → hangup → ended。
func TestFullCallFlow(t *testing.T) {
	fx := newFixture(t)
	ws := dial(t, fx.url, "dev-A")
	defer ws.Close(websocket.StatusNormalClosure, "")

	hello, ok := readJSON(t, ws).(*proto.Hello)
	if !ok {
		t.Fatalf("最初は hello のはず")
	}
	if !hello.Registered || hello.Extension != "101" || hello.Account != "101" || hello.Call != nil {
		t.Fatalf("hello が不正: %+v", hello)
	}

	fx.fb(t).InjectIncoming("102", "Bob", 0)
	inc, ok := readJSON(t, ws).(*proto.Incoming)
	if !ok {
		t.Fatalf("incoming のはず")
	}
	if inc.From != "102" || inc.Display != "Bob" {
		t.Fatalf("incoming が不正: %+v", inc)
	}

	writeJSON(t, ws, &proto.Answer{T: proto.TAnswer, CallID: inc.CallID})
	ans, ok := readJSON(t, ws).(*proto.Answered)
	if !ok || ans.CallID != inc.CallID {
		t.Fatalf("answered のはず: %+v", ans)
	}

	// バイナリ往復 (fake はエコー)。
	rtp := []byte{0x80, 0x00, 0x12, 0x34, 1, 2, 3, 4}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageBinary, rtp); err != nil {
		t.Fatalf("バイナリ送信失敗: %v", err)
	}
	typ, echo := readMsg(t, ws)
	if typ != websocket.MessageBinary || string(echo) != string(rtp) {
		t.Fatalf("エコーが戻らない: typ=%v data=%v", typ, echo)
	}

	writeJSON(t, ws, &proto.Hangup{T: proto.THangup, CallID: inc.CallID})
	end, ok := readJSON(t, ws).(*proto.Ended)
	if !ok || end.Reason != "bye" {
		t.Fatalf("ended(bye) のはず: %+v", end)
	}
}

// TestFirstAnswerWins は複数セッションへのファンアウトと勝者決定である。
func TestFirstAnswerWins(t *testing.T) {
	fx := newFixture(t)
	a := dial(t, fx.url, "dev-A")
	defer a.Close(websocket.StatusNormalClosure, "")
	b := dial(t, fx.url, "dev-B")
	defer b.Close(websocket.StatusNormalClosure, "")
	if _, ok := readJSON(t, a).(*proto.Hello); !ok {
		t.Fatalf("A に hello が来ない")
	}
	if _, ok := readJSON(t, b).(*proto.Hello); !ok {
		t.Fatalf("B に hello が来ない")
	}

	fx.fb(t).InjectIncoming("103", "", 8)
	incA, ok := readJSON(t, a).(*proto.Incoming)
	if !ok {
		t.Fatalf("A に incoming が来ない")
	}
	if _, ok := readJSON(t, b).(*proto.Incoming); !ok {
		t.Fatalf("B に incoming が来ない")
	}

	writeJSON(t, a, &proto.Answer{T: proto.TAnswer, CallID: incA.CallID})
	if _, ok := readJSON(t, a).(*proto.Answered); !ok {
		t.Fatalf("A に answered が来ない")
	}
	end, ok := readJSON(t, b).(*proto.Ended)
	if !ok || end.Reason != "answered_elsewhere" {
		t.Fatalf("B には answered_elsewhere のはず: %+v", end)
	}

	// 敗者のバイナリは中継されない。勝者のみ疎通する。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = b.Write(ctx, websocket.MessageBinary, []byte{9, 9, 9})
	_ = a.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3})
	typ, data := readMsg(t, a)
	if typ != websocket.MessageBinary || string(data) != string([]byte{1, 2, 3}) {
		t.Fatalf("勝者のエコーが不正: %v %v", typ, data)
	}

	writeJSON(t, a, &proto.Hangup{T: proto.THangup, CallID: incA.CallID})
	if _, ok := readJSON(t, a).(*proto.Ended); !ok {
		t.Fatalf("A に ended が来ない")
	}
	if _, ok := readJSON(t, b).(*proto.Ended); !ok {
		t.Fatalf("B に ended が来ない")
	}
}

// TestPingAndRegisterPush は ping/pong と push 登録である。
func TestPingAndRegisterPush(t *testing.T) {
	fx := newFixture(t)
	ws := dial(t, fx.url, "dev-A")
	defer ws.Close(websocket.StatusNormalClosure, "")
	if _, ok := readJSON(t, ws).(*proto.Hello); !ok {
		t.Fatalf("hello が来ない")
	}
	writeJSON(t, ws, &proto.Ping{T: proto.TPing, Ts: 123})
	pong, ok := readJSON(t, ws).(*proto.Pong)
	if !ok || pong.Ts != 123 {
		t.Fatalf("pong が不正: %+v", pong)
	}
	writeJSON(t, ws, &proto.RegisterPush{T: proto.TRegisterPush, Provider: "fcm", Token: "tok-abc"})
	// 応答は無い。次の ping で疎通確認する。
	writeJSON(t, ws, &proto.Ping{T: proto.TPing, Ts: 124})
	if _, ok := readJSON(t, ws).(*proto.Pong); !ok {
		t.Fatalf("2 回目の pong が来ない")
	}
}

// TestMissingDeviceID は X-Device-Id 無しで 400 になることである。
func TestMissingDeviceID(t *testing.T) {
	fx := newFixture(t)
	httpURL := "http://" + fx.srv.Listener.Addr().String() + "/v1/session"
	resp, err := http.Get(httpURL)
	if err != nil {
		t.Fatalf("GET 失敗: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d (400 のはず)", resp.StatusCode)
	}
}

// TestResumeTimerDoesNotKillNextCall は、勝者切断の猶予中に呼が終わり
// 次の着信が来ても、残った猶予タイマが次の呼を切らないことを確かめる。
func TestResumeTimerDoesNotKillNextCall(t *testing.T) {
	const resume = 300 * time.Millisecond
	fx := newFixtureCfg(t, push.Noop{}, nil, session.Config{
		DefaultAccount:  defaultTestAccount,
		DefaultPassword: defaultTestPassword,
		ResumeTimeout:   resume,
	})
	fx.waitRegistered(t, defaultTestAccount)

	a := dial(t, fx.url, "dev-A")
	b := dial(t, fx.url, "dev-B")
	defer b.Close(websocket.StatusNormalClosure, "")
	readJSON(t, a) // hello
	readJSON(t, b) // hello

	id1 := fx.fb(t).InjectIncoming("102", "Bob", 0)
	readJSON(t, a) // incoming
	readJSON(t, b) // incoming
	writeJSON(t, a, &proto.Answer{T: proto.TAnswer, CallID: id1})
	readJSON(t, a) // answered
	readJSON(t, b) // ended(answered_elsewhere)

	// 勝者 A が落ちる → 猶予タイマ開始。猶予内に相手が切る。
	_ = a.CloseNow()
	time.Sleep(resume / 3)
	fx.fb(t).InjectRemoteHangup(id1)
	if end, ok := readJSON(t, b).(*proto.Ended); !ok || end.CallID != id1 {
		t.Fatalf("1 本目の ended のはず: %+v", end)
	}

	// 猶予の残り時間内に次の着信。猶予満了を過ぎても鳴り続けること。
	id2 := fx.fb(t).InjectIncoming("103", "Carol", 0)
	if inc, ok := readJSON(t, b).(*proto.Incoming); !ok || inc.CallID != id2 {
		t.Fatalf("2 本目の incoming のはず: %+v", inc)
	}
	ctx, cancel := context.WithTimeout(context.Background(), resume*2)
	defer cancel()
	if _, data, err := b.Read(ctx); err == nil {
		t.Fatalf("2 本目の呼に余計なメッセージ (古い猶予タイマで切られた?): %s", data)
	}
}

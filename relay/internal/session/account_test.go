package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tmlksu/sipbridge/relay/internal/proto"
	"github.com/tmlksu/sipbridge/relay/internal/state"
)

// sendSipAccount は sip_account を送り、受理後の hello を返す。
// 途中の registration は読み飛ばす。error が来たらテスト失敗。
func sendSipAccount(t *testing.T, ws *websocket.Conn, user, password, display string) *proto.Hello {
	t.Helper()
	writeJSON(t, ws, &proto.SipAccount{
		T: proto.TSipAccount, User: user, Password: password, Display: display,
	})
	for i := 0; i < 10; i++ {
		switch m := readJSON(t, ws).(type) {
		case *proto.Hello:
			return m
		case *proto.Registration:
			continue
		case *proto.Error:
			t.Fatalf("sip_account が拒否された: %+v", m)
		default:
			t.Fatalf("hello のはずが %T", m)
		}
	}
	t.Fatalf("hello が来ない")
	return nil
}

// expectError は次に来る error メッセージを読む (registration は読み飛ばす)。
func expectError(t *testing.T, ws *websocket.Conn) *proto.Error {
	t.Helper()
	for i := 0; i < 10; i++ {
		switch m := readJSON(t, ws).(type) {
		case *proto.Error:
			return m
		case *proto.Registration:
			continue
		default:
			t.Fatalf("error のはずが %T (%+v)", m, m)
		}
	}
	t.Fatalf("error が来ない")
	return nil
}

// readSkipReg は registration を読み飛ばして次のメッセージを返す。
func readSkipReg(t *testing.T, ws *websocket.Conn) any {
	t.Helper()
	for i := 0; i < 10; i++ {
		msg := readJSON(t, ws)
		if _, ok := msg.(*proto.Registration); ok {
			continue
		}
		return msg
	}
	t.Fatalf("registration 以外のメッセージが来ない")
	return nil
}

// waitAccountStopped は account のグループが停止するまで待つ。
func waitAccountStopped(t *testing.T, fx *fixture, account string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !fx.hub.HasAccount(account) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("account %q のグループが停止しない", account)
}

// TestTwoAccountsIsolation は 2 account × 2 device で、着信が
// 正しいグループの端末だけに届くことを確認する。
func TestTwoAccountsIsolation(t *testing.T) {
	fx := newMultiFixture(t, nil)

	a := dial(t, fx.url, "dev-A")
	defer a.Close(websocket.StatusNormalClosure, "")
	hello, ok := readSkipReg(t, a).(*proto.Hello)
	if !ok || hello.Account != "" {
		t.Fatalf("既定アカウント無しでは account は空のはず: %+v", hello)
	}
	if h := sendSipAccount(t, a, "101", "pw101", "居間"); h.Account != "101" || h.Extension != "101" {
		t.Fatalf("A の hello.account が不正: %+v", h)
	}

	b := dial(t, fx.url, "dev-B")
	defer b.Close(websocket.StatusNormalClosure, "")
	if _, ok := readSkipReg(t, b).(*proto.Hello); !ok {
		t.Fatalf("B に hello が来ない")
	}
	if h := sendSipAccount(t, b, "102", "pw102", ""); h.Account != "102" {
		t.Fatalf("B の hello.account が不正: %+v", h)
	}
	fx.waitRegistered(t, "101")
	fx.waitRegistered(t, "102")

	// 101 への着信は A だけに届く。102 への着信は B だけに届く。
	// WS のフレーム順は保たれるので、各接続の「次のメッセージ」が
	// 自分の account の着信であることを見れば混線していないと分かる。
	fx.backend(t, "101").InjectIncoming("900", "Caller1", 0)
	fx.backend(t, "102").InjectIncoming("901", "Caller2", 0)
	incA, ok := readSkipReg(t, a).(*proto.Incoming)
	if !ok || incA.From != "900" {
		t.Fatalf("A の incoming が不正 (900 のはず): %+v", incA)
	}
	incB, ok := readSkipReg(t, b).(*proto.Incoming)
	if !ok || incB.From != "901" {
		t.Fatalf("B の incoming が不正 (901 のはず): %+v", incB)
	}

	// それぞれ独立に応答・メディア中継できる。
	writeJSON(t, a, &proto.Answer{T: proto.TAnswer, CallID: incA.CallID})
	if ans, ok := readSkipReg(t, a).(*proto.Answered); !ok || ans.CallID != incA.CallID {
		t.Fatalf("A の answered が不正: %+v", ans)
	}
	writeJSON(t, b, &proto.Answer{T: proto.TAnswer, CallID: incB.CallID})
	if ans, ok := readSkipReg(t, b).(*proto.Answered); !ok || ans.CallID != incB.CallID {
		t.Fatalf("B の answered が不正: %+v", ans)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3}); err != nil {
		t.Fatalf("A のバイナリ送信失敗: %v", err)
	}
	if err := b.Write(ctx, websocket.MessageBinary, []byte{9, 9, 9}); err != nil {
		t.Fatalf("B のバイナリ送信失敗: %v", err)
	}
	typ, echo := readMsg(t, a)
	if typ != websocket.MessageBinary || string(echo) != string([]byte{1, 2, 3}) {
		t.Fatalf("A のエコーが不正 (他グループのメディアが混ざった?): %v %v", typ, echo)
	}
	typ, echo = readMsg(t, b)
	if typ != websocket.MessageBinary || string(echo) != string([]byte{9, 9, 9}) {
		t.Fatalf("B のエコーが不正 (他グループのメディアが混ざった?): %v %v", typ, echo)
	}

	// A の切断は B の通話に影響しない。
	writeJSON(t, a, &proto.Hangup{T: proto.THangup, CallID: incA.CallID})
	if end, ok := readSkipReg(t, a).(*proto.Ended); !ok || end.Reason != "bye" || end.CallID != incA.CallID {
		t.Fatalf("A の ended が不正: %+v", end)
	}
	writeJSON(t, b, &proto.Hangup{T: proto.THangup, CallID: incB.CallID})
	end, ok := readSkipReg(t, b).(*proto.Ended)
	if !ok || end.CallID != incB.CallID {
		t.Fatalf("B の ended が不正 (A の ended が漏れた?): %+v", end)
	}
}

// TestNoAccountOperations は account 無しの端末が通話操作を送ると
// error no_account になることを確認する。
func TestNoAccountOperations(t *testing.T) {
	fx := newMultiFixture(t, nil)
	ws := dial(t, fx.url, "dev-X")
	defer ws.Close(websocket.StatusNormalClosure, "")
	if h, ok := readSkipReg(t, ws).(*proto.Hello); !ok || h.Account != "" {
		t.Fatalf("hello.account は空のはず: %+v", h)
	}
	for _, msg := range []any{
		&proto.Dial{T: proto.TDial, To: "102"},
		&proto.Answer{T: proto.TAnswer, CallID: "c1"},
		&proto.Reject{T: proto.TReject, CallID: "c1"},
		&proto.Hangup{T: proto.THangup, CallID: "c1"},
	} {
		writeJSON(t, ws, msg)
		if e := expectError(t, ws); e.Code != "no_account" {
			t.Errorf("%T の error.code = %q (no_account のはず)", msg, e.Code)
		}
	}
	// register_push は account 無しでも受け付ける。
	registerPush(t, ws, "tok-X", 1)
	if d, ok := fx.store.Device("dev-X"); !ok || d.Push == nil || d.Push.Token != "tok-X" {
		t.Errorf("push 登録が保存されていない: %+v %v", d, ok)
	}
}

// TestPasswordMismatchRejected は登録済み account に別パスワードで
// 結び付こうとすると拒否されることを確認する。
func TestPasswordMismatchRejected(t *testing.T) {
	fx := newMultiFixture(t, nil)
	a := dial(t, fx.url, "dev-A")
	defer a.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, a) // hello
	sendSipAccount(t, a, "101", "pw101", "")
	fx.waitRegistered(t, "101")

	b := dial(t, fx.url, "dev-B")
	defer b.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, b) // hello
	writeJSON(t, b, &proto.SipAccount{T: proto.TSipAccount, User: "101", Password: "wrong"})
	if e := expectError(t, b); e.Code != "account_password_mismatch" {
		t.Fatalf("error.code = %q (account_password_mismatch のはず)", e.Code)
	}
	// 結び付けは変わらない (B は account 無しのまま)。
	if d, ok := fx.store.Device("dev-B"); ok && d.Account != "" {
		t.Errorf("拒否されたのに結び付いている: %+v", d)
	}
	writeJSON(t, b, &proto.Dial{T: proto.TDial, To: "999"})
	if e := expectError(t, b); e.Code != "no_account" {
		t.Errorf("error.code = %q (no_account のはず)", e.Code)
	}
	if n := fx.backendCount("101"); n != 1 {
		t.Errorf("Backend 生成回数 = %d (作り直されてはいけない)", n)
	}
	// 正しいパスワードなら受理される。
	if h := sendSipAccount(t, b, "101", "pw101", ""); h.Account != "101" {
		t.Fatalf("正しいパスワードで結び付けできない: %+v", h)
	}
}

// TestPasswordChangeWhenUnregistered は未登録 account のパスワード変更が
// 受理され、Backend が作り直されることを確認する。
func TestPasswordChangeWhenUnregistered(t *testing.T) {
	fx := newMultiFixture(t, nil)
	fx.mu.Lock()
	fx.unregistered["103"] = true
	fx.mu.Unlock()

	a := dial(t, fx.url, "dev-A")
	defer a.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, a) // hello
	if h := sendSipAccount(t, a, "103", "old-pw", ""); h.Registered {
		t.Fatalf("登録失敗中のはず: %+v", h)
	}
	if n := fx.backendCount("103"); n != 1 {
		t.Fatalf("Backend 生成回数 = %d (1 のはず)", n)
	}
	// 未登録なので新パスワードを受理し、Backend を作り直す。
	if h := sendSipAccount(t, a, "103", "new-pw", ""); h.Account != "103" {
		t.Fatalf("新パスワードが受理されない: %+v", h)
	}
	if n := fx.backendCount("103"); n != 2 {
		t.Errorf("Backend 生成回数 = %d (2 のはず)", n)
	}
	if acc, ok := fx.store.Account("103"); !ok || acc.Password != "new-pw" {
		t.Errorf("新パスワードが保存されていない: %+v", acc)
	}
}

// TestDefaultAccountCompat は sip_account を送らない端末が
// 既定アカウント (SIP_USER) のグループに入ることを確認する
// (結び付けは永続化しない)。
func TestDefaultAccountCompat(t *testing.T) {
	fx := newFixture(t)
	ws := dial(t, fx.url, "dev-legacy")
	defer ws.Close(websocket.StatusNormalClosure, "")
	hello, ok := readSkipReg(t, ws).(*proto.Hello)
	if !ok || hello.Account != defaultTestAccount || hello.Extension != defaultTestAccount {
		t.Fatalf("既定アカウントに入っていない: %+v", hello)
	}
	// 暫定結び付けなので永続化されない。
	if d, ok := fx.store.Device("dev-legacy"); ok && d.Account != "" {
		t.Errorf("既定アカウントが永続化されている: %+v", d)
	}
	fx.fb(t).InjectIncoming("902", "Bob", 0)
	if inc, ok := readSkipReg(t, ws).(*proto.Incoming); !ok || inc.From != "902" {
		t.Fatalf("既定アカウントの着信が届かない: %+v", inc)
	}
}

// TestGroupStopsWhenNoDevices は結び付いた端末が 0 になった account の
// グループが停止し、state からも消えることを確認する。
// 逆に、切断しただけ (結び付けは残る) では停止しない。
func TestGroupStopsWhenNoDevices(t *testing.T) {
	fx := newMultiFixture(t, nil)
	a := dial(t, fx.url, "dev-A")
	readSkipReg(t, a) // hello
	sendSipAccount(t, a, "104", "pw104", "")
	if !fx.hub.HasAccount("104") {
		t.Fatalf("グループが起動していない")
	}

	// 切断しただけでは停止しない (PUSH モード端末のため登録を維持する)。
	_ = a.Close(websocket.StatusNormalClosure, "")
	waitSessions(t, fx, 0, 3*time.Second)
	if !fx.hub.HasAccount("104") {
		t.Fatalf("切断だけでグループが停止した")
	}
	if _, ok := fx.store.Account("104"); !ok {
		t.Fatalf("切断だけで account が消えた")
	}

	// 再接続して結び付けを解除すると停止する。
	b := dial(t, fx.url, "dev-A")
	defer b.Close(websocket.StatusNormalClosure, "")
	if h, ok := readSkipReg(t, b).(*proto.Hello); !ok || h.Account != "104" {
		t.Fatalf("再接続で結び付けが復元されない: %+v", h)
	}
	if h := sendSipAccount(t, b, "", "", ""); h.Account != "" {
		t.Fatalf("解除後の hello.account = %q", h.Account)
	}
	waitAccountStopped(t, fx, "104", 3*time.Second)
	if _, ok := fx.store.Account("104"); ok {
		t.Errorf("account が state に残っている")
	}
}

// TestRestoreBindingFromState は起動時に state の account が起動し、
// 端末が接続しただけでそのグループへ入ることを確認する。
func TestRestoreBindingFromState(t *testing.T) {
	st, err := state.New("")
	if err != nil {
		t.Fatalf("state.New 失敗: %v", err)
	}
	if err := st.SetAccount("105", state.Account{Password: "pw105", Display: "寝室"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDeviceAccount("dev-R", "105"); err != nil {
		t.Fatal(err)
	}
	fx := newMultiFixture(t, st)
	if !fx.hub.HasAccount("105") {
		t.Fatalf("起動時に account が起動していない")
	}
	ws := dial(t, fx.url, "dev-R")
	defer ws.Close(websocket.StatusNormalClosure, "")
	hello, ok := readSkipReg(t, ws).(*proto.Hello)
	if !ok || hello.Account != "105" {
		t.Fatalf("永続化された結び付けが使われていない: %+v", hello)
	}
	fx.backend(t, "105").InjectIncoming("903", "", 0)
	if inc, ok := readSkipReg(t, ws).(*proto.Incoming); !ok || inc.From != "903" {
		t.Fatalf("着信が届かない: %+v", inc)
	}
}

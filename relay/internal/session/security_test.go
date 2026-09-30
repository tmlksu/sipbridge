package session_test

// セキュリティ修正 (#30 #31 と未登録中の乗っ取り) の確認。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tmlksu/sipbridge/relay/internal/proto"
	"github.com/tmlksu/sipbridge/relay/internal/push"
	"github.com/tmlksu/sipbridge/relay/internal/session"
	"github.com/tmlksu/sipbridge/relay/internal/state"
)

// dialWith は任意ヘッダで接続を試みる (失敗時の HTTP 応答も返す)。
func dialWith(t *testing.T, url, device, principal string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := http.Header{"X-Device-Id": {device}}
	if principal != "" {
		h.Set("X-Test-Principal", principal)
	}
	return websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: h})
}

// dialStatus は接続が拒否されることを確認し、その HTTP ステータスを返す。
func dialStatus(t *testing.T, url, device, principal string) int {
	t.Helper()
	ws, resp, err := dialWith(t, url, device, principal)
	if err == nil {
		_ = ws.Close(websocket.StatusNormalClosure, "")
		t.Fatalf("device %q の接続が受理された (拒否のはず)", device)
	}
	if resp == nil {
		t.Fatalf("HTTP 応答が無い: %v", err)
	}
	return resp.StatusCode
}

// newSecFixture は既定アカウント無しで Config を指定できる fixture である。
func newSecFixture(t *testing.T, store *state.Store, cfg session.Config) *fixture {
	t.Helper()
	return newFixtureCfg(t, push.Noop{}, store, cfg)
}

// TestUnregisteredTakeoverRejected は、account が未登録 (Asterisk 再起動中など)
// の間でも、結び付いていない端末の誤パスワードの sip_account が拒否され、
// 結び付け・Backend が変わらないことを確認する (未登録中の乗っ取り)。
func TestUnregisteredTakeoverRejected(t *testing.T) {
	fx := newMultiFixture(t, nil)
	fx.mu.Lock()
	fx.unregistered["103"] = true
	fx.mu.Unlock()

	a := dial(t, fx.url, "dev-A")
	defer a.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, a) // hello
	if h := sendSipAccount(t, a, "103", "right-pw", ""); h.Registered {
		t.Fatalf("登録失敗中のはず: %+v", h)
	}

	// 攻撃側 (未結び付け) が未登録の間に別パスワードを送る。
	b := dial(t, fx.url, "dev-B")
	defer b.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, b) // hello
	writeJSON(t, b, &proto.SipAccount{T: proto.TSipAccount, User: "103", Password: "attacker-pw"})
	if e := expectError(t, b); e.Code != "account_password_mismatch" {
		t.Fatalf("error.code = %q (account_password_mismatch のはず)", e.Code)
	}
	if d, ok := fx.store.Device("dev-B"); ok && d.Account != "" {
		t.Errorf("攻撃側が結び付いた: %+v", d)
	}
	if n := fx.backendCount("103"); n != 1 {
		t.Errorf("Backend 生成回数 = %d (攻撃側のパスワードで作り直されてはいけない)", n)
	}
	if acc, _ := fx.store.Account("103"); acc.Password != "right-pw" {
		t.Errorf("保存パスワードが書き換えられた: %+v", acc)
	}
	writeJSON(t, b, &proto.Dial{T: proto.TDial, To: "999"})
	if e := expectError(t, b); e.Code != "no_account" {
		t.Errorf("攻撃側が発信できる状態: error.code = %q (no_account のはず)", e.Code)
	}

	// 正規のパスワードなら未結び付け端末でも受理される (2 台目の端末の追加)。
	if h := sendSipAccount(t, b, "103", "right-pw", ""); h.Account != "103" {
		t.Fatalf("正しいパスワードで結び付けできない: %+v", h)
	}
	if n := fx.backendCount("103"); n != 1 {
		t.Errorf("Backend 生成回数 = %d (一致なら作り直さない)", n)
	}
}

// TestDefaultAccountTakeoverRejected は、既定アカウントに暫定参加しただけの
// 端末 (永続化された結び付けが無い) が、既定アカウントのパスワードを
// 書き換えられないことを確認する。
func TestDefaultAccountTakeoverRejected(t *testing.T) {
	fx := newFixtureCfg(t, push.Noop{}, nil, session.Config{
		DefaultAccount: defaultTestAccount, DefaultPassword: defaultTestPassword,
	})
	ws := dial(t, fx.url, "dev-legacy")
	defer ws.Close(websocket.StatusNormalClosure, "")
	if h, ok := readSkipReg(t, ws).(*proto.Hello); !ok || h.Account != defaultTestAccount {
		t.Fatalf("既定アカウントに入っていない: %+v", h)
	}
	writeJSON(t, ws, &proto.SipAccount{T: proto.TSipAccount, User: defaultTestAccount, Password: "other"})
	if e := expectError(t, ws); e.Code != "account_password_mismatch" {
		t.Fatalf("error.code = %q (account_password_mismatch のはず)", e.Code)
	}
	if n := fx.backendCount(defaultTestAccount); n != 1 {
		t.Errorf("Backend 生成回数 = %d (作り直されてはいけない)", n)
	}
}

// TestStoredButStoppedAccountRequiresPassword は、保存済みだが起動していない
// account (起動時の Backend 生成失敗など) にも、結び付いていない端末は保存
// パスワードの一致が必要なことを確認する。
func TestStoredButStoppedAccountRequiresPassword(t *testing.T) {
	st, _ := state.New("")
	fx := newMultiFixture(t, st)
	// Run の後に保存するので「保存済みだが未起動」になる。
	if err := st.SetAccount("106", state.Account{Password: "pw106"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDeviceAccount("dev-owner", "106"); err != nil {
		t.Fatal(err)
	}
	if fx.hub.HasAccount("106") {
		t.Fatalf("未起動のはず")
	}

	x := dial(t, fx.url, "dev-X")
	defer x.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, x) // hello
	writeJSON(t, x, &proto.SipAccount{T: proto.TSipAccount, User: "106", Password: "guess"})
	if e := expectError(t, x); e.Code != "account_password_mismatch" {
		t.Fatalf("error.code = %q (account_password_mismatch のはず)", e.Code)
	}
	if fx.hub.HasAccount("106") {
		t.Errorf("誤パスワードで account が起動した")
	}
	if acc, _ := fx.store.Account("106"); acc.Password != "pw106" {
		t.Errorf("保存パスワードが書き換えられた: %+v", acc)
	}
	if h := sendSipAccount(t, x, "106", "pw106", ""); h.Account != "106" {
		t.Fatalf("正しいパスワードで結び付けできない: %+v", h)
	}
}

// TestSipAccountFailuresDisconnect は 1 接続で sip_account の誤りが
// 3 回続くと、3 回目の error の後に接続が閉じられる (1008) ことを確認する。
func TestSipAccountFailuresDisconnect(t *testing.T) {
	const delay = 50 * time.Millisecond
	fx := newSecFixture(t, nil, session.Config{AuthFailureDelay: delay})
	a := dial(t, fx.url, "dev-A")
	defer a.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, a)
	sendSipAccount(t, a, "101", "pw101", "")
	fx.waitRegistered(t, "101")

	b := dial(t, fx.url, "dev-B")
	defer b.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, b)
	for i := 1; i <= 3; i++ {
		start := time.Now()
		writeJSON(t, b, &proto.SipAccount{T: proto.TSipAccount, User: "101", Password: "guess"})
		if e := expectError(t, b); e.Code != "account_password_mismatch" {
			t.Fatalf("%d 回目: error.code = %q", i, e.Code)
		}
		if el := time.Since(start); el < delay {
			t.Errorf("%d 回目の応答が遅延されていない (%s)", i, el)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := b.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("3 回目の後は 1008 で閉じられるはず: %v", err)
	}
	// 正規端末は影響を受けない。
	writeJSON(t, a, &proto.Ping{T: proto.TPing, Ts: 1})
	if _, ok := readSkipReg(t, a).(*proto.Pong); !ok {
		t.Fatalf("正規端末の接続が影響を受けた")
	}
}

// TestAttemptBucket は全体の試行バケットが「未結び付け端末の試行」と
// 「新規 account 作成」だけで消費され、結び付いた端末の再送では消費されない
// ことを確認する。
func TestAttemptBucket(t *testing.T) {
	fx := newSecFixture(t, nil, session.Config{AttemptBurst: 2, AttemptInterval: time.Hour})
	a := dial(t, fx.url, "dev-A")
	defer a.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, a)
	sendSipAccount(t, a, "101", "pw101", "") // 新規作成: 1 消費 (残 1)
	fx.waitRegistered(t, "101")

	// 結び付いた端末の再送 (再接続ごとに送る) は何度でも通る。
	for i := 0; i < 5; i++ {
		if h := sendSipAccount(t, a, "101", "pw101", ""); h.Account != "101" {
			t.Fatalf("結び付いた端末の再送が拒否された: %+v", h)
		}
	}
	a2 := dial(t, fx.url, "dev-A") // 再接続でも同じ
	defer a2.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, a2)
	sendSipAccount(t, a2, "101", "pw101", "")

	b := dial(t, fx.url, "dev-B")
	defer b.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, b)
	sendSipAccount(t, b, "101", "pw101", "") // 未結び付け端末の試行: 1 消費 (残 0)

	c := dial(t, fx.url, "dev-C")
	defer c.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, c)
	writeJSON(t, c, &proto.SipAccount{T: proto.TSipAccount, User: "101", Password: "pw101"})
	if e := expectError(t, c); e.Code != "rate_limited" {
		t.Fatalf("バケット枯渇で rate_limited のはず: %q", e.Code)
	}
	writeJSON(t, c, &proto.SipAccount{T: proto.TSipAccount, User: "202", Password: "x"})
	if e := expectError(t, c); e.Code != "rate_limited" {
		t.Fatalf("新規作成も rate_limited のはず: %q", e.Code)
	}
	if fx.hub.HasAccount("202") {
		t.Errorf("rate_limited なのに account が作られた")
	}
}

// TestMaxAccounts は新規 account の作成が上限で拒否され、起動時の
// 状態ファイル読み込みは上限超過でも全件起動することを確認する。
func TestMaxAccounts(t *testing.T) {
	st, _ := state.New("")
	for _, u := range []string{"101", "102", "103"} {
		_ = st.SetAccount(u, state.Account{Password: "pw" + u})
		_ = st.SetDeviceAccount("dev-"+u, u)
	}
	fx := newSecFixture(t, st, session.Config{MaxAccounts: 2})
	for _, u := range []string{"101", "102", "103"} {
		if !fx.hub.HasAccount(u) {
			t.Errorf("起動時に %s が起動していない (上限超過でも読むはず)", u)
		}
	}
	x := dial(t, fx.url, "dev-X")
	defer x.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, x)
	writeJSON(t, x, &proto.SipAccount{T: proto.TSipAccount, User: "104", Password: "pw104"})
	if e := expectError(t, x); e.Code != "too_many_accounts" {
		t.Fatalf("error.code = %q (too_many_accounts のはず)", e.Code)
	}
	if fx.hub.HasAccount("104") {
		t.Errorf("上限超過で account が作られた")
	}
	// 既存 account への参加は上限に関係なく通る。
	if h := sendSipAccount(t, x, "102", "pw102", ""); h.Account != "102" {
		t.Fatalf("既存 account への参加が拒否された: %+v", h)
	}
}

// TestMaxStoredDevices は新規端末の保存 (sip_account / register_push) が
// 上限で拒否され、保存済み端末は引き続き使えることを確認する。
func TestMaxStoredDevices(t *testing.T) {
	fx := newSecFixture(t, nil, session.Config{MaxStoredDevices: 2})
	a := dial(t, fx.url, "dev-A")
	defer a.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, a)
	sendSipAccount(t, a, "101", "pw101", "")
	b := dial(t, fx.url, "dev-B")
	defer b.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, b)
	registerPush(t, b, "tok-B", 1)

	c := dial(t, fx.url, "dev-C")
	defer c.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, c)
	writeJSON(t, c, &proto.SipAccount{T: proto.TSipAccount, User: "101", Password: "pw101"})
	if e := expectError(t, c); e.Code != "too_many_devices" {
		t.Fatalf("sip_account: error.code = %q (too_many_devices のはず)", e.Code)
	}
	writeJSON(t, c, &proto.RegisterPush{T: proto.TRegisterPush, Provider: "fcm", Token: "tok-C"})
	if e := expectError(t, c); e.Code != "too_many_devices" {
		t.Fatalf("register_push: error.code = %q (too_many_devices のはず)", e.Code)
	}
	writeJSON(t, c, &proto.SipAccount{T: proto.TSipAccount, User: "909", Password: "x"})
	if e := expectError(t, c); e.Code != "too_many_devices" {
		t.Fatalf("新規 account: error.code = %q (too_many_devices のはず)", e.Code)
	}
	if fx.hub.HasAccount("909") {
		t.Errorf("端末を保存できないのに account が作られた")
	}
	if _, ok := fx.store.Device("dev-C"); ok {
		t.Errorf("上限超過の端末が保存された")
	}
	// 保存済み端末 (B) は account を結び付けられる。
	if h := sendSipAccount(t, b, "101", "pw101", ""); h.Account != "101" {
		t.Fatalf("保存済み端末の結び付けが拒否された: %+v", h)
	}
}

// TestValidDeviceID は X-Device-Id の形式検証である。
func TestValidDeviceID(t *testing.T) {
	for _, id := range []string{
		"6f1c2a4e-8b3d-4c5e-9f70-1a2b3c4d5e6f", // アプリ (UUID v4)
		"e2e-reg",                              // ops
		"wsprobe-150405.000",                   // wsprobe
		"dev-A", "probe-101", "a", "host:1",
		strings.Repeat("x", 64),
	} {
		if !session.ValidDeviceID(id) {
			t.Errorf("%q は有効のはず", id)
		}
	}
	for _, id := range []string{
		"", strings.Repeat("x", 65), "a b", "dev\x01", "dev\n", "a/b", "日本語", "a\"b", "../x",
	} {
		if session.ValidDeviceID(id) {
			t.Errorf("%q は無効のはず", id)
		}
	}
}

// TestDeviceIDHeaderRejected は不正な X-Device-Id が WS 昇格前に 400 になり、
// 有効な形式は接続できることを確認する。
func TestDeviceIDHeaderRejected(t *testing.T) {
	fx := newMultiFixture(t, nil)
	for _, id := range []string{strings.Repeat("x", 65), "dev\x01", "a b", "a/b", "日本語"} {
		r := httptest.NewRequest(http.MethodGet, "/v1/session", nil)
		r.Header["X-Device-Id"] = []string{id} // 制御文字も含めてそのまま渡す
		w := httptest.NewRecorder()
		fx.hub.ServeWS(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("X-Device-Id %q: status = %d (400 のはず)", id, w.Code)
		}
	}
	if n := fx.hub.SessionCount(); n != 0 {
		t.Errorf("拒否した接続が登録された: %d", n)
	}
	for _, id := range []string{"6f1c2a4e-8b3d-4c5e-9f70-1a2b3c4d5e6f", "e2e-reg", "wsprobe-150405.000"} {
		ws := dial(t, fx.url, id)
		if _, ok := readSkipReg(t, ws).(*proto.Hello); !ok {
			t.Errorf("%q で hello が来ない", id)
		}
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}
}

// TestOnlineDeviceLimit はオンライン端末数の上限で新規端末が 503 になり、
// 接続中端末の再接続 (張り替え) と保存済み端末は受け入れることを確認する。
func TestOnlineDeviceLimit(t *testing.T) {
	st, _ := state.New("")
	_ = st.SetDevicePush("dev-K", state.Push{Provider: "fcm", Token: "tok-K"}) // 既知の端末
	fx := newSecFixture(t, st, session.Config{MaxOnlineDevices: 2})
	a := dial(t, fx.url, "dev-A")
	defer a.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, a)
	b := dial(t, fx.url, "dev-B")
	defer b.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, b)

	if code := dialStatus(t, fx.url, "dev-C", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("上限超過の新規端末: status = %d (503 のはず)", code)
	}
	// 接続中端末の再接続は通る (古い接続は置換される)。
	a2 := dial(t, fx.url, "dev-A")
	defer a2.Close(websocket.StatusNormalClosure, "")
	if _, ok := readSkipReg(t, a2).(*proto.Hello); !ok {
		t.Fatalf("再接続で hello が来ない")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := a.Read(ctx); websocket.CloseStatus(err) != 4001 {
		t.Fatalf("古い接続が置換されない: %v", err)
	}
	// 保存済み (既知) の端末は上限に達していても通る。
	k := dial(t, fx.url, "dev-K")
	defer k.Close(websocket.StatusNormalClosure, "")
	if _, ok := readSkipReg(t, k).(*proto.Hello); !ok {
		t.Fatalf("既知端末で hello が来ない")
	}
	// 切断で枠が空けば新規端末も入れる。
	_ = b.Close(websocket.StatusNormalClosure, "")
	_ = k.Close(websocket.StatusNormalClosure, "")
	waitSessions(t, fx, 1, 3*time.Second)
	c := dial(t, fx.url, "dev-C")
	defer c.Close(websocket.StatusNormalClosure, "")
	if _, ok := readSkipReg(t, c).(*proto.Hello); !ok {
		t.Fatalf("枠が空いた後の新規端末で hello が来ない")
	}
}

// TestDeviceBindingEnforce は DEVICE_BINDING=enforce で、記録済みと異なる
// principal からの同じ端末 ID の接続が WS 昇格前に 409 になり、オンライン
// 扱いにならない (正規端末への push を止めない) ことを確認する。
func TestDeviceBindingEnforce(t *testing.T) {
	fx := newSecFixture(t, nil, session.Config{DeviceBinding: session.DeviceBindingEnforce})
	a, _, err := dialWith(t, fx.url, "dev-A", "alice.access")
	if err != nil {
		t.Fatalf("接続失敗: %v", err)
	}
	readSkipReg(t, a)
	sendSipAccount(t, a, "101", "pw101", "") // 端末が保存され principal が記録される
	if d, _ := fx.store.Device("dev-A"); d.Principal != "alice.access" {
		t.Fatalf("principal が記録されていない: %+v", d)
	}
	_ = a.Close(websocket.StatusNormalClosure, "")
	waitSessions(t, fx, 0, 3*time.Second)

	if code := dialStatus(t, fx.url, "dev-A", "mallory.access"); code != http.StatusConflict {
		t.Fatalf("別 principal: status = %d (409 のはず)", code)
	}
	if n := fx.hub.SessionCount(); n != 0 {
		t.Errorf("拒否した接続がオンライン扱いになった: %d", n)
	}
	if d, _ := fx.store.Device("dev-A"); d.Principal != "alice.access" || d.Account != "101" {
		t.Errorf("記録が書き換えられた: %+v", d)
	}
	// 同じ principal なら通る。
	a2, _, err := dialWith(t, fx.url, "dev-A", "alice.access")
	if err != nil {
		t.Fatalf("同じ principal の再接続が拒否: %v", err)
	}
	defer a2.Close(websocket.StatusNormalClosure, "")
	if h, ok := readSkipReg(t, a2).(*proto.Hello); !ok || h.Account != "101" {
		t.Fatalf("再接続の hello が不正: %+v", h)
	}
	// principal の無い接続 (AUTH_MODE=token 相当) は検査しない。
	n, _, err := dialWith(t, fx.url, "dev-A", "")
	if err != nil {
		t.Fatalf("principal 無しの接続が拒否: %v", err)
	}
	_ = n.Close(websocket.StatusNormalClosure, "")
}

// TestDeviceBindingWarn は warn では不一致でも接続でき、記録は書き換えない
// ことを確認する。既存の端末 (principal 未記録) は最初の接続で記録する。
func TestDeviceBindingWarn(t *testing.T) {
	st, _ := state.New("")
	_ = st.SetAccount("101", state.Account{Password: "pw101"})
	_ = st.SetDeviceAccount("dev-old", "101")    // 更新前から保存済みの端末
	fx := newSecFixture(t, st, session.Config{}) // 既定 = warn
	a, _, err := dialWith(t, fx.url, "dev-old", "alice.access")
	if err != nil {
		t.Fatalf("接続失敗: %v", err)
	}
	readSkipReg(t, a)
	if d, _ := fx.store.Device("dev-old"); d.Principal != "alice.access" {
		t.Fatalf("既存端末の principal が最初の接続で記録されない: %+v", d)
	}
	m, _, err := dialWith(t, fx.url, "dev-old", "mallory.access")
	if err != nil {
		t.Fatalf("warn で不一致の接続が拒否された: %v", err)
	}
	defer m.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, m)
	if d, _ := fx.store.Device("dev-old"); d.Principal != "alice.access" {
		t.Errorf("warn で記録が書き換えられた: %+v", d)
	}
	// 接続しただけで保存されていない端末は記録しない (状態ファイルを増やさない)。
	p, _, err := dialWith(t, fx.url, "wsprobe-150405.000", "probe.access")
	if err != nil {
		t.Fatalf("接続失敗: %v", err)
	}
	readSkipReg(t, p)
	_ = p.Close(websocket.StatusNormalClosure, "")
	if _, ok := fx.store.Device("wsprobe-150405.000"); ok {
		t.Errorf("接続しただけの端末が保存された")
	}
}

// TestDeviceBindingOff は off では principal を記録しないことを確認する。
func TestDeviceBindingOff(t *testing.T) {
	fx := newSecFixture(t, nil, session.Config{DeviceBinding: session.DeviceBindingOff})
	a, _, err := dialWith(t, fx.url, "dev-A", "alice.access")
	if err != nil {
		t.Fatalf("接続失敗: %v", err)
	}
	defer a.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, a)
	sendSipAccount(t, a, "101", "pw101", "")
	if d, _ := fx.store.Device("dev-A"); d.Principal != "" {
		t.Errorf("off なのに principal が記録された: %+v", d)
	}
}

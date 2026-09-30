package session_test

// レビュー指摘への対応の確認: パスワード変更の試行制限、仮の資格情報の取り消し
// (新規 account の削除・パスワード変更の差し戻し)、register_push の乱用対策。

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tmlksu/sipbridge/relay/internal/proto"
	"github.com/tmlksu/sipbridge/relay/internal/push"
	"github.com/tmlksu/sipbridge/relay/internal/session"
	"github.com/tmlksu/sipbridge/relay/internal/state"
)

// waitFor は cond が真になるまで待つ。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s にならない", what)
}

// TestBoundPasswordChangeRateLimited は、結び付いた端末からの (未登録中の)
// パスワード変更も全体の試行制限を受けることを確認する。未使用内線を 1 つ作って
// パスワード変更を繰り返せば Asterisk のパスワードを総当たりできた (レビュー指摘 1)。
// 同じパスワードの再送は制限を受けない。
func TestBoundPasswordChangeRateLimited(t *testing.T) {
	fx := newSecFixture(t, nil, session.Config{AttemptBurst: 3, AttemptInterval: time.Hour, MaxAuthFailures: 100})
	fx.mu.Lock()
	fx.unregistered["103"] = true
	fx.mu.Unlock()

	a := dial(t, fx.url, "dev-A")
	defer a.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, a)
	sendSipAccount(t, a, "103", "guess-0", "") // 新規作成: 1 消費
	sendSipAccount(t, a, "103", "guess-1", "") // 変更: 2 消費
	sendSipAccount(t, a, "103", "guess-2", "") // 変更: 3 消費 (枯渇)
	for i := 3; i < 30; i++ {
		writeJSON(t, a, &proto.SipAccount{T: proto.TSipAccount, User: "103", Password: "guess-x"})
		if e := expectError(t, a); e.Code != "rate_limited" {
			t.Fatalf("パスワード変更の %d 回目: error.code = %q (rate_limited のはず)", i+1, e.Code)
		}
	}
	if n := fx.backendCount("103"); n != 3 {
		t.Errorf("Backend 生成回数 = %d (制限後は作り直さない)", n)
	}
	// 現在のパスワードの再送 (再接続ごとの送信) は制限を受けない。
	for i := 0; i < 3; i++ {
		if h := sendSipAccount(t, a, "103", "guess-2", ""); h.Account != "103" {
			t.Fatalf("同じパスワードの再送が拒否された: %+v", h)
		}
	}
}

// TestBoundPasswordChangeCountsAsFailure は、パスワード変更が受理されても
// 失敗と同じく 1 接続あたりの回数に数えられ、上限で切断されることを確認する。
func TestBoundPasswordChangeCountsAsFailure(t *testing.T) {
	fx := newSecFixture(t, nil, session.Config{})
	fx.mu.Lock()
	fx.unregistered["103"] = true
	fx.mu.Unlock()

	a := dial(t, fx.url, "dev-A")
	defer a.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, a)
	sendSipAccount(t, a, "103", "guess-0", "") // 1 回目 (新規作成)
	sendSipAccount(t, a, "103", "guess-1", "") // 2 回目 (変更)
	writeJSON(t, a, &proto.SipAccount{T: proto.TSipAccount, User: "103", Password: "guess-2"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, _, err := a.Read(ctx)
		if err == nil {
			continue // registration 等
		}
		if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
			t.Fatalf("3 回目の試行の後は 1008 で閉じられるはず: %v", err)
		}
		break
	}
}

// TestProvisionalNewAccountRemoved は、新規 account が SIP サーバに認証で
// 拒否され続けたら削除される (REGISTER を打ち続けない) ことを確認する。
// タイムアウト等 (Code 0) は数えない。猶予 (ProvisionalGrace) はテスト用に短くする。
func TestProvisionalNewAccountRemoved(t *testing.T) {
	fx := newSecFixture(t, nil, session.Config{ProvisionalGrace: 20 * time.Millisecond})
	fx.mu.Lock()
	fx.unregistered["201"] = true
	fx.mu.Unlock()

	a := dial(t, fx.url, "dev-A")
	defer a.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, a)
	sendSipAccount(t, a, "201", "wrong", "") // Start で 401 が 1 回
	fb := fx.backend(t, "201")
	for i := 0; i < 5; i++ {
		fb.InjectRegistration(false, 0, "register 応答待ち失敗: timeout")
	}
	fb.InjectRegistration(false, 401, "register 401 Unauthorized")
	time.Sleep(50 * time.Millisecond)
	if !fx.hub.HasAccount("201") {
		t.Fatalf("認証拒否 2 回 + タイムアウトで削除された (3 回の認証拒否までは残るはず)")
	}
	fb.InjectRegistration(false, 403, "register 403 Forbidden")
	if e := expectError(t, a); e.Code != "account_failed" {
		t.Fatalf("error.code = %q (account_failed のはず)", e.Code)
	}
	waitFor(t, "account 201 の停止", func() bool { return !fx.hub.HasAccount("201") })
	if _, ok := fx.store.Account("201"); ok {
		t.Errorf("account が状態ファイルに残っている")
	}
	if d, _ := fx.store.Device("dev-A"); d.Account != "" {
		t.Errorf("結び付けが残っている: %+v", d)
	}
	writeJSON(t, a, &proto.Dial{T: proto.TDial, To: "102"})
	if e := expectError(t, a); e.Code != "no_account" {
		t.Errorf("削除後の発信: error.code = %q (no_account のはず)", e.Code)
	}
}

// TestProvisionalGrace は、認証拒否が 3 回以上でも最初の拒否から猶予
// (ProvisionalGrace) が経つまでは取り消さず、猶予内に一度でも成功すれば
// 検証済みになることを確認する (内線を Asterisk に後から追加する運用の猶予)。
func TestProvisionalGrace(t *testing.T) {
	const grace = 300 * time.Millisecond
	fx := newSecFixture(t, nil, session.Config{ProvisionalGrace: grace})
	fx.mu.Lock()
	fx.unregistered["203"] = true
	fx.unregistered["204"] = true
	fx.mu.Unlock()
	a := dial(t, fx.url, "dev-A")
	defer a.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, a)
	start := time.Now()
	sendSipAccount(t, a, "203", "not-yet", "") // Start で 401 (1 回目)
	fb := fx.backend(t, "203")
	for i := 0; i < 4; i++ {
		fb.InjectRegistration(false, 401, "register 401 Unauthorized")
	}
	time.Sleep(50 * time.Millisecond)
	if !fx.hub.HasAccount("203") {
		t.Fatalf("猶予内 (%s) に取り消された", time.Since(start))
	}
	time.Sleep(time.Until(start.Add(grace + 50*time.Millisecond)))
	fb.InjectRegistration(false, 401, "register 401 Unauthorized")
	waitFor(t, "猶予後の取り消し", func() bool { return !fx.hub.HasAccount("203") })

	// 猶予内に成功すれば、その後いくら拒否されても取り消さない。
	b := dial(t, fx.url, "dev-B")
	defer b.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, b)
	start = time.Now()
	sendSipAccount(t, b, "204", "added-later", "")
	fb = fx.backend(t, "204")
	fb.InjectRegistration(false, 401, "register 401 Unauthorized")
	fb.InjectRegistration(true, 200, "registered") // Asterisk 側に内線が追加された
	time.Sleep(time.Until(start.Add(grace + 50*time.Millisecond)))
	for i := 0; i < 5; i++ {
		fb.InjectRegistration(false, 401, "register 401 Unauthorized")
	}
	time.Sleep(100 * time.Millisecond)
	if !fx.hub.HasAccount("204") {
		t.Fatalf("一度成功した account が取り消された")
	}
}

// TestProvisionalClearedOnSuccess は、一度 REGISTER に成功した新規 account は
// その後に認証拒否が続いても削除されない (正規の新規設定を壊さない) ことを確認する。
func TestProvisionalClearedOnSuccess(t *testing.T) {
	fx := newSecFixture(t, nil, session.Config{})
	a := dial(t, fx.url, "dev-A")
	defer a.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, a)
	sendSipAccount(t, a, "202", "right", "")
	fx.waitRegistered(t, "202")
	fb := fx.backend(t, "202")
	for i := 0; i < 5; i++ {
		fb.InjectRegistration(false, 401, "register 401 Unauthorized")
	}
	time.Sleep(100 * time.Millisecond)
	if !fx.hub.HasAccount("202") {
		t.Fatalf("登録に成功した account が削除された")
	}
	if _, ok := fx.store.Account("202"); !ok {
		t.Fatalf("登録に成功した account が状態ファイルから消えた")
	}
}

// TestProvisionalPasswordChangeReverted は、結び付いた端末のパスワード変更が
// 認証で拒否され続けたら、変更前の資格情報に戻して REGISTER し直すことを確認する。
func TestProvisionalPasswordChangeReverted(t *testing.T) {
	st, _ := state.New("")
	_ = st.SetAccount("104", state.Account{Password: "good", Display: "居間"})
	_ = st.SetDeviceAccount("dev-A", "104")
	// 起動時の Backend から未登録状態 (Asterisk 再起動中などを模擬) にする。
	fx := newFixtureUnregistered(t, st, session.Config{MaxAuthFailures: 100, ProvisionalGrace: 20 * time.Millisecond}, "104")

	a := dial(t, fx.url, "dev-A")
	defer a.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, a)
	before := fx.backendCount("104")
	sendSipAccount(t, a, "104", "typo", "") // 未登録中なので変更として受理 (Start で 401 が 1 回)
	if n := fx.backendCount("104"); n != before+1 {
		t.Fatalf("Backend 生成回数 = %d (変更で作り直すはず)", n)
	}
	fb := fx.backend(t, "104")
	fx.mu.Lock()
	fx.unregistered["104"] = false // 戻した資格情報は通る
	fx.mu.Unlock()
	time.Sleep(30 * time.Millisecond) // 猶予 (20ms) を過ぎてから 2・3 回目の拒否
	fb.InjectRegistration(false, 401, "register 401 Unauthorized")
	fb.InjectRegistration(false, 401, "register 401 Unauthorized")
	if e := expectError(t, a); e.Code != "account_failed" {
		t.Fatalf("error.code = %q (account_failed のはず)", e.Code)
	}
	waitFor(t, "元の資格情報での作り直し", func() bool { return fx.backendCount("104") == before+2 })
	if acc, _ := fx.store.Account("104"); acc.Password != "good" || acc.Display != "居間" {
		t.Errorf("状態ファイルが元に戻っていない: %+v", acc)
	}
	fx.waitRegistered(t, "104")
	if d, _ := fx.store.Device("dev-A"); d.Account != "104" {
		t.Errorf("結び付けが外れた: %+v", d)
	}
}

// newFixtureUnregistered は accounts の Backend を起動時から未登録状態で作る fixture である。
func newFixtureUnregistered(t *testing.T, st *state.Store, cfg session.Config, accounts ...string) *fixture {
	t.Helper()
	unreg := make(map[string]bool)
	for _, a := range accounts {
		unreg[a] = true
	}
	return newFixtureOpts(t, push.Noop{}, st, cfg, unreg)
}

// TestRegisterPushAbuse は、状態ファイルに無い端末の register_push が全体の
// 試行バケットを消費し、保存済み端末のトークン変更は間引かれることを確認する。
func TestRegisterPushAbuse(t *testing.T) {
	fx := newSecFixture(t, nil, session.Config{AttemptBurst: 2, AttemptInterval: time.Hour})
	var conns []*websocket.Conn
	defer func() {
		for _, c := range conns {
			_ = c.Close(websocket.StatusNormalClosure, "")
		}
	}()
	for _, id := range []string{"dev-1", "dev-2"} {
		c := dial(t, fx.url, id)
		conns = append(conns, c)
		readSkipReg(t, c)
		registerPush(t, c, "tok-"+id, 1)
	}
	c3 := dial(t, fx.url, "dev-3")
	conns = append(conns, c3)
	readSkipReg(t, c3)
	writeJSON(t, c3, &proto.RegisterPush{T: proto.TRegisterPush, Provider: "fcm", Token: "tok-3"})
	if e := expectError(t, c3); e.Code != "rate_limited" {
		t.Fatalf("新規端末の register_push: error.code = %q (rate_limited のはず)", e.Code)
	}
	if _, ok := fx.store.Device("dev-3"); ok {
		t.Errorf("制限されたのに端末が保存された")
	}

	// 保存済み端末: 同じトークンの再送は何度でも通る (書かない)。
	c1 := conns[0]
	registerPush(t, c1, "tok-dev-1", 2)
	registerPush(t, c1, "tok-dev-1", 3)
	// トークン変更は 1 回目は通り、直後の 2 回目は間引かれる。
	registerPush(t, c1, "tok-new", 4)
	writeJSON(t, c1, &proto.RegisterPush{T: proto.TRegisterPush, Provider: "fcm", Token: "tok-newer"})
	if e := expectError(t, c1); e.Code != "rate_limited" {
		t.Fatalf("連続したトークン変更: error.code = %q (rate_limited のはず)", e.Code)
	}
	if d, _ := fx.store.Device("dev-1"); d.Push == nil || d.Push.Token != "tok-new" {
		t.Errorf("トークンが不正: %+v", d.Push)
	}
}

// TestStoredDevicesEvictUnbound は、保存数が上限のとき account の無い最も古い
// (接続していない) 端末を追い出して新しい端末を保存することを確認する。
// 接続中の端末と account に結び付いた端末は追い出さない。
func TestStoredDevicesEvictUnbound(t *testing.T) {
	fx := newSecFixture(t, nil, session.Config{MaxStoredDevices: 3})
	a := dial(t, fx.url, "dev-A") // account あり (追い出されない)
	readSkipReg(t, a)
	sendSipAccount(t, a, "101", "pw101", "")
	_ = a.Close(websocket.StatusNormalClosure, "")

	p1 := dial(t, fx.url, "dev-P1") // push だけ・切断済み (追い出し対象)
	readSkipReg(t, p1)
	registerPush(t, p1, "tok-P1", 1)
	_ = p1.Close(websocket.StatusNormalClosure, "")
	p2 := dial(t, fx.url, "dev-P2") // push だけ・接続中 (対象外)
	defer p2.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, p2)
	registerPush(t, p2, "tok-P2", 1)
	waitSessions(t, fx, 1, 3*time.Second)

	n := dial(t, fx.url, "dev-N")
	defer n.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, n)
	registerPush(t, n, "tok-N", 1)
	if _, ok := fx.store.Device("dev-P1"); ok {
		t.Errorf("account の無い切断済み端末が追い出されていない")
	}
	for _, id := range []string{"dev-A", "dev-P2", "dev-N"} {
		if _, ok := fx.store.Device(id); !ok {
			t.Errorf("%s が消えた", id)
		}
	}
	// 追い出せる端末が無ければ too_many_devices。
	m := dial(t, fx.url, "dev-M")
	defer m.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, m)
	writeJSON(t, m, &proto.RegisterPush{T: proto.TRegisterPush, Provider: "fcm", Token: "tok-M"})
	if e := expectError(t, m); e.Code != "too_many_devices" {
		t.Fatalf("error.code = %q (too_many_devices のはず)", e.Code)
	}
}

// TestStoredDevicesNoEvictWithDefaultAccount は、既定アカウントがある構成では
// account の無い端末にも push が飛ぶので追い出さないことを確認する。
func TestStoredDevicesNoEvictWithDefaultAccount(t *testing.T) {
	fx := newFixtureCfg(t, push.Noop{}, nil, session.Config{
		DefaultAccount: defaultTestAccount, DefaultPassword: defaultTestPassword, MaxStoredDevices: 1,
	})
	p1 := dial(t, fx.url, "dev-P1")
	readSkipReg(t, p1)
	registerPush(t, p1, "tok-P1", 1)
	_ = p1.Close(websocket.StatusNormalClosure, "")
	waitSessions(t, fx, 0, 3*time.Second)
	n := dial(t, fx.url, "dev-N")
	defer n.Close(websocket.StatusNormalClosure, "")
	readSkipReg(t, n)
	writeJSON(t, n, &proto.RegisterPush{T: proto.TRegisterPush, Provider: "fcm", Token: "tok-N"})
	if e := expectError(t, n); e.Code != "too_many_devices" {
		t.Fatalf("error.code = %q (too_many_devices のはず)", e.Code)
	}
	if _, ok := fx.store.Device("dev-P1"); !ok {
		t.Errorf("既定アカウントの構成で端末が追い出された")
	}
}

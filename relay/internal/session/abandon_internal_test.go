package session

// 仮の資格情報の取り消し (abandonProvisional) と、接続・パスワード変更の競合の確認。

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/tmlksu/sipbridge/relay/internal/call"
	"github.com/tmlksu/sipbridge/relay/internal/fakebackend"
	"github.com/tmlksu/sipbridge/relay/internal/push"
)

// newUnregisteredHub は全 account の Backend が登録失敗状態 (401) になる Hub である。
func newUnregisteredHub(t *testing.T) *Hub {
	t.Helper()
	factory := func(user, password, display string) (call.Backend, error) {
		return fakebackend.NewUnregistered(), nil
	}
	h := NewHub(factory, push.Noop{}, nil, Config{Version: "test", AuthFailureDelay: time.Millisecond}, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := h.Run(ctx); err != nil {
		t.Fatal(err)
	}
	return h
}

// testConn は WS を持たない接続である (addConn / bindAccount の検証用。送信はキューに積むだけ)。
func testConn(h *Hub, device string) *Conn {
	return &Conn{hub: h, deviceID: device, out: make(chan outFrame, sendQueueSize)}
}

func (h *Hub) groupFor(account string) *group {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.groups[account]
}

// markAbandoning は onRegistration が取り消しを依頼した直後の状態を作る。
func markAbandoning(g *group) {
	g.mu.Lock()
	g.abandoning = true
	g.mu.Unlock()
}

// TestAbandonRacesAddConn は、再接続 (addConn) がグループを解決した直後に新規 account の
// 取り消しが走っても、取り外したグループに接続が付かず、誤パスワードの account が
// 検証済みとして残らないことを確認する (再レビュー指摘 1)。
func TestAbandonRacesAddConn(t *testing.T) {
	h := newUnregisteredHub(t)
	c1 := testConn(h, "dev-A")
	h.addConn(c1)
	if code, msg, _ := h.bindAccount(c1, "201", "wrong", ""); code != "" {
		t.Fatalf("新規作成が拒否: %s %s", code, msg)
	}
	g := h.groupFor("201")
	mgr := g.manager()
	markAbandoning(g)

	c2 := testConn(h, "dev-A") // 同じ端末の再接続
	h.addConnHook = func() {
		h.addConnHook = nil
		h.abandonProvisional(g, mgr, maxProvisionalAuthFailures, time.Minute, "test")
	}
	h.addConn(c2)

	if h.groupFor("201") != nil {
		t.Fatalf("取り消したグループが残っている")
	}
	if !g.isStopped() {
		t.Errorf("グループが停止していない")
	}
	if c1.group() != nil || c2.group() != nil {
		t.Errorf("取り外したグループに接続が付いている: c1=%v c2=%v", c1.group(), c2.group())
	}
	if n := g.connCount(); n != 0 {
		t.Errorf("取り外したグループの接続数 = %d", n)
	}
	if _, ok := h.store.Account("201"); ok {
		t.Errorf("account が状態ファイルに残っている")
	}
	// 誤パスワードの再送は「一致して受理 (検証済みで復活)」ではなく新規作成 (仮) になる。
	if code, msg, guessed := h.bindAccount(c2, "201", "wrong", ""); code != "" || !guessed {
		t.Fatalf("再送: code=%q %s guessed=%v (新規作成のはず)", code, msg, guessed)
	}
	g2 := h.groupFor("201")
	g2.mu.Lock()
	mode := g2.prov.mode
	g2.mu.Unlock()
	if g2 == g || mode != provNewAccount {
		t.Errorf("作り直した account が仮の新規になっていない: same=%v mode=%v", g2 == g, mode)
	}
}

// TestPasswordChangeWhileAbandoning は、取り消し待ちの間に結び付いた端末が
// パスワードを変えても、新規 account の扱いが引き継がれ (「変更」に化けない)、
// 古い Manager 宛ての取り消しは何もしないことを確認する (再レビュー指摘 2)。
func TestPasswordChangeWhileAbandoning(t *testing.T) {
	h := newUnregisteredHub(t)
	c := testConn(h, "dev-A")
	h.addConn(c)
	if code, msg, _ := h.bindAccount(c, "201", "wrong-1", ""); code != "" {
		t.Fatalf("新規作成が拒否: %s %s", code, msg)
	}
	g := h.groupFor("201")
	oldMgr := g.manager()
	markAbandoning(g)

	// 取り消し待ちの間のパスワード変更 (結び付いた端末・未登録なので受理される)。
	if code, msg, _ := h.bindAccount(c, "201", "wrong-2", ""); code != "" {
		t.Fatalf("パスワード変更が拒否: %s %s", code, msg)
	}
	g.mu.Lock()
	prov, abandoning := g.prov, g.abandoning
	g.mu.Unlock()
	if prov.mode != provNewAccount {
		t.Fatalf("新規 account の扱いが引き継がれていない: %+v", prov)
	}
	if abandoning {
		t.Errorf("作り直した Backend が取り消し待ちのまま")
	}
	// 古い Manager 宛ての取り消しは何もしない。
	h.abandonProvisional(g, oldMgr, maxProvisionalAuthFailures, time.Minute, "test")
	if h.groupFor("201") != g || g.isStopped() {
		t.Fatalf("古い Manager 宛ての取り消しでグループが外された")
	}
	if acc, ok := h.store.Account("201"); !ok || acc.Password != "wrong-2" {
		t.Errorf("状態ファイルが不正: %+v %v", acc, ok)
	}
	// 新しい Manager での取り消しは「新規 account の削除」として行われる。
	markAbandoning(g)
	h.abandonProvisional(g, g.manager(), maxProvisionalAuthFailures, time.Minute, "test")
	if h.groupFor("201") != nil {
		t.Errorf("新規 account が削除されていない")
	}
	if _, ok := h.store.Account("201"); ok {
		t.Errorf("account が状態ファイルに残っている")
	}
}

// TestRevertFailureKeepsStore は、パスワード変更の差し戻しで元の資格情報での
// 起動に失敗したら状態ファイルを変えないことを確認する。
func TestRevertFailureKeepsStore(t *testing.T) {
	fail := false
	factory := func(user, password, display string) (call.Backend, error) {
		if fail {
			return nil, context.DeadlineExceeded
		}
		return fakebackend.NewUnregistered(), nil
	}
	h := NewHub(factory, push.Noop{}, nil, Config{Version: "test", AuthFailureDelay: time.Millisecond}, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := h.Run(ctx); err != nil {
		t.Fatal(err)
	}
	c := testConn(h, "dev-A")
	h.addConn(c)
	if code, _, _ := h.bindAccount(c, "301", "pw-new", ""); code != "" {
		t.Fatal(code)
	}
	g := h.groupFor("301")
	g.mu.Lock()
	g.prov = provisional{mode: provPasswordChange, prevPassword: "pw-old"}
	g.abandoning = true
	g.mu.Unlock()
	fail = true
	h.abandonProvisional(g, g.manager(), maxProvisionalAuthFailures, time.Minute, "test")
	if acc, _ := h.store.Account("301"); acc.Password != "pw-new" {
		t.Errorf("起動に失敗したのに状態ファイルが書き換えられた: %+v", acc)
	}
	if pw, _ := g.credentials(); pw != "pw-new" {
		t.Errorf("メモリ上の資格情報が変わった: %q", pw)
	}
}

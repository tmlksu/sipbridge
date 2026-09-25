package session

import (
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tmlksu/sipbridge/relay/internal/call"
	"github.com/tmlksu/sipbridge/relay/internal/fakebackend"
	"github.com/tmlksu/sipbridge/relay/internal/proto"
	"github.com/tmlksu/sipbridge/relay/internal/push"
)

// RTP ホットパスのキャッシュ (group.route / Conn.grp) が attach/detach・
// 勝者の交代・通話終了・Backend 作り直しに追従することを内部状態で確かめる。
// WS を張らずに Conn を直接作る (attach/detach/handleBinary/handleText は
// ws に触れない。送信は out キューに積まれるだけ)。

type routeFixture struct {
	h  *Hub
	g  *group
	mu sync.Mutex
	fb *fakebackend.Fake
}

func newRouteFixture(t *testing.T) *routeFixture {
	t.Helper()
	rf := &routeFixture{}
	factory := func(user, password, display string) (call.Backend, error) {
		fb := fakebackend.New()
		rf.mu.Lock()
		rf.fb = fb
		rf.mu.Unlock()
		return fb, nil
	}
	rf.h = NewHub(factory, push.Noop{}, nil, Config{ResumeTimeout: time.Hour}, nil)
	g, err := rf.h.ensureGroup("101", "pw", "")
	if err != nil {
		t.Fatalf("ensureGroup: %v", err)
	}
	t.Cleanup(g.stop)
	rf.g = g
	waitFor(t, "登録", func() bool { return g.registered() })
	return rf
}

func (rf *routeFixture) backend() *fakebackend.Fake {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.fb
}

func (rf *routeFixture) newConn(device string) *Conn {
	c := &Conn{hub: rf.h, deviceID: device, out: make(chan outFrame, sendQueueSize)}
	rf.h.moveConn(c, rf.g)
	return c
}

func (rf *routeFixture) remove(c *Conn) {
	rf.h.mu.Lock()
	if g := c.grp.Swap(nil); g != nil {
		g.detach(c)
	}
	rf.h.mu.Unlock()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s を待ったが成立しない", what)
}

func sendText(t *testing.T, c *Conn, v any) {
	t.Helper()
	data, err := proto.Encode(v)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	c.handleText(data)
}

// nextBinary は out から次のバイナリを取り出す (テキストは読み飛ばす)。
func nextBinary(c *Conn, d time.Duration) ([]byte, bool) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case f := <-c.out:
			if f.typ == websocket.MessageBinary {
				return f.data, true
			}
		case <-timer.C:
			return nil, false
		}
	}
}

func routeTargets(r *mediaRoute) map[*Conn]bool {
	out := map[*Conn]bool{}
	if r != nil {
		for _, c := range r.targets {
			out[c] = true
		}
	}
	return out
}

// answer は device の接続 c で着信に応答し、route が確定するまで待つ。
func (rf *routeFixture) answer(t *testing.T, c *Conn) string {
	t.Helper()
	id := rf.backend().InjectIncoming("102", "", 0)
	waitFor(t, "着信", func() bool {
		cur := rf.g.manager().Current()
		return cur != nil && cur.CallID == id
	})
	sendText(t, c, &proto.Answer{T: proto.TAnswer, CallID: id})
	waitFor(t, "route 確定", func() bool {
		r := rf.g.route.Load()
		return r != nil && r.winner == c.deviceID && r.pipe != nil && r.pipe == rf.g.manager().Pipe()
	})
	return id
}

func TestRouteFollowsAttachDetachAndCallEnd(t *testing.T) {
	rf := newRouteFixture(t)
	a1 := rf.newConn("A")
	b := rf.newConn("B")
	if rf.g.route.Load() != nil {
		t.Fatalf("通話前は route が nil のはず")
	}

	id := rf.answer(t, a1)
	if got := routeTargets(rf.g.route.Load()); len(got) != 1 || !got[a1] {
		t.Fatalf("配信先は A の接続のみのはず: %v", got)
	}

	// 上り → echo → 下りが勝者に届く。敗者の上りは捨てる。
	b.handleBinary([]byte{9, 9, 9})
	a1.handleBinary([]byte{1, 2, 3})
	if data, ok := nextBinary(a1, 2*time.Second); !ok || string(data) != string([]byte{1, 2, 3}) {
		t.Fatalf("勝者にエコーが届かない: %v %v", data, ok)
	}
	if data, ok := nextBinary(a1, 50*time.Millisecond); ok {
		t.Fatalf("敗者の上りが中継された: %v", data)
	}
	if _, ok := nextBinary(b, 50*time.Millisecond); ok {
		t.Fatalf("敗者に下りが届いた")
	}

	// 勝者の重複接続 (再接続) → 旧接続の切断で配信先が新接続に移る。
	a2 := rf.newConn("A")
	if got := routeTargets(rf.g.route.Load()); len(got) != 2 || !got[a1] || !got[a2] {
		t.Fatalf("配信先は A の 2 接続のはず: %v", got)
	}
	rf.remove(a1)
	if got := routeTargets(rf.g.route.Load()); len(got) != 1 || !got[a2] {
		t.Fatalf("切断済み接続が配信先に残っている: %v", got)
	}
	if a1.group() != nil {
		t.Fatalf("切断済み接続の group は nil のはず")
	}
	a1.handleBinary([]byte{7}) // 切断済み接続の上りは捨てる
	a2.handleBinary([]byte{4, 5, 6})
	if data, ok := nextBinary(a2, 2*time.Second); !ok || string(data) != string([]byte{4, 5, 6}) {
		t.Fatalf("新接続にエコーが届かない: %v %v", data, ok)
	}
	if _, ok := nextBinary(a1, 50*time.Millisecond); ok {
		t.Fatalf("切断済み接続に下りが届いた")
	}

	// 通話終了で route は消え、上りはどこにも送らない。
	oldPipe := rf.g.route.Load().pipe
	sendText(t, a2, &proto.Hangup{T: proto.THangup, CallID: id})
	waitFor(t, "route 解除", func() bool { return rf.g.route.Load() == nil })
	a2.handleBinary([]byte{8})
	if _, ok := nextBinary(a2, 50*time.Millisecond); ok {
		t.Fatalf("通話終了後に下りが届いた")
	}

	// 次の通話は B が勝者。前の呼のパイプ・勝者を指さないこと。
	rf.answer(t, b)
	r := rf.g.route.Load()
	if r.pipe == oldPipe {
		t.Fatalf("前の呼のパイプを指している")
	}
	if got := routeTargets(r); len(got) != 1 || !got[b] {
		t.Fatalf("配信先は B のみのはず: %v", got)
	}
	a2.handleBinary([]byte{9}) // 前の勝者の上りは捨てる
	b.handleBinary([]byte{1})
	if data, ok := nextBinary(b, 2*time.Second); !ok || string(data) != string([]byte{1}) {
		t.Fatalf("B にエコーが届かない: %v %v", data, ok)
	}
	if data, ok := nextBinary(b, 50*time.Millisecond); ok {
		t.Fatalf("前の勝者の上りが中継された: %v", data)
	}
	if _, ok := nextBinary(a2, 50*time.Millisecond); ok {
		t.Fatalf("前の勝者に下りが届いた")
	}

	// 別グループへの付け替え (account 変更) で route から外れる。
	rf.h.moveConn(b, nil)
	if got := routeTargets(rf.g.route.Load()); len(got) != 0 {
		t.Fatalf("付け替え後も配信先に残っている: %v", got)
	}

	// Backend 作り直しで route は消える (古い Manager のパイプを指さない)。
	if err := rf.g.startBackend("pw2", ""); err != nil {
		t.Fatalf("startBackend: %v", err)
	}
	if rf.g.route.Load() != nil {
		t.Fatalf("Backend 作り直し後は route が nil のはず")
	}
}

// TestRouteConcurrentChurn は上り/下り RTP が流れている最中に接続の出入り・
// 勝者の張り替えを並行で起こし、-race でデータ競合が無いことを確かめる。
func TestRouteConcurrentChurn(t *testing.T) {
	rf := newRouteFixture(t)
	a := rf.newConn("A")
	rf.answer(t, a)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	// 上り (勝者)。echo パイプ経由で下りのメディアポンプも回る。
	wg.Add(1)
	go func() {
		defer wg.Done()
		pkt := []byte{0x80, 0, 0, 1}
		for {
			select {
			case <-stop:
				return
			default:
			}
			a.handleBinary(pkt)
			time.Sleep(100 * time.Microsecond)
		}
	}()
	// 下りを読み捨てる (キュー溢れのログを避ける)。
	drain := func(c *Conn) {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-c.out:
			}
		}
	}
	wg.Add(1)
	go drain(a)
	// 勝者デバイスの重複接続と他デバイスの出入り。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			dev := "A"
			if i%2 == 1 {
				dev = "B"
			}
			c := rf.newConn(dev)
			c.handleBinary([]byte{1})
			rf.remove(c)
		}
	}()
	// 勝者の張り替え (同じ値の再設定も route を作り直す)。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			rf.g.setWinner("A")
			time.Sleep(50 * time.Microsecond)
		}
	}()

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()

	if got := routeTargets(rf.g.route.Load()); len(got) != 1 || !got[a] {
		t.Fatalf("churn 後の配信先は A の元接続のみのはず: %v", got)
	}
}

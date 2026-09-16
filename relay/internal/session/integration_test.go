//go:build integration

// マルチアカウント結合テスト (Docker Asterisk 必須)。
//
//	cd deploy/test && docker compose -f compose.yml up -d
//	cd relay && go test -tags integration ./internal/session/ -v
//
// 同一 relay プロセス (同一 Hub) 内に内線 101 と 102 の 2 アカウントを
// 登録し、device A (101) から device B (102) へ Asterisk 経由で発信する。
package session_test

import (
	"context"
	"encoding/binary"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tmlksu/sipbridge/relay/internal/call"
	"github.com/tmlksu/sipbridge/relay/internal/proto"
	"github.com/tmlksu/sipbridge/relay/internal/push"
	"github.com/tmlksu/sipbridge/relay/internal/session"
	"github.com/tmlksu/sipbridge/relay/internal/sipbackend"
	"github.com/tmlksu/sipbridge/relay/internal/state"
)

// 結合テストの既定値 (deploy/test/asterisk/*.conf のダミー値と対応)。
const (
	itestRTPMin    = 41000
	itestRTPMax    = 41100
	itestRegWait   = 90 * time.Second // Asterisk 起動直後は登録に時間がかかる
	itestTalk      = 3 * time.Second
	itestMinPacket = 20 // 双方向で最低限受け取りたい RTP パケット数
)

func itestEnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func itestAsterisk(t *testing.T) (host string, port int, pass101, pass102 string) {
	t.Helper()
	host = itestEnvOr("TEST_ASTERISK_HOST", "127.0.0.1")
	port, err := strconv.Atoi(itestEnvOr("TEST_ASTERISK_PORT", "5060"))
	if err != nil {
		t.Fatalf("TEST_ASTERISK_PORT が不正: %v", err)
	}
	return host, port, itestEnvOr("TEST_SIP_101_PASS", "test101pass"),
		itestEnvOr("TEST_SIP_102_PASS", "test102pass")
}

// peer は 1 端末分の WS 接続である。受信は goroutine で読み、
// テキストはチャネルへ、バイナリ (RTP) は数える。
type peer struct {
	t      *testing.T
	name   string
	ws     *websocket.Conn
	msgs   chan any
	rtpIn  atomic.Int64
	rtpOut atomic.Int64
	wmu    sync.Mutex
}

func newPeer(t *testing.T, url, device string) *peer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"X-Device-Id": {device}},
	})
	if err != nil {
		t.Fatalf("%s の WS 接続失敗: %v", device, err)
	}
	ws.SetReadLimit(1 << 20)
	p := &peer{t: t, name: device, ws: ws, msgs: make(chan any, 256)}
	go p.readLoop()
	t.Cleanup(func() { _ = ws.Close(websocket.StatusNormalClosure, "bye") })
	return p
}

func (p *peer) readLoop() {
	defer close(p.msgs)
	for {
		typ, data, err := p.ws.Read(context.Background())
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageBinary:
			p.rtpIn.Add(1)
		case websocket.MessageText:
			msg, derr := proto.Decode(data)
			if derr != nil {
				continue
			}
			select {
			case p.msgs <- msg:
			default:
			}
		}
	}
}

func (p *peer) send(v any) {
	p.t.Helper()
	data, err := proto.Encode(v)
	if err != nil {
		p.t.Fatalf("%s: Encode 失敗: %v", p.name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if err := p.ws.Write(ctx, websocket.MessageText, data); err != nil {
		p.t.Fatalf("%s: 送信失敗: %v", p.name, err)
	}
}

// wait は pred を満たすメッセージが来るまで読む。
func (p *peer) wait(what string, timeout time.Duration, pred func(any) bool) any {
	p.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remain := time.Until(deadline)
		if remain <= 0 {
			p.t.Fatalf("%s: %s が来ない", p.name, what)
		}
		timer := time.NewTimer(remain)
		select {
		case msg, ok := <-p.msgs:
			timer.Stop()
			if !ok {
				p.t.Fatalf("%s: 接続が切れた (%s 待ち)", p.name, what)
			}
			if pred(msg) {
				return msg
			}
		case <-timer.C:
			p.t.Fatalf("%s: %s が来ない (タイムアウト)", p.name, what)
		}
	}
}

// sendRTP は PCMU 無音の RTP を 20ms 間隔で d の間送る。
func (p *peer) sendRTP(d time.Duration) {
	var seq uint16
	var ts uint32
	ssrc := uint32(0x1234abcd)
	if p.name != "" {
		ssrc += uint32(len(p.name))
	}
	until := time.Now().Add(d)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for range tick.C {
		if time.Now().After(until) {
			return
		}
		pkt := make([]byte, 12+160)
		pkt[0] = 0x80
		pkt[1] = 0 // PCMU
		binary.BigEndian.PutUint16(pkt[2:4], seq)
		binary.BigEndian.PutUint32(pkt[4:8], ts)
		binary.BigEndian.PutUint32(pkt[8:12], ssrc)
		for i := 12; i < len(pkt); i++ {
			pkt[i] = 0xFF // μ-law の無音
		}
		seq++
		ts += 160
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		p.wmu.Lock()
		err := p.ws.Write(ctx, websocket.MessageBinary, pkt)
		p.wmu.Unlock()
		cancel()
		if err != nil {
			return
		}
		p.rtpOut.Add(1)
	}
}

func isHelloMsg(msg any) bool { _, ok := msg.(*proto.Hello); return ok }

// bindAccount は sip_account を送り、受理後の hello を待つ。
func (p *peer) bindAccount(user, password string) *proto.Hello {
	p.t.Helper()
	p.send(&proto.SipAccount{T: proto.TSipAccount, User: user, Password: password})
	hello := p.wait("sip_account 後の hello", 10*time.Second, isHelloMsg).(*proto.Hello)
	if hello.Account != user {
		p.t.Fatalf("%s: hello.account = %q (%q のはず)", p.name, hello.Account, user)
	}
	return hello
}

// waitRegisteredLong は account が REGISTER 済みになるまで待つ。
func waitRegisteredLong(t *testing.T, hub *session.Hub, account string) {
	t.Helper()
	deadline := time.Now().Add(itestRegWait)
	for time.Now().Before(deadline) {
		if hub.AccountRegistered(account) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("account %q が Asterisk に登録できない", account)
}

// TestMultiAccountCallThroughAsterisk は同一 Hub の 101 → 102 発信である。
// device A (101) が dial 102 → device B (102) に incoming → answer →
// RTP 双方向 → hangup → 両方 ended。
func TestMultiAccountCallThroughAsterisk(t *testing.T) {
	host, port, pass101, pass102 := itestAsterisk(t)

	store, err := state.New(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("state.New 失敗: %v", err)
	}
	factory := func(user, password, display string) (call.Backend, error) {
		return sipbackend.New(sipbackend.Config{
			SIPHost:    host,
			SIPPort:    port,
			User:       user,
			Password:   password,
			Display:    display,
			LocalIP:    "127.0.0.1",
			RTPPortMin: itestRTPMin,
			RTPPortMax: itestRTPMax,
		}, slog.Default())
	}
	hub := session.NewHub(factory, push.Noop{}, store, session.Config{
		Version:       "itest",
		PingInterval:  30 * time.Second,
		ResumeTimeout: 2 * time.Second,
	}, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		// 各 account の登録解除 (REGISTER Expires: 0) を送り切ってから抜ける
		// (Asterisk に古い contact を残さない)。
		time.Sleep(500 * time.Millisecond)
	}()
	if err := hub.Run(ctx); err != nil {
		t.Fatalf("hub.Run 失敗: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(hub.ServeWS))
	defer srv.Close()
	url := "ws://" + srv.Listener.Addr().String() + "/v1/session"

	a := newPeer(t, url, "itest-A")
	a.wait("hello", 5*time.Second, isHelloMsg)
	a.bindAccount("101", pass101)
	b := newPeer(t, url, "itest-B")
	b.wait("hello", 5*time.Second, isHelloMsg)
	b.bindAccount("102", pass102)

	waitRegisteredLong(t, hub, "101")
	waitRegisteredLong(t, hub, "102")
	t.Logf("101/102 とも REGISTER 済み")

	// A が 102 へ発信 → B に着信。
	a.send(&proto.Dial{T: proto.TDial, To: "102"})
	inc := b.wait("incoming", 20*time.Second, func(msg any) bool {
		_, ok := msg.(*proto.Incoming)
		return ok
	}).(*proto.Incoming)
	t.Logf("B に着信: callId=%s from=%s", inc.CallID, inc.From)

	// B が応答 → 双方 answered。
	b.send(&proto.Answer{T: proto.TAnswer, CallID: inc.CallID})
	ansB := b.wait("answered (B)", 15*time.Second, func(msg any) bool {
		_, ok := msg.(*proto.Answered)
		return ok
	}).(*proto.Answered)
	ansA := a.wait("answered (A)", 15*time.Second, func(msg any) bool {
		_, ok := msg.(*proto.Answered)
		return ok
	}).(*proto.Answered)
	t.Logf("応答: A callId=%s / B callId=%s", ansA.CallID, ansB.CallID)

	// 双方向 RTP。
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.sendRTP(itestTalk) }()
	go func() { defer wg.Done(); b.sendRTP(itestTalk) }()
	wg.Wait()
	time.Sleep(500 * time.Millisecond) // 残りの受信を待つ

	t.Logf("RTP: A 送信 %d / A 受信 %d, B 送信 %d / B 受信 %d",
		a.rtpOut.Load(), a.rtpIn.Load(), b.rtpOut.Load(), b.rtpIn.Load())
	if a.rtpIn.Load() < itestMinPacket {
		t.Errorf("A の受信 RTP = %d (%d 以上のはず)", a.rtpIn.Load(), itestMinPacket)
	}
	if b.rtpIn.Load() < itestMinPacket {
		t.Errorf("B の受信 RTP = %d (%d 以上のはず)", b.rtpIn.Load(), itestMinPacket)
	}

	// A が切断 → 双方 ended。
	a.send(&proto.Hangup{T: proto.THangup, CallID: ansA.CallID})
	endA := a.wait("ended (A)", 10*time.Second, func(msg any) bool {
		_, ok := msg.(*proto.Ended)
		return ok
	}).(*proto.Ended)
	endB := b.wait("ended (B)", 10*time.Second, func(msg any) bool {
		_, ok := msg.(*proto.Ended)
		return ok
	}).(*proto.Ended)
	t.Logf("終了: A reason=%s / B reason=%s", endA.Reason, endB.Reason)
	if endB.Reason != "bye" {
		t.Errorf("B の ended.reason = %q (bye のはず)", endB.Reason)
	}
}

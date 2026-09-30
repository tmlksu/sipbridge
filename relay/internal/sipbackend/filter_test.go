// Package sipbackend の送信元フィルタの結合テスト (#28, #29)。
// 127.0.0.2 / 127.0.0.3 など 127.0.0.1 以外のループバックを「LAN の別ホスト」
// として使う (Linux では lo の 127.0.0.0/8 から任意に bind できる)。
package sipbackend

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"github.com/tmlksu/sipbridge/relay/internal/call"
)

// listenFrom は ip に bind した UDP ソケットを作る。bind できなければ Skip する。
func listenFrom(t *testing.T, ip string) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(ip)})
	if err != nil {
		t.Skipf("%s に bind できない: %v", ip, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// recordingRegistrar は REGISTER に 200 を返し、送信元 (sipgo client の
// エフェメラルソケット) を通知するサーバである。
func recordingRegistrar(t *testing.T, ctx context.Context) (string, <-chan string) {
	t.Helper()
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("test-registrar"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ua.Close() })
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatal(err)
	}
	srcCh := make(chan string, 16)
	srv.OnRegister(func(req *sip.Request, tx sip.ServerTransaction) {
		select {
		case srcCh <- req.Source():
		default:
		}
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		_ = tx.Respond(res)
	})
	addr := freeAddr(t)
	go func() { _ = srv.ListenAndServe(ctx, "udp", addr) }()
	time.Sleep(200 * time.Millisecond)
	return addr, srcCh
}

// rawRequest は最小限の SIP 要求を組み立てる。
func rawRequest(method string, from *net.UDPConn, to *net.UDPAddr, callID, tag string, body []byte) []byte {
	la := from.LocalAddr().(*net.UDPAddr)
	var b strings.Builder
	fmt.Fprintf(&b, "%s sip:101@%s SIP/2.0\r\n", method, to.String())
	fmt.Fprintf(&b, "Via: SIP/2.0/UDP %s;branch=z9hG4bK%s;rport\r\n", la.String(), randHex(8))
	b.WriteString("Max-Forwards: 70\r\n")
	fmt.Fprintf(&b, "From: <sip:tester@%s>;tag=%s\r\n", la.IP.String(), tag)
	fmt.Fprintf(&b, "To: <sip:101@%s>\r\n", to.IP.String())
	fmt.Fprintf(&b, "Call-ID: %s\r\n", callID)
	fmt.Fprintf(&b, "CSeq: 1 %s\r\n", method)
	fmt.Fprintf(&b, "Contact: <sip:tester@%s>\r\n", la.String())
	if len(body) > 0 {
		b.WriteString("Content-Type: application/sdp\r\n")
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n", len(body))
	b.Write(body)
	return []byte(b.String())
}

func sendRaw(t *testing.T, c *net.UDPConn, to *net.UDPAddr, msg []byte) {
	t.Helper()
	if _, err := c.WriteToUDP(msg, to); err != nil {
		t.Fatal(err)
	}
}

// expectNoEvent は d の間イベントが来ないことを確かめる (登録結果は無視)。
func expectNoEvent(t *testing.T, ev <-chan call.Event, d time.Duration) {
	t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case e := <-ev:
			if _, ok := e.(call.EvRegistered); ok {
				continue
			}
			t.Fatalf("許可外の送信元でイベントが出た: %+v", e)
		case <-timer.C:
			return
		}
	}
}

// waitIncoming は EvIncoming を待つ (登録結果は読み飛ばす)。
func waitIncoming(t *testing.T, ev <-chan call.Event, d time.Duration) call.EvIncoming {
	t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case e := <-ev:
			if in, ok := e.(call.EvIncoming); ok {
				return in
			}
			if _, ok := e.(call.EvRegistered); ok {
				continue
			}
			t.Fatalf("Incoming ではなく %+v", e)
		case <-timer.C:
			t.Fatal("Incoming が来ない")
		}
	}
}

// TestSIPSourceFilter は許可外 IP (127.0.0.2) からの INVITE/BYE/CANCEL/OPTIONS が
// 待受ソケットと REGISTER 送信ソケットの両方で捨てられ、その後も正規の
// 送信元 (127.0.0.1) からの INVITE が通る (読み取りループが生きている) ことを確かめる。
// 同じ Call-ID のダイアログ外 INVITE が 482 で拒否されることも確かめる。
func TestSIPSourceFilter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	evil := listenFrom(t, "127.0.0.2")
	good := listenFrom(t, "127.0.0.1")

	regAddr, srcCh := recordingRegistrar(t, ctx)
	be, ev := startTestBackend(t, ctx, regAddr, "101", "", 36200)
	waitRegistered(t, ev)
	var regSrc string
	select {
	case regSrc = <-srcCh:
	case <-time.After(3 * time.Second):
		t.Fatal("REGISTER の送信元が分からない")
	}
	regPort := netip.MustParseAddrPort(regSrc).Port()
	listenAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: be.ServerPort()}
	regSockAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(regPort)}
	if int(regPort) == be.ServerPort() {
		t.Logf("REGISTER は待受ソケットから送られた (port %d)", regPort)
	} else {
		t.Logf("待受 %d / REGISTER 送信 %d", be.ServerPort(), regPort)
	}

	sdp := buildOfferSDP("127.0.0.2", 40000)
	targets := []*net.UDPAddr{listenAddr, regSockAddr}
	for _, dst := range targets {
		sendRaw(t, evil, dst, rawRequest("INVITE", evil, dst, "evil-"+randHex(4), "t1", sdp))
		sendRaw(t, evil, dst, rawRequest("BYE", evil, dst, "evil-"+randHex(4), "t2", nil))
		sendRaw(t, evil, dst, rawRequest("CANCEL", evil, dst, "evil-"+randHex(4), "t3", nil))
		sendRaw(t, evil, dst, rawRequest("OPTIONS", evil, dst, "evil-"+randHex(4), "t4", nil))
	}
	expectNoEvent(t, ev, 700*time.Millisecond)
	// 許可外には応答も返さない (100 Trying / 481 / 200 も出さない)。
	if msg := readSIPUntil(t, evil, "SIP/2.0", 300*time.Millisecond); msg != "" {
		t.Errorf("許可外の送信元に応答した:\n%s", msg)
	}
	if n := be.DroppedSIPRequests(); n < 8 {
		t.Errorf("破棄数 %d (8 以上のはず)", n)
	}

	// 正規の送信元からは REGISTER 送信ソケット宛ての INVITE も通る
	// (Asterisk の rewrite_contact/force_rport 構成)。
	sdpGood := buildOfferSDP("127.0.0.1", 40002)
	sendRaw(t, good, regSockAddr, rawRequest("INVITE", good, regSockAddr, "good-1", "g1", sdpGood))
	in1 := waitIncoming(t, ev, 5*time.Second)
	if in1.CallID != "good-1" {
		t.Errorf("CallID = %q", in1.CallID)
	}
	if err := be.Reject(in1.CallID, 486); err != nil {
		t.Fatal(err)
	}

	// 待受ソケットの読み取りループも生きている。
	sendRaw(t, good, listenAddr, rawRequest("INVITE", good, listenAddr, "good-2", "g2", sdpGood))
	in2 := waitIncoming(t, ev, 5*time.Second)
	if in2.CallID != "good-2" {
		t.Errorf("CallID = %q", in2.CallID)
	}

	// 同じ Call-ID・別タグのダイアログ外 INVITE は 482 で拒否し、通話を上書きしない。
	// (応答の読み取りの前に、これまでの 100/180/486 を読み捨てる)
	_ = readSIPUntil(t, good, "never", 300*time.Millisecond)
	sendRaw(t, good, listenAddr, rawRequest("INVITE", good, listenAddr, "good-2", "other", sdpGood))
	if msg := readSIPUntil(t, good, "SIP/2.0 482", 3*time.Second); msg == "" {
		t.Error("同じ Call-ID の INVITE に 482 が返らない")
	}
	expectNoEvent(t, ev, 300*time.Millisecond)
	be.mu.Lock()
	sc := be.calls["good-2"]
	be.mu.Unlock()
	if sc == nil || sc.from != "tester" || sc.dlgSrv == nil {
		t.Errorf("元の通話エントリが壊れた: %+v", sc)
	}
	if err := be.Reject("good-2", 486); err != nil {
		t.Error(err)
	}
}

// ---- RTP ----

func rtpPkt(seq byte) []byte {
	return []byte{0x80, 0x00, 0x00, seq, 0, 0, 0, 0, 0, 0, 0, 1, 0xAA, 0xBB}
}

func expectRecv(t *testing.T, p *rtpPipe, want []byte, d time.Duration) {
	t.Helper()
	select {
	case got := <-p.Recv():
		if string(got) != string(want) {
			t.Fatalf("受信内容が違う: %v (期待 %v)", got, want)
		}
	case <-time.After(d):
		t.Fatalf("受信されない: %v", want)
	}
}

func expectNoRecv(t *testing.T, p *rtpPipe, d time.Duration) {
	t.Helper()
	select {
	case got := <-p.Recv():
		t.Fatalf("捨てるべきパケットを受信した: %v", got)
	case <-time.After(d):
	}
}

// TestRTPSourceFilter は RTP の送信元照合 (#29) を確かめる。
func TestRTPSourceFilter(t *testing.T) {
	// 本番と同じくワイルドカード (デュアルスタック) で bind する。
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	dst := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: conn.LocalAddr().(*net.UDPAddr).Port}
	asterisk := listenFrom(t, "127.0.0.1")
	asterisk2 := listenFrom(t, "127.0.0.1") // 同 IP・別ポート
	evil := listenFrom(t, "127.0.0.2")
	sipPeer := listenFrom(t, "127.0.0.3") // SIP の信頼集合にだけ入っている IP

	trusted := func(a netip.Addr) bool { return a == netip.MustParseAddr("127.0.0.3") }
	p := newRTPPipe(conn, asterisk.LocalAddr().(*net.UDPAddr), nil, trusted)
	defer p.Close()
	const wait = 300 * time.Millisecond

	// c= の IP (127.0.0.1) からは受ける。ポート違いも受ける。
	sendRaw(t, asterisk, dst, rtpPkt(1))
	expectRecv(t, p, rtpPkt(1), 3*time.Second)
	sendRaw(t, asterisk2, dst, rtpPkt(2))
	expectRecv(t, p, rtpPkt(2), 3*time.Second)
	// 別 IP は捨てる。
	sendRaw(t, evil, dst, rtpPkt(3))
	expectNoRecv(t, p, wait)
	// SIP の信頼集合の IP は受ける (同居で c=127.0.0.1 / 実送信元 LOCAL_IP の構成)。
	sendRaw(t, sipPeer, dst, rtpPkt(4))
	expectRecv(t, p, rtpPkt(4), 3*time.Second)
	// 短いパケット・バージョン違いは捨てる。
	sendRaw(t, asterisk, dst, []byte{0x80, 0x00, 0x00, 0x05, 0, 0, 0, 0})
	bad := rtpPkt(6)
	bad[0] = 0x40 // version 1
	sendRaw(t, asterisk, dst, bad)
	expectNoRecv(t, p, wait)

	// setRemote に追従する: 以後は 127.0.0.2 を受け、127.0.0.1 を捨てる。
	p.setRemote(evil.LocalAddr().(*net.UDPAddr))
	sendRaw(t, evil, dst, rtpPkt(7))
	expectRecv(t, p, rtpPkt(7), 3*time.Second)
	sendRaw(t, asterisk, dst, rtpPkt(8))
	expectNoRecv(t, p, wait)

	// 宛先未確定 (nil) は信頼集合の IP も含め全て捨てる (fail-closed)。
	p.setRemote(nil)
	sendRaw(t, evil, dst, rtpPkt(9))
	sendRaw(t, sipPeer, dst, rtpPkt(10))
	expectNoRecv(t, p, wait)

	if n := p.RejectedPackets(); n != 6 {
		t.Errorf("破棄数 %d (6 のはず)", n)
	}
}

// TestUpdateRemoteIgnoresHold は c=0.0.0.0 (保留) の SDP が宛先を潰さないことを確かめる。
func TestUpdateRemoteIgnoresHold(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	orig := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40000}
	p := newRTPPipe(conn, orig, nil, nil)
	defer p.Close()
	sc := &sipCall{remote: orig, pipe: p}

	hold := []byte("v=0\r\no=- 1 2 IN IP4 0.0.0.0\r\ns=-\r\nc=IN IP4 0.0.0.0\r\nt=0 0\r\nm=audio 40000 RTP/AVP 0\r\na=sendonly\r\n")
	off, err := parseOffer(hold)
	if err != nil {
		t.Fatalf("保留 SDP の解析失敗: %v", err)
	}
	updateRemote(sc, off.addr)
	if sc.remote != orig {
		t.Errorf("sc.remote が保留で潰れた: %v", sc.remote)
	}
	p.mu.RLock()
	got := p.remote
	p.mu.RUnlock()
	if got != orig {
		t.Errorf("pipe.remote が保留で潰れた: %v", got)
	}

	moved := &net.UDPAddr{IP: net.ParseIP("127.0.0.5"), Port: 40010}
	updateRemote(sc, moved)
	p.mu.RLock()
	got, gotIP := p.remote, p.remoteIP
	p.mu.RUnlock()
	if sc.remote != moved || got != moved || gotIP != netip.MustParseAddr("127.0.0.5") {
		t.Errorf("宛先変更に追従しない: sc=%v pipe=%v ip=%v", sc.remote, got, gotIP)
	}
	updateRemote(sc, nil)
	if sc.remote != moved {
		t.Error("nil で宛先が潰れた")
	}
}

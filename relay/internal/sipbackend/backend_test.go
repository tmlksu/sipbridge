// Package sipbackend の単体テスト (Docker 不要)。
// 結合テスト (Asterisk) は integration_test.go (//go:build integration)。
package sipbackend

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"

	"github.com/tmlksu/sipbridge/relay/internal/call"
)

func testCtx(t *testing.T, d time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), d)
}

// freeAddr は 127.0.0.1 の空き UDP ポートを返す。
func freeAddr(t *testing.T) string {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("空きポートの確保失敗: %v", err)
	}
	defer c.Close()
	return c.LocalAddr().String()
}

// waitEvent は Backend イベントを 1 件待つ。
func waitEvent(t *testing.T, ch <-chan call.Event, d time.Duration) call.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(d):
		t.Fatalf("イベントが来ない (%v)", d)
		return nil
	}
}

func TestBuildParseSDPRoundtrip(t *testing.T) {
	for _, pt := range []int{PTPCMU, PTPCMA} {
		sdp := buildAnswerSDP("192.168.1.5", 20002, pt)
		off, err := parseOffer(sdp)
		if err != nil {
			t.Fatalf("pt=%d 解析失敗: %v", pt, err)
		}
		if off.pt != pt {
			t.Errorf("pt=%d が %d になった", pt, off.pt)
		}
		if off.addr.Port != 20002 || off.addr.IP.String() != "192.168.1.5" {
			t.Errorf("宛先が不正: %v", off.addr)
		}
	}
	// 発信オファー (0 8 両提示) は先頭の PCMU が選ばれる。
	off, err := parseOffer(buildOfferSDP("127.0.0.1", 30000))
	if err != nil {
		t.Fatalf("オファー解析失敗: %v", err)
	}
	if off.pt != PTPCMU {
		t.Errorf("オファーの選択 PT = %d (PCMU のはず)", off.pt)
	}
}

func TestParseOfferErrors(t *testing.T) {
	bad := []string{
		"",
		"v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\n",
		"v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 5000 RTP/AVP 9 101\r\n",
		"v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 0 RTP/AVP 0\r\n",
		"v=0\r\nc=IN IP4 not-an-ip\r\nm=audio 5000 RTP/AVP 0\r\n",
	}
	for i, s := range bad {
		if _, err := parseOffer([]byte(s)); err == nil {
			t.Errorf("不正 SDP #%d が通ってしまった", i)
		}
	}
	// PCMA のみ提示 → PT 8 が選ばれる。
	off, err := parseOffer([]byte("v=0\r\nc=IN IP4 10.0.0.2\r\nm=audio 5004 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n"))
	if err != nil {
		t.Fatalf("PCMA 解析失敗: %v", err)
	}
	if off.pt != PTPCMA || off.addr.Port != 5004 {
		t.Errorf("PCMA 解析結果が不正: %+v", off)
	}
}

func TestRTPPipeLoopback(t *testing.T) {
	a, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	b, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	pa := newRTPPipe(a, b.LocalAddr().(*net.UDPAddr))
	pb := newRTPPipe(b, a.LocalAddr().(*net.UDPAddr))
	defer pa.Close()
	defer pb.Close()

	pkt := []byte{0x80, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x02}
	if err := pa.Send(pkt); err != nil {
		t.Fatalf("Send 失敗: %v", err)
	}
	select {
	case got := <-pb.Recv():
		if string(got) != string(pkt) {
			t.Errorf("受信内容が違う: %v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Recv タイムアウト")
	}

	// RTCP (SR, PT=200) は捨てられる。
	rtcp := []byte{0x80, 200, 0x00, 0x06, 0, 0, 0, 0}
	if err := pa.Send(rtcp); err != nil {
		t.Fatalf("RTCP Send 失敗: %v", err)
	}
	select {
	case got := <-pb.Recv():
		t.Fatalf("RTCP が届いてしまった: %v", got)
	case <-time.After(300 * time.Millisecond):
	}

	// Close は冪等で、Recv チャネルを閉じる。
	_ = pa.Close()
	_ = pa.Close()
	if err := pa.Send(pkt); err == nil {
		t.Error("閉じたパイプの Send が通ってしまった")
	}
}

// ---- テスト用 SIP サーバ ----

// dummyRegistrar は REGISTER に 200 を返すだけのサーバである。
// needAuth の場合は初回 401 (qop=auth) を返し、digest を検証してから 200 を返す。
func dummyRegistrar(t *testing.T, ctx context.Context, needAuth bool, user, pass string) string {
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
	chal := &digest.Challenge{
		Realm:     "test",
		Nonce:     "testnonce123",
		Algorithm: "MD5",
		Opaque:    "testopaque",
		QOP:       []string{"auth"},
	}
	srv.OnRegister(func(req *sip.Request, tx sip.ServerTransaction) {
		if needAuth && req.GetHeader("Authorization") == nil {
			res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
			// qop=auth を要求し、バックエンドの対応を確認する。
			res.AppendHeader(sip.NewHeader("WWW-Authenticate", chal.String()))
			_ = tx.Respond(res)
			return
		}
		if needAuth {
			h := req.GetHeader("Authorization")
			cred, err := digest.ParseCredentials(h.Value())
			if err != nil {
				_ = tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
				return
			}
			expect, err := digest.Digest(chal, digest.Options{
				Method:   "REGISTER",
				URI:      cred.URI,
				Username: user,
				Password: pass,
				// qop=auth では応答が cnonce/nc に依存するため、
				// クライアントの値を引き継いで再計算する (Asterisk と同じ方式)。
				Cnonce: cred.Cnonce,
				Count:  cred.Nc,
			})
			if err != nil || cred.Username != user || cred.Response != expect.Response {
				_ = tx.Respond(sip.NewResponseFromRequest(req, 403, "Forbidden", nil))
				return
			}
		}
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		res.AppendHeader(sip.NewHeader("Contact", req.GetHeader("Contact").Value()+";expires=300"))
		_ = tx.Respond(res)
	})
	addr := freeAddr(t)
	go func() {
		_ = srv.ListenAndServe(ctx, "udp", addr)
	}()
	// 待ち受け開始を少し待つ。
	time.Sleep(200 * time.Millisecond)
	return addr
}

// startTestBackend はテスト用 Backend を起動する。Asterisk 役は regAddr。
func startTestBackend(t *testing.T, ctx context.Context, regAddr, user, pass string, rtpBase int) (*Backend, <-chan call.Event) {
	t.Helper()
	host, portStr, _ := net.SplitHostPort(regAddr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	be, err := New(Config{
		SIPHost:    host,
		SIPPort:    port,
		User:       user,
		Password:   pass,
		LocalIP:    "127.0.0.1",
		RTPPortMin: rtpBase,
		RTPPortMax: rtpBase + 20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ev := make(chan call.Event, 32)
	if err := be.Start(ctx, ev); err != nil {
		t.Fatal(err)
	}
	return be, ev
}

func waitRegistered(t *testing.T, ev <-chan call.Event) {
	t.Helper()
	ctx, cancel := testCtx(t, 15*time.Second)
	defer cancel()
	for {
		select {
		case e := <-ev:
			if reg, ok := e.(call.EvRegistered); ok {
				if !reg.OK {
					t.Fatalf("REGISTER 失敗: %s", reg.Detail)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("REGISTER 成功が来ない")
		}
	}
}

func TestRegisterDigestQopAuth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	regAddr := dummyRegistrar(t, ctx, true, "101", "secret101")
	be, ev := startTestBackend(t, ctx, regAddr, "101", "secret101", 31000)
	waitRegistered(t, ev)
	if !be.Registered() {
		t.Error("Registered() が false")
	}
}

func TestRegisterNoAuth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	regAddr := dummyRegistrar(t, ctx, false, "", "")
	be, ev := startTestBackend(t, ctx, regAddr, "101", "", 31100)
	waitRegistered(t, ev)
	if !be.Registered() {
		t.Error("Registered() が false")
	}
}

// ---- 着信フロー (UAS 側) ----

// testUAC はテスト用の発呼側 (INVITE→RTP→BYE 受信) である。
type testUAC struct {
	cli     *sipgo.Client
	srv     *sipgo.Server
	dlgCli  *sipgo.DialogClientCache
	rtp     *net.UDPConn
	byeCh   chan struct{}
	contact sip.ContactHeader
}

func newTestUAC(t *testing.T, ctx context.Context) *testUAC {
	t.Helper()
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("test-uac"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ua.Close() })
	cli, err := sipgo.NewClient(ua, sipgo.WithClientHostname("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatal(err)
	}
	sipAddr := freeAddr(t)
	_, portStr, _ := net.SplitHostPort(sipAddr)
	var sipPort int
	fmt.Sscanf(portStr, "%d", &sipPort)
	u := &testUAC{byeCh: make(chan struct{}, 4)}
	u.contact = sip.ContactHeader{
		Address: sip.Uri{Scheme: "sip", User: "tester", Host: "127.0.0.1", Port: sipPort},
	}
	u.cli = cli
	u.srv = srv
	u.dlgCli = sipgo.NewDialogClientCache(cli, u.contact)
	srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		if err := u.dlgCli.ReadBye(req, tx); err != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, 481, "No Dialog", nil))
			return
		}
		select {
		case u.byeCh <- struct{}{}:
		default:
		}
	})
	srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {})
	go func() {
		_ = srv.ListenAndServe(ctx, "udp", sipAddr)
	}()
	rc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	u.rtp = rc
	time.Sleep(200 * time.Millisecond)
	return u
}

func (u *testUAC) rtpPort() int { return u.rtp.LocalAddr().(*net.UDPAddr).Port }

func TestIncomingFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	regAddr := dummyRegistrar(t, ctx, false, "", "")
	be, ev := startTestBackend(t, ctx, regAddr, "101", "", 31200)
	waitRegistered(t, ev)

	uac := newTestUAC(t, ctx)
	bePort := be.ServerPort()

	offer := buildOfferSDP("127.0.0.1", uac.rtpPort())
	recipient := sip.Uri{Scheme: "sip", User: "101", Host: "127.0.0.1", Port: bePort}
	from := &sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: "tester", Host: "127.0.0.1"}}
	from.Params.Add("tag", sip.GenerateTagN(8))
	sess, err := uac.dlgCli.Invite(ctx, recipient, offer,
		sip.NewHeader("Content-Type", "application/sdp"), from)
	if err != nil {
		t.Fatalf("INVITE 送信失敗: %v", err)
	}
	answerDone := make(chan error, 1)
	go func() {
		if err := sess.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
			answerDone <- err
			return
		}
		answerDone <- sess.Ack(ctx)
	}()

	// relay が Incoming を上げる。
	var incoming call.EvIncoming
	e := waitEvent(t, ev, 10*time.Second)
	var ok bool
	incoming, ok = e.(call.EvIncoming)
	if !ok {
		t.Fatalf("Incoming ではなく %+v", e)
	}
	if incoming.From != "tester" {
		t.Errorf("From = %q (tester のはず)", incoming.From)
	}
	if incoming.PT != PTPCMU {
		t.Errorf("PT = %d (PCMU のはず)", incoming.PT)
	}

	// 応答 → UAC 側で 200+ACK。
	pipe, err := be.Answer(incoming.CallID, 0)
	if err != nil {
		t.Fatalf("Answer 失敗: %v", err)
	}
	select {
	case err := <-answerDone:
		if err != nil {
			t.Fatalf("UAC 応答待ち失敗: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("UAC が 200 を受けない")
	}
	// 200 の SDP から relay の RTP ポートを得る。
	remote, err := parseOffer(sess.InviteResponse.Body())
	if err != nil {
		t.Fatalf("200 SDP 解析失敗: %v", err)
	}

	// UAC→relay: pipe.Recv で受け取れる。
	uacPkt := []byte{0x80, 0x00, 0x12, 0x34, 0, 0, 0, 0, 0, 0, 0, 0, 0xAA, 0xBB}
	if _, err := uac.rtp.WriteToUDP(uacPkt, remote.addr); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-pipe.Recv():
		if string(got) != string(uacPkt) {
			t.Errorf("relay 受信が違う: %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay が RTP を受けない")
	}

	// relay→UAC: pipe.Send が届く。
	bePkt := []byte{0x80, 0x00, 0x56, 0x78, 0, 0, 0, 0, 0, 0, 0, 0, 0xCC}
	if err := pipe.Send(bePkt); err != nil {
		t.Fatalf("pipe.Send 失敗: %v", err)
	}
	buf := make([]byte, 2048)
	_ = uac.rtp.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := uac.rtp.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("UAC が RTP を受けない: %v", err)
	}
	if string(buf[:n]) != string(bePkt) {
		t.Errorf("UAC 受信が違う: %v", buf[:n])
	}

	// relay 発の Hangup → UAC に BYE。
	if err := be.Hangup(incoming.CallID); err != nil {
		t.Fatalf("Hangup 失敗: %v", err)
	}
	select {
	case <-uac.byeCh:
	case <-time.After(8 * time.Second):
		t.Fatal("UAC が BYE を受けない")
	}
}

func TestIncomingReject(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	regAddr := dummyRegistrar(t, ctx, false, "", "")
	be, ev := startTestBackend(t, ctx, regAddr, "101", "", 31300)
	waitRegistered(t, ev)

	uac := newTestUAC(t, ctx)
	bePort := be.ServerPort()
	offer := buildOfferSDP("127.0.0.1", uac.rtpPort())
	recipient := sip.Uri{Scheme: "sip", User: "101", Host: "127.0.0.1", Port: bePort}
	from := &sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: "tester", Host: "127.0.0.1"}}
	from.Params.Add("tag", sip.GenerateTagN(8))
	sess, err := uac.dlgCli.Invite(ctx, recipient, offer,
		sip.NewHeader("Content-Type", "application/sdp"), from)
	if err != nil {
		t.Fatal(err)
	}
	respCh := make(chan error, 1)
	go func() {
		respCh <- sess.WaitAnswer(ctx, sipgo.AnswerOptions{})
	}()
	e := waitEvent(t, ev, 10*time.Second)
	incoming, ok := e.(call.EvIncoming)
	if !ok {
		t.Fatalf("Incoming ではなく %+v", e)
	}
	if err := be.Reject(incoming.CallID, 486); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-respCh:
		derr, ok := err.(*sipgo.ErrDialogResponse)
		if !ok {
			t.Fatalf("486 が返らない: %v", err)
		}
		if derr.Res.StatusCode != 486 {
			t.Fatalf("486 ではなく %d", derr.Res.StatusCode)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("UAC が失敗応答を受けない")
	}
}

// ---- 発信フロー (UAC 側) ----

// testUAS はテスト用の着呼側 (INVITE 受信→180/200→BYE 送出) である。
type testUAS struct {
	srv      *sipgo.Server
	dlgSrv   *sipgo.DialogServerCache
	rtp      *net.UDPConn
	dlgCh    chan *sipgo.DialogServerSession
	needAuth bool
	user     string
	pass     string
	authed   bool
}

func newTestUAS(t *testing.T, ctx context.Context, needAuth bool, user, pass string) (*testUAS, string) {
	t.Helper()
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("test-uas"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ua.Close() })
	cli, err := sipgo.NewClient(ua, sipgo.WithClientHostname("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatal(err)
	}
	sipAddr := freeAddr(t)
	_, portStr, _ := net.SplitHostPort(sipAddr)
	var sipPort int
	fmt.Sscanf(portStr, "%d", &sipPort)
	u := &testUAS{dlgCh: make(chan *sipgo.DialogServerSession, 4), needAuth: needAuth, user: user, pass: pass}
	contact := sip.ContactHeader{
		Address: sip.Uri{Scheme: "sip", User: "201", Host: "127.0.0.1", Port: sipPort},
	}
	u.dlgSrv = sipgo.NewDialogServerCache(cli, contact)
	rc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	u.rtp = rc
	chal := &digest.Challenge{Realm: "test-uas", Nonce: "uasnonce", Algorithm: "MD5"}
	srv.OnRegister(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = u.dlgSrv.ReadAck(req, tx)
	})
	srv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		if needAuth && !u.authed && req.GetHeader("Authorization") == nil && req.GetHeader("Proxy-Authorization") == nil {
			res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
			res.AppendHeader(sip.NewHeader("WWW-Authenticate", chal.String()))
			_ = tx.Respond(res)
			return
		}
		u.authed = true
		dlg, err := u.dlgSrv.ReadInvite(req, tx)
		if err != nil {
			return
		}
		_ = dlg.Respond(sip.StatusTrying, "Trying", nil)
		_ = dlg.Respond(sip.StatusRinging, "Ringing", nil)
		time.Sleep(100 * time.Millisecond)
		// 200 OK は ACK までブロックするため、ハンドラ内で同期的に送る。
		// (ハンドラ復帰後の TerminateGracefully に殺されないよう)
		_ = dlg.RespondSDP(buildAnswerSDP("127.0.0.1", u.rtp.LocalAddr().(*net.UDPAddr).Port, PTPCMU))
		select {
		case u.dlgCh <- dlg:
		default:
		}
	})
	go func() {
		_ = srv.ListenAndServe(ctx, "udp", sipAddr)
	}()
	u.srv = srv
	time.Sleep(200 * time.Millisecond)
	return u, sipAddr
}

func TestOutgoingFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	uas, uasAddr := newTestUAS(t, ctx, true, "101", "secret101")
	host, portStr, _ := net.SplitHostPort(uasAddr)
	var uasPort int
	fmt.Sscanf(portStr, "%d", &uasPort)
	_ = host
	be, err := New(Config{
		SIPHost:    "127.0.0.1",
		SIPPort:    uasPort,
		User:       "101",
		Password:   "secret101",
		LocalIP:    "127.0.0.1",
		RTPPortMin: 31400,
		RTPPortMax: 31420,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ev := make(chan call.Event, 32)
	if err := be.Start(ctx, ev); err != nil {
		t.Fatal(err)
	}
	waitRegistered(t, ev)

	callID, err := be.Dial("201")
	if err != nil {
		t.Fatalf("Dial 失敗: %v", err)
	}
	// 180 → Ringing。
	e := waitEvent(t, ev, 10*time.Second)
	ringing, ok := e.(call.EvRinging)
	if !ok {
		t.Fatalf("Ringing ではなく %+v", e)
	}
	if ringing.CallID != callID || ringing.Early {
		t.Errorf("Ringing が不正: %+v", ringing)
	}
	// 200 → Answered (パイプ付き)。
	e = waitEvent(t, ev, 10*time.Second)
	answered, ok := e.(call.EvAnswered)
	if !ok {
		t.Fatalf("Answered ではなく %+v", e)
	}
	if answered.CallID != callID || answered.Pipe == nil || answered.PT != PTPCMU {
		t.Fatalf("Answered が不正: %+v", answered)
	}
	pipe := answered.Pipe

	// relay→UAS: pipe.Send が届く。
	bePkt := []byte{0x80, 0x00, 0x11, 0x11, 0, 0, 0, 0, 0, 0, 0, 0, 0xDD}
	if err := pipe.Send(bePkt); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	_ = uas.rtp.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := uas.rtp.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("UAS が RTP を受けない: %v", err)
	}
	if string(buf[:n]) != string(bePkt) {
		t.Errorf("UAS 受信が違う: %v", buf[:n])
	}
	// UAS→relay: pipe.Recv で受け取れる。宛先は backend の RTP ソケット。
	uasPkt := []byte{0x80, 0x08, 0x22, 0x22, 0, 0, 0, 0, 0, 0, 0, 0, 0xEE}
	if _, err := uas.rtp.WriteToUDP(uasPkt, backendRTPAddr(t, be, callID)); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-pipe.Recv():
		if string(got) != string(uasPkt) {
			t.Errorf("relay 受信が違う: %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay が RTP を受けない")
	}

	// UAS 発の BYE → Ended(bye)。
	var dlg *sipgo.DialogServerSession
	select {
	case dlg = <-uas.dlgCh:
	case <-time.After(10 * time.Second):
		t.Fatal("UAS ダイアログが無い")
	}
	byeCtx, byeCancel := context.WithTimeout(ctx, 8*time.Second)
	defer byeCancel()
	if err := dlg.Bye(byeCtx); err != nil {
		t.Fatalf("UAS BYE 失敗: %v", err)
	}
	e = waitEvent(t, ev, 10*time.Second)
	ended, ok := e.(call.EvEnded)
	if !ok {
		t.Fatalf("Ended ではなく %+v", e)
	}
	if ended.CallID != callID || ended.Reason != "bye" || ended.Code != 200 {
		t.Errorf("Ended が不正: %+v", ended)
	}
}

// backendRTPAddr はテスト用に backend の RTP 待ち受けを返す。
func backendRTPAddr(t *testing.T, be *Backend, callID string) *net.UDPAddr {
	t.Helper()
	be.mu.Lock()
	defer be.mu.Unlock()
	sc, ok := be.calls[callID]
	if !ok {
		t.Fatalf("通話 %q が無い", callID)
	}
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: sc.port}
}

func TestOutgoingCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, uasAddr := newTestUAS(t, ctx, false, "", "")
	_, portStr, _ := net.SplitHostPort(uasAddr)
	var uasPort int
	fmt.Sscanf(portStr, "%d", &uasPort)
	be, err := New(Config{
		SIPHost:    "127.0.0.1",
		SIPPort:    uasPort,
		User:       "101",
		Password:   "",
		LocalIP:    "127.0.0.1",
		RTPPortMin: 31500,
		RTPPortMax: 31520,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ev := make(chan call.Event, 32)
	if err := be.Start(ctx, ev); err != nil {
		t.Fatal(err)
	}
	waitRegistered(t, ev)

	callID, err := be.Dial("201")
	if err != nil {
		t.Fatal(err)
	}
	// 即 Hangup (CANCEL)。Ringing が先に来てもよいが Ended は来てはならない。
	if err := be.Hangup(callID); err != nil {
		t.Fatalf("Hangup 失敗: %v", err)
	}
	timeout := time.After(3 * time.Second)
	for {
		select {
		case e := <-ev:
			if ended, ok := e.(call.EvEnded); ok && ended.CallID == callID {
				t.Fatalf("自発 Hangup なのに Ended が来た: %+v", ended)
			}
			// Ringing は競合で来ても正常。
		case <-timeout:
			return
		}
	}
}

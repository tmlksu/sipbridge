//go:build integration

// Asterisk 結合テスト (Docker 必須)。
//
//	cd deploy/test && docker compose -f compose.yml up -d
//	cd relay && go test -tags integration ./internal/sipbackend/ -v
package sipbackend

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"

	"github.com/tmlksu/sipbridge/relay/internal/call"
)

func itestEnv(t *testing.T) (host string, port int, pass101, pass102 string) {
	t.Helper()
	host = getenvOr("TEST_ASTERISK_HOST", "127.0.0.1")
	port = atoiOr(os.Getenv("TEST_ASTERISK_PORT"), 5060)
	pass101 = getenvOr("TEST_SIP_101_PASS", "test101pass")
	pass102 = getenvOr("TEST_SIP_102_PASS", "test102pass")
	return host, port, pass101, pass102
}

func getenvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

// startRelay101 は内線 101 として Asterisk に REGISTER する Backend を起動し、
// 登録成功を待って返す。
func startRelay101(t *testing.T, ctx context.Context, host string, port int, pass, rtpBase string) (*Backend, <-chan call.Event) {
	t.Helper()
	rtpMin, _ := strconv.Atoi(rtpBase)
	be, err := New(Config{
		SIPHost:    host,
		SIPPort:    port,
		User:       "101",
		Password:   pass,
		LocalIP:    "127.0.0.1",
		RTPPortMin: rtpMin,
		RTPPortMax: rtpMin + 50,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ev := make(chan call.Event, 32)
	if err := be.Start(ctx, ev); err != nil {
		t.Fatal(err)
	}
	// Asterisk コンテナの起動直後は時間がかかるため長めに待つ。
	deadline := time.Now().Add(90 * time.Second)
	for {
		select {
		case e := <-ev:
			if reg, ok := e.(call.EvRegistered); ok {
				if !reg.OK {
					t.Logf("REGISTER 再試行中: %s", reg.Detail)
					continue
				}
				return be, ev
			}
		case <-time.After(time.Until(deadline)):
			t.Fatal("Asterisk への REGISTER が成功しない")
			return nil, nil
		case <-ctx.Done():
			t.Fatal("ctx 終了")
			return nil, nil
		}
	}
}

// asteriskUAC はテスト用の素の SIP 電話機 (内線 102) である。
type asteriskUAC struct {
	cli     *sipgo.Client
	dlgCli  *sipgo.DialogClientCache
	rtp     *net.UDPConn
	contact sip.ContactHeader
}

func newAsteriskUAC(t *testing.T, ctx context.Context, user string) *asteriskUAC {
	t.Helper()
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("sipbridge-test-uac"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ua.Close() })
	cli, err := sipgo.NewClient(ua,
		sipgo.WithClientHostname("127.0.0.1"),
		sipgo.WithClientNAT(),
	)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	u := &asteriskUAC{cli: cli, rtp: rc}
	u.contact = sip.ContactHeader{
		Address: sip.Uri{Scheme: "sip", User: user, Host: "127.0.0.1", Port: rc.LocalAddr().(*net.UDPAddr).Port},
	}
	u.dlgCli = sipgo.NewDialogClientCache(cli, u.contact)
	_ = ctx
	return u
}

func (u *asteriskUAC) rtpPort() int { return u.rtp.LocalAddr().(*net.UDPAddr).Port }

// register は Asterisk に REGISTER する (digest 再送付き)。
func (u *asteriskUAC) register(t *testing.T, ctx context.Context, host string, port int, user, pass string) {
	t.Helper()
	recipient := sip.Uri{Scheme: "sip", Host: host, Port: port}
	req := sip.NewRequest(sip.REGISTER, recipient)
	req.SetTransport("UDP")
	from := &sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: user, Host: host}}
	from.Params.Add("tag", sip.GenerateTagN(8))
	to := &sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: user, Host: host}}
	contact := u.contact
	exp := sip.ExpiresHeader(120)
	callID := sip.CallIDHeader("test-uac-" + user)
	req.AppendHeader(from)
	req.AppendHeader(to)
	req.AppendHeader(&contact)
	req.AppendHeader(&exp)
	req.AppendHeader(&callID)

	send := func() *sip.Response {
		req.RemoveHeader("Via")
		tx, err := u.cli.TransactionRequest(ctx, req, sipgo.ClientRequestRegisterBuild)
		if err != nil {
			t.Fatalf("REGISTER 送信失敗: %v", err)
		}
		defer tx.Terminate()
		res, err := waitFinal(tx, 8*time.Second)
		if err != nil {
			t.Fatalf("REGISTER 応答待ち失敗: %v", err)
		}
		return res
	}
	res := send()
	if res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired {
		hdrName := "WWW-Authenticate"
		if res.StatusCode == sip.StatusProxyAuthRequired {
			hdrName = "Proxy-Authenticate"
		}
		chal, err := digest.ParseChallenge(res.GetHeader(hdrName).Value())
		if err != nil {
			t.Fatalf("チャレンジ解析失敗: %v", err)
		}
		cred, err := digest.Digest(chal, digest.Options{
			Method:   "REGISTER",
			URI:      req.Recipient.Addr(),
			Username: user,
			Password: pass,
		})
		if err != nil {
			t.Fatalf("digest 計算失敗: %v", err)
		}
		if cseq := req.CSeq(); cseq != nil {
			cseq.SeqNo++
		}
		req.AppendHeader(sip.NewHeader("Authorization", cred.String()))
		res = send()
	}
	if res.StatusCode != sip.StatusOK {
		t.Fatalf("REGISTER 失敗: %d %s", res.StatusCode, res.Reason)
	}
}

// inviteAsync は発呼を開始し、200 受信+ACK をバックグラウンドで行う。
// Asterisk 経由の着信では、200 が返る前に relay が Answer する必要があるため、
// 同期版ではデッドロックする。セッションと結果チャネルを返す。
func (u *asteriskUAC) inviteAsync(t *testing.T, ctx context.Context, host string, port int, user, pass, to string) (*sipgo.DialogClientSession, <-chan error) {
	t.Helper()
	offer := buildOfferSDP("127.0.0.1", u.rtpPort())
	recipient := sip.Uri{Scheme: "sip", User: to, Host: host, Port: port}
	from := &sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: user, Host: host}}
	from.Params.Add("tag", sip.GenerateTagN(8))
	toHdr := &sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: to, Host: host}}
	sess, err := u.dlgCli.Invite(ctx, recipient, offer,
		sip.NewHeader("Content-Type", "application/sdp"), from, toHdr)
	if err != nil {
		t.Fatalf("INVITE 送信失敗: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		waitCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
		defer cancel()
		if err := sess.WaitAnswer(waitCtx, sipgo.AnswerOptions{Username: user, Password: pass}); err != nil {
			done <- err
			return
		}
		done <- sess.Ack(ctx)
	}()
	return sess, done
}

func TestIntegrationAsteriskRegister(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host, port, pass101, _ := itestEnv(t)
	be, _ := startRelay101(t, ctx, host, port, pass101, "40000")
	if !be.Registered() {
		t.Error("Registered() が false")
	}
}

func TestIntegrationAsteriskIncoming(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host, port, pass101, pass102 := itestEnv(t)
	be, ev := startRelay101(t, ctx, host, port, pass101, "40100")

	uac := newAsteriskUAC(t, ctx, "102")
	regCtx, regCancel := context.WithTimeout(ctx, 30*time.Second)
	defer regCancel()
	uac.register(t, regCtx, host, port, "102", pass102)

	sess, answerDone := uac.inviteAsync(t, ctx, host, port, "102", pass102, "101")

	// relay が Incoming を上げる。
	e := waitEvent(t, ev, 30*time.Second)
	incoming, ok := e.(call.EvIncoming)
	if !ok {
		t.Fatalf("Incoming ではなく %+v", e)
	}
	if incoming.From != "102" {
		t.Errorf("From = %q (102 のはず)", incoming.From)
	}
	pipe, err := be.Answer(incoming.CallID, 0)
	if err != nil {
		t.Fatalf("Answer 失敗: %v", err)
	}
	// UAC 側の 200 受信+ACK を待つ。
	select {
	case err := <-answerDone:
		if err != nil {
			t.Fatalf("UAC 応答待ち失敗: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("UAC が 200 を受けない")
	}
	remote, err := parseOffer(sess.InviteResponse.Body())
	if err != nil {
		t.Fatalf("Asterisk の 200 SDP が不正: %v", err)
	}
	t.Logf("Asterisk SDP: %v pt=%d", remote.addr, remote.pt)

	// UAC→relay。注意: Asterisk がメディアパスに入るため RTP ヘッダは
	// 書き換わる。ペイロード部のみ比較する。
	uacPayload := []byte{0x11, 0x22, 0x33, 0x44}
	uacPkt := append([]byte{0x80, 0x00, 0x77, 0x01, 0, 0, 0x12, 0x34, 0, 0, 0, 0}, uacPayload...)
	relayAddr := backendRTPAddr(t, be, incoming.CallID)
	// Asterisk の symmetric RTP 学習のため数回送る。
	for i := 0; i < 5; i++ {
		if _, err := uac.rtp.WriteToUDP(uacPkt, relayAddr); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case got := <-pipe.Recv():
		if len(got) < 12+len(uacPayload) || string(got[12:12+len(uacPayload)]) != string(uacPayload) {
			t.Errorf("relay 受信ペイロードが違う: %v", got)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("relay が RTP を受けない")
	}

	// relay→UAC (ペイロード比較)。Asterisk の学習のため数回送り、
	// ペイロード一致まで受信する。
	bePayload := []byte{0x55, 0x66}
	bePkt := append([]byte{0x80, 0x00, 0x77, 0x02, 0, 0, 0x56, 0x78, 0, 0, 0, 0}, bePayload...)
	for i := 0; i < 5; i++ {
		if err := pipe.Send(bePkt); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	buf := make([]byte, 2048)
	_ = uac.rtp.SetReadDeadline(time.Now().Add(15 * time.Second))
	gotPayload := false
	for !gotPayload {
		n, _, err := uac.rtp.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("UAC が RTP を受けない: %v", err)
		}
		if n >= 12+len(bePayload) && string(buf[12:12+len(bePayload)]) == string(bePayload) {
			gotPayload = true
		}
	}

	// UAC 発の BYE → relay に Ended(bye)。
	byeCtx, byeCancel := context.WithTimeout(ctx, 15*time.Second)
	defer byeCancel()
	if err := sess.Bye(byeCtx); err != nil {
		t.Fatalf("UAC BYE 失敗: %v", err)
	}
	e = waitEvent(t, ev, 20*time.Second)
	ended, ok := e.(call.EvEnded)
	if !ok {
		t.Fatalf("Ended ではなく %+v", e)
	}
	if ended.CallID != incoming.CallID || ended.Reason != "bye" {
		t.Errorf("Ended が不正: %+v", ended)
	}
}

func TestIntegrationAsteriskEcho(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host, port, pass101, _ := itestEnv(t)
	be, ev := startRelay101(t, ctx, host, port, pass101, "40200")

	// relay 発信で *43 (Echo) を呼ぶ。送った RTP が戻ってくる。
	callID, err := be.Dial("*43")
	if err != nil {
		t.Fatalf("Dial 失敗: %v", err)
	}
	// 180/183 を飛ばして Answered を待つ。
	var pipe interface {
		Send([]byte) error
		Recv() <-chan []byte
		Close() error
	}
	deadline := time.Now().Add(30 * time.Second)
waitAnswered:
	for {
		select {
		case e := <-ev:
			switch v := e.(type) {
			case call.EvAnswered:
				if v.CallID != callID {
					t.Fatalf("別通話の Answered: %+v", v)
				}
				pipe = v.Pipe
				break waitAnswered
			case call.EvEnded:
				t.Fatalf("発信が終了した: %+v", v)
			case call.EvRinging:
				t.Logf("Ringing: %+v", v)
			}
		case <-time.After(time.Until(deadline)):
			t.Fatal("Answered が来ない")
		case <-ctx.Done():
			t.Fatal("ctx 終了")
		}
	}
	if pipe == nil {
		t.Fatal("パイプが無い")
	}
	// エコー確認: 識別できるペイロードを送り、同じペイロードが戻るのを待つ。
	// 注意: Asterisk の Echo はペイロードをそのまま返すが、RTP ヘッダの
	// seq/ts は書き換えるため、ペイロード部のみ比較する。
	echoPayload := []byte{0x5A, 0x5B, 0x5C, 0x5D, 0x5E}
	echoPkt := append([]byte{0x80, 0x00, 0x99, 0x01, 0x11, 0x22, 0x33, 0x44, 0, 0, 0, 0}, echoPayload...)
	for i := 0; i < 10; i++ {
		if err := pipe.Send(echoPkt); err != nil {
			t.Fatalf("Send 失敗: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	deadline = time.Now().Add(15 * time.Second)
	for {
		select {
		case got := <-pipe.Recv():
			if len(got) >= 12+len(echoPayload) && string(got[12:12+len(echoPayload)]) == string(echoPayload) {
				t.Logf("エコー確認 OK")
				goto hangup
			}
			// 無音パケット等は無視して待ち続ける。
		case <-time.After(time.Until(deadline)):
			t.Fatal("エコーが戻らない")
		case <-ctx.Done():
			t.Fatal("ctx 終了")
		}
	}
hangup:
	if err := be.Hangup(callID); err != nil {
		t.Fatalf("Hangup 失敗: %v", err)
	}
	_ = fmt.Sprint(callID)
}

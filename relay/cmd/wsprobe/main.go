// Command wsprobe は relay の WS セッション (docs/PROTOCOL.md) を叩く
// E2E 用のプローブ CLI である。1 端末として接続し、SIP アカウントの登録・
// 発信・着信応答・RTP 送受信・切断までを自動で行い、結果を 1 行 JSON で
// 標準出力に書く。期待に反したら終了コード 1 を返す。
//
// 使用例 (発信側):
//
//	wsprobe -url ws://127.0.0.1:18080 -token dev -account 101:pw101 \
//	        -expect-registered -dial 102 -talk 5s -hangup-after 5s -expect-ended
//
// 使用例 (着信側):
//
//	wsprobe -url ws://127.0.0.1:18080 -token dev -account 102:pw102 \
//	        -expect-registered -wait-incoming -answer -talk 5s -expect-ended
//
// 依存は標準ライブラリ + coder/websocket のみ。
package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/tmlksu/sipbridge/relay/internal/proto"
)

// ClientVersion は X-Client-Version として送る値である。
const ClientVersion = "wsprobe/0.2.0"

// RTP 定数 (PCMU 8kHz / ptime 20ms)。
const (
	rtpPayloadType = 0   // PCMU
	rtpSamples     = 160 // 20ms @ 8kHz
	rtpInterval    = 20 * time.Millisecond
	toneHz         = 1000.0
	toneAmplitude  = 8000.0
)

type options struct {
	url              string
	token            string
	cfID             string
	cfSecret         string
	device           string
	account          string
	dialTo           string
	waitIncoming     bool
	answer           bool
	talk             time.Duration
	hangupAfter      time.Duration
	timeout          time.Duration
	expectRegistered bool
	expectEnded      bool
	jsonOnly         bool
}

// result は標準出力に 1 行で書く結果である。
type result struct {
	Hello      *proto.Hello    `json:"hello"`
	Registered bool            `json:"registered"`
	Incoming   *proto.Incoming `json:"incoming"`
	Answered   bool            `json:"answered"`
	RTPSent    int             `json:"rtpSent"`
	RTPRecv    int             `json:"rtpRecv"`
	Ended      *proto.Ended    `json:"ended"`
	Errors     []string        `json:"errors"`
}

func main() {
	opt := parseFlags()
	p := &probe{opt: opt, res: result{Errors: []string{}}}
	p.run()
	out, err := json.Marshal(p.res)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wsprobe: 結果の JSON 化に失敗:", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
	if len(p.res.Errors) > 0 {
		os.Exit(1)
	}
}

func parseFlags() options {
	var o options
	flag.StringVar(&o.url, "url", "ws://127.0.0.1:8080", "relay の URL (/v1/session は自動付与)")
	flag.StringVar(&o.token, "token", "", "Authorization: Bearer に使う開発用トークン")
	flag.StringVar(&o.cfID, "cf-id", "", "CF-Access-Client-Id")
	flag.StringVar(&o.cfSecret, "cf-secret", "", "CF-Access-Client-Secret")
	flag.StringVar(&o.device, "device", "", "X-Device-Id (既定はランダム)")
	flag.StringVar(&o.account, "account", "", "SIP アカウント user:password[:display]")
	flag.StringVar(&o.dialTo, "dial", "", "発信先")
	flag.BoolVar(&o.waitIncoming, "wait-incoming", false, "着信を待つ")
	flag.BoolVar(&o.answer, "answer", false, "着信に応答する")
	flag.DurationVar(&o.talk, "talk", 0, "通話中に PCMU トーンを送る時間")
	flag.DurationVar(&o.hangupAfter, "hangup-after", 0, "応答から何秒後に hangup するか")
	flag.DurationVar(&o.timeout, "timeout", 30*time.Second, "全体のタイムアウト")
	flag.BoolVar(&o.expectRegistered, "expect-registered", false, "registered=true にならなければ失敗")
	flag.BoolVar(&o.expectEnded, "expect-ended", false, "最後に ended を受けなければ失敗")
	flag.BoolVar(&o.jsonOnly, "json", false, "進捗ログ (stderr) を出さない")
	flag.Parse()
	return o
}

type probe struct {
	opt  options
	res  result
	ws   *websocket.Conn
	msgs chan any // 受信したテキストメッセージ (decode 済み)

	rtpRecv atomic.Int64
	rtpSent atomic.Int64

	deadline   time.Time
	callID     string
	answeredAt time.Time
	writeMu    sync.Mutex
}

func (p *probe) logf(format string, args ...any) {
	if p.opt.jsonOnly {
		return
	}
	fmt.Fprintf(os.Stderr, "wsprobe: "+format+"\n", args...)
}

func (p *probe) fail(format string, args ...any) {
	p.res.Errors = append(p.res.Errors, fmt.Sprintf(format, args...))
}

// run は接続から切断までの一連の動作を行う。
func (p *probe) run() {
	p.deadline = time.Now().Add(p.opt.timeout)
	ctx, cancel := context.WithDeadline(context.Background(), p.deadline)
	defer cancel()

	if err := p.connect(ctx); err != nil {
		p.fail("接続失敗: %v", err)
		return
	}
	defer func() {
		p.res.RTPSent = int(p.rtpSent.Load())
		p.res.RTPRecv = int(p.rtpRecv.Load())
		_ = p.ws.Close(websocket.StatusNormalClosure, "bye")
	}()

	// 1) 最初の hello。
	if _, err := p.waitFor(isHello); err != nil {
		p.fail("hello を受け取れない: %v", err)
		return
	}
	// 2) SIP アカウント登録 (受理されると 2 回目の hello が来る)。
	if p.opt.account != "" {
		if err := p.sendSipAccount(); err != nil {
			p.fail("%v", err)
			return
		}
	}
	// 3) 登録待ち。
	if p.opt.expectRegistered && !p.res.Registered {
		if _, err := p.waitFor(func(any) bool { return p.res.Registered }); err != nil {
			p.fail("registered=true にならない: %v", err)
			return
		}
	}
	p.logf("registered=%v account=%q", p.res.Registered, p.helloAccount())

	// 4) 発信 / 着信待ち。
	switch {
	case p.opt.dialTo != "":
		if err := p.doDial(); err != nil {
			p.fail("%v", err)
			return
		}
	case p.opt.waitIncoming:
		if err := p.doWaitIncoming(); err != nil {
			p.fail("%v", err)
			return
		}
	}

	// 5) 通話中のメディア。
	if p.opt.talk > 0 && p.res.Answered {
		p.talk()
	}
	// 6) 切断。
	if p.opt.hangupAfter > 0 && p.callID != "" && p.res.Ended == nil {
		p.waitUntil(p.answeredAt.Add(p.opt.hangupAfter))
		if p.res.Ended == nil {
			p.logf("hangup callId=%s", p.callID)
			if err := p.send(&proto.Hangup{T: proto.THangup, CallID: p.callID}); err != nil {
				p.fail("hangup 送信失敗: %v", err)
			}
		}
	}
	// 7) ended 待ち。
	if p.opt.expectEnded && p.res.Ended == nil {
		if _, err := p.waitFor(func(any) bool { return p.res.Ended != nil }); err != nil {
			p.fail("ended を受け取れない: %v", err)
		}
	}
	p.res.RTPSent = int(p.rtpSent.Load())
	p.res.RTPRecv = int(p.rtpRecv.Load())
}

func (p *probe) helloAccount() string {
	if p.res.Hello == nil {
		return ""
	}
	return p.res.Hello.Account
}

// connect は WS 接続を確立し、受信ループを開始する。
func (p *probe) connect(ctx context.Context) error {
	u, err := sessionURL(p.opt.url)
	if err != nil {
		return err
	}
	device := p.opt.device
	if device == "" {
		device = randomDeviceID()
	}
	h := http.Header{}
	h.Set("X-Device-Id", device)
	h.Set("X-Client-Version", ClientVersion)
	if p.opt.token != "" {
		h.Set("Authorization", "Bearer "+p.opt.token)
	}
	if p.opt.cfID != "" {
		h.Set("CF-Access-Client-Id", p.opt.cfID)
		h.Set("CF-Access-Client-Secret", p.opt.cfSecret)
	}
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(dialCtx, u, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		return err
	}
	ws.SetReadLimit(1 << 20)
	p.ws = ws
	p.msgs = make(chan any, 64)
	p.logf("接続 %s device=%s", u, device)
	go p.readLoop()
	return nil
}

// readLoop は受信フレームを読み、テキストは decode して msgs に流し、
// バイナリ (RTP) は数える。
func (p *probe) readLoop() {
	defer close(p.msgs)
	for {
		typ, data, err := p.ws.Read(context.Background())
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageBinary:
			p.rtpRecv.Add(1)
		case websocket.MessageText:
			msg, derr := proto.Decode(data)
			if derr != nil {
				continue
			}
			select {
			case p.msgs <- msg:
			default: // 溢れたら捨てる (制御メッセージは取りこぼしても致命ではない)
			}
		}
	}
}

func (p *probe) send(v any) error {
	data, err := proto.Encode(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return p.ws.Write(ctx, websocket.MessageText, data)
}

// handle は受信メッセージを結果へ反映する。
func (p *probe) handle(msg any) {
	switch m := msg.(type) {
	case *proto.Hello:
		p.res.Hello = m
		if m.Registered {
			p.res.Registered = true
		}
		if m.Call != nil {
			p.callID = m.Call.CallID
		}
	case *proto.Registration:
		p.res.Registered = m.OK
		if !m.OK {
			p.logf("registration ok=false detail=%s", m.Detail)
		}
	case *proto.Incoming:
		p.res.Incoming = m
		p.callID = m.CallID
		p.logf("incoming callId=%s from=%s", m.CallID, m.From)
	case *proto.Ringing:
		p.callID = m.CallID
	case *proto.Answered:
		p.res.Answered = true
		p.callID = m.CallID
		if p.answeredAt.IsZero() {
			p.answeredAt = time.Now()
		}
		p.logf("answered callId=%s pt=%d", m.CallID, m.PT)
	case *proto.Ended:
		p.res.Ended = m
		p.logf("ended callId=%s reason=%s code=%d", m.CallID, m.Reason, m.Code)
	case *proto.Error:
		p.fail("relay error %s: %s", m.Code, m.Message)
	}
}

func isHello(msg any) bool {
	_, ok := msg.(*proto.Hello)
	return ok
}

// waitFor はメッセージを読みつつ pred が真になるまで待つ。
func (p *probe) waitFor(pred func(any) bool) (any, error) {
	for {
		timeout := time.Until(p.deadline)
		if timeout <= 0 {
			return nil, fmt.Errorf("タイムアウト")
		}
		timer := time.NewTimer(timeout)
		select {
		case msg, ok := <-p.msgs:
			timer.Stop()
			if !ok {
				return nil, fmt.Errorf("接続が切れた")
			}
			p.handle(msg)
			if pred(msg) {
				return msg, nil
			}
		case <-timer.C:
			return nil, fmt.Errorf("タイムアウト")
		}
	}
}

// waitUntil は指定時刻までメッセージを読み続ける (ended が来たら打ち切る)。
func (p *probe) waitUntil(until time.Time) {
	for {
		remain := time.Until(until)
		if remain <= 0 || p.res.Ended != nil {
			return
		}
		timer := time.NewTimer(remain)
		select {
		case msg, ok := <-p.msgs:
			timer.Stop()
			if !ok {
				return
			}
			p.handle(msg)
		case <-timer.C:
			return
		}
	}
}

// sendSipAccount は -account を送り、受理後の hello を待つ。
func (p *probe) sendSipAccount() error {
	user, password, display := parseAccount(p.opt.account)
	if user == "" {
		return fmt.Errorf("-account の形式は user:password[:display]")
	}
	if err := p.send(&proto.SipAccount{
		T: proto.TSipAccount, User: user, Password: password, Display: display,
	}); err != nil {
		return fmt.Errorf("sip_account 送信失敗: %w", err)
	}
	if _, err := p.waitFor(func(msg any) bool {
		if _, ok := msg.(*proto.Error); ok {
			return true
		}
		return isHello(msg)
	}); err != nil {
		return fmt.Errorf("sip_account の hello を受け取れない: %w", err)
	}
	if len(p.res.Errors) > 0 {
		return fmt.Errorf("sip_account が拒否された")
	}
	p.logf("sip_account 受理 user=%s", user)
	return nil
}

// doDial は発信し、answered まで待つ。
func (p *probe) doDial() error {
	if err := p.send(&proto.Dial{T: proto.TDial, To: p.opt.dialTo}); err != nil {
		return fmt.Errorf("dial 送信失敗: %w", err)
	}
	p.logf("dial to=%s", p.opt.dialTo)
	if _, err := p.waitFor(func(any) bool {
		return p.res.Answered || p.res.Ended != nil || len(p.res.Errors) > 0
	}); err != nil {
		return fmt.Errorf("発信の応答待ちに失敗: %w", err)
	}
	if !p.res.Answered {
		return fmt.Errorf("発信が応答されなかった")
	}
	return nil
}

// doWaitIncoming は着信を待ち、-answer なら応答して answered まで待つ。
func (p *probe) doWaitIncoming() error {
	if p.res.Incoming == nil {
		if _, err := p.waitFor(func(any) bool { return p.res.Incoming != nil }); err != nil {
			return fmt.Errorf("着信待ちに失敗: %w", err)
		}
	}
	if !p.opt.answer {
		return nil
	}
	pt := p.res.Incoming.PT
	if err := p.send(&proto.Answer{T: proto.TAnswer, CallID: p.res.Incoming.CallID, PT: &pt}); err != nil {
		return fmt.Errorf("answer 送信失敗: %w", err)
	}
	if _, err := p.waitFor(func(any) bool {
		return p.res.Answered || p.res.Ended != nil || len(p.res.Errors) > 0
	}); err != nil {
		return fmt.Errorf("answered 待ちに失敗: %w", err)
	}
	if !p.res.Answered {
		return fmt.Errorf("応答できなかった")
	}
	return nil
}

// talk は -talk の間 PCMU 1kHz トーンを 20ms 間隔で送り、同時に
// 制御メッセージを読み続ける。受信 RTP 数は readLoop が数える。
func (p *probe) talk() {
	until := time.Now().Add(p.opt.talk)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.sendTone(until, stop)
	}()
	p.waitUntil(until)
	close(stop)
	wg.Wait()
	p.logf("talk 完了 sent=%d recv=%d", p.rtpSent.Load(), p.rtpRecv.Load())
}

// sendTone は PCMU の 1kHz トーンを RTP フレームとして送る。
// SSRC/seq/timestamp はプローブ側で生成する (docs/PROTOCOL.md)。
func (p *probe) sendTone(until time.Time, stop <-chan struct{}) {
	var ssrcBuf [4]byte
	_, _ = rand.Read(ssrcBuf[:])
	ssrc := binary.BigEndian.Uint32(ssrcBuf[:])
	var seq uint16
	var ts uint32
	phase := 0.0
	step := 2 * math.Pi * toneHz / 8000.0

	ticker := time.NewTicker(rtpInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if time.Now().After(until) {
				return
			}
			pkt := make([]byte, 12+rtpSamples)
			pkt[0] = 0x80
			pkt[1] = rtpPayloadType
			binary.BigEndian.PutUint16(pkt[2:4], seq)
			binary.BigEndian.PutUint32(pkt[4:8], ts)
			binary.BigEndian.PutUint32(pkt[8:12], ssrc)
			for i := 0; i < rtpSamples; i++ {
				pkt[12+i] = linearToULaw(int16(toneAmplitude * math.Sin(phase)))
				phase += step
				if phase > 2*math.Pi {
					phase -= 2 * math.Pi
				}
			}
			seq++
			ts += rtpSamples
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			p.writeMu.Lock()
			err := p.ws.Write(ctx, websocket.MessageBinary, pkt)
			p.writeMu.Unlock()
			cancel()
			if err != nil {
				return
			}
			p.rtpSent.Add(1)
		}
	}
}

// linearToULaw は 16bit PCM を G.711 μ-law に変換する。
func linearToULaw(sample int16) byte {
	const (
		bias = 0x84
		clip = 32635
	)
	var sign byte
	v := int(sample)
	if v < 0 {
		v = -v
		sign = 0x80
	}
	if v > clip {
		v = clip
	}
	v += bias
	exp := 7
	for mask := 0x4000; exp > 0 && v&mask == 0; mask >>= 1 {
		exp--
	}
	mantissa := (v >> (exp + 3)) & 0x0F
	return ^(sign | byte(exp<<4) | byte(mantissa))
}

// sessionURL は -url を WS の /v1/session URL に整える。
func sessionURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("-url の解析失敗: %w", err)
	}
	switch u.Scheme {
	case "ws", "wss":
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("-url のスキームは ws/wss/http/https (現在 %q)", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("-url にホストが無い")
	}
	if p := strings.TrimSuffix(u.Path, "/"); p == "" {
		u.Path = "/v1/session"
	}
	return u.String(), nil
}

// parseAccount は "user:password[:display]" を分解する。
func parseAccount(s string) (user, password, display string) {
	parts := strings.SplitN(s, ":", 3)
	user = parts[0]
	if len(parts) > 1 {
		password = parts[1]
	}
	if len(parts) > 2 {
		display = parts[2]
	}
	return user, password, display
}

// randomDeviceID は UUID 形式のランダムなデバイス ID を作る。
func randomDeviceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "wsprobe-" + time.Now().Format("150405.000")
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// Package sipbackend は call.Backend の sipgo 実装である。
//
// 対向の SIP サーバ (Asterisk / ひかり電話 HGW など任意のレジストラ) に対して
// 普通の SIP 内線 (UDP) として振る舞う:
//   - REGISTER (digest MD5, qop=auth 対応, Expires 300, 240 秒で再登録、
//     失敗時は 5→60 秒バックオフ)
//   - 着信 INVITE (UAS): 100/180 即応、Answer で RTP ソケット確保+200 OK
//   - 発信 INVITE (UAC): 1xx→Ringing、200→Answered、401/407 は digest 再送
//   - RTP は UDP ソケットで終端し、MediaPipe で受け渡す。RTCP は捨てる。
//
// 注意: Backend メソッドは call.Manager のロック中に呼ばれるため、
// イベント送信 (ev <-) をメソッド内で同期的に行ってはならない。
// すべて emit() (goroutine 経由) で送る。fakebackend が参考実装である。
package sipbackend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"

	"github.com/tmlksu/sipbridge/relay/internal/call"
)

// SIP/タイマ関連の定数。
const (
	registerExpiresSec = 300               // REGISTER の Expires
	reRegisterInterval = 240 * time.Second // 成功時の再登録間隔
	registerBackoffMin = 5 * time.Second   // 失敗時の初回バックオフ
	registerBackoffMax = 60 * time.Second  // 失敗時の最大バックオフ
	registerTimeout    = 8 * time.Second   // REGISTER 応答待ち
	unregisterTimeout  = 3 * time.Second   // 停止時の登録解除 (Expires: 0) 応答待ち
	byeTimeout         = 5 * time.Second   // BYE/ACK 待ちの上限
)

// Config は SIP バックエンドの設定である。
// 通常は relay/internal/config.Config から移し替える。
type Config struct {
	SIPHost    string
	SIPPort    int
	User       string
	Password   string
	Display    string
	LocalIP    string // SDP/Contact に載せる自 IP。空なら自動検出
	RTPPortMin int
	RTPPortMax int
}

// sipCall は追跡中の 1 通話である。
type sipCall struct {
	callID  string
	dir     string // "in" | "out"
	state   string // "ringing" | "active"
	from    string // 着信: 発信者番号 / 発信: 発信先
	display string
	pt      int

	remote   *net.UDPAddr // 相手 RTP 宛先
	conn     *net.UDPConn // RTP ソケット (pipe 作成前のみ有効)
	port     int          // RTP ポート
	pipe     *rtpPipe     // 作成済みパイプ (Manager に渡したものも含む)
	localSDP []byte       // 200 OK / INVITE で提示した自 SDP

	dlgSrv *sipgo.DialogServerSession // 着信用
	dlgCli *sipgo.DialogClientSession // 発信用
	cancel context.CancelFunc         // 発信の WaitAnswer 中断用 (CANCEL 送出)

	// resolveCh は着信の解決 (Answer/Reject→onInvite ハンドラ) 用である。
	// sipgo はハンドラ復帰時に未確定トランザクションを破棄するため、
	// 最終応答 (200/4xx-6xx) はハンドラゴルーチン内で送る必要がある。
	resolveCh chan incomingResolution // バッファ 1。着信のみ使用

	sentRinging bool // 発信の 180 を通知済み
	sentEarly   bool // 発信の 183 (early) を通知済み
	done        bool // 終了済み (これ以上イベントを上げない)
}

// incomingResolution は着信に対する応答決定である。
type incomingResolution struct {
	answer bool
	pt     int
	code   int
}

// Backend は call.Backend の sipgo 実装である。
type Backend struct {
	cfg Config
	log *slog.Logger

	mu     sync.Mutex
	ev     chan<- call.Event
	ctx    context.Context
	cancel context.CancelFunc

	ua     *sipgo.UserAgent
	client *sipgo.Client
	server *sipgo.Server
	dlgSrv *sipgo.DialogServerCache
	dlgCli *sipgo.DialogClientCache

	contact    sip.ContactHeader
	localIP    string
	serverPort int
	sipConn    net.PacketConn

	calls map[string]*sipCall
	seq   int

	rtpMu   sync.Mutex
	rtpNext int

	regMu      sync.Mutex   // regReq の排他 (登録ループと停止時の登録解除)
	regReq     *sip.Request // REGISTER テンプレート
	regFromTag string
	regCallID  string

	registered atomic.Bool
}

var _ call.Backend = (*Backend)(nil)

// New は Backend を作る。Start 時に SIP 送受信を開始する。
func New(cfg Config, log *slog.Logger) (*Backend, error) {
	if cfg.SIPHost == "" {
		return nil, fmt.Errorf("SIPHost が空")
	}
	if cfg.SIPPort <= 0 || cfg.SIPPort > 65535 {
		return nil, fmt.Errorf("SIPPort が不正 (%d)", cfg.SIPPort)
	}
	if cfg.User == "" {
		return nil, fmt.Errorf("SIP User が空")
	}
	if cfg.RTPPortMin <= 0 || cfg.RTPPortMax <= 0 || cfg.RTPPortMin > cfg.RTPPortMax {
		return nil, fmt.Errorf("RTP ポート範囲が不正 (%d-%d)", cfg.RTPPortMin, cfg.RTPPortMax)
	}
	if log == nil {
		log = slog.Default()
	}
	return &Backend{
		cfg:     cfg,
		log:     log,
		calls:   make(map[string]*sipCall),
		rtpNext: cfg.RTPPortMin,
	}, nil
}

// Registered は直近の REGISTER 成功状態を返す (テスト用)。
func (b *Backend) Registered() bool { return b.registered.Load() }

// LocalIP は SDP/Contact 用の自 IP を返す (テスト用)。
func (b *Backend) LocalIP() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.localIP
}

// ServerPort は SIP 待ち受け UDP ポートを返す (テスト用)。
func (b *Backend) ServerPort() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.serverPort
}

// Start は SIP 送受信と REGISTER ループを開始する。
func (b *Backend) Start(ctx context.Context, ev chan<- call.Event) error {
	localIP := b.cfg.LocalIP
	if localIP == "" {
		localIP = detectLocalIP(net.JoinHostPort(b.cfg.SIPHost, itoa(b.cfg.SIPPort)), b.log)
		b.log.Info("LOCAL_IP を自動検出", "ip", localIP)
	}
	sipConn, err := net.ListenPacket("udp", "0.0.0.0:0")
	if err != nil {
		return fmt.Errorf("SIP 待受ソケットの確保失敗: %w", err)
	}
	serverPort := sipConn.LocalAddr().(*net.UDPAddr).Port

	ua, err := sipgo.NewUA(sipgo.WithUserAgent("sipbridge-relay"))
	if err != nil {
		_ = sipConn.Close()
		return fmt.Errorf("sipgo UA 作成失敗: %w", err)
	}
	client, err := sipgo.NewClient(ua,
		sipgo.WithClientHostname(localIP),
		sipgo.WithClientNAT(), // Via に rport を付け、NAT 越しでも応答を受けられるようにする
	)
	if err != nil {
		_ = sipConn.Close()
		_ = ua.Close()
		return fmt.Errorf("sipgo client 作成失敗: %w", err)
	}
	server, err := sipgo.NewServer(ua)
	if err != nil {
		_ = sipConn.Close()
		_ = ua.Close()
		return fmt.Errorf("sipgo server 作成失敗: %w", err)
	}

	contact := sip.ContactHeader{
		Address: sip.Uri{Scheme: "sip", User: b.cfg.User, Host: localIP, Port: serverPort},
	}

	b.mu.Lock()
	b.ev = ev
	b.localIP = localIP
	b.serverPort = serverPort
	b.sipConn = sipConn
	b.ua = ua
	b.client = client
	b.server = server
	b.contact = contact
	b.dlgSrv = sipgo.NewDialogServerCache(client, contact)
	b.dlgCli = sipgo.NewDialogClientCache(client, contact)
	b.regFromTag = sip.GenerateTagN(16)
	b.regCallID = randHexID()
	b.regReq = b.buildRegisterReq()
	b.ctx, b.cancel = context.WithCancel(ctx)
	b.mu.Unlock()

	b.registerHandlers()

	go func() {
		// ctx 終了で登録解除 (Expires: 0) を 1 回送ってから
		// ソケットを閉じ、ServeUDP を抜けさせる。
		<-b.ctx.Done()
		b.unregister()
		_ = sipConn.Close()
		_ = ua.Close()
	}()
	go func() {
		if err := server.ServeUDP(sipConn); err != nil {
			// 通常終了 (Close 起因) 以外は記録する。
			select {
			case <-b.ctx.Done():
			default:
				b.log.Error("SIP サーバ終了", "err", err)
			}
		}
	}()
	go b.registerLoop()

	return nil
}

// emit はイベントを非同期で送る。Manager のロック中に呼ばれる
// Backend メソッドから直接 ev <- してはならないため、必ず goroutine 経由。
func (b *Backend) emit(e call.Event) {
	b.mu.Lock()
	ev := b.ev
	ctx := b.ctx
	b.mu.Unlock()
	if ev == nil {
		return
	}
	go func() {
		if ctx != nil {
			select {
			case ev <- e:
			case <-ctx.Done():
			}
			return
		}
		select {
		case ev <- e:
		default:
		}
	}()
}

// ---- REGISTER ----

func (b *Backend) buildRegisterReq() *sip.Request {
	recipient := sip.Uri{Scheme: "sip", Host: b.cfg.SIPHost, Port: b.cfg.SIPPort}
	req := sip.NewRequest(sip.REGISTER, recipient)
	req.SetTransport("UDP")
	display := b.cfg.Display
	if display == "" {
		display = b.cfg.User
	}
	from := &sip.FromHeader{
		DisplayName: display,
		Address:     sip.Uri{Scheme: "sip", User: b.cfg.User, Host: b.cfg.SIPHost},
	}
	from.Params.Add("tag", b.regFromTag)
	to := &sip.ToHeader{
		Address: sip.Uri{Scheme: "sip", User: b.cfg.User, Host: b.cfg.SIPHost},
	}
	contact := &sip.ContactHeader{
		Address: sip.Uri{Scheme: "sip", User: b.cfg.User, Host: b.localIP, Port: b.serverPort},
	}
	contact.Params.Add("expires", itoa(registerExpiresSec))
	exp := sip.ExpiresHeader(registerExpiresSec)
	callID := sip.CallIDHeader(b.regCallID)
	req.AppendHeader(from)
	req.AppendHeader(to)
	req.AppendHeader(contact)
	req.AppendHeader(&exp)
	req.AppendHeader(&callID)
	return req
}

func (b *Backend) registerLoop() {
	backoff := registerBackoffMin
	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}
		ok, detail := b.doRegister()
		b.registered.Store(ok)
		b.emit(call.EvRegistered{OK: ok, Detail: detail})
		wait := reRegisterInterval
		if !ok {
			wait = backoff
			backoff *= 2
			if backoff > registerBackoffMax {
				backoff = registerBackoffMax
			}
		} else {
			backoff = registerBackoffMin
		}
		if wait <= 0 {
			wait = registerBackoffMin
		}
		b.log.Info("REGISTER 結果", "ok", ok, "detail", detail, "next", wait.String())
		select {
		case <-b.ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// doRegister は 1 回の REGISTER 試行 (digest 再送付き) を行う。
// 登録ループ専用ゴルーチンからのみ呼ぶ。
func (b *Backend) doRegister() (bool, string) {
	b.regMu.Lock()
	defer b.regMu.Unlock()
	ctx, cancel := context.WithTimeout(b.ctx, registerTimeout)
	defer cancel()
	req := b.regReq
	setRegisterExpires(req, registerExpiresSec)
	req.RemoveHeader("Via")
	req.RemoveHeader("Authorization")
	req.RemoveHeader("Proxy-Authorization")
	tx, err := b.client.TransactionRequest(ctx, req, sipgo.ClientRequestRegisterBuild)
	if err != nil {
		return false, fmt.Sprintf("register 送信失敗: %v", err)
	}
	defer tx.Terminate()
	res, err := waitFinal(tx, registerTimeout)
	if err != nil {
		return false, fmt.Sprintf("register 応答待ち失敗: %v", err)
	}
	if res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired {
		res2, err := b.registerDigest(ctx, req, res)
		if err != nil {
			return false, err.Error()
		}
		res = res2
	}
	if res.StatusCode != sip.StatusOK {
		return false, fmt.Sprintf("register %d %s", res.StatusCode, res.Reason)
	}
	return true, "registered"
}

// unregister は停止時に REGISTER Expires: 0 (登録解除) を 1 回送る。
// b.ctx は既にキャンセル済みのため独立した context を使い、
// 失敗しても無視する (ログのみ)。
func (b *Backend) unregister() {
	b.regMu.Lock()
	defer b.regMu.Unlock()
	if b.regReq == nil || b.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), unregisterTimeout)
	defer cancel()
	req := b.regReq
	req.RemoveHeader("Via")
	req.RemoveHeader("Authorization")
	req.RemoveHeader("Proxy-Authorization")
	setRegisterExpires(req, 0)
	tx, err := b.client.TransactionRequest(ctx, req, sipgo.ClientRequestRegisterBuild)
	if err != nil {
		b.log.Warn("登録解除の送信失敗", "err", err)
		return
	}
	defer tx.Terminate()
	res, err := waitFinal(tx, unregisterTimeout)
	if err != nil {
		b.log.Warn("登録解除の応答待ち失敗", "err", err)
		return
	}
	if res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired {
		res, err = b.registerDigest(ctx, req, res)
		if err != nil {
			b.log.Warn("登録解除の再送失敗", "err", err)
			return
		}
	}
	b.registered.Store(false)
	b.log.Info("登録解除", "status", res.StatusCode, "user", b.cfg.User)
}

// setRegisterExpires は REGISTER の Expires (ヘッダと Contact パラメータ) を設定する。
func setRegisterExpires(req *sip.Request, sec int) {
	req.RemoveHeader("Expires")
	exp := sip.ExpiresHeader(sec)
	req.AppendHeader(&exp)
	if ct := req.Contact(); ct != nil {
		ct.Params.Add("expires", itoa(sec))
	}
}

// registerDigest は 401/407 への digest 再送を行う。
func (b *Backend) registerDigest(ctx context.Context, req *sip.Request, res *sip.Response) (*sip.Response, error) {
	proxy := res.StatusCode == sip.StatusProxyAuthRequired
	hdrName := "WWW-Authenticate"
	if proxy {
		hdrName = "Proxy-Authenticate"
	}
	h := res.GetHeader(hdrName)
	if h == nil {
		return nil, fmt.Errorf("register %d に認証チャレンジが無い", res.StatusCode)
	}
	if b.cfg.Password == "" {
		return nil, fmt.Errorf("認証が要求されたが SIP_PASSWORD が空")
	}
	chal, err := digest.ParseChallenge(h.Value())
	if err != nil {
		return nil, fmt.Errorf("チャレンジ解析失敗: %w", err)
	}
	cred, err := digest.Digest(chal, digest.Options{
		Method:   "REGISTER",
		URI:      req.Recipient.Addr(),
		Username: b.cfg.User,
		Password: b.cfg.Password,
	})
	if err != nil {
		return nil, fmt.Errorf("digest 計算失敗: %w", err)
	}
	req.RemoveHeader("Via")
	if cseq := req.CSeq(); cseq != nil {
		cseq.SeqNo++
	}
	if proxy {
		req.AppendHeader(sip.NewHeader("Proxy-Authorization", cred.String()))
	} else {
		req.AppendHeader(sip.NewHeader("Authorization", cred.String()))
	}
	tx, err := b.client.TransactionRequest(ctx, req, sipgo.ClientRequestAddVia)
	if err != nil {
		return nil, fmt.Errorf("register 再送失敗: %w", err)
	}
	defer tx.Terminate()
	res2, err := waitFinal(tx, registerTimeout)
	if err != nil {
		return nil, fmt.Errorf("register 再送の応答待ち失敗: %w", err)
	}
	return res2, nil
}

// waitFinal は最終応答 (provisional を飛ばす) を待つ。
func waitFinal(tx sip.ClientTransaction, timeout time.Duration) (*sip.Response, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case res := <-tx.Responses():
			if res.IsProvisional() {
				continue
			}
			return res, nil
		case <-tx.Done():
			if err := tx.Err(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("トランザクション終了")
		case <-timer.C:
			return nil, fmt.Errorf("応答タイムアウト")
		}
	}
}

// ---- SIP サーバハンドラ ----

func (b *Backend) registerHandlers() {
	b.server.OnInvite(b.onInvite)
	b.server.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
		// 200 OK への ACK 確認 (DialogServerSession の ACK 待ち解除用)。
		// re-INVITE の ACK 等、該当なしは無視する。
		_ = b.dlgSrv.ReadAck(req, tx)
	})
	b.server.OnBye(b.onBye)
	b.server.OnOptions(func(req *sip.Request, tx sip.ServerTransaction) {
		// Asterisk の qualify (OPTIONS) には素直に 200 で返す。
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
	})
	b.server.OnCancel(func(req *sip.Request, tx sip.ServerTransaction) {
		// 対応する INVITE トランザクションがある CANCEL は
		// トランザクション層で自動処理される。ここに来るのは
		// 迷子の CANCEL のみ → 481。
		_ = tx.Respond(sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil))
	})
	b.server.OnNoRoute(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 405, "Method Not Allowed", nil))
	})
}

// onInvite は着信 INVITE と re-INVITE を処理する。
func (b *Backend) onInvite(req *sip.Request, tx sip.ServerTransaction) {
	// ダイアログ内要求 (re-INVITE) かを先に見る。着信・発信どちらの
	// ダイアログにも属し得る (Asterisk からの re-INVITE)。
	if dlg, err := b.dlgSrv.MatchDialogRequest(req); err == nil {
		b.handleReinvite(req, tx, dlg, true)
		return
	}
	if sess, err := b.dlgCli.MatchRequestDialog(req); err == nil {
		b.handleReinviteCli(req, tx, sess)
		return
	}
	// 新規着信。
	offer, err := parseOffer(req.Body())
	if err != nil {
		b.log.Info("着信 SDP が扱えないため 488", "err", err)
		_ = tx.Respond(sip.NewResponseFromRequest(req, 488, "Not Acceptable Here", nil))
		return
	}
	dlg, err := b.dlgSrv.ReadInvite(req, tx)
	if err != nil {
		b.log.Warn("ReadInvite 失敗", "err", err)
		return
	}
	callID := ""
	if h := req.CallID(); h != nil {
		callID = h.Value()
	}
	if callID == "" {
		callID = "in-" + randHexID()
	}
	from, display := "", ""
	if f := req.From(); f != nil {
		from = f.Address.User
		display = f.DisplayName
	}
	sc := &sipCall{
		callID: callID, dir: "in", state: "ringing",
		from: from, display: display, pt: offer.pt,
		remote: offer.addr, dlgSrv: dlg,
		resolveCh: make(chan incomingResolution, 1),
	}
	b.mu.Lock()
	b.calls[callID] = sc
	b.mu.Unlock()

	// 100/180 即応 (PROTOCOL: relay は既に 180 を返している)。
	_ = dlg.Respond(sip.StatusTrying, "Trying", nil)
	_ = dlg.Respond(sip.StatusRinging, "Ringing", nil)

	b.log.Info("着信", "callID", callID, "from", from, "pt", offer.pt)
	b.emit(call.EvIncoming{CallID: callID, From: from, Display: display, PT: offer.pt})

	// 最終応答はこのハンドラ内で送る。sipgo はハンドラ復帰時に
	// 未確定のサーバトランザクションを破棄するため、別ゴルーチンに
	// 委ねると 200/4xx が送れなくなる。Answer/Reject からの解決、
	// CANCEL、シャットダウンのいずれかまでブロックする。
	select {
	case r := <-sc.resolveCh:
		b.resolveIncoming(sc, dlg, r)
	case <-dlg.Context().Done():
		// CANCEL 等で取り消された (487 はトランザクション層が自動送出)。
		b.cancelIncoming(sc)
	case <-b.ctx.Done():
		return
	}
}

// resolveIncoming はハンドラ内で最終応答を送る。
func (b *Backend) resolveIncoming(sc *sipCall, dlg *sipgo.DialogServerSession, r incomingResolution) {
	callID := sc.callID
	if !r.answer {
		_ = dlg.Respond(r.code, failureReason(r.code), nil)
		return
	}
	// 200 OK + SDP。ACK 待ち (T1 タイマによる再送付き) まで行う。
	b.mu.Lock()
	local := sc.localSDP
	pipe := sc.pipe
	b.mu.Unlock()
	if err := dlg.RespondSDP(local); err != nil {
		b.log.Warn("200 OK 応答失敗", "callID", callID, "err", err)
		b.mu.Lock()
		if cur, ok := b.calls[callID]; ok && cur == sc && !cur.done {
			cur.done = true
			delete(b.calls, callID)
		}
		b.mu.Unlock()
		if pipe != nil {
			_ = pipe.Close()
		}
		b.emit(call.EvEnded{CallID: callID, Reason: "error", Code: 500})
	}
	// 成功時: Manager は Answer の戻りですでに active 化・パイプ保持済みのため
	// イベント不要。ACK 以降は BYE で終わる (onBye 経由)。
}

// cancelIncoming は応答前の CANCEL/取下げを処理する。
func (b *Backend) cancelIncoming(sc *sipCall) {
	b.mu.Lock()
	callID := sc.callID
	if cur, ok := b.calls[callID]; !ok || cur != sc || cur.done {
		b.mu.Unlock()
		return
	}
	sc.done = true
	delete(b.calls, callID)
	pipe := sc.pipe
	b.mu.Unlock()
	if pipe != nil {
		_ = pipe.Close()
	}
	b.log.Info("着信が取り消された", "callID", callID)
	b.emit(call.EvEnded{CallID: callID, Reason: "cancel", Code: 487})
}

// handleReinvite は着信用ダイアログへの re-INVITE を処理する。
// 同一 SDP で 200 を返し、宛先が変わっていれば追従する。
func (b *Backend) handleReinvite(req *sip.Request, tx sip.ServerTransaction, dlg *sipgo.DialogServerSession, _ bool) {
	sc := b.findByDlgSrv(dlg)
	if sc == nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil))
		return
	}
	b.mu.Lock()
	if sc.done {
		b.mu.Unlock()
		_ = tx.Respond(sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil))
		return
	}
	if len(req.Body()) > 0 {
		off, err := parseOffer(req.Body())
		if err != nil {
			b.mu.Unlock()
			_ = tx.Respond(sip.NewResponseFromRequest(req, 488, "Not Acceptable Here", nil))
			return
		}
		sc.remote = off.addr
		if sc.pipe != nil {
			sc.pipe.setRemote(off.addr)
		}
	}
	local := sc.localSDP
	b.mu.Unlock()
	if len(local) == 0 {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 500, "Server Internal Error", nil))
		return
	}
	res := sip.NewSDPResponseFromRequest(req, local)
	contact := b.contact
	res.AppendHeader(&contact)
	if err := tx.Respond(res); err != nil {
		b.log.Warn("re-INVITE 応答失敗", "err", err)
	}
}

// handleReinviteCli は発信用ダイアログへの re-INVITE を処理する。
func (b *Backend) handleReinviteCli(req *sip.Request, tx sip.ServerTransaction, sess *sipgo.DialogClientSession) {
	sc := b.findByDlgCli(sess)
	if sc == nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil))
		return
	}
	b.mu.Lock()
	if sc.done {
		b.mu.Unlock()
		_ = tx.Respond(sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil))
		return
	}
	if len(req.Body()) > 0 {
		off, err := parseOffer(req.Body())
		if err != nil {
			b.mu.Unlock()
			_ = tx.Respond(sip.NewResponseFromRequest(req, 488, "Not Acceptable Here", nil))
			return
		}
		sc.remote = off.addr
		if sc.pipe != nil {
			sc.pipe.setRemote(off.addr)
		}
	}
	local := sc.localSDP
	b.mu.Unlock()
	if len(local) == 0 {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 500, "Server Internal Error", nil))
		return
	}
	res := sip.NewSDPResponseFromRequest(req, local)
	contact := b.contact
	res.AppendHeader(&contact)
	if err := tx.Respond(res); err != nil {
		b.log.Warn("re-INVITE 応答失敗 (発信)", "err", err)
	}
}

// onBye は相手側からの通話終了を処理する。
func (b *Backend) onBye(req *sip.Request, tx sip.ServerTransaction) {
	if dlg, err := b.dlgSrv.MatchDialogRequest(req); err == nil {
		if err := dlg.ReadBye(req, tx); err != nil {
			b.log.Warn("BYE 処理失敗 (着信)", "err", err)
			return
		}
		b.finishRemoteBye(b.findByDlgSrv(dlg))
		return
	}
	if sess, err := b.dlgCli.MatchRequestDialog(req); err == nil {
		if err := sess.ReadBye(req, tx); err != nil {
			b.log.Warn("BYE 処理失敗 (発信)", "err", err)
			return
		}
		b.finishRemoteBye(b.findByDlgCli(sess))
		return
	}
	_ = tx.Respond(sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil))
}

// finishRemoteBye は相手 BYE 後の後片付けと Ended 通知である。
// パイプは Manager も閉じるが、Manager に届かない場合に備え
// ここでも閉じる (Close は冪等)。
func (b *Backend) finishRemoteBye(sc *sipCall) {
	if sc == nil {
		return
	}
	b.mu.Lock()
	callID := sc.callID
	if cur, ok := b.calls[callID]; !ok || cur.done {
		b.mu.Unlock()
		return
	}
	sc.done = true
	delete(b.calls, callID)
	pipe := sc.pipe
	b.mu.Unlock()
	if pipe != nil {
		_ = pipe.Close()
	} else if sc.conn != nil {
		_ = sc.conn.Close()
	}
	b.log.Info("相手から BYE", "callID", callID)
	b.emit(call.EvEnded{CallID: callID, Reason: "bye", Code: 200})
}

func (b *Backend) findByDlgSrv(dlg *sipgo.DialogServerSession) *sipCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, sc := range b.calls {
		if sc.dlgSrv == dlg {
			return sc
		}
	}
	return nil
}

func (b *Backend) findByDlgCli(sess *sipgo.DialogClientSession) *sipCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, sc := range b.calls {
		if sc.dlgCli == sess {
			return sc
		}
	}
	return nil
}

// ---- call.Backend 実装 ----

// Answer は着信に応答する。RTP ソケットを確保し、RTP パイプを即座に返す。
// 200 OK の送出・ACK 待ちは onInvite ハンドラが行う。
func (b *Backend) Answer(callID string, pt int) (call.MediaPipe, error) {
	b.mu.Lock()
	sc, ok := b.calls[callID]
	if !ok || sc.dir != "in" || sc.state != "ringing" || sc.done {
		b.mu.Unlock()
		return nil, fmt.Errorf("応答できる着信 %q が無い", callID)
	}
	if pt != PTPCMU && pt != PTPCMA {
		pt = sc.pt
	}
	conn, port, err := b.bindRTPPort()
	if err != nil {
		b.mu.Unlock()
		return nil, err
	}
	pipe := newRTPPipe(conn, sc.remote, b.log)
	sc.conn = nil // 所有権は pipe に移る
	sc.pipe = pipe
	sc.port = port
	sc.state = "active"
	sc.pt = pt
	sc.localSDP = buildAnswerSDP(b.localIP, port, pt)
	resolveCh := sc.resolveCh
	b.mu.Unlock()

	// ハンドラに解決を渡す。ハンドラが既に抜けていれば誰も受信しない。
	select {
	case resolveCh <- incomingResolution{answer: true, pt: pt}:
		return pipe, nil
	default:
		_ = pipe.Close()
		return nil, fmt.Errorf("応答できる着信 %q が無い (取下げ済み)", callID)
	}
}

// Reject は着信を拒否する (486 等)。最終応答はハンドラが送る。
// イベントは Manager が発行するため上げない。
func (b *Backend) Reject(callID string, code int) error {
	if code == 0 {
		code = 486
	}
	b.mu.Lock()
	sc, ok := b.calls[callID]
	if !ok || sc.dir != "in" || sc.state != "ringing" || sc.done {
		b.mu.Unlock()
		return fmt.Errorf("拒否できる着信 %q が無い", callID)
	}
	sc.done = true
	delete(b.calls, callID)
	resolveCh := sc.resolveCh
	b.mu.Unlock()

	select {
	case resolveCh <- incomingResolution{answer: false, code: code}:
		return nil
	default:
		// ハンドラが既に抜けた (CANCEL 競合)。487 は自動送出済み。
		return nil
	}
}

// Hangup は通話 (ACTIVE) または発信 (RINGING_OUT) を切断する。
// BYE/CANCEL は別 goroutine で送り、即座に戻る。イベントは Manager が発行する。
func (b *Backend) Hangup(callID string) error {
	b.mu.Lock()
	sc, ok := b.calls[callID]
	if !ok || sc.done {
		b.mu.Unlock()
		return fmt.Errorf("通話 %q が無い", callID)
	}
	switch {
	case sc.dir == "in" && sc.state == "active":
		sc.done = true
		delete(b.calls, callID)
		dlg := sc.dlgSrv
		b.mu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(b.ctx, byeTimeout)
			defer cancel()
			_ = dlg.Bye(ctx)
		}()
		return nil
	case sc.dir == "out" && (sc.state == "ringing" || sc.state == "active"):
		sc.done = true
		delete(b.calls, callID)
		cancel := sc.cancel
		sess := sc.dlgCli
		pipe := sc.pipe
		conn := sc.conn
		wasActive := sc.state == "active"
		b.mu.Unlock()
		if !wasActive {
			// RINGING_OUT: WaitAnswer を中断し CANCEL を送らせる。
			// 後片付けは発信ゴルーチンが行う。まだ INVITE 前ならここで閉じる。
			if cancel != nil {
				cancel()
			} else {
				if pipe != nil {
					_ = pipe.Close()
				} else if conn != nil {
					_ = conn.Close()
				}
			}
			return nil
		}
		go func() {
			ctx, cancel2 := context.WithTimeout(b.ctx, byeTimeout)
			defer cancel2()
			_ = sess.Bye(ctx)
		}()
		return nil
	}
	b.mu.Unlock()
	return fmt.Errorf("通話 %q は切断できる状態ではない", callID)
}

// Dial は発信する。callID を即座に返し、INVITE 送出・応答待ちは
// 別ゴルーチンで行う。1xx/200/失敗はイベントで通知する。
func (b *Backend) Dial(to string) (string, error) {
	if to == "" {
		return "", fmt.Errorf("発信先が空")
	}
	b.mu.Lock()
	b.seq++
	callID := fmt.Sprintf("out-%d-%s", b.seq, randHex(4))
	conn, port, err := b.bindRTPPort()
	if err != nil {
		b.mu.Unlock()
		return "", err
	}
	offer := buildOfferSDP(b.localIP, port)
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: to, Host: b.cfg.SIPHost, Port: b.cfg.SIPPort})
	req.SetTransport("UDP")
	display := b.cfg.Display
	if display == "" {
		display = b.cfg.User
	}
	from := &sip.FromHeader{
		DisplayName: display,
		Address:     sip.Uri{Scheme: "sip", User: b.cfg.User, Host: b.cfg.SIPHost},
	}
	from.Params.Add("tag", sip.GenerateTagN(16))
	toHdr := &sip.ToHeader{
		Address: sip.Uri{Scheme: "sip", User: to, Host: b.cfg.SIPHost},
	}
	callIDHdr := sip.CallIDHeader(callID)
	req.AppendHeader(from)
	req.AppendHeader(toHdr)
	req.AppendHeader(&callIDHdr)
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody(offer)
	sc := &sipCall{
		callID: callID, dir: "out", state: "ringing",
		from: to, conn: conn, port: port, localSDP: offer,
	}
	b.calls[callID] = sc
	b.mu.Unlock()

	go b.runOutgoing(callID, req)
	return callID, nil
}

// runOutgoing は発信 INVITE の送出と応答待ちを行う。
func (b *Backend) runOutgoing(callID string, req *sip.Request) {
	ctx, cancel := context.WithCancel(b.ctx)
	b.mu.Lock()
	sc, ok := b.calls[callID]
	if !ok || sc.done {
		b.mu.Unlock()
		cancel()
		// Hangup が先行した場合の後片付け (Hangup 側で cancel が無ければ)。
		b.mu.Lock()
		leftover := sc
		b.mu.Unlock()
		if leftover != nil {
			if leftover.pipe != nil {
				_ = leftover.pipe.Close()
			} else if leftover.conn != nil {
				_ = leftover.conn.Close()
			}
		}
		return
	}
	sc.cancel = cancel
	conn := sc.conn
	b.mu.Unlock()

	sess, err := b.dlgCli.WriteInvite(ctx, req)
	if err != nil {
		b.failOutgoing(callID, "error", 500, true)
		return
	}
	b.mu.Lock()
	if cur, ok := b.calls[callID]; ok && !cur.done {
		cur.dlgCli = sess
	}
	b.mu.Unlock()

	err = sess.WaitAnswer(ctx, sipgo.AnswerOptions{
		Username: b.cfg.User,
		Password: b.cfg.Password,
		OnResponse: func(res *sip.Response) error {
			b.onProvisional(callID, res)
			return nil
		},
	})
	if err != nil {
		if derr, ok := err.(*sipgo.ErrDialogResponse); ok {
			code := derr.Res.StatusCode
			b.log.Info("発信失敗応答", "callID", callID, "code", code)
			b.failOutgoing(callID, outgoingFailReason(code), code, true)
			return
		}
		// CANCEL 起因 (自発 Hangup)・シャットダウン等。
		// 自発の場合はイベント不要のため、done を見て黙って片付ける。
		b.failOutgoing(callID, "error", 500, false)
		return
	}
	// 200 OK。SDP アンサーを確定させる。
	ans, err := parseOffer(sess.InviteResponse.Body())
	if err != nil {
		b.log.Warn("200 OK の SDP が扱えない", "callID", callID, "err", err)
		go func() {
			ctx2, cancel2 := context.WithTimeout(b.ctx, byeTimeout)
			defer cancel2()
			_ = sess.Bye(ctx2)
		}()
		b.failOutgoing(callID, "error", 500, true)
		return
	}
	b.mu.Lock()
	cur, ok := b.calls[callID]
	if !ok || cur.done {
		b.mu.Unlock()
		// 自発 Hangup との競合: ゴースト通話にしないよう BYE する。
		go func() {
			ctx2, cancel2 := context.WithTimeout(b.ctx, byeTimeout)
			defer cancel2()
			_ = sess.Ack(ctx2)
			_ = sess.Bye(ctx2)
		}()
		if sc.pipe != nil {
			_ = sc.pipe.Close()
		} else if conn != nil {
			_ = conn.Close()
		}
		return
	}
	cur.dlgCli = sess
	cur.remote = ans.addr
	cur.pt = ans.pt
	cur.state = "active"
	if cur.pipe == nil {
		cur.pipe = newRTPPipe(cur.conn, ans.addr, b.log)
		cur.conn = nil
	} else {
		cur.pipe.setRemote(ans.addr)
	}
	pipe := cur.pipe
	b.mu.Unlock()

	if err := sess.Ack(ctx); err != nil {
		b.log.Warn("ACK 送信失敗", "callID", callID, "err", err)
		b.failOutgoing(callID, "error", 500, true)
		return
	}
	b.log.Info("発信応答", "callID", callID, "pt", ans.pt)
	b.emit(call.EvAnswered{CallID: callID, PT: ans.pt, Pipe: pipe})
}

// onProvisional は発信への 1xx 応答を Ringing イベントに変える。
// 183 の SDP 付き (early media) では RTP パイプを添える。
func (b *Backend) onProvisional(callID string, res *sip.Response) {
	if !res.IsProvisional() {
		return
	}
	b.mu.Lock()
	sc, ok := b.calls[callID]
	if !ok || sc.done || sc.dir != "out" || sc.state != "ringing" {
		b.mu.Unlock()
		return
	}
	switch res.StatusCode {
	case sip.StatusRinging: // 180
		if sc.sentRinging {
			b.mu.Unlock()
			return
		}
		sc.sentRinging = true
		b.mu.Unlock()
		b.emit(call.EvRinging{CallID: callID, Early: false})
	case 183: // Session Progress (early media の可能性)
		if len(res.Body()) > 0 {
			if off, err := parseOffer(res.Body()); err == nil {
				sc.remote = off.addr
				if sc.pipe == nil {
					sc.pipe = newRTPPipe(sc.conn, off.addr, b.log)
					sc.conn = nil
				} else {
					sc.pipe.setRemote(off.addr)
				}
				pipe := sc.pipe
				wasSent := sc.sentEarly
				sc.sentEarly = true
				sc.sentRinging = true
				b.mu.Unlock()
				if !wasSent {
					b.emit(call.EvRinging{CallID: callID, Early: true, Pipe: pipe})
				}
				return
			}
		}
		if sc.sentRinging {
			b.mu.Unlock()
			return
		}
		sc.sentRinging = true
		b.mu.Unlock()
		b.emit(call.EvRinging{CallID: callID, Early: false})
	default:
		b.mu.Unlock()
		// 100 Trying 等は通知しない。
	}
}

// failOutgoing は発信失敗の後片付けと (必要なら) Ended 通知である。
// alreadyGone が偽の場合、自発終了の可能性があるため Ended を上げない。
func (b *Backend) failOutgoing(callID, reason string, code int, notify bool) {
	b.mu.Lock()
	sc, ok := b.calls[callID]
	aborted := !ok || (ok && sc.done)
	if ok && !sc.done {
		sc.done = true
		delete(b.calls, callID)
	}
	var pipe *rtpPipe
	var conn *net.UDPConn
	if ok {
		pipe, conn = sc.pipe, sc.conn
	}
	b.mu.Unlock()
	if pipe != nil {
		_ = pipe.Close()
	} else if conn != nil {
		_ = conn.Close()
	}
	if notify && !aborted {
		b.emit(call.EvEnded{CallID: callID, Reason: reason, Code: code})
	}
}

// outgoingFailReason は発信失敗の SIP 応答を ended.reason に写像する。
func outgoingFailReason(code int) string {
	switch code {
	case 486, 603:
		return "reject"
	case 487:
		return "cancel"
	case 408, 480:
		return "timeout"
	default:
		return "error"
	}
}

// failureReason は拒否応答の reason 句である。
func failureReason(code int) string {
	switch code {
	case 486:
		return "Busy Here"
	case 480:
		return "Temporarily Unavailable"
	case 487:
		return "Request Terminated"
	case 488:
		return "Not Acceptable Here"
	case 603:
		return "Decline"
	default:
		return "Decline"
	}
}

// bindRTPPort は RTP_PORT_MIN..MAX から UDP ソケットを確保する。
func (b *Backend) bindRTPPort() (*net.UDPConn, int, error) {
	b.rtpMu.Lock()
	defer b.rtpMu.Unlock()
	lo, hi := b.cfg.RTPPortMin, b.cfg.RTPPortMax
	if b.rtpNext < lo || b.rtpNext > hi {
		b.rtpNext = lo
	}
	for i := lo; i <= hi; i++ {
		port := b.rtpNext
		b.rtpNext++
		if b.rtpNext > hi {
			b.rtpNext = lo
		}
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: port})
		if err != nil {
			continue
		}
		return conn, port, nil
	}
	return nil, 0, fmt.Errorf("RTP ポート枯渇 (%d-%d)", lo, hi)
}

// detectLocalIP は Asterisk への経路から自 IP を推定する。
// 失敗時はループバック・リンクローカル以外の最初の IPv4、無ければ 127.0.0.1。
//
// 経路以外から選んだ IP は SDP と Contact に載るため、外れると片通話や
// 再 INVITE の不達になる。推測で進めた場合は LOCAL_IP を促す警告を出す。
func detectLocalIP(target string, log *slog.Logger) string {
	if log == nil {
		log = slog.Default()
	}
	if conn, err := net.Dial("udp", target); err == nil {
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil && !addr.IP.IsUnspecified() {
			ip := addr.IP.String()
			_ = conn.Close()
			return ip
		}
		_ = conn.Close()
	}
	log.Warn("SIP サーバへの経路から自 IP を特定できず、interface から推測する",
		"target", target, "hint", "LOCAL_IP を明示すると確実")
	ifs, err := net.Interfaces()
	if err == nil {
		for _, inf := range ifs {
			if inf.Flags&net.FlagUp == 0 || inf.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := inf.Addrs()
			if err != nil {
				continue
			}
			for _, a := range addrs {
				var ip net.IP
				switch v := a.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				// リンクローカル (169.254.0.0/16) は相手から到達できない。
				if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
					continue
				}
				if ip = ip.To4(); ip != nil {
					log.Warn("interface から自 IP を推測した (docker0 等を選ぶ可能性がある)",
						"ip", ip.String(), "interface", inf.Name)
					return ip.String()
				}
			}
		}
	}
	log.Error("自 IP を特定できず 127.0.0.1 を使う (SDP/Contact が不正になる)",
		"hint", "LOCAL_IP を設定")
	return "127.0.0.1"
}

func randHexID() string { return randHex(8) }

func randHex(n int) string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(buf[:])[:n*2]
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}

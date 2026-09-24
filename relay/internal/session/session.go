// Package session は WebSocket セッション管理を行う。
//
// relay は複数の SIP アカウント (内線) を同時に扱う (docs/PROTOCOL.md v1.1)。
// account ごとに「グループ」(Backend + call.Manager + 所属接続 + 勝者/発信元 +
// メディアポンプ) を持ち、端末 (X-Device-Id) は sip_account でグループに
// 結び付く。hello 送信、ping/pong、incoming のファンアウト (最初の answer が
// 勝つ)、勝者セッションとバックエンド間のバイナリ (RTP) 中継はグループ単位で
// 行う。
//
// ロック順序: hub.mu → group.mu の一方向のみ。group.mu を保持したまま
// hub.mu を取ってはならない。WS への書き込みはロック解放後に行う。
package session

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/tmlksu/sipbridge/relay/internal/call"
	"github.com/tmlksu/sipbridge/relay/internal/proto"
	"github.com/tmlksu/sipbridge/relay/internal/push"
	"github.com/tmlksu/sipbridge/relay/internal/state"
)

// 既定値。
const (
	DefaultPingInterval = 20 * time.Second
	// DefaultResumeTimeout は通話中の切断から BYE までの猶予である。
	// アプリ側の再接続バックオフと Access/Tunnel のハンドシェイクを含めて収まる値にする。
	DefaultResumeTimeout = 30 * time.Second
	writeTimeout         = 10 * time.Second
	// sendQueueSize は接続ごとの送信キュー長である。RTP は 20ms 間隔なので
	// 128 は約 2.5 秒分に当たる。これを超えて滞る接続は既に使い物にならない。
	sendQueueSize = 128
)

// BackendFactory は account ごとの call.Backend を作る。
// main では sipbackend / fakebackend、テストでは fakebackend を渡す。
type BackendFactory func(user, password, display string) (call.Backend, error)

// Config は Hub の設定である。
type Config struct {
	Version string
	// DefaultAccount/Password/Display は互換用の既定アカウント (SIP_USER 等)。
	// 設定されていれば、結び付けの無い端末を接続時に暫定的にここへ結び付ける
	// (永続化しない)。空なら端末が sip_account を送るまで account 無しになる。
	DefaultAccount  string
	DefaultPassword string
	DefaultDisplay  string
	PingInterval    time.Duration // WS ping 周期。0 なら既定 20 秒
	ResumeTimeout   time.Duration // 通話中の勝者切断から BYE までの猶予。0 なら DefaultResumeTimeout
	NoAnswerTimeout time.Duration // 着信の無応答タイムアウト。0 なら call の既定 (25 秒)
}

func (c Config) withDefaults() Config {
	if c.PingInterval <= 0 {
		c.PingInterval = DefaultPingInterval
	}
	if c.ResumeTimeout <= 0 {
		c.ResumeTimeout = DefaultResumeTimeout
	}
	return c
}

// Hub は全 WS 接続と account グループを管理する。
type Hub struct {
	factory BackendFactory
	pusher  push.Pusher
	store   *state.Store
	cfg     Config
	log     *slog.Logger

	mu     sync.Mutex
	ctx    context.Context               // Run で受け取った親 ctx (group の親)
	groups map[string]*group             // account → グループ
	conns  map[string]map[*Conn]struct{} // deviceID → 接続集合 (全 account 横断)
}

// NewHub は Hub を作る。store が nil ならメモリのみの状態を使う。
func NewHub(factory BackendFactory, pusher push.Pusher, store *state.Store, cfg Config, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	if store == nil {
		store, _ = state.New("")
	}
	return &Hub{
		factory: factory, pusher: pusher, store: store,
		cfg: cfg.withDefaults(), log: log,
		groups: make(map[string]*group),
		conns:  make(map[string]map[*Conn]struct{}),
	}
}

// Run は永続化済みの全 account (と既定アカウント) のグループを起動する。
// 個々の account の起動失敗は警告に留め、relay 全体は起動する。
func (h *Hub) Run(ctx context.Context) error {
	h.mu.Lock()
	h.ctx = ctx
	h.mu.Unlock()

	started := 0
	if a := h.cfg.DefaultAccount; a != "" {
		if _, err := h.ensureGroup(a, h.cfg.DefaultPassword, h.cfg.DefaultDisplay); err != nil {
			h.log.Warn("既定アカウントの起動に失敗", "account", a, "err", err)
		} else {
			started++
		}
	}
	for user, acc := range h.store.Accounts() {
		if user == h.cfg.DefaultAccount {
			continue
		}
		if _, err := h.ensureGroup(user, acc.Password, acc.Display); err != nil {
			h.log.Warn("アカウントの起動に失敗", "account", user, "err", err)
			continue
		}
		started++
	}
	h.log.Info("アカウント起動", "count", started)
	return nil
}

// AccountCount は起動中の account 数である。
func (h *Hub) AccountCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.groups)
}

// baseCtx は group の親 context を返す (Run 前は Background)。
func (h *Hub) baseCtx() context.Context {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx == nil {
		return context.Background()
	}
	return h.ctx
}

// ensureGroup は account のグループを取得する。無ければ Backend を作って起動する。
func (h *Hub) ensureGroup(account, password, display string) (*group, error) {
	h.mu.Lock()
	if g, ok := h.groups[account]; ok {
		h.mu.Unlock()
		return g, nil
	}
	h.mu.Unlock()

	g := newGroup(h, account)
	if err := g.startBackend(password, display); err != nil {
		return nil, err
	}
	h.mu.Lock()
	// 競合して別ゴルーチンが先に作っていたら、そちらを使う。
	if exist, ok := h.groups[account]; ok {
		h.mu.Unlock()
		g.stop()
		return exist, nil
	}
	h.groups[account] = g
	h.mu.Unlock()
	h.log.Info("アカウント開始", "account", account)
	return g, nil
}

// stopGroupIfUnused は結び付いた端末が 0 になったグループを停止し、
// state から account を削除する。既定アカウントは常駐させる。
func (h *Hub) stopGroupIfUnused(g *group) {
	if g == nil || g.account == "" || g.account == h.cfg.DefaultAccount {
		return
	}
	if h.store.CountDevicesForAccount(g.account) > 0 {
		return
	}
	h.mu.Lock()
	if h.groups[g.account] != g || g.connCount() > 0 {
		h.mu.Unlock()
		return
	}
	delete(h.groups, g.account)
	h.mu.Unlock()

	g.stop()
	if err := h.store.RemoveAccount(g.account); err != nil {
		h.log.Warn("account の削除に失敗", "account", g.account, "err", err)
	}
	h.log.Info("アカウント停止 (結び付いた端末が 0)", "account", g.account)
}

// sessionCount は現在の WS 接続総数である。
func (h *Hub) sessionCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, set := range h.conns {
		n += len(set)
	}
	return n
}

// onlineDevices は WS 接続中のデバイス ID 集合である。
func (h *Hub) onlineDevices() map[string]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]bool, len(h.conns))
	for dev, set := range h.conns {
		if len(set) > 0 {
			out[dev] = true
		}
	}
	return out
}

// pushIncomingOffline は account に結び付いた WS 未接続デバイスへ FCM を送る。
// PUSH モード端末は他端末が接続中でも着信に起床する必要があるため、
// 「接続ゼロ時のみ」ではなく「オフライン端末ごと」に送る。
// 実送信はイベントポンプを塞がないよう非同期に行う。
func (h *Hub) pushIncomingOffline(ctx context.Context, account string, e call.EvIncoming) {
	if h.pusher == nil || account == "" {
		return
	}
	devs := h.store.DevicesForAccount(account)
	if account == h.cfg.DefaultAccount {
		// 既定アカウント: sip_account を送らない旧アプリ (結び付け未永続化) の
		// 端末も対象にする。
		for id, d := range h.store.Devices() {
			if d.Account == "" {
				devs[id] = d
			}
		}
	}
	online := h.onlineDevices()
	tokens := make([]string, 0, len(devs))
	for id, d := range devs {
		if d.Push == nil || d.Push.Token == "" || online[id] {
			continue
		}
		tokens = append(tokens, d.Push.Token)
	}
	if len(tokens) == 0 {
		return
	}
	p := push.Payload{Type: "incoming", CallID: e.CallID, From: e.From, Display: e.Display}
	// 送信は別 goroutine で行う。イベントポンプ上で FCM の HTTP 往復を
	// 待つと、後続の answered/ended の配送が最大でタイムアウト分遅れる。
	// tokens は spawn 前に確定済みなので競合しない。
	// ctx は Hub 停止でキャンセルされるが、push は投げ切りたいので
	// キャンセルだけ切り離す (値は引き継ぐ)。
	sendCtx := context.WithoutCancel(ctx)
	go func() {
		ctx2, cancel := context.WithTimeout(sendCtx, 10*time.Second)
		defer cancel()
		if err := h.pusher.Send(ctx2, tokens, p); err != nil {
			h.log.Warn("push 送信失敗", "err", err, "callId", e.CallID)
		} else {
			h.log.Info("push 送信", "devices", len(tokens), "callId", e.CallID, "account", account)
		}
	}()
}

// ---- WS ハンドラ ----

// ServeWS は 1 本の WS 接続を受け付ける。認証済みであることが前提。
// X-Device-Id ヘッダが必須である。
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	deviceID := r.Header.Get("X-Device-Id")
	if deviceID == "" {
		http.Error(w, "X-Device-Id ヘッダが必要", http.StatusBadRequest)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: false,
	})
	if err != nil {
		h.log.Warn("WS accept 失敗", "err", err)
		return
	}
	c := &Conn{
		hub: h, ws: ws, deviceID: deviceID,
		version: r.Header.Get("X-Client-Version"),
		out:     make(chan outFrame, sendQueueSize),
	}
	h.addConn(c)
	defer h.removeConn(c)

	g := c.group()
	account := ""
	if g != nil {
		account = g.account
	}
	h.log.Info("WS 接続", "device", deviceID, "version", c.version, "account", account)
	if err := c.writeHello(); err != nil {
		h.log.Warn("hello 送信失敗", "err", err)
		_ = ws.Close(websocket.StatusInternalError, "hello failed")
		return
	}
	// 同一デバイスの再接続で通話継続 (resume) の場合は猶予タイマを止める。
	if g != nil {
		g.cancelResumeIfWinner(deviceID)
	}

	ctx := r.Context()
	go c.pingLoop(ctx)
	// hello 送信後に送信ループを回す (hello より前に積まれたフレームもここで流れる)。
	go c.writeLoop(ctx)
	c.readLoop(ctx)
}

// addConn は接続を登録し、結び付け済み account (無ければ既定アカウント) の
// グループへ参加させる。
func (h *Hub) addConn(c *Conn) {
	account, password, display := h.initialAccount(c.deviceID)
	var g *group
	if account != "" {
		var err error
		if g, err = h.ensureGroup(account, password, display); err != nil {
			h.log.Warn("アカウントの起動に失敗", "account", account, "err", err)
			g = nil
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	set, ok := h.conns[c.deviceID]
	if !ok {
		set = make(map[*Conn]struct{})
		h.conns[c.deviceID] = set
	}
	set[c] = struct{}{}
	c.grp = g
	if g != nil {
		g.attach(c)
	}
}

// initialAccount は接続時に使う account を決める。
// 永続化された結び付けが最優先、無ければ既定アカウント (暫定, 永続化しない)。
func (h *Hub) initialAccount(deviceID string) (account, password, display string) {
	if d, ok := h.store.Device(deviceID); ok && d.Account != "" {
		if a, ok2 := h.store.Account(d.Account); ok2 {
			return d.Account, a.Password, a.Display
		}
		// 資格情報が失われている場合は結び付けを無視する (端末が再送する)。
		h.log.Warn("結び付いた account の資格情報が無い", "device", deviceID, "account", d.Account)
	}
	if h.cfg.DefaultAccount != "" {
		return h.cfg.DefaultAccount, h.cfg.DefaultPassword, h.cfg.DefaultDisplay
	}
	return "", "", ""
}

func (h *Hub) removeConn(c *Conn) {
	_ = c.ws.Close(websocket.StatusNormalClosure, "bye")
	h.mu.Lock()
	if set, ok := h.conns[c.deviceID]; ok {
		delete(set, c)
		if len(set) == 0 {
			delete(h.conns, c.deviceID)
		}
	}
	g := c.grp
	c.grp = nil
	if g != nil {
		g.detach(c)
	}
	h.mu.Unlock()
	h.log.Info("WS 切断", "device", c.deviceID)
}

// moveConn は接続を別グループ (nil = account 無し) へ付け替える。
func (h *Hub) moveConn(c *Conn, g *group) {
	h.mu.Lock()
	old := c.grp
	if old == g {
		h.mu.Unlock()
		return
	}
	if old != nil {
		old.detach(c)
	}
	c.grp = g
	if g != nil {
		g.attach(c)
	}
	h.mu.Unlock()
	h.stopGroupIfUnused(old)
}

// bindAccount は sip_account を処理する。戻り値は error メッセージの
// code/message (空なら成功)。docs/PROTOCOL.md「SIP アカウント」節に従う。
func (h *Hub) bindAccount(c *Conn, user, password, display string) (code, message string) {
	if user == "" {
		if err := h.store.SetDeviceAccount(c.deviceID, ""); err != nil {
			return "store_failed", err.Error()
		}
		h.moveConn(c, nil)
		h.log.Info("account 結び付け解除", "device", c.deviceID)
		return "", ""
	}

	h.mu.Lock()
	g := h.groups[user]
	h.mu.Unlock()

	if g != nil {
		curPassword, curDisplay := g.credentials()
		switch {
		case password == curPassword:
			if display != "" && display != curDisplay {
				g.setDisplay(display)
				if err := h.store.SetAccount(user, state.Account{Password: password, Display: display}); err != nil {
					h.log.Warn("account の保存に失敗", "account", user, "err", err)
				}
			}
		case g.registered():
			// 登録済み内線の乗っ取り・DoS を防ぐ。結び付けは変更しない。
			h.log.Warn("account のパスワード不一致", "account", user, "device", c.deviceID)
			return "account_password_mismatch", "登録済みアカウントのパスワードが一致しない"
		default:
			// 未登録/登録失敗中なら新しいパスワードで Backend を作り直す。
			if err := g.startBackend(password, display); err != nil {
				return "account_failed", err.Error()
			}
			if err := h.store.SetAccount(user, state.Account{Password: password, Display: display}); err != nil {
				h.log.Warn("account の保存に失敗", "account", user, "err", err)
			}
			h.log.Info("account のパスワードを更新", "account", user)
		}
	} else {
		var err error
		if g, err = h.ensureGroup(user, password, display); err != nil {
			return "account_failed", err.Error()
		}
		if err := h.store.SetAccount(user, state.Account{Password: password, Display: display}); err != nil {
			h.log.Warn("account の保存に失敗", "account", user, "err", err)
		}
	}

	if err := h.store.SetDeviceAccount(c.deviceID, user); err != nil {
		return "store_failed", err.Error()
	}
	h.moveConn(c, g)
	h.log.Info("account 結び付け", "device", c.deviceID, "account", user)
	return "", ""
}

// ---- Conn ----

// Conn は 1 本の WS 接続である。
type Conn struct {
	hub      *Hub
	ws       *websocket.Conn
	deviceID string
	version  string

	// grp は所属グループ。hub.mu で保護する。
	grp *group

	writeMu sync.Mutex

	// out は送信キューである。配信側 (イベントポンプ・メディアポンプ) が
	// 遅いクライアントの書き込みを待たないよう、実送信は writeLoop に任せる。
	out          chan outFrame
	mediaDropped atomic.Uint64
}

// outFrame は送信キューの 1 フレームである。
type outFrame struct {
	typ  websocket.MessageType
	data []byte
}

// writeLoop は送信キューを順に流す。書き込みに失敗したら接続を畳む
// (readLoop もエラーで抜け、通常の切断処理に合流する)。
func (c *Conn) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-c.out:
			if err := c.writeRaw(ctx, f.typ, f.data); err != nil {
				_ = c.ws.Close(websocket.StatusGoingAway, "write failed")
				return
			}
		}
	}
}

// enqueue は送信キューに積む。キューが満ならメディアは捨て、
// 制御メッセージなら接続を畳む (古い状態を持ったまま繋げるより、
// 再接続させて hello で同期させる方が安全)。
func (c *Conn) enqueue(typ websocket.MessageType, data []byte) {
	select {
	case c.out <- outFrame{typ: typ, data: data}:
		return
	default:
	}
	if typ == websocket.MessageBinary {
		if n := c.mediaDropped.Add(1); n%100 == 1 {
			c.hub.log.Warn("送信キュー満のため RTP を破棄", "device", c.deviceID, "dropped", n)
		}
		return
	}
	c.hub.log.Warn("送信キュー滞留のため切断", "device", c.deviceID)
	// Close は close ハンドシェイク (書き込み中の writeRaw の完了待ち + 相手の応答待ち) で
	// 最大十数秒ブロックする。ここは配信側 (イベント/メディアポンプ) から呼ばれるので、
	// 同期で待つと詰まった 1 台が再び全体を止める。別 goroutine で閉じる (多重呼び出しは安全)。
	go func() { _ = c.ws.Close(websocket.StatusPolicyViolation, "send queue overflow") }()
}

// group は所属グループを返す (account 無しなら nil)。
func (c *Conn) group() *group {
	c.hub.mu.Lock()
	defer c.hub.mu.Unlock()
	return c.grp
}

func (c *Conn) writeRaw(ctx context.Context, typ websocket.MessageType, data []byte) error {
	ctx2, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.Write(ctx2, typ, data)
}

// sendJSON は送信キュー経由で JSON を送る (error は符号化失敗のみ)。
func (c *Conn) sendJSON(v any) error {
	data, err := proto.Encode(v)
	if err != nil {
		return err
	}
	c.enqueue(websocket.MessageText, data)
	return nil
}

// sendJSONSync は writeLoop を介さずに即送する (hello のように
// 失敗をその場で判定したいフレーム用)。
func (c *Conn) sendJSONSync(v any) error {
	data, err := proto.Encode(v)
	if err != nil {
		return err
	}
	return c.writeRaw(context.Background(), websocket.MessageText, data)
}

func (c *Conn) sendError(code, message string) {
	_ = c.sendJSON(&proto.Error{T: proto.TError, Code: code, Message: message})
}

// writeHello は hello を送る (接続直後と sip_account 受理後)。
func (c *Conn) writeHello() error {
	account := ""
	registered := false
	var ci *proto.CallInfo
	if g := c.group(); g != nil {
		account = g.account
		mgr := g.manager()
		registered, _ = mgr.Registered()
		if cur := mgr.Current(); cur != nil {
			ci = &proto.CallInfo{
				CallID: cur.CallID, Direction: cur.Direction, State: cur.State,
				From: cur.From, Display: cur.Display, PT: cur.PT,
				StartedAt: cur.StartedAt,
			}
		}
	}
	return c.sendJSONSync(&proto.Hello{
		T: proto.THello, RelayVersion: c.hub.cfg.Version,
		Extension: account, Account: account, Registered: registered,
		Call: ci, ServerTime: time.Now().Unix(),
	})
}

// pingLoop は 20 秒周期で WS ping を送り、無応答なら接続を切る。
func (c *Conn) pingLoop(ctx context.Context) {
	t := time.NewTicker(c.hub.cfg.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				c.hub.log.Warn("ping 無応答のため切断", "device", c.deviceID, "err", err)
				_ = c.ws.Close(websocket.StatusGoingAway, "ping timeout")
				return
			}
		}
	}
}

// readLoop は受信フレームを処理する。
func (c *Conn) readLoop(ctx context.Context) {
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageText:
			c.handleText(data)
		case websocket.MessageBinary:
			c.handleBinary(data)
		}
	}
}

func (c *Conn) handleBinary(data []byte) {
	g := c.group()
	if g == nil {
		return // account 無しのメディアは捨てる
	}
	if g.winner() != c.deviceID {
		return // 勝者以外のメディアは捨てる
	}
	pipe := g.manager().Pipe()
	if pipe == nil {
		return
	}
	cp := append([]byte(nil), data...)
	_ = pipe.Send(cp)
}

func (c *Conn) handleText(data []byte) {
	msg, err := proto.Decode(data)
	if err != nil {
		c.sendError("bad_message", err.Error())
		return
	}
	switch m := msg.(type) {
	case *proto.Answer:
		g := c.requireGroup()
		if g == nil {
			return
		}
		pt := 0
		if m.PT != nil {
			pt = *m.PT
		}
		if err := g.manager().Answer(m.CallID, pt); err != nil {
			c.sendError("answer_failed", err.Error())
			return
		}
		g.setWinner(c.deviceID)
	case *proto.Reject:
		g := c.requireGroup()
		if g == nil {
			return
		}
		code := 486
		if m.Code != nil {
			code = *m.Code
		}
		if err := g.manager().Reject(m.CallID, code); err != nil {
			c.sendError("reject_failed", err.Error())
		}
	case *proto.Hangup:
		g := c.requireGroup()
		if g == nil {
			return
		}
		if err := g.manager().Hangup(m.CallID); err != nil {
			c.sendError("hangup_failed", err.Error())
		}
	case *proto.Dial:
		g := c.requireGroup()
		if g == nil {
			return
		}
		if _, err := g.manager().Dial(m.To); err != nil {
			c.sendError("dial_failed", err.Error())
			return
		}
		g.setDialer(c.deviceID)
	case *proto.Dtmf:
		// v2 対応。現状は未対応エラーを返す。
		c.sendError("not_supported", "dtmf は v2 で対応予定")
	case *proto.RegisterPush:
		if m.Provider == "" || m.Token == "" {
			c.sendError("bad_message", "provider と token が必要")
			return
		}
		// push 登録は account の有無に関わらず端末情報として保存する。
		if err := c.hub.store.SetDevicePush(c.deviceID, state.Push{Provider: m.Provider, Token: m.Token}); err != nil {
			c.hub.log.Warn("push 登録の保存失敗", "err", err)
			c.sendError("store_failed", "push 登録の保存に失敗")
			return
		}
		c.hub.log.Info("push 登録", "device", c.deviceID, "provider", m.Provider)
	case *proto.SipAccount:
		code, message := c.hub.bindAccount(c, m.User, m.Password, m.Display)
		if code != "" {
			c.sendError(code, message)
			return
		}
		// 受理したら改めて hello を送る (account/registered/call は新しい group のもの)。
		if err := c.writeHello(); err != nil {
			c.hub.log.Warn("hello 送信失敗", "err", err, "device", c.deviceID)
		}
	case *proto.Ping:
		_ = c.sendJSON(&proto.Pong{T: proto.TPong, Ts: m.Ts})
	default:
		c.sendError("bad_message", fmt.Sprintf("relay は種別 %T を受け付けない", msg))
	}
}

// requireGroup は所属グループを返し、無ければ no_account を返して nil を返す。
func (c *Conn) requireGroup() *group {
	g := c.group()
	if g == nil {
		c.sendError("no_account", "SIP アカウントが設定されていない (sip_account を送ること)")
		return nil
	}
	return g
}

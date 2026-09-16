package session

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/tmlksu/sipbridge/relay/internal/call"
	"github.com/tmlksu/sipbridge/relay/internal/proto"
)

// group は 1 つの SIP アカウント (内線) に対応する単位である。
// Backend + call.Manager + 所属接続 + 勝者/発信元 + メディアポンプを持つ。
// 1 account = 同時 1 通話。
//
// ロックは group.mu だけを使い、hub.mu を取ってはならない
// (ロック順序は hub.mu → group.mu)。WS への書き込みはロック解放後に行う。
type group struct {
	hub     *Hub
	account string
	log     *slog.Logger

	mu       sync.Mutex
	password string
	display  string
	mgr      *call.Manager
	cancel   context.CancelFunc // Backend/Manager の停止
	stopped  bool

	devs         map[string]map[*Conn]struct{} // deviceID → 接続集合
	winnerDevice string                        // アクティブ通話の勝者デバイス
	dialerDevice string                        // 発信中 (RINGING_OUT) の要求元デバイス
	resumeTimer  *time.Timer
	pumping      bool // メディアポンプ起動済み (通話ごとに 1 本)
}

func newGroup(h *Hub, account string) *group {
	return &group{
		hub:     h,
		account: account,
		log:     h.log.With("account", account),
		devs:    make(map[string]map[*Conn]struct{}),
	}
}

// startBackend は Backend と Manager を (再) 生成して起動する。
// 既存のものがあれば context キャンセルで停止する (sipbackend は
// 停止時に REGISTER Expires:0 を送る)。
func (g *group) startBackend(password, display string) error {
	be, err := g.hub.factory(g.account, password, display)
	if err != nil {
		return err
	}
	mgr := call.NewManager(be)
	if t := g.hub.cfg.NoAnswerTimeout; t > 0 {
		mgr.NoAnswerTimeout = t
	}
	ctx, cancel := context.WithCancel(g.hub.baseCtx())
	if err := mgr.Start(ctx); err != nil {
		cancel()
		return err
	}
	g.mu.Lock()
	old := g.cancel
	g.cancel = cancel
	g.mgr = mgr
	g.password = password
	g.display = display
	g.winnerDevice = ""
	g.dialerDevice = ""
	g.pumping = false
	if g.resumeTimer != nil {
		g.resumeTimer.Stop()
		g.resumeTimer = nil
	}
	g.mu.Unlock()
	if old != nil {
		old() // 旧 Backend を停止 (登録解除)
	}
	go g.eventPump(ctx, mgr)
	return nil
}

// stop はグループを停止する (Backend の登録解除を含む)。
func (g *group) stop() {
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		return
	}
	g.stopped = true
	cancel := g.cancel
	g.cancel = nil
	if g.resumeTimer != nil {
		g.resumeTimer.Stop()
		g.resumeTimer = nil
	}
	g.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (g *group) manager() *call.Manager {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.mgr
}

func (g *group) credentials() (password, display string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.password, g.display
}

func (g *group) setDisplay(display string) {
	g.mu.Lock()
	g.display = display
	g.mu.Unlock()
}

// registered は Backend の REGISTER 状態である。
func (g *group) registered() bool {
	mgr := g.manager()
	if mgr == nil {
		return false
	}
	ok, _ := mgr.Registered()
	return ok
}

func (g *group) winner() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.winnerDevice
}

func (g *group) setWinner(deviceID string) {
	g.mu.Lock()
	g.winnerDevice = deviceID
	g.mu.Unlock()
}

// setDialer は発信要求元を記録する (発信元が勝者になる)。
func (g *group) setDialer(deviceID string) {
	g.mu.Lock()
	g.winnerDevice = deviceID
	g.dialerDevice = deviceID
	g.mu.Unlock()
}

// attach は接続をグループに加える (呼び出し側が hub.mu を保持)。
func (g *group) attach(c *Conn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	set, ok := g.devs[c.deviceID]
	if !ok {
		set = make(map[*Conn]struct{})
		g.devs[c.deviceID] = set
	}
	set[c] = struct{}{}
}

// detach は接続を外す (呼び出し側が hub.mu を保持)。勝者デバイスが
// 全断したら猶予後に BYE するタイマを張る。
func (g *group) detach(c *Conn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if set, ok := g.devs[c.deviceID]; ok {
		delete(set, c)
		if len(set) == 0 {
			delete(g.devs, c.deviceID)
		}
	}
	if g.stopped || g.winnerDevice == "" || g.winnerDevice != c.deviceID {
		return
	}
	if set, ok := g.devs[c.deviceID]; ok && len(set) > 0 {
		return // 同一デバイスの別接続が残っている
	}
	if g.mgr != nil && g.mgr.Current() != nil {
		g.armResumeLocked()
	}
}

// connCount はグループ内の接続数である。
func (g *group) connCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, set := range g.devs {
		n += len(set)
	}
	return n
}

// armResumeLocked は勝者全断からの猶予タイマを開始する (呼び出し側が mu 保持)。
func (g *group) armResumeLocked() {
	if g.resumeTimer != nil {
		g.resumeTimer.Stop()
	}
	timeout := g.hub.cfg.ResumeTimeout
	mgr := g.mgr
	g.log.Info("勝者切断、猶予後に BYE", "timeout", timeout)
	g.resumeTimer = time.AfterFunc(timeout, func() {
		g.mu.Lock()
		if _, ok := g.devs[g.winnerDevice]; ok || g.stopped {
			g.mu.Unlock()
			return // 再接続済み / 停止済み
		}
		g.mu.Unlock()
		g.log.Info("猶予超過のため切断")
		mgr.HangupTimeout()
	})
}

// cancelResumeIfWinner は勝者デバイスの再接続時に猶予タイマを止める。
func (g *group) cancelResumeIfWinner(deviceID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.winnerDevice != "" && g.winnerDevice == deviceID && g.resumeTimer != nil {
		g.resumeTimer.Stop()
		g.resumeTimer = nil
		g.log.Info("通話継続 (再接続)", "device", deviceID)
	}
}

// ---- イベント配送 ----

// eventPump は Manager の公開イベントをグループ内の接続へ配送する。
// Backend を作り直すと ctx がキャンセルされ、このポンプも終了する。
func (g *group) eventPump(ctx context.Context, mgr *call.Manager) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-mgr.Events():
			if g.manager() != mgr {
				return // 作り直された古い Manager のイベントは捨てる
			}
			g.dispatch(ctx, mgr, ev)
		}
	}
}

func (g *group) dispatch(ctx context.Context, mgr *call.Manager, ev call.Event) {
	switch e := ev.(type) {
	case call.EvRegistered:
		g.broadcast(&proto.Registration{T: proto.TRegistration, OK: e.OK, Detail: e.Detail})
	case call.EvIncoming:
		g.broadcast(&proto.Incoming{
			T: proto.TIncoming, CallID: e.CallID,
			From: e.From, Display: e.Display, PT: e.PT,
		})
		g.hub.pushIncomingOffline(ctx, g.account, e)
	case call.EvRinging:
		g.broadcast(&proto.Ringing{T: proto.TRinging, CallID: e.CallID, Early: e.Early})
		if e.Early {
			g.startMediaPump(mgr) // 早期メディア (183)
		}
	case call.EvAnswered:
		g.onAnswered(mgr, e)
	case call.EvEnded:
		g.mu.Lock()
		g.winnerDevice = ""
		g.dialerDevice = ""
		g.pumping = false
		g.mu.Unlock()
		g.broadcast(&proto.Ended{
			T: proto.TEnded, CallID: e.CallID, Reason: e.Reason, Code: e.Code,
		})
	}
}

// onAnswered は answer 確定時の配送である。着信 (direction=in) では勝者以外に
// ended{reason:answered_elsewhere} を送り、メディアは勝者のみにする。
func (g *group) onAnswered(mgr *call.Manager, e call.EvAnswered) {
	cur := mgr.Current()
	winner := g.winner()
	answered := &proto.Answered{T: proto.TAnswered, CallID: e.CallID, PT: e.PT}
	if cur != nil && cur.Direction == "in" && winner != "" {
		g.sendToDevice(winner, answered)
		elsewhere := &proto.Ended{
			T: proto.TEnded, CallID: e.CallID,
			Reason: "answered_elsewhere", Code: 200,
		}
		g.broadcastExcept(winner, elsewhere)
		g.startMediaPump(mgr)
		return
	}
	g.broadcast(answered)
	g.startMediaPump(mgr)
}

// conns はグループ内の接続一覧である (except/only でデバイスを絞る)。
func (g *group) conns(onlyDevice, exceptDevice string) []*Conn {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []*Conn
	for dev, set := range g.devs {
		if onlyDevice != "" && dev != onlyDevice {
			continue
		}
		if exceptDevice != "" && dev == exceptDevice {
			continue
		}
		for c := range set {
			out = append(out, c)
		}
	}
	return out
}

// writeAll はロックを解放した状態で各接続へ書き込む。
func writeAll(targets []*Conn, typ websocket.MessageType, data []byte) {
	for _, c := range targets {
		_ = c.writeRaw(context.Background(), typ, data)
	}
}

// broadcast はグループの全接続に JSON を送る。
func (g *group) broadcast(v any) {
	data, err := proto.Encode(v)
	if err != nil {
		return
	}
	writeAll(g.conns("", ""), websocket.MessageText, data)
}

// broadcastExcept は指定デバイス以外に JSON を送る。
func (g *group) broadcastExcept(exceptDevice string, v any) {
	data, err := proto.Encode(v)
	if err != nil {
		return
	}
	writeAll(g.conns("", exceptDevice), websocket.MessageText, data)
}

// sendToDevice は指定デバイスの全接続に JSON を送る。
func (g *group) sendToDevice(deviceID string, v any) {
	data, err := proto.Encode(v)
	if err != nil {
		return
	}
	writeAll(g.conns(deviceID, ""), websocket.MessageText, data)
}

// startMediaPump はバックエンド→勝者への RTP 転送を開始する。
func (g *group) startMediaPump(mgr *call.Manager) {
	pipe := mgr.Pipe()
	if pipe == nil {
		return
	}
	g.mu.Lock()
	if g.pumping {
		g.mu.Unlock()
		return // 183 と 200 で二重起動しない
	}
	g.pumping = true
	g.mu.Unlock()
	go func() {
		for pkt := range pipe.Recv() {
			// 勝者不在 (resume 猶予中) でもパイプは読み続け、破棄する。
			w := g.winner()
			if w == "" {
				continue
			}
			writeAll(g.conns(w, ""), websocket.MessageBinary, pkt)
		}
	}()
}

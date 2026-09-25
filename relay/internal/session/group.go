package session

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
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
	pumpPipe     call.MediaPipe // メディアポンプが読んでいるパイプ (通話ごとに 1 本)
	curRec       *callRecord    // 進行中の通話の品質計測 (メディア開始で作る)

	// route は RTP ホットパス (上り handleBinary / 下りメディアポンプ) 用の
	// 勝者・パイプ・勝者の接続集合のスナップショットである。パケットごとに
	// hub.mu / group.mu / Manager.mu を取らずに済むよう atomic で公開する。
	// 書き換えは必ず group.mu 保持下の refreshRouteLocked で行い、公開した
	// mediaRoute は不変 (書き換えず作り直す)。
	route atomic.Pointer[mediaRoute]
}

// mediaRoute は RTP 中継先の不変スナップショットである。
//
// 勝者・所属接続・パイプのいずれかが変わる箇所 (setWinner/setDialer, attach/detach,
// startMediaPump, 通話終了, Backend 作り直し, stop) で作り直す。Manager 内部で
// パイプが閉じられてから EvEnded を配送するまでの短い間は閉じたパイプを
// 指しうるが、MediaPipe.Send は Close 後にエラーを返すだけなので害は無い
// (従来も同じ窓でパケットは捨てられていた)。
type mediaRoute struct {
	winner  string         // 勝者デバイス (空にはならない。勝者不在なら route 自体が nil)
	pipe    call.MediaPipe // 上り送信先 (未確保なら nil)
	targets []*Conn        // 下り配信先 = 勝者デバイスの接続 (読み取り専用)
	stats   *callRecord    // 品質計測 (メディア開始前は nil。書き込みは atomic のみ)
}

// refreshRouteLocked は route を現在の状態から作り直す (呼び出し側が mu 保持)。
// Manager.mu を取るが、Manager は group を呼ばないのでロック順序
// group.mu → Manager.mu は安全 (detach も同じ順で取る)。
func (g *group) refreshRouteLocked() {
	if g.stopped || g.winnerDevice == "" {
		g.route.Store(nil)
		return
	}
	r := &mediaRoute{winner: g.winnerDevice, stats: g.curRec}
	if g.mgr != nil {
		r.pipe = g.mgr.Pipe()
	}
	if set := g.devs[g.winnerDevice]; len(set) > 0 {
		r.targets = make([]*Conn, 0, len(set))
		for c := range set {
			r.targets = append(r.targets, c)
		}
	}
	g.route.Store(r)
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
	mgr.Log = g.log
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
	oldWinner := g.winnerDevice
	g.winnerDevice = ""
	g.dialerDevice = ""
	g.pumpPipe = nil
	oldRec := g.curRec
	g.curRec = nil
	if g.resumeTimer != nil {
		g.resumeTimer.Stop()
		g.resumeTimer = nil
	}
	g.refreshRouteLocked()
	g.mu.Unlock()
	g.hub.stats.end(oldRec, oldWinner) // 作り直しで打ち切られた通話 (通常は nil)
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
	rec, winner := g.curRec, g.winnerDevice
	g.curRec = nil
	g.refreshRouteLocked()
	g.mu.Unlock()
	g.hub.stats.end(rec, winner)
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
	g.refreshRouteLocked()
	g.mu.Unlock()
}

// setDialer は発信要求元を記録する (発信元が勝者になる)。
func (g *group) setDialer(deviceID string) {
	g.mu.Lock()
	g.winnerDevice = deviceID
	g.dialerDevice = deviceID
	g.refreshRouteLocked()
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
	if c.deviceID == g.winnerDevice {
		g.refreshRouteLocked() // 勝者の再接続・重複接続を下りの配信先に加える
	}
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
	if c.deviceID == g.winnerDevice {
		g.refreshRouteLocked() // 切断済み接続を下りの配信先から外す
	}
	if g.stopped || g.winnerDevice == "" || g.winnerDevice != c.deviceID {
		return
	}
	if set, ok := g.devs[c.deviceID]; ok && len(set) > 0 {
		return // 同一デバイスの別接続が残っている
	}
	if g.mgr != nil {
		if cur := g.mgr.Current(); cur != nil {
			g.armResumeLocked(cur.CallID)
		}
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
// タイマは張った時点の呼 (callID) にだけ効く。猶予中にその呼が終わり
// 次の呼が始まっても、次の呼を切らない。
func (g *group) armResumeLocked(callID string) {
	if g.resumeTimer != nil {
		g.resumeTimer.Stop()
	}
	timeout := g.hub.cfg.ResumeTimeout
	mgr := g.mgr
	winner := g.winnerDevice
	g.log.Info("勝者切断、猶予後に BYE", "timeout", timeout, "callId", callID)
	g.resumeTimer = time.AfterFunc(timeout, func() {
		g.mu.Lock()
		if _, ok := g.devs[winner]; ok || g.stopped {
			g.mu.Unlock()
			return // 再接続済み / 停止済み
		}
		g.mu.Unlock()
		g.log.Info("猶予超過のため切断", "callId", callID)
		mgr.HangupTimeoutIf(callID)
	})
}

// stopResumeLocked は猶予タイマを止める (呼び出し側が mu 保持)。
func (g *group) stopResumeLocked() {
	if g.resumeTimer != nil {
		g.resumeTimer.Stop()
		g.resumeTimer = nil
	}
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
			g.beginStats(mgr)
			g.startMediaPump(mgr) // 早期メディア (183)
		}
	case call.EvAnswered:
		g.onAnswered(mgr, e)
	case call.EvEnded:
		g.mu.Lock()
		g.stopResumeLocked() // 呼が終わったら猶予タイマは不要 (次の呼を切らせない)
		winner := g.winnerDevice
		var rec *callRecord
		if g.curRec != nil && g.curRec.callID == e.CallID {
			rec = g.curRec
			g.curRec = nil
		}
		g.winnerDevice = ""
		g.dialerDevice = ""
		g.pumpPipe = nil
		g.refreshRouteLocked()
		g.mu.Unlock()
		g.hub.stats.end(rec, winner)
		g.broadcast(&proto.Ended{
			T: proto.TEnded, CallID: e.CallID, Reason: e.Reason, Code: e.Code,
		})
	}
}

// onAnswered は answer 確定時の配送である。着信 (direction=in) では勝者以外に
// ended{reason:answered_elsewhere} を送り、メディアは勝者のみにする。
func (g *group) onAnswered(mgr *call.Manager, e call.EvAnswered) {
	g.beginStats(mgr) // answered を送る前 (= アプリが上り RTP を送り始める前) に計測を始める
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

// writeAll はロックを解放した状態で各接続の送信キューへ積む。
// ここで実送信まで待つと、詰まった 1 台が writeTimeout 分だけ
// 他の端末への配信とイベントポンプを止めてしまう。
func writeAll(targets []*Conn, typ websocket.MessageType, data []byte) {
	for _, c := range targets {
		c.enqueue(typ, data)
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

// beginStats は通話の品質計測の記録を作り、上りの計測を route に載せる。
// answered/早期メディアをアプリへ送る前に呼ぶ (アプリが送り始める最初の
// 上り RTP から数えるため)。
func (g *group) beginStats(mgr *call.Manager) {
	pipe := mgr.Pipe()
	if pipe == nil {
		return
	}
	g.mu.Lock()
	if g.mgr == mgr {
		g.beginStatsLocked(mgr, pipe)
		g.refreshRouteLocked()
	}
	g.mu.Unlock()
}

// beginStatsLocked は呼び出し側が mu 保持。183→200 で同じ呼なら同じ記録に
// パイプを足す。stats.mu は葉のロックなので group.mu 保持下で取ってよい
// (Manager.mu も group.mu → Manager.mu の順で安全)。
func (g *group) beginStatsLocked(mgr *call.Manager, pipe call.MediaPipe) {
	cur := mgr.Current()
	if cur == nil {
		return
	}
	g.curRec = g.hub.stats.begin(g.account, cur.CallID, g.winnerDevice, pipe)
}

// startMediaPump はバックエンド→勝者への RTP 転送を開始する。
// 同じパイプに対しては 1 本だけ起動する (183 と 200 で二重起動しない)。
// 200 で早期メディアと別のパイプが渡された場合は新しいパイプ用に起動し直す
// (古いパイプは Manager が閉じるので古いポンプは自然に終わる)。
func (g *group) startMediaPump(mgr *call.Manager) {
	pipe := mgr.Pipe()
	if pipe == nil {
		return
	}
	g.mu.Lock()
	if g.mgr != mgr || g.pumpPipe == pipe {
		g.mu.Unlock()
		return // 作り直された古い Manager / 起動済み
	}
	g.pumpPipe = pipe
	g.beginStatsLocked(mgr, pipe)
	g.refreshRouteLocked() // 上りの送信先パイプを確定させる
	g.mu.Unlock()
	go func() {
		for pkt := range pipe.Recv() {
			// 勝者不在 (resume 猶予中・通話終了後) でもパイプは読み続け、破棄する。
			// 配信先はパケットごとに route を読み直すので、勝者の再接続・
			// 重複接続の置換にも追従する。
			// 別のパイプ (前の呼の残り・183→200 で差し替えられた早期メディア) の
			// パケットは今の勝者へ流さない。
			r := g.route.Load()
			if r == nil || r.pipe != pipe {
				continue
			}
			var dropped uint64
			for _, c := range r.targets {
				if !c.enqueue(websocket.MessageBinary, pkt) {
					dropped++
				}
			}
			if dropped > 0 && r.stats != nil {
				r.stats.txDrop.Add(dropped)
			}
		}
	}()
}

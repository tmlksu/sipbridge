// Package sipbackend の RTP パイプ (UDP)。
package sipbackend

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"

	"github.com/tmlksu/sipbridge/relay/internal/call"
	"github.com/tmlksu/sipbridge/relay/internal/rtpstats"
)

// recvQueueLen は MediaPipe.Recv のバッファ長である。
// fakebackend と同じ 50 とし、溢れたら古い方を捨てる。
const recvQueueLen = 50

// recvSlabSize は受信パケットを切り出す塊の大きさである。
// G.711 20ms (約 172 バイト) なら 1 塊で約 95 パケット、確保は 2 秒に 1 回程度になる。
const recvSlabSize = 16 << 10

// maxDatagram は 1 回の受信で読む上限である。
const maxDatagram = 2048

// minRTPHeader は RTP 固定ヘッダの長さである。
const minRTPHeader = 12

// rtpPipe は UDP ソケット上の RTP 送受パイプである。
// call.MediaPipe を満たす。RTCP と見られるパケットは捨てる。
// Close は冪等である。
//
// 受信は送信元 IP を「現在の SDP c= の IP」∪「SIP の信頼集合 (trusted)」と照合し、
// それ以外を捨てる。ポートは見ない (NAT・rtp_symmetric・再ネゴシエーションで
// ずれるため)。同居構成では c=127.0.0.1 に対し実送信元が LOCAL_IP になるので、
// c= だけで照合すると全音声が落ちる。宛先が未確定 (remote nil) なら全て捨てる。
// 受信元へのラッチ (送信先の書き換え) はしない。
type rtpPipe struct {
	conn *net.UDPConn

	mu       sync.RWMutex
	remote   *net.UDPAddr
	remoteIP netip.Addr // remote の IP (比較用に正規化済み)

	// trusted は SIP の信頼集合の照合関数である (nil なら c= のみで照合)。
	trusted func(netip.Addr) bool

	ch        chan []byte
	closed    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup

	// log は破棄の警告先である (未設定なら slog.Default)。
	log     *slog.Logger
	dropped atomic.Uint64
	// rejected は送信元・形式の検査で捨てたパケット数である。
	rejected atomic.Uint64
	// stats は Asterisk→relay 区間の受信統計 (readLoop だけが書く。atomic)。
	stats rtpstats.Stats
}

var _ call.MediaPipe = (*rtpPipe)(nil)

// newRTPPipe はソケットと初期宛先からパイプを作り、受信ループを開始する。
// trusted は SIP の信頼集合の照合関数 (nil 可) である。
func newRTPPipe(conn *net.UDPConn, remote *net.UDPAddr, log *slog.Logger, trusted func(netip.Addr) bool) *rtpPipe {
	p := &rtpPipe{
		conn:    conn,
		ch:      make(chan []byte, recvQueueLen),
		closed:  make(chan struct{}),
		log:     log,
		trusted: trusted,
	}
	p.setRemote(remote)
	p.wg.Add(1)
	go p.readLoop()
	return p
}

// DroppedPackets は受信キュー溢れで捨てたパケット数である。
func (p *rtpPipe) DroppedPackets() uint64 { return p.dropped.Load() }

// RTPStats は Asterisk から受信した RTP の統計である (docs/QUALITY_STATS.md)。
func (p *rtpPipe) RTPStats() rtpstats.Snapshot { return p.stats.Snapshot() }

func (p *rtpPipe) logger() *slog.Logger {
	if p.log != nil {
		return p.log
	}
	return slog.Default()
}

// RejectedPackets は送信元・形式の検査で捨てたパケット数である。
func (p *rtpPipe) RejectedPackets() uint64 { return p.rejected.Load() }

// setRemote は送信宛先を更新する (re-INVITE 追従用)。受信の送信元照合も追従する。
func (p *rtpPipe) setRemote(remote *net.UDPAddr) {
	var ip netip.Addr
	if remote != nil {
		if a, ok := netip.AddrFromSlice(remote.IP); ok {
			ip = normAddr(a)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.remote = remote
	p.remoteIP = ip
}

// acceptSource は送信元 IP からの受信を受け入れるかを返す。
func (p *rtpPipe) acceptSource(src netip.Addr) bool {
	src = normAddr(src)
	p.mu.RLock()
	hasRemote := p.remote != nil
	rip := p.remoteIP
	p.mu.RUnlock()
	if !hasRemote {
		return false // 宛先未確定は fail-closed
	}
	if rip.IsValid() && !rip.IsUnspecified() && src == rip {
		return true
	}
	return p.trusted != nil && p.trusted(src)
}

// reject は検査で捨てたパケットを数え、間引いてログに出す。
func (p *rtpPipe) reject(src netip.Addr, why string) {
	if n := p.rejected.Add(1); n == 1 || n%dropLogEvery == 0 {
		p.logger().Warn("RTP 受信を破棄", "reason", why, "src", src.String(), "rejected", n)
	}
}

// Send は RTP パケットを相手に UDP 送信する。
func (p *rtpPipe) Send(rtp []byte) error {
	select {
	case <-p.closed:
		return fmt.Errorf("RTP パイプは閉じている")
	default:
	}
	p.mu.RLock()
	remote := p.remote
	p.mu.RUnlock()
	if remote == nil {
		return fmt.Errorf("RTP 宛先が未確定")
	}
	// 送信中に Close されても conn が閉じるだけ (エラーで返す)。
	_, err := p.conn.WriteToUDP(rtp, remote)
	return err
}

// Recv は受信パケットのチャネルである。Close で閉じる。
func (p *rtpPipe) Recv() <-chan []byte { return p.ch }

// Close はソケットと受信ループを止め、チャネルを閉じる。
func (p *rtpPipe) Close() error {
	p.closeOnce.Do(func() {
		close(p.closed)
		_ = p.conn.Close()
		p.wg.Wait()
		close(p.ch)
	})
	return nil
}

func (p *rtpPipe) readLoop() {
	defer p.wg.Done()
	buf := make([]byte, maxDatagram)
	// 受信パケットは slab (塊) から重ならないように切り出して渡す。
	// sync.Pool で使い回さないのは、パケットが Recv の先で session の
	// メディアポンプ → 接続ごとの送信キュー (勝者の複数接続へ同じスライスを
	// 共有) → writeLoop へ渡り、キュー溢れ・切断で途中破棄もされるため、
	// 「いつ誰が返却するか」を MediaPipe の境界越しに追えないからである。
	// slab は一度切り出した領域を二度と書かないので、所有権を追う必要が無く
	// GC が最後の参照が消えた時点で回収する。
	var slab []byte
	for {
		n, src, err := p.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			// Close による終了が通常系である。
			return
		}
		if n <= 0 {
			continue
		}
		if !p.acceptSource(src.Addr()) {
			p.reject(src.Addr(), "source")
			continue
		}
		if isRTCP(buf[:n]) {
			continue // v1 では RTCP を扱わない (rtcp_mux 時。数えない)
		}
		if n < minRTPHeader || buf[0]>>6 != 2 {
			p.reject(src.Addr(), "not_rtp")
			continue
		}
		p.stats.Observe(buf[:n])
		if len(slab) < n {
			slab = make([]byte, recvSlabSize)
		}
		// cap を n に絞り、受け手が append しても隣のパケットを壊さないようにする。
		cp := slab[:n:n]
		slab = slab[n:]
		copy(cp, buf[:n])
		select {
		case p.ch <- cp:
		default:
			// 溢れたら古い方を捨てて入れ直す。キューが埋まるのは相手 (WS 側) の
			// 取り出しが遅れているときで、そのまま音切れになる。無言で捨てない。
			select {
			case <-p.ch:
			default:
			}
			// 20ms 間隔なので毎回出すと五月蝿い。50 パケット (約 1 秒) ごとに 1 回。
			if d := p.dropped.Add(1); d%recvQueueLen == 1 {
				p.logger().Warn("RTP 受信キュー溢れ", "dropped", d, "queue", recvQueueLen)
			}
			select {
			case p.ch <- cp:
			case <-p.closed:
				return
			}
		}
		select {
		case <-p.closed:
			return
		default:
		}
	}
}

// isRTCP は RTCP パケットらしいかを判定する。
// RTP の PT は 7 bit (0-127) だが、RTCP のパケット種別 (SR=200, RR=201,
// SDES=202, BYE=203, APP=204) は 200 以上になることを利用する。
func isRTCP(pkt []byte) bool {
	if len(pkt) < 2 {
		return false
	}
	if pkt[0]>>6 != 2 {
		return false // RTP バージョン 2 でも RTCP でも無い
	}
	t := pkt[1]
	return t >= 200 && t <= 204
}

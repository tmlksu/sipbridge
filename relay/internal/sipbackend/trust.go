// Package sipbackend の送信元フィルタ (SIP 要求・RTP)。
//
// relay の SIP/RTP ソケットは全インタフェース ([::] デュアルスタック) で
// 待ち受けるため、LAN 上の任意のホストから偽の INVITE/BYE/RTP を送り込める。
// ここでは「SIP サーバ (SIP_HOST) とみなせる送信元」の IP 集合を持ち、
// それ以外からの SIP 要求を transport 層で捨てる。
//
// これは認証ではない (UDP の送信元は偽装できる)。LAN セグメントを信頼境界と
// したうえで、同一セグメントの別ホストや誤送信を弾くための多層防御である。
package sipbackend

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/sipgo/sip"
)

const (
	// resolveTimeout は SIP_HOST の名前解決の上限である。
	resolveTimeout = 3 * time.Second
	// maxLearned は REGISTER 200 OK の送信元として覚える IP の上限である。
	maxLearned = 8
	// dropLogEvery は破棄ログの間引き間隔 (初回と N 件ごと) である。
	dropLogEvery = 100
)

// ParseTrustedSources は SIP_TRUSTED_SOURCES (CIDR のカンマ区切り) を解析する。
// 単独の IP はホスト 1 個のプレフィックス (/32, /128) とみなす。空要素は無視する。
func ParseTrustedSources(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if strings.Contains(f, "/") {
			p, err := netip.ParsePrefix(f)
			if err != nil {
				return nil, fmt.Errorf("SIP_TRUSTED_SOURCES の %q は CIDR ではない: %w", f, err)
			}
			if p.Addr().Is4In6() {
				bits := p.Bits() - 96
				if bits < 0 {
					return nil, fmt.Errorf("SIP_TRUSTED_SOURCES の %q は範囲が広すぎる", f)
				}
				p = netip.PrefixFrom(p.Addr().Unmap(), bits)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, fmt.Errorf("SIP_TRUSTED_SOURCES の %q は IP/CIDR ではない: %w", f, err)
		}
		a = normAddr(a)
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// normAddr は比較用に IPv4-mapped IPv6 を IPv4 に戻し、zone を除く。
func normAddr(a netip.Addr) netip.Addr {
	return a.Unmap().WithZone("")
}

// addrFromNet は net.Addr から IP を取り出す。
func addrFromNet(a net.Addr) (netip.Addr, bool) {
	switch v := a.(type) {
	case *net.UDPAddr:
		if ip, ok := netip.AddrFromSlice(v.IP); ok {
			return normAddr(ip), true
		}
		return netip.Addr{}, false
	case nil:
		return netip.Addr{}, false
	}
	return addrFromString(a.String())
}

// addrFromString は "ip:port" / "[ip]:port" / "ip" から IP を取り出す。
func addrFromString(s string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return normAddr(ap.Addr()), true
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return normAddr(a), true
	}
	return netip.Addr{}, false
}

// sourceFilter は SIP サーバとみなす送信元 IP の集合である。
// 許可集合 = SIP_HOST を解決した全アドレス
//
//	∪ REGISTER 200 OK の送信元 (学習)
//	∪ (SIP_HOST がループバック/自ホストなら) 127.0.0.1, ::1, 自ホストの全インタフェースアドレス, LOCAL_IP
//
// 同居判定では LOCAL_IP (明示設定時) も自ホストのアドレスとみなす。LOCAL_IP が
// インタフェースに無い構成 (NAT 越しの公開 IP を SDP/Contact に載せる等) で
// SIP_HOST = LOCAL_IP のとき、Asterisk からの INVITE/RTP を落とさないため。
//
//	∪ SIP_TRUSTED_SOURCES
//
// ポートは見ない (Asterisk の送信ポートは待受ポートと一致しないことがある)。
// 更新は登録ループ・トランザクション応答から、参照は transport の読み取り
// ゴルーチンと RTP 受信ループから並行に行われる。
type sourceFilter struct {
	host    string
	trusted []netip.Prefix // 不変
	log     *slog.Logger

	mu       sync.RWMutex
	resolved []netip.Addr
	learned  []netip.Addr
	local    []netip.Addr
	// localIP は LOCAL_IP (明示設定時のみ。無効値なら未設定)。
	localIP netip.Addr

	dropped atomic.Uint64

	// lookup と ifaceAddrs はテストで差し替える。
	lookup     func(ctx context.Context, host string) ([]netip.Addr, error)
	ifaceAddrs func() ([]netip.Addr, error)
}

func newSourceFilter(host string, trusted []netip.Prefix, log *slog.Logger) *sourceFilter {
	if log == nil {
		log = slog.Default()
	}
	f := &sourceFilter{
		host:       host,
		trusted:    append([]netip.Prefix(nil), trusted...),
		log:        log,
		lookup:     lookupHost,
		ifaceAddrs: interfaceAddrs,
	}
	// IP リテラルなら名前解決を待たずに入れておく。
	if a, err := netip.ParseAddr(host); err == nil {
		f.resolved = []netip.Addr{normAddr(a)}
	}
	return f
}

// setLocalIP は LOCAL_IP を同居判定に加える (空・不正値なら何もしない)。
// refresh より前に呼ぶこと。
func (f *sourceFilter) setLocalIP(s string) {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil || !a.IsValid() || a.IsUnspecified() {
		return
	}
	f.mu.Lock()
	f.localIP = normAddr(a)
	f.mu.Unlock()
}

func lookupHost(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// interfaceAddrs は自ホストの全インタフェースアドレス (プレフィックスでなくアドレス) である。
func interfaceAddrs() ([]netip.Addr, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if na, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, normAddr(na))
		}
	}
	return out, nil
}

// refresh は SIP_HOST を再解決し、同居判定をやり直す。
// 解決に失敗したら前回値を保つ。
func (f *sourceFilter) refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	addrs, err := f.lookup(ctx, f.host)
	var resolved []netip.Addr
	for _, a := range addrs {
		a = normAddr(a)
		if a.IsValid() && !containsAddr(resolved, a) {
			resolved = append(resolved, a)
		}
	}
	ifs, ierr := f.ifaceAddrs()

	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil || len(resolved) == 0 {
		f.log.Warn("SIP_HOST の名前解決に失敗。送信元フィルタは前回の解決結果を使う",
			"host", f.host, "err", err, "previous", fmtAddrs(f.resolved))
	} else {
		f.resolved = resolved
	}
	if ierr != nil {
		// インタフェース列挙の失敗は一時的とみなし、同居判定は前回値を保つ。
		f.log.Warn("インタフェースアドレスの取得に失敗", "err", ierr)
		return
	}
	if f.localIP.IsValid() && !containsAddr(ifs, f.localIP) {
		ifs = append(append([]netip.Addr(nil), ifs...), f.localIP)
	}
	colocated := false
	for _, a := range f.resolved {
		if a.IsLoopback() || containsAddr(ifs, a) {
			colocated = true
			break
		}
	}
	if !colocated {
		f.local = nil
		return
	}
	local := []netip.Addr{netip.AddrFrom4([4]byte{127, 0, 0, 1}), netip.IPv6Loopback()}
	for _, a := range ifs {
		if !containsAddr(local, a) {
			local = append(local, a)
		}
	}
	f.local = local
}

// learn は REGISTER 200 OK の送信元 (res.Source()) を許可集合に加える。
func (f *sourceFilter) learn(src string) {
	a, ok := addrFromString(src)
	if !ok || !a.IsValid() || a.IsUnspecified() {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if containsAddr(f.learned, a) {
		return
	}
	f.learned = append(f.learned, a)
	if len(f.learned) > maxLearned {
		f.learned = append([]netip.Addr(nil), f.learned[len(f.learned)-maxLearned:]...)
	}
	f.log.Info("SIP サーバの送信元を学習", "ip", a.String())
}

// Allowed は ip が SIP サーバとみなせる送信元かを返す。
func (f *sourceFilter) Allowed(ip netip.Addr) bool {
	if f == nil {
		return false
	}
	ip = normAddr(ip)
	if !ip.IsValid() {
		return false
	}
	f.mu.RLock()
	ok := containsAddr(f.resolved, ip) || containsAddr(f.learned, ip) || containsAddr(f.local, ip)
	f.mu.RUnlock()
	if ok {
		return true
	}
	for _, p := range f.trusted {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// sipResponsePrefix は SIP 応答の Status-Line の先頭である。
var sipResponsePrefix = []byte("SIP/2.0 ")

// readFilter は sip.TransportReadFilter である (ServeUDP の待受と、sipgo client
// が REGISTER/INVITE 送信に使うエフェメラルソケットの両方に掛かる)。
//
// 応答はそのまま通す (トランザクション照合で保護される)。要求は送信元 IP が
// 許可集合に無ければ捨てる。transport は error を返すと読み取りループを
// 終了してしまうため、破棄は必ず (nil, nil) で返す。
func (f *sourceFilter) readFilter(info sip.TransportReadProps, data []byte) ([]byte, error) {
	if bytes.HasPrefix(bytes.TrimLeft(data, "\r\n"), sipResponsePrefix) {
		return data, nil
	}
	ip, ok := addrFromNet(info.RemoteAddr)
	if ok && f.Allowed(ip) {
		return data, nil
	}
	n := f.dropped.Add(1)
	if n == 1 || n%dropLogEvery == 0 {
		src := ""
		if ok {
			src = ip.String()
		} else if info.RemoteAddr != nil {
			src = info.RemoteAddr.String()
		}
		f.log.Warn("許可外の送信元からの SIP 要求を破棄",
			"src", src, "dropped", n, "sipHost", f.host,
			"hint", "正規の SIP サーバなら SIP_TRUSTED_SOURCES に追加")
	}
	return nil, nil
}

// Dropped は破棄した SIP 要求の数である。
func (f *sourceFilter) Dropped() uint64 { return f.dropped.Load() }

func containsAddr(list []netip.Addr, a netip.Addr) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}

func fmtAddrs(list []netip.Addr) string {
	s := make([]string, len(list))
	for i, a := range list {
		s[i] = a.String()
	}
	return strings.Join(s, ",")
}

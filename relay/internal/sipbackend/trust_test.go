// Package sipbackend の送信元フィルタのテスト (#28)。
package sipbackend

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"

	"github.com/emiago/sipgo/sip"
)

func mustAddrs(ss ...string) []netip.Addr {
	out := make([]netip.Addr, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParseAddr(s)
	}
	return out
}

// newTestFilter はテスト用のフィルタを作る。名前解決とインタフェース列挙は差し替える。
func newTestFilter(t *testing.T, host string, trusted string, dns map[string][]netip.Addr, ifs []netip.Addr) *sourceFilter {
	t.Helper()
	pfx, err := ParseTrustedSources(trusted)
	if err != nil {
		t.Fatal(err)
	}
	f := newSourceFilter(host, pfx, nil)
	f.lookup = func(ctx context.Context, h string) ([]netip.Addr, error) {
		if a, err := netip.ParseAddr(h); err == nil {
			return []netip.Addr{a}, nil
		}
		if v, ok := dns[h]; ok {
			return v, nil
		}
		return nil, errors.New("NXDOMAIN")
	}
	f.ifaceAddrs = func() ([]netip.Addr, error) { return ifs, nil }
	f.refresh(context.Background())
	return f
}

func udpFrom(s string) net.Addr {
	ap := netip.MustParseAddrPort(s)
	return net.UDPAddrFromAddrPort(ap)
}

// filtered は readFilter を通し、通過したかを返す。破棄は必ず (nil, nil) であること。
func filtered(t *testing.T, f *sourceFilter, src string, data string) bool {
	t.Helper()
	out, err := f.readFilter(propsFrom(src), []byte(data))
	if err != nil {
		t.Fatalf("readFilter がエラーを返した (読み取りループが止まる): %v", err)
	}
	return len(out) > 0
}

func propsFrom(s string) sip.TransportReadProps {
	return sip.TransportReadProps{Transport: "UDP", RemoteAddr: udpFrom(s)}
}

const testInvite = "INVITE sip:101@127.0.0.1 SIP/2.0\r\nVia: SIP/2.0/UDP 192.0.2.1;branch=z9hG4bKx\r\n\r\n"
const testResponse = "SIP/2.0 200 OK\r\nVia: SIP/2.0/UDP 127.0.0.1;branch=z9hG4bKx\r\n\r\n"

func TestParseTrustedSources(t *testing.T) {
	p, err := ParseTrustedSources(" 172.17.0.0/16, 10.1.2.3 ,, fd00::/8, ::ffff:192.0.2.0/120, 2001:db8::1 ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"172.17.0.0/16", "10.1.2.3/32", "fd00::/8", "192.0.2.0/24", "2001:db8::1/128"}
	if len(p) != len(want) {
		t.Fatalf("%v", p)
	}
	for i, w := range want {
		if p[i].String() != w {
			t.Errorf("[%d] = %s (期待 %s)", i, p[i], w)
		}
	}
	if p, err := ParseTrustedSources(""); err != nil || len(p) != 0 {
		t.Errorf("空: %v %v", p, err)
	}
	for _, bad := range []string{"172.17.0.0/33", "example.com", "10.0.0.1/", "::ffff:0:0/90"} {
		if _, err := ParseTrustedSources(bad); err == nil {
			t.Errorf("%q が受理された", bad)
		}
	}
}

func TestSourceFilterTable(t *testing.T) {
	ifs := mustAddrs("127.0.0.1", "::1", "192.168.1.5", "fe80::1")
	dns := map[string][]netip.Addr{
		"pbx.example": mustAddrs("192.0.2.20", "2001:db8::20"),
		"localhost":   mustAddrs("127.0.0.1", "::1"),
	}
	type tc struct {
		name  string
		src   string
		data  string
		allow bool
	}
	cases := []struct {
		host    string
		trusted string
		cases   []tc
	}{
		{host: "192.0.2.10", trusted: "172.17.0.0/16", cases: []tc{
			{"SIP_HOST", "192.0.2.10:5060", testInvite, true},
			{"SIP_HOST 別ポート", "192.0.2.10:40000", testInvite, true},
			{"SIP_HOST の ::ffff: 形式", "[::ffff:192.0.2.10]:5060", testInvite, true},
			{"LAN の別ホスト", "192.0.2.11:5060", testInvite, false},
			{"LAN の別ホスト (::ffff:)", "[::ffff:192.0.2.11]:5060", testInvite, false},
			{"非同居でループバック", "127.0.0.1:5060", testInvite, false},
			{"非同居で自ホスト", "192.168.1.5:5060", testInvite, false},
			{"SIP_TRUSTED_SOURCES", "172.17.0.3:5060", testInvite, true},
			{"SIP_TRUSTED_SOURCES (::ffff:)", "[::ffff:172.17.0.3]:5060", testInvite, true},
			{"応答は通す", "198.51.100.1:5060", testResponse, true},
			{"先頭 CRLF 付き応答", "198.51.100.1:5060", "\r\n" + testResponse, true},
			{"許可外のキープアライブ", "198.51.100.1:5060", "\r\n\r\n", false},
			{"許可外の CANCEL", "198.51.100.1:5060", "CANCEL sip:101@x SIP/2.0\r\n\r\n", false},
		}},
		{host: "pbx.example", cases: []tc{
			{"DNS の A", "192.0.2.20:5060", testInvite, true},
			{"DNS の AAAA", "[2001:db8::20]:5060", testInvite, true},
			{"別ホスト", "192.0.2.21:5060", testInvite, false},
		}},
		{host: "127.0.0.1", cases: []tc{
			{"同居: 127.0.0.1", "127.0.0.1:5060", testInvite, true},
			{"同居: ::1", "[::1]:5060", testInvite, true},
			{"同居: LAN の自アドレス", "192.168.1.5:5060", testInvite, true},
			{"同居: 127.0.0.2 は自アドレスでない (完全一致)", "127.0.0.2:5060", testInvite, false},
			{"同居: LAN の別ホスト", "192.168.1.6:5060", testInvite, false},
		}},
		{host: "192.168.1.5", cases: []tc{
			{"自ホストの LAN アドレスを SIP_HOST に: ループバック", "127.0.0.1:5060", testInvite, true},
			{"自ホストの LAN アドレスを SIP_HOST に: 本人", "192.168.1.5:5060", testInvite, true},
			{"自ホストの LAN アドレスを SIP_HOST に: 別ホスト", "192.168.1.6:5060", testInvite, false},
		}},
		{host: "localhost", cases: []tc{
			{"localhost: 127.0.0.1", "127.0.0.1:5060", testInvite, true},
			{"localhost: 自 LAN", "192.168.1.5:5060", testInvite, true},
			{"localhost: 127.0.0.2", "127.0.0.2:5060", testInvite, false},
		}},
	}
	for _, g := range cases {
		f := newTestFilter(t, g.host, g.trusted, dns, ifs)
		for _, c := range g.cases {
			if got := filtered(t, f, c.src, c.data); got != c.allow {
				t.Errorf("host=%s %s (%s): 通過=%v (期待 %v)", g.host, c.name, c.src, got, c.allow)
			}
		}
	}
}

func TestSourceFilterLearnAndResolveFailure(t *testing.T) {
	dns := map[string][]netip.Addr{"pbx.example": mustAddrs("192.0.2.20")}
	f := newTestFilter(t, "pbx.example", "", dns, mustAddrs("127.0.0.1", "192.168.1.5"))
	if filtered(t, f, "203.0.113.7:5060", testInvite) {
		t.Fatal("学習前に通過した")
	}
	// REGISTER 200 OK の送信元を学習する。
	f.learn("203.0.113.7:5060")
	f.learn("[::ffff:203.0.113.8]:5060")
	if !filtered(t, f, "203.0.113.7:6000", testInvite) || !filtered(t, f, "203.0.113.8:5060", testInvite) {
		t.Error("学習した送信元が通らない")
	}
	// 名前解決に失敗しても前回値を保つ。
	delete(dns, "pbx.example")
	f.refresh(context.Background())
	if !filtered(t, f, "192.0.2.20:5060", testInvite) {
		t.Error("解決失敗で前回値が消えた")
	}
	// 解決結果が変われば追従する。
	dns["pbx.example"] = mustAddrs("192.0.2.30")
	f.refresh(context.Background())
	if filtered(t, f, "192.0.2.20:5060", testInvite) || !filtered(t, f, "192.0.2.30:5060", testInvite) {
		t.Error("再解決に追従しない")
	}
	// 学習の上限。
	for i := 0; i < 20; i++ {
		f.learn(netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}).String() + ":5060")
	}
	f.mu.RLock()
	n := len(f.learned)
	f.mu.RUnlock()
	if n != maxLearned {
		t.Errorf("学習数 %d (上限 %d)", n, maxLearned)
	}
	if f.Dropped() == 0 {
		t.Error("破棄カウンタが増えない")
	}
}

// TestSourceFilterConcurrent は更新 (refresh/learn) と参照 (readFilter) の並行実行を
// -race で確かめる。
func TestSourceFilterConcurrent(t *testing.T) {
	dns := map[string][]netip.Addr{"pbx.example": mustAddrs("192.0.2.20")}
	f := newTestFilter(t, "pbx.example", "10.0.0.0/8", dns, mustAddrs("127.0.0.1"))
	var dnsMu sync.Mutex
	f.lookup = func(ctx context.Context, h string) ([]netip.Addr, error) {
		dnsMu.Lock()
		defer dnsMu.Unlock()
		return dns[h], nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				switch i {
				case 0:
					f.refresh(context.Background())
				case 1:
					f.learn(netip.AddrFrom4([4]byte{203, 0, 113, byte(j)}).String() + ":5060")
				default:
					_, _ = f.readFilter(propsFrom("192.0.2.20:5060"), []byte(testInvite))
					_ = f.Allowed(netip.MustParseAddr("10.1.1.1"))
				}
			}
		}(i)
	}
	wg.Wait()
}

// TestSourceFilterLocalIP は LOCAL_IP (明示設定) を同居判定で自ホストの
// アドレスとみなすことを確認する。LOCAL_IP がインタフェースに無い構成
// (NAT 越しの公開 IP) で SIP_HOST = LOCAL_IP でも Asterisk からの要求を通す。
func TestSourceFilterLocalIP(t *testing.T) {
	ifs := mustAddrs("127.0.0.1", "::1", "192.168.1.5")
	mk := func(host, localIP string) *sourceFilter {
		f := newSourceFilter(host, nil, nil)
		f.setLocalIP(localIP)
		f.lookup = func(ctx context.Context, h string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(h)}, nil
		}
		f.ifaceAddrs = func() ([]netip.Addr, error) { return ifs, nil }
		f.refresh(context.Background())
		return f
	}
	// SIP_HOST = LOCAL_IP (インタフェースに無い) → 同居扱い。
	f := mk("203.0.113.9", "203.0.113.9")
	for _, src := range []string{"203.0.113.9:5060", "127.0.0.1:5060", "192.168.1.5:5060"} {
		if !filtered(t, f, src, testInvite) {
			t.Errorf("SIP_HOST=LOCAL_IP: %s が通らない", src)
		}
	}
	if filtered(t, f, "192.168.1.6:5060", testInvite) {
		t.Errorf("SIP_HOST=LOCAL_IP: LAN の別ホストが通った")
	}
	// ループバックで同居 → LOCAL_IP も信頼集合に入る。
	f = mk("127.0.0.1", "203.0.113.9")
	if !filtered(t, f, "203.0.113.9:40000", testInvite) {
		t.Errorf("同居時に LOCAL_IP からの要求が通らない")
	}
	// 非同居なら LOCAL_IP は信頼しない。
	f = mk("192.0.2.10", "203.0.113.9")
	if filtered(t, f, "203.0.113.9:5060", testInvite) {
		t.Errorf("非同居なのに LOCAL_IP からの要求が通った")
	}
	// 空・不正値は無視する。
	for _, v := range []string{"", "not-an-ip", "0.0.0.0"} {
		f = mk("192.0.2.10", v)
		if f.localIP.IsValid() {
			t.Errorf("LOCAL_IP %q が設定された", v)
		}
	}
}

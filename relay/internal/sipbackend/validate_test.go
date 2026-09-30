// Package sipbackend の入力検証テスト (#32)。
package sipbackend

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tmlksu/sipbridge/relay/internal/call"
)

func TestNormalizeDialTarget(t *testing.T) {
	ok := []struct{ in, want string }{
		{"102", "102"},
		{"*43", "*43"},
		{"+81312345678", "+81312345678"},
		{"%2B81312345678", "+81312345678"},
		{"*67%23", "*67#"},
		{"*67#", "*67#"},
		{"alice", "alice"},
		{"alice.b", "alice.b"},
		{"bob-2_x", "bob-2_x"},
		{"03-1234-5678", "0312345678"},
		{"(03) 1234 5678", "0312345678"},
		{"  102  ", "102"},
		{"102\n", "102"}, // 前後の空白 (改行含む) は trim で落ちる
		{"\t102", "102"},
		{"090.1234.5678", "09012345678"},
		{"+81 3-1234-5678", "+81312345678"},
		// 全角 (連絡帳・IME 入力) は ASCII に畳む。
		{"０３－１２３４－５６７８", "0312345678"},
		{"＋８１　３　１２３４　５６７８", "+81312345678"},
		{"＊６７＃", "*67#"},
		{"（０３）１２３４‐５６７８", "0312345678"},
		{"090ー1234ー5678", "09012345678"},
		{"03–1234—5678", "0312345678"},
		{"03−1234―5678", "0312345678"},
		{"　１０２　", "102"},
		{"１０２", "102"},                         // 以前は非 ASCII として拒否していた
		{"%EF%BC%91%EF%BC%90%EF%BC%92", "102"}, // %エンコードされた全角 "１０２"
	}
	for _, c := range ok {
		got, err := normalizeDialTarget(c.in)
		if err != nil {
			t.Errorf("%q: 受理されるはずが拒否: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: %q (期待 %q)", c.in, got, c.want)
		}
	}
	bad := []string{
		"",
		"   ",
		"102\r\nX-Evil: 1",
		"102%0D%0AX-Evil:%201",
		"10\n2",
		"10\t2",
		"10\r2",
		"102%0A", // 復号後の改行は trim しない
		"alice@evil.example",
		"102>",
		"102;maddr=198.51.100.1",
		"102%26x",
		"102&x",
		"102%",     // 不正なエスケープ
		"102%zz",   // 不正なエスケープ
		"102%2",    // 不正なエスケープ
		"%2523",    // 二重エスケープ (復号は 1 回だけ → "%23" で拒否)
		"sip:102",  // スキームは受けない
		"102:5060", // ポート指定
		"a,b",
		"<102>",
		`"102"`,
		"102?x=y",
		"10/2",
		"10=2",
		"+81+3",     // + は先頭のみ
		"alice b",   // 英字は区切り記号・空白を除かない
		"alice(b)",  // 同上
		"alice\x00", // 制御文字
		"102\x7f",   // DEL
		strings.Repeat("1", 33),
		strings.Repeat("a", 65),
		"()- .",
	}
	for _, in := range bad {
		if got, err := normalizeDialTarget(in); err == nil {
			t.Errorf("%q: 拒否されるはずが受理 (%q)", in, got)
		}
	}
	// 境界: 32 桁・64 文字は受理。
	if _, err := normalizeDialTarget(strings.Repeat("1", 32)); err != nil {
		t.Errorf("32 桁が拒否: %v", err)
	}
	if _, err := normalizeDialTarget("+" + strings.Repeat("1", 32)); err != nil {
		t.Errorf("+ と 32 桁が拒否: %v", err)
	}
	if _, err := normalizeDialTarget(strings.Repeat("a", 64)); err != nil {
		t.Errorf("64 文字が拒否: %v", err)
	}
}

func TestValidateUser(t *testing.T) {
	for _, u := range []string{"101", "2104", "alice", "a.b_c-d", "+81312345678", strings.Repeat("x", 64)} {
		if err := ValidateUser(u); err != nil {
			t.Errorf("%q が拒否: %v", u, err)
		}
	}
	for _, u := range []string{"", "101\r\nX: y", "a@b", "a:b", "a;b", "a b", "a<b", `a"b`, "a%0d", "ｱ", strings.Repeat("x", 65)} {
		if err := ValidateUser(u); err == nil {
			t.Errorf("%q が受理された", u)
		}
	}
}

func TestSanitizeDisplay(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Taro", "Taro"},
		{"山田 太郎", "山田 太郎"},
		{"A\r\nX-Evil: 1", "AX-Evil: 1"},
		{"\tA\x00B\x7f", "AB"},
		{"  A  ", "A"},
		{"A B", "AB"},
		{"bad\xffutf8", "bad�utf8"},
		{`A"B\C`, `A"B\C`}, // エスケープはしない
		// 書式文字 (unicode.Cf) の除去: 双方向制御・ゼロ幅空白・BOM。
		{"abc\u202Egnp.exe", "abcgnp.exe"},
		{"\u2066A\u2069\u200EB\u200F", "AB"},
		{"A\u200BB\uFEFF", "AB"},
		{"\u061C山田", "山田"},
		// ZWJ (絵文字の結合) は残す。
		{"👨\u200D👩", "👨\u200D👩"},
	}
	for _, c := range cases {
		got := SanitizeDisplay(c.in)
		if got != c.want {
			t.Errorf("SanitizeDisplay(%q) = %q (期待 %q)", c.in, got, c.want)
		}
		if again := SanitizeDisplay(got); again != got {
			t.Errorf("SanitizeDisplay が冪等でない: %q → %q", got, again)
		}
	}
	long := strings.Repeat("あ", 100)
	if got := SanitizeDisplay(long); len([]rune(got)) != maxDisplayRunes {
		t.Errorf("切り詰めが効かない: %d 文字", len([]rune(got)))
	}
}

func TestQuoteDisplay(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Taro", "Taro"},
		{`A"B`, `A\"B`},
		{`A\B`, `A\\B`},
		{"A\"\r\nB", `A\"B`},
	}
	for _, c := range cases {
		if got := quoteDisplay(c.in); got != c.want {
			t.Errorf("quoteDisplay(%q) = %q (期待 %q)", c.in, got, c.want)
		}
	}
}

func TestNewRejectsBadUser(t *testing.T) {
	for _, u := range []string{"101\r\nX-Evil: 1", "101@evil", "101>"} {
		_, err := New(Config{
			SIPHost: "127.0.0.1", SIPPort: 5060, User: u,
			RTPPortMin: 36000, RTPPortMax: 36010,
		}, nil)
		if err == nil {
			t.Errorf("User %q が受理された", u)
		}
	}
	be, err := New(Config{
		SIPHost: "127.0.0.1", SIPPort: 5060, User: "101", Display: "A\r\nB",
		RTPPortMin: 36000, RTPPortMax: 36010,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if be.cfg.Display != "AB" {
		t.Errorf("Display が整形されていない: %q", be.cfg.Display)
	}
}

// readSIPUntil は raw ソケットで start から始まる SIP メッセージを待つ。
// 見つからなければ "" を返す。
func readSIPUntil(t *testing.T, c *net.UDPConn, start string, d time.Duration) string {
	t.Helper()
	buf := make([]byte, 8192)
	deadline := time.Now().Add(d)
	for {
		_ = c.SetReadDeadline(deadline)
		n, _, err := c.ReadFromUDP(buf)
		if err != nil {
			return ""
		}
		if msg := string(buf[:n]); strings.HasPrefix(msg, start) {
			return msg
		}
	}
}

// TestDialRejectsInjection は不正な発信先で Dial がエラーを返し INVITE を
// 送らないこと、正規の発信先・表示名は注入なしで送られることを確かめる。
func TestDialRejectsInjection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// SIP サーバ役は応答しない raw ソケット (送出バイト列を見る)。
	srv, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	port := srv.LocalAddr().(*net.UDPAddr).Port
	be, err := New(Config{
		SIPHost: "127.0.0.1", SIPPort: port, User: "101",
		Display:    "A\"B\\C\r\nX-Evil: 1",
		LocalIP:    "127.0.0.1",
		RTPPortMin: 36100, RTPPortMax: 36120,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ev := make(chan call.Event, 32)
	if err := be.Start(ctx, ev); err != nil {
		t.Fatal(err)
	}

	for _, to := range []string{"102\r\nX-Evil: 1", "102%0D%0AX-Evil:%201", "alice@evil", "102>", "102;maddr=198.51.100.1", "102%26", ""} {
		if id, err := be.Dial(to); err == nil {
			t.Errorf("Dial(%q) が受理された (callID %s)", to, id)
		}
	}
	if msg := readSIPUntil(t, srv, "INVITE ", 500*time.Millisecond); msg != "" {
		t.Fatalf("拒否した発信で INVITE が送られた:\n%s", msg)
	}
	be.mu.Lock()
	n := len(be.calls)
	be.mu.Unlock()
	if n != 0 {
		t.Errorf("拒否した発信の通話エントリが残っている (%d)", n)
	}

	if _, err := be.Dial("*67%23"); err != nil {
		t.Fatalf("Dial(*67%%23) 失敗: %v", err)
	}
	msg := readSIPUntil(t, srv, "INVITE ", 5*time.Second)
	if msg == "" {
		t.Fatal("INVITE が来ない")
	}
	head := msg
	if i := strings.Index(msg, "\r\n\r\n"); i >= 0 {
		head = msg[:i]
	}
	lines := strings.Split(head, "\r\n")
	if want := "INVITE sip:*67#@127.0.0.1:"; !strings.HasPrefix(lines[0], want) {
		t.Errorf("Request-Line = %q (期待 %s…)", lines[0], want)
	}
	var from string
	for _, l := range lines[1:] {
		if strings.HasPrefix(strings.ToLower(l), "x-evil") {
			t.Errorf("ヘッダが注入された: %q", l)
		}
		if strings.HasPrefix(l, "From:") {
			from = l
		}
		if strings.HasPrefix(l, "To:") && !strings.Contains(l, "<sip:*67#@127.0.0.1>") {
			t.Errorf("To = %q", l)
		}
	}
	if want := `From: "A\"B\\CX-Evil: 1" <sip:101@127.0.0.1>`; !strings.HasPrefix(from, want) {
		t.Errorf("From = %q (期待 %s…)", from, want)
	}
}

// Package sipbackend の入力検証 (発信先・SIP アカウント)。
//
// sipgo は URI の user 部も From/To の表示名もエスケープせずに書き出す
// (sip/uri.go の StringWrite、headers.go の valueStringWrite)。アプリから
// 来た文字列をそのまま載せると CR/LF でヘッダを注入できるため、SIP
// メッセージを組み立てる前にここで検証・正規化する。
package sipbackend

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 発信先・ユーザ名の許容形式。
var (
	// dialNumericRe は電話番号系の発信先 (区切り記号除去後) である。
	// キーパッドの 0-9 * # と先頭の + のみ。
	dialNumericRe = regexp.MustCompile(`^\+?[0-9*#]{1,32}$`)
	// dialAlphaRe は英字を含む発信先 (SIP の user 名、例 alice) である。
	dialAlphaRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	// userRe は SIP アカウントのユーザ名 (内線番号・認証 ID) である。
	userRe = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
)

// maxDisplayRunes は SIP 表示名の最大文字数である。
const maxDisplayRunes = 64

// normalizeDialTarget は発信先を検証し、Request-URI / To の user 部に
// 載せる文字列を返す。
//
// 規則:
//  1. 前後の空白を除く。
//  2. percent-encoding を 1 回だけ厳格に復号する (不正なエスケープは拒否)。
//     Telecom 経由 (TelecomCompat.numberFrom) では "%2B81…" や "*67%23" で届く。
//  3. 全角の数字・＋＊＃・空白・括弧・ピリオド・ハイフン類 (－ ‐ – — ― − ー 等) を
//     ASCII に畳む (連絡帳の "０３－１２３４－５６７８" 等)。
//  4. 制御文字・非 ASCII を含めば拒否する。
//  5. 英字を含む場合は SIP の user 名とみなし、区切り記号は除かず
//     ^[A-Za-z0-9._-]{1,64}$ に一致することを要求する (alice.b を壊さない)。
//  6. 英字を含まない場合は電話番号とみなし、空白と ( ) - . を除いてから
//     ^\+?[0-9*#]{1,32}$ に一致することを要求する (連絡帳の "03-1234-5678" 等)。
//
// 結果として CR/LF・@ : ; , < > " % & ? / = 等は拒否される。
// # は RFC 3261 上はエスケープ対象だが、Asterisk/HGW の実績に合わせそのまま送る。
func normalizeDialTarget(to string) (string, error) {
	s := strings.TrimSpace(to)
	if s == "" {
		return "", fmt.Errorf("発信先が空")
	}
	dec, err := url.PathUnescape(s)
	if err != nil {
		return "", fmt.Errorf("発信先の %%エスケープが不正")
	}
	// 復号後に trim はしない (%0A などで紛れ込んだ制御文字は下で拒否する)。
	dec = strings.Map(foldDialRune, dec)
	hasAlpha := false
	for i := 0; i < len(dec); i++ {
		c := dec[i]
		if c < 0x20 || c >= 0x7f {
			return "", fmt.Errorf("発信先に制御文字または非 ASCII 文字が含まれる")
		}
		if ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z') {
			hasAlpha = true
		}
	}
	if hasAlpha {
		if !dialAlphaRe.MatchString(dec) {
			return "", fmt.Errorf("発信先の形式が不正 (英字の宛先は A-Z a-z 0-9 . _ - のみ、64 文字以内)")
		}
		return dec, nil
	}
	num := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '(', ')', '-', '.':
			return -1
		}
		return r
	}, dec)
	if !dialNumericRe.MatchString(num) {
		return "", fmt.Errorf("発信先の形式が不正 (番号は 0-9 * # と先頭の + のみ、32 桁以内)")
	}
	return num, nil
}

// foldDialRune は発信先に現れる全角文字を ASCII に畳む (外部依存を増やさない
// ため自前の対応表)。対象外の文字はそのまま返す (後段で非 ASCII として拒否される)。
func foldDialRune(r rune) rune {
	if r >= '０' && r <= '９' {
		return '0' + (r - '０')
	}
	switch r {
	case '＋':
		return '+'
	case '＊':
		return '*'
	case '＃':
		return '#'
	case '\u3000': // 全角空白
		return ' '
	case '（':
		return '('
	case '）':
		return ')'
	case '．':
		return '.'
	case '－', '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212', 'ー', 'ｰ', '\uFE63':
		// 全角ハイフンマイナス、ハイフン類・ダッシュ類、マイナス記号、長音記号 (全角・半角)
		return '-'
	}
	return r
}

// ValidateUser は SIP アカウントのユーザ名 (sip_account.user / SIP_USER) を検証する。
// From/To/Contact の URI user 部と digest の username にそのまま載るため、
// ^[A-Za-z0-9._+-]{1,64}$ 以外は拒否する。
func ValidateUser(user string) error {
	if !userRe.MatchString(user) {
		return fmt.Errorf("SIP ユーザ名の形式が不正 (A-Z a-z 0-9 . _ + - のみ、1-64 文字)")
	}
	return nil
}

// SanitizeDisplay は SIP 表示名 (sip_account.display / SIP_DISPLAY) を
// 保存・表示に使える形に整える。制御文字 (CR/LF/TAB/DEL 等) と書式文字
// (unicode.Cf: U+202E 等の双方向制御、ゼロ幅空白、BOM。ただし絵文字や
// 一部の文字体系で使う ZWJ/ZWNJ は残す) を除き、
// 不正な UTF-8 を置換し、前後の空白を除いて 64 文字に切り詰める。
//
// 冪等である (何度通しても結果は変わらない)。引用符 " とバックスラッシュ \ の
// エスケープはここでは行わず、sipbackend がヘッダを組み立てる直前に
// quoteDisplay で行う (呼び出し側で事前にエスケープすると二重になる)。
func SanitizeDisplay(display string) string {
	s := strings.ToValidUTF8(display, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || (unicode.Is(unicode.Cf, r) && r != '\u200c' && r != '\u200d') || r == ' ' || r == ' ' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxDisplayRunes {
		s = strings.TrimSpace(string([]rune(s)[:maxDisplayRunes]))
	}
	return s
}

// quoteDisplay は sipgo の DisplayName に入れる値を作る。sipgo は
// `"` + DisplayName + `" ` とそのまま書き出すため、quoted-string
// (RFC 3261 25.1) の中身として " と \ を quoted-pair にする。
// 制御文字は SanitizeDisplay で除いておく。
func quoteDisplay(display string) string {
	s := SanitizeDisplay(display)
	if !strings.ContainsAny(s, `"\`) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

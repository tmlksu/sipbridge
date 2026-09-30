// Package config は relay の環境変数読み込みと検証を行う。
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultStateFile は STATE_FILE / PUSH_STATE_FILE 未設定時の既定パスである。
const DefaultStateFile = "/var/lib/sipbridge/state.json"

// Config は relay 全体の設定を保持する。
type Config struct {
	Listen   string // 例 "127.0.0.1:8080"
	AuthMode string // "cf-access" | "token"
	// CFTeamDomain は正規化済み (スキーム・末尾 / 無し、小文字) のチームドメイン
	// (例 "example.cloudflareaccess.com")。JWKS 取得先と JWT の iss に使う。
	CFTeamDomain string
	CFAccessAUD  string // Access アプリケーションの AUD タグ
	DevToken     string // AUTH_MODE=token 時の共有トークン
	// AllowInsecureListen は AUTH_MODE=token で LISTEN を loopback 以外に
	// 開くことを明示的に許す (ALLOW_INSECURE_LISTEN=1)。審査用 relay 等で
	// 前段に別の TLS 終端がある場合だけ使う。
	AllowInsecureListen bool
	// DeviceBinding は X-Device-Id と認証主体 (principal) の TOFU 結び付けの
	// 扱いである (DEVICE_BINDING=off|warn|enforce、既定 warn)。
	DeviceBinding string
	// MaxAccounts は同時に扱う SIP アカウント数の上限 (MAX_ACCOUNTS、既定 16)。
	// 新規作成時のみ適用し、状態ファイルからの起動時読み込みは超過でも読む。
	MaxAccounts int
	// MaxStoredDevices は状態ファイルに保存する端末数の上限
	// (MAX_STORED_DEVICES、既定 64)。新規保存時のみ適用する。
	MaxStoredDevices int
	// MaxOnlineDevices は同時に WS 接続できる端末 (deviceID) 数の上限
	// (MAX_ONLINE_DEVICES、既定 32)。接続中・保存済みの端末は数えても拒否しない。
	MaxOnlineDevices int
	// SIPHost/SIPPort は REGISTER/INVITE の宛先となる SIP サーバである
	// (SIP_HOST/SIP_PORT。旧名 ASTERISK_HOST/ASTERISK_PORT も読む)。
	// Asterisk に限らず、任意の SIP レジストラを指定できる
	// (例: ひかり電話 HGW の LAN 側 IP)。
	SIPHost string
	SIPPort int
	// SIPUser/SIPPassword/SIPDisplay は任意の「既定アカウント」である (v1.1)。
	// 設定されていれば、結び付けの無い端末を接続時に暫定的にこの account へ
	// 結び付ける (永続化はしない)。空なら端末が sip_account で登録する。
	SIPUser     string
	SIPPassword string
	SIPDisplay  string
	LocalIP     string // SDP/Contact に載せる自 IP。空なら自動検出
	RTPPortMin  int
	RTPPortMax  int
	Backend     string // "fake" | "sip"
	LogLevel    string // "debug" | "info" | "warn" | "error"
	// ResumeTimeoutSec は通話中に WS が切れてから BYE するまでの猶予 (秒) である。
	// アプリ側の再接続 (バックオフ + Access/Tunnel のハンドシェイク) が収まる値にする。
	ResumeTimeoutSec int
	// WSPingInterval は relay→app の WS ping 周期である (WS_PING_INTERVAL、既定 20s)。
	// 待機コスト計測 (docs/QUALITY_STATS.md E1) 用に変えられるようにしている。
	// Cloudflare の WS アイドル切断 (~100 秒) より短くすること。
	WSPingInterval time.Duration
	// StateFile はアカウント/端末結び付け/push トークンの永続化先である
	// (STATE_FILE。旧名 PUSH_STATE_FILE も読む)。
	StateFile string
	// FCM 設定 (push 送信用。どちらか空なら push は no-op)
	FCMProjectID          string
	FCMServiceAccountFile string

	// SIPTrustedSources は SIP_HOST 以外に SIP 要求・RTP を受け入れる送信元
	// (CIDR/IP のカンマ区切り、SIP_TRUSTED_SOURCES、既定は空)。解析と検証は
	// main で sipbackend.ParseTrustedSources により起動時に行う。
	SIPTrustedSources string
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// FromEnv は環境変数から Config を組み立てる。
func FromEnv() (Config, error) {
	c := Config{
		Listen:                getenv("LISTEN", "127.0.0.1:8080"),
		AuthMode:              getenv("AUTH_MODE", "token"),
		CFTeamDomain:          NormalizeTeamDomain(getenv("CF_TEAM_DOMAIN", "")),
		CFAccessAUD:           getenv("CF_ACCESS_AUD", ""),
		DevToken:              getenv("DEV_TOKEN", ""),
		SIPHost:               sipHost(),
		SIPUser:               getenv("SIP_USER", ""),
		SIPPassword:           getenv("SIP_PASSWORD", ""),
		SIPDisplay:            getenv("SIP_DISPLAY", ""),
		LocalIP:               getenv("LOCAL_IP", ""),
		Backend:               getenv("BACKEND", "fake"),
		LogLevel:              strings.ToLower(getenv("LOG_LEVEL", "info")),
		StateFile:             stateFile(),
		FCMProjectID:          getenv("FCM_PROJECT_ID", ""),
		FCMServiceAccountFile: getenv("FCM_SERVICE_ACCOUNT_FILE", ""),
		DeviceBinding:         strings.ToLower(getenv("DEVICE_BINDING", "warn")),
		SIPTrustedSources:     getenv("SIP_TRUSTED_SOURCES", ""),
	}
	var err error
	if c.AllowInsecureListen, err = boolEnv("ALLOW_INSECURE_LISTEN"); err != nil {
		return Config{}, err
	}
	if c.MaxAccounts, err = atoiEnv("MAX_ACCOUNTS", 16); err != nil {
		return Config{}, err
	}
	if c.MaxStoredDevices, err = atoiEnv("MAX_STORED_DEVICES", 64); err != nil {
		return Config{}, err
	}
	if c.MaxOnlineDevices, err = atoiEnv("MAX_ONLINE_DEVICES", 32); err != nil {
		return Config{}, err
	}
	if c.SIPPort, err = sipPort(); err != nil {
		return Config{}, err
	}
	if c.RTPPortMin, err = atoiEnv("RTP_PORT_MIN", 20000); err != nil {
		return Config{}, err
	}
	if c.RTPPortMax, err = atoiEnv("RTP_PORT_MAX", 20100); err != nil {
		return Config{}, err
	}
	if c.ResumeTimeoutSec, err = atoiEnv("RESUME_TIMEOUT_SEC", 30); err != nil {
		return Config{}, err
	}
	if c.WSPingInterval, err = durationEnv("WS_PING_INTERVAL", 20*time.Second); err != nil {
		return Config{}, err
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// NormalizeTeamDomain は CF_TEAM_DOMAIN を正規化する: 前後の空白、
// "https://" / "http://" と末尾の "/" を除き、小文字にする。
// 例 "https://Example.CloudflareAccess.com/" → "example.cloudflareaccess.com"。
func NormalizeTeamDomain(s string) string {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	for _, p := range []string{"https://", "http://"} {
		if strings.HasPrefix(lower, p) {
			s = s[len(p):]
			break
		}
	}
	s = strings.TrimRight(s, "/")
	return strings.ToLower(s)
}

// boolEnv は 1/true/yes/on を true、空/0/false/no/off を false として読む。
func boolEnv(key string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	default:
		return false, fmt.Errorf("%s の値 %q は 1|0 のいずれか", key, os.Getenv(key))
	}
}

// IsLoopbackListen は LISTEN のホスト部が loopback (127.0.0.0/8, ::1, localhost)
// かを返す。ホスト部が空 (":8080") やワイルドカードは全インタフェースなので false。
func IsLoopbackListen(listen string) (bool, error) {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false, fmt.Errorf("LISTEN %q の解析失敗: %w", listen, err)
	}
	if strings.EqualFold(host, "localhost") {
		return true, nil
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback(), nil
}

// Warnings は起動を止めるほどではないが運用上危険な設定の警告を返す。
func (c Config) Warnings() []string {
	var w []string
	if c.AuthMode == "cf-access" && c.SIPUser != "" {
		w = append(w, "既定アカウント (SIP_USER) が設定されている: 漏洩した Access Service Token だけで "+
			"sip_account 無しに既定内線 "+c.SIPUser+" で発信できる。端末ごとに sip_account で設定し SIP_USER は空にすることを推奨 (docs/SECURITY.md)")
	}
	if c.AuthMode == "token" && c.AllowInsecureListen {
		if ok, err := IsLoopbackListen(c.Listen); err == nil && !ok {
			w = append(w, "ALLOW_INSECURE_LISTEN=1: 共有トークン認証のまま "+c.Listen+" で待ち受ける。前段で TLS 終端すること")
		}
	}
	if c.AuthMode == "token" && c.DeviceBinding == "enforce" {
		w = append(w, "DEVICE_BINDING=enforce: AUTH_MODE=token には認証主体 (principal) が無いので端末 ID の結び付けは検査されない")
	}
	if c.AuthMode == "cf-access" && c.DeviceBinding == "off" {
		w = append(w, "DEVICE_BINDING=off: 端末 ID と Access の認証主体の結び付けを検査しない")
	}
	return w
}

// stateFile は STATE_FILE を優先し、無ければ旧 PUSH_STATE_FILE、
// どちらも無ければ既定パスを返す。
func stateFile() string {
	if v := os.Getenv("STATE_FILE"); v != "" {
		return v
	}
	if v := os.Getenv("PUSH_STATE_FILE"); v != "" {
		return v
	}
	return DefaultStateFile
}

// sipHost は SIP_HOST を優先し、無ければ旧 ASTERISK_HOST を読む。
func sipHost() string {
	if v := os.Getenv("SIP_HOST"); v != "" {
		return v
	}
	if v := os.Getenv("ASTERISK_HOST"); v != "" {
		return v
	}
	return "127.0.0.1"
}

// sipPort は SIP_PORT を優先し、無ければ旧 ASTERISK_PORT を読む。
func sipPort() (int, error) {
	if v := os.Getenv("SIP_PORT"); v != "" {
		return atoiEnv("SIP_PORT", 5060)
	}
	return atoiEnv("ASTERISK_PORT", 5060)
}

func atoiEnv(key string, def int) (int, error) {
	s, ok := os.LookupEnv(key)
	if !ok || s == "" {
		return def, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s の値 %q は整数ではない: %w", key, s, err)
	}
	return v, nil
}

// durationEnv は "45s" のような time.ParseDuration 形式、または整数 (秒) を読む。
func durationEnv(key string, def time.Duration) (time.Duration, error) {
	s, ok := os.LookupEnv(key)
	if !ok || s == "" {
		return def, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s の値 %q は時間 (例 45s) ではない: %w", key, s, err)
	}
	return d, nil
}

// Validate は設定の整合性を検査する。
func (c Config) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("LISTEN が空")
	}
	loopback, err := IsLoopbackListen(c.Listen)
	if err != nil {
		return err
	}
	switch c.AuthMode {
	case "token":
		if c.DevToken == "" {
			return fmt.Errorf("AUTH_MODE=token のとき DEV_TOKEN が必須")
		}
		// 共有トークンは平文 HTTP で流れるうえ端末を区別できない。LAN に
		// 開くと同一 LAN の誰でも盗聴・総当たりできるため既定では拒否する。
		if !loopback && !c.AllowInsecureListen {
			return fmt.Errorf("AUTH_MODE=token では LISTEN を loopback (127.0.0.1 / ::1 / localhost) に限る (現在 %q)。"+
				"前段で TLS 終端する等で意図的に開くなら ALLOW_INSECURE_LISTEN=1", c.Listen)
		}
	case "cf-access":
		if c.CFTeamDomain == "" {
			return fmt.Errorf("AUTH_MODE=cf-access のとき CF_TEAM_DOMAIN が必須")
		}
		if strings.ContainsAny(c.CFTeamDomain, "/:@?# ") {
			return fmt.Errorf("CF_TEAM_DOMAIN はホスト名のみ (例 example.cloudflareaccess.com、現在 %q)", c.CFTeamDomain)
		}
		if c.CFAccessAUD == "" {
			return fmt.Errorf("AUTH_MODE=cf-access のとき CF_ACCESS_AUD が必須")
		}
	default:
		return fmt.Errorf("AUTH_MODE は cf-access|token のいずれか (現在 %q)", c.AuthMode)
	}
	switch c.Backend {
	case "fake", "sip":
	default:
		return fmt.Errorf("BACKEND は fake|sip のいずれか (現在 %q)", c.Backend)
	}
	if c.RTPPortMin <= 0 || c.RTPPortMax <= 0 || c.RTPPortMin > c.RTPPortMax {
		return fmt.Errorf("RTP_PORT_MIN/MAX の範囲が不正 (%d-%d)", c.RTPPortMin, c.RTPPortMax)
	}
	// 不正な LOCAL_IP は SDP/Contact にそのまま載り、片通話になるまで気づけない。
	if c.LocalIP != "" && net.ParseIP(c.LocalIP) == nil {
		return fmt.Errorf("LOCAL_IP が IP アドレスではない (現在 %q)", c.LocalIP)
	}
	if c.ResumeTimeoutSec < 1 || c.ResumeTimeoutSec > 300 {
		return fmt.Errorf("RESUME_TIMEOUT_SEC は 1..300 の範囲 (現在 %d)", c.ResumeTimeoutSec)
	}
	if c.WSPingInterval < 5*time.Second || c.WSPingInterval > 5*time.Minute {
		return fmt.Errorf("WS_PING_INTERVAL は 5s..5m の範囲 (現在 %s)", c.WSPingInterval)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("LOG_LEVEL は debug|info|warn|error のいずれか (現在 %q)", c.LogLevel)
	}
	switch c.DeviceBinding {
	case "off", "warn", "enforce":
	default:
		return fmt.Errorf("DEVICE_BINDING は off|warn|enforce のいずれか (現在 %q)", c.DeviceBinding)
	}
	if c.MaxAccounts < 1 || c.MaxStoredDevices < 1 || c.MaxOnlineDevices < 1 {
		return fmt.Errorf("MAX_ACCOUNTS / MAX_STORED_DEVICES / MAX_ONLINE_DEVICES は 1 以上 (現在 %d / %d / %d)",
			c.MaxAccounts, c.MaxStoredDevices, c.MaxOnlineDevices)
	}
	return nil
}

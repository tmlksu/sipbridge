// Package config は relay の環境変数読み込みと検証を行う。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// DefaultStateFile は STATE_FILE / PUSH_STATE_FILE 未設定時の既定パスである。
const DefaultStateFile = "/var/lib/sipbridge/state.json"

// Config は relay 全体の設定を保持する。
type Config struct {
	Listen       string // 例 "127.0.0.1:8080"
	AuthMode     string // "cf-access" | "token"
	CFTeamDomain string // 例 "example.cloudflareaccess.com"
	CFAccessAUD  string // Access アプリケーションの AUD タグ
	DevToken     string // AUTH_MODE=token 時の共有トークン
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
	// StateFile はアカウント/端末結び付け/push トークンの永続化先である
	// (STATE_FILE。旧名 PUSH_STATE_FILE も読む)。
	StateFile string
	// FCM 設定 (push 送信用。どちらか空なら push は no-op)
	FCMProjectID          string
	FCMServiceAccountFile string
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
		CFTeamDomain:          getenv("CF_TEAM_DOMAIN", ""),
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
	}
	var err error
	if c.SIPPort, err = sipPort(); err != nil {
		return Config{}, err
	}
	if c.RTPPortMin, err = atoiEnv("RTP_PORT_MIN", 20000); err != nil {
		return Config{}, err
	}
	if c.RTPPortMax, err = atoiEnv("RTP_PORT_MAX", 20100); err != nil {
		return Config{}, err
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
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

// Validate は設定の整合性を検査する。
func (c Config) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("LISTEN が空")
	}
	switch c.AuthMode {
	case "token":
		if c.DevToken == "" {
			return fmt.Errorf("AUTH_MODE=token のとき DEV_TOKEN が必須")
		}
	case "cf-access":
		if c.CFTeamDomain == "" {
			return fmt.Errorf("AUTH_MODE=cf-access のとき CF_TEAM_DOMAIN が必須")
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
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("LOG_LEVEL は debug|info|warn|error のいずれか (現在 %q)", c.LogLevel)
	}
	return nil
}

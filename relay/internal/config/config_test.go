package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"LISTEN", "AUTH_MODE", "CF_TEAM_DOMAIN", "CF_ACCESS_AUD", "DEV_TOKEN",
		"SIP_HOST", "SIP_PORT", "ASTERISK_HOST", "ASTERISK_PORT",
		"SIP_USER", "SIP_PASSWORD", "SIP_DISPLAY",
		"LOCAL_IP", "RTP_PORT_MIN", "RTP_PORT_MAX", "BACKEND", "RESUME_TIMEOUT_SEC",
		"LOG_LEVEL", "STATE_FILE", "PUSH_STATE_FILE",
		"FCM_PROJECT_ID", "FCM_SERVICE_ACCOUNT_FILE", "WS_PING_INTERVAL",
		"ALLOW_INSECURE_LISTEN", "DEVICE_BINDING", "MAX_ACCOUNTS", "MAX_STORED_DEVICES", "MAX_ONLINE_DEVICES",
	} {
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("Unsetenv %s 失敗: %v", k, err)
		}
	}
}

func baseTokenEnv(t *testing.T) {
	t.Helper()
	clearEnv(t)
	setEnv(t, map[string]string{
		"AUTH_MODE": "token",
		"DEV_TOKEN": "x",
	})
}

func TestDefaults(t *testing.T) {
	baseTokenEnv(t)
	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv 失敗: %v", err)
	}
	if c.Listen != "127.0.0.1:8080" {
		t.Errorf("Listen = %q", c.Listen)
	}
	if c.SIPPort != 5060 || c.RTPPortMin != 20000 || c.RTPPortMax != 20100 {
		t.Errorf("ポート既定が不正: %+v", c)
	}
	if c.ResumeTimeoutSec != 30 {
		t.Errorf("ResumeTimeoutSec 既定 = %d", c.ResumeTimeoutSec)
	}
	if c.WSPingInterval != 20*time.Second {
		t.Errorf("WSPingInterval 既定 = %s", c.WSPingInterval)
	}
	if c.Backend != "fake" || c.LogLevel != "info" {
		t.Errorf("既定が不正: %+v", c)
	}
	if c.StateFile != DefaultStateFile {
		t.Errorf("StateFile 既定 = %q", c.StateFile)
	}
	if c.SIPUser != "" || c.SIPPassword != "" {
		t.Errorf("既定アカウントは未設定のはず: %+v", c)
	}
	if c.DeviceBinding != "warn" || c.AllowInsecureListen {
		t.Errorf("DEVICE_BINDING / ALLOW_INSECURE_LISTEN の既定が不正: %+v", c)
	}
	if c.MaxAccounts != 16 || c.MaxStoredDevices != 64 || c.MaxOnlineDevices != 32 {
		t.Errorf("上限の既定が不正: %d %d %d", c.MaxAccounts, c.MaxStoredDevices, c.MaxOnlineDevices)
	}
}

func TestValidation(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		ok   bool
	}{
		{"token 正常", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x"}, true},
		{"token 無しは不可", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": ""}, false},
		{"cf-access 正常", map[string]string{"AUTH_MODE": "cf-access", "CF_TEAM_DOMAIN": "t.example.com", "CF_ACCESS_AUD": "aud"}, true},
		{"cf-access team 無しは不可", map[string]string{"AUTH_MODE": "cf-access", "CF_TEAM_DOMAIN": "", "CF_ACCESS_AUD": "aud"}, false},
		{"cf-access aud 無しは不可", map[string]string{"AUTH_MODE": "cf-access", "CF_TEAM_DOMAIN": "t.example.com", "CF_ACCESS_AUD": ""}, false},
		{"不明な auth", map[string]string{"AUTH_MODE": "zzz", "DEV_TOKEN": "x"}, false},
		{"不明な backend", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "BACKEND": "zzz"}, false},
		{"sip backend 可", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "BACKEND": "sip"}, true},
		{"sip backend は SIP_USER 無しでも可", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "BACKEND": "sip", "SIP_USER": ""}, true},
		{"RTP 範囲逆転は不可", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "RTP_PORT_MIN": "30000", "RTP_PORT_MAX": "20000"}, false},
		{"RTP 非整数は不可", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "RTP_PORT_MIN": "abc"}, false},
		{"RESUME_TIMEOUT_SEC 正常", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "RESUME_TIMEOUT_SEC": "45"}, true},
		{"RESUME_TIMEOUT_SEC 0 は不可", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "RESUME_TIMEOUT_SEC": "0"}, false},
		{"RESUME_TIMEOUT_SEC 上限超は不可", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "RESUME_TIMEOUT_SEC": "301"}, false},
		{"WS_PING_INTERVAL 45s", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "WS_PING_INTERVAL": "45s"}, true},
		{"WS_PING_INTERVAL 整数秒", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "WS_PING_INTERVAL": "60"}, true},
		{"WS_PING_INTERVAL 短すぎ", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "WS_PING_INTERVAL": "1s"}, false},
		{"WS_PING_INTERVAL 不正", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "WS_PING_INTERVAL": "abc"}, false},
		{"不明な loglevel は不可", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "LOG_LEVEL": "verbose"}, false},
		// #37: token モードは loopback 以外で待ち受けない。
		{"token + e2e の 127.0.0.1:18080", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "LISTEN": "127.0.0.1:18080"}, true},
		{"token + 127.0.0.2", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "LISTEN": "127.0.0.2:8080"}, true},
		{"token + [::1]", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "LISTEN": "[::1]:8080"}, true},
		{"token + localhost", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "LISTEN": "localhost:8080"}, true},
		{"token + 0.0.0.0 は不可", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "LISTEN": "0.0.0.0:8080"}, false},
		{"token + 空ホストは不可", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "LISTEN": ":8080"}, false},
		{"token + [::] は不可", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "LISTEN": "[::]:8080"}, false},
		{"token + LAN IP は不可", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "LISTEN": "192.168.1.10:8080"}, false},
		{"token + ホスト名は不可", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "LISTEN": "relay.lan:8080"}, false},
		{"token + 0.0.0.0 + ALLOW_INSECURE_LISTEN=1", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "LISTEN": "0.0.0.0:8080", "ALLOW_INSECURE_LISTEN": "1"}, true},
		{"ALLOW_INSECURE_LISTEN 不正値", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "ALLOW_INSECURE_LISTEN": "maybe"}, false},
		{"cf-access は 0.0.0.0 でも可", map[string]string{"AUTH_MODE": "cf-access", "CF_TEAM_DOMAIN": "t.example.com", "CF_ACCESS_AUD": "aud", "LISTEN": "0.0.0.0:8080"}, true},
		{"LISTEN 形式不正", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "LISTEN": "8080"}, false},
		{"cf-access team にパス付きは不可", map[string]string{"AUTH_MODE": "cf-access", "CF_TEAM_DOMAIN": "t.example.com/x", "CF_ACCESS_AUD": "aud"}, false},
		{"DEVICE_BINDING enforce", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "DEVICE_BINDING": "enforce"}, true},
		{"DEVICE_BINDING OFF (大文字)", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "DEVICE_BINDING": "OFF"}, true},
		{"DEVICE_BINDING 不正", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "DEVICE_BINDING": "strict"}, false},
		{"MAX_ACCOUNTS 0 は不可", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "MAX_ACCOUNTS": "0"}, false},
		{"MAX_ONLINE_DEVICES 非整数は不可", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "MAX_ONLINE_DEVICES": "many"}, false},
		{"MAX_STORED_DEVICES 128", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "MAX_STORED_DEVICES": "128"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			setEnv(t, tc.env)
			_, err := FromEnv()
			if tc.ok && err != nil {
				t.Errorf("成功のはずが失敗: %v", err)
			}
			if !tc.ok && err == nil {
				t.Errorf("失敗のはずが成功")
			}
		})
	}
}

func TestDefaultAccountAndFCM(t *testing.T) {
	baseTokenEnv(t)
	setEnv(t, map[string]string{
		"SIP_USER": "101", "SIP_PASSWORD": "pw", "SIP_DISPLAY": "居間",
		"FCM_PROJECT_ID": "proj", "FCM_SERVICE_ACCOUNT_FILE": "/tmp/sa.json",
	})
	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv 失敗: %v", err)
	}
	if c.SIPUser != "101" || c.SIPPassword != "pw" || c.SIPDisplay != "居間" {
		t.Errorf("既定アカウントが読めていない: %+v", c)
	}
	if c.FCMProjectID != "proj" || c.FCMServiceAccountFile != "/tmp/sa.json" {
		t.Errorf("FCM 設定が読めていない: %+v", c)
	}
}

// TestStateFilePrecedence は STATE_FILE > PUSH_STATE_FILE (旧名) > 既定 の優先順である。
func TestStateFilePrecedence(t *testing.T) {
	baseTokenEnv(t)
	setEnv(t, map[string]string{"PUSH_STATE_FILE": "/tmp/old.json"})
	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv 失敗: %v", err)
	}
	if c.StateFile != "/tmp/old.json" {
		t.Errorf("旧 PUSH_STATE_FILE が使われない: %q", c.StateFile)
	}
	setEnv(t, map[string]string{"STATE_FILE": "/tmp/new.json"})
	c, err = FromEnv()
	if err != nil {
		t.Fatalf("FromEnv 失敗: %v", err)
	}
	if c.StateFile != "/tmp/new.json" {
		t.Errorf("STATE_FILE が優先されない: %q", c.StateFile)
	}
}

// TestSIPHostPrecedence は SIP_HOST/SIP_PORT が旧 ASTERISK_HOST/PORT より
// 優先され、旧名だけでも従来どおり読めることを確認する。
func TestSIPHostPrecedence(t *testing.T) {
	baseTokenEnv(t)
	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv 失敗: %v", err)
	}
	if c.SIPHost != "127.0.0.1" || c.SIPPort != 5060 {
		t.Errorf("既定が不正: %s:%d", c.SIPHost, c.SIPPort)
	}

	setEnv(t, map[string]string{"ASTERISK_HOST": "10.0.0.1", "ASTERISK_PORT": "5070"})
	c, err = FromEnv()
	if err != nil {
		t.Fatalf("FromEnv 失敗: %v", err)
	}
	if c.SIPHost != "10.0.0.1" || c.SIPPort != 5070 {
		t.Errorf("旧 ASTERISK_HOST/PORT が使われない: %s:%d", c.SIPHost, c.SIPPort)
	}

	setEnv(t, map[string]string{"SIP_HOST": "192.168.1.1", "SIP_PORT": "5060"})
	c, err = FromEnv()
	if err != nil {
		t.Fatalf("FromEnv 失敗: %v", err)
	}
	if c.SIPHost != "192.168.1.1" || c.SIPPort != 5060 {
		t.Errorf("SIP_HOST/PORT が優先されない: %s:%d", c.SIPHost, c.SIPPort)
	}
}

func TestWSPingInterval(t *testing.T) {
	for in, want := range map[string]time.Duration{"45s": 45 * time.Second, "60": time.Minute, "1m30s": 90 * time.Second} {
		baseTokenEnv(t)
		t.Setenv("WS_PING_INTERVAL", in)
		c, err := FromEnv()
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if c.WSPingInterval != want {
			t.Errorf("%s → %s, want %s", in, c.WSPingInterval, want)
		}
	}
}

// TestNormalizeTeamDomain は CF_TEAM_DOMAIN の正規化 (スキーム・末尾 / 除去、小文字化) である。
func TestNormalizeTeamDomain(t *testing.T) {
	for in, want := range map[string]string{
		"example.cloudflareaccess.com":           "example.cloudflareaccess.com",
		"https://example.cloudflareaccess.com":   "example.cloudflareaccess.com",
		"https://example.cloudflareaccess.com/":  "example.cloudflareaccess.com",
		"HTTPS://Example.CloudflareAccess.com//": "example.cloudflareaccess.com",
		"http://example.cloudflareaccess.com":    "example.cloudflareaccess.com",
		"  example.cloudflareaccess.com/  ":      "example.cloudflareaccess.com",
		"":                                       "",
	} {
		if got := NormalizeTeamDomain(in); got != want {
			t.Errorf("NormalizeTeamDomain(%q) = %q, want %q", in, got, want)
		}
	}
	clearEnv(t)
	setEnv(t, map[string]string{"AUTH_MODE": "cf-access", "CF_TEAM_DOMAIN": "https://Example.CloudflareAccess.com/", "CF_ACCESS_AUD": "aud"})
	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv 失敗: %v", err)
	}
	if c.CFTeamDomain != "example.cloudflareaccess.com" {
		t.Errorf("CFTeamDomain = %q", c.CFTeamDomain)
	}
}

// TestWarnings は既定アカウント + cf-access の警告である (#37)。
func TestWarnings(t *testing.T) {
	c := Config{AuthMode: "cf-access", SIPUser: "101", DeviceBinding: "warn", Listen: "127.0.0.1:8080"}
	if w := c.Warnings(); len(w) != 1 || !strings.Contains(w[0], "SIP_USER") {
		t.Errorf("既定アカウントの警告が出ない: %q", w)
	}
	c.SIPUser = ""
	if w := c.Warnings(); len(w) != 0 {
		t.Errorf("警告は無いはず: %q", w)
	}
	// token モードの既定アカウントは警告しない (開発用)。
	c = Config{AuthMode: "token", SIPUser: "101", Listen: "127.0.0.1:8080"}
	if w := c.Warnings(); len(w) != 0 {
		t.Errorf("token モードでは警告しないはず: %q", w)
	}
	c = Config{AuthMode: "token", Listen: "127.0.0.1:8080", DeviceBinding: "enforce"}
	if w := c.Warnings(); len(w) != 1 || !strings.Contains(w[0], "DEVICE_BINDING=enforce") {
		t.Errorf("token + enforce の警告が出ない: %q", w)
	}
	c = Config{AuthMode: "token", Listen: "0.0.0.0:8080", AllowInsecureListen: true}
	if w := c.Warnings(); len(w) != 1 || !strings.Contains(w[0], "ALLOW_INSECURE_LISTEN") {
		t.Errorf("ALLOW_INSECURE_LISTEN の警告が出ない: %q", w)
	}
}

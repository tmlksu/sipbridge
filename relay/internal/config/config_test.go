package config

import (
	"os"
	"testing"
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
		"LOCAL_IP", "RTP_PORT_MIN", "RTP_PORT_MAX", "BACKEND",
		"LOG_LEVEL", "STATE_FILE", "PUSH_STATE_FILE",
		"FCM_PROJECT_ID", "FCM_SERVICE_ACCOUNT_FILE",
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
	if c.Backend != "fake" || c.LogLevel != "info" {
		t.Errorf("既定が不正: %+v", c)
	}
	if c.StateFile != DefaultStateFile {
		t.Errorf("StateFile 既定 = %q", c.StateFile)
	}
	if c.SIPUser != "" || c.SIPPassword != "" {
		t.Errorf("既定アカウントは未設定のはず: %+v", c)
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
		{"不明な loglevel は不可", map[string]string{"AUTH_MODE": "token", "DEV_TOKEN": "x", "LOG_LEVEL": "verbose"}, false},
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

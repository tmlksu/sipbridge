package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestTokenAuth(t *testing.T) {
	a := NewTokenAuth("secret")
	if a.Mode() != "token" {
		t.Errorf("Mode = %q", a.Mode())
	}
	ok := &http.Request{Header: http.Header{"Authorization": {"Bearer secret"}}}
	if err := a.Authenticate(ok); err != nil {
		t.Errorf("正しいトークンが拒否: %v", err)
	}
	for name, h := range map[string]http.Header{
		"不一致":   {"Authorization": {"Bearer wrong"}},
		"接頭辞無し": {"Authorization": {"secret"}},
		"空":     {},
	} {
		if err := a.Authenticate(&http.Request{Header: h}); err == nil {
			t.Errorf("%s: 拒否されるはず", name)
		}
	}
}

// jwksJSON は RSA 公開鍵から JWKS 文書を作る。
func jwksJSON(pub *rsa.PublicKey, kid string) string {
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	return fmt.Sprintf(`{"keys":[{"kty":"RSA","kid":%q,"alg":"RS256","n":%q,"e":%q}]}`, kid, n, e)
}

func sign(t *testing.T, priv *rsa.PrivateKey, kid, aud string, exp time.Time) string {
	t.Helper()
	claims := jwt.MapClaims{"aud": aud, "exp": jwt.NewNumericDate(exp), "iss": "https://example.cloudflareaccess.com"}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("署名失敗: %v", err)
	}
	return s
}

func TestCFAccessAuth(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("鍵生成失敗: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, jwksJSON(&priv.PublicKey, "k1"))
	}))
	defer srv.Close()

	a := NewCFAccessAuthWithURL(srv.URL, "my-aud")
	if a.Mode() != "cf-access" {
		t.Errorf("Mode = %q", a.Mode())
	}
	req := func(token string) *http.Request {
		r := &http.Request{Header: http.Header{}}
		if token != "" {
			r.Header.Set("Cf-Access-Jwt-Assertion", token)
		}
		return r
	}
	valid := sign(t, priv, "k1", "my-aud", time.Now().Add(time.Hour))
	if err := a.Authenticate(req(valid)); err != nil {
		t.Errorf("正しい JWT が拒否: %v", err)
	}
	// 2 回目はキャッシュ経路 (サーバを閉じても通るはず)。
	srv.Close()
	if err := a.Authenticate(req(valid)); err != nil {
		t.Errorf("キャッシュ経路の JWT が拒否: %v", err)
	}

	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	cases := []struct {
		name  string
		token string
	}{
		{"ヘッダ無し", ""},
		{"aud 不一致", sign(t, priv, "k1", "other-aud", time.Now().Add(time.Hour))},
		{"期限切れ", sign(t, priv, "k1", "my-aud", time.Now().Add(-time.Hour))},
		{"未知の kid", sign(t, priv, "k9", "my-aud", time.Now().Add(time.Hour))},
		{"署名不一致", sign(t, other, "k1", "my-aud", time.Now().Add(time.Hour))},
		{"壊れたトークン", "not.a.jwt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// キャッシュ済みのためサーバ不要。
			if err := a.Authenticate(req(tc.token)); err == nil {
				t.Errorf("拒否されるはず")
			}
		})
	}
}

func TestCFAccessAuthFetchFailure(t *testing.T) {
	a := NewCFAccessAuthWithURL("http://127.0.0.1:1/certs", "aud")
	r := &http.Request{Header: http.Header{"Cf-Access-Jwt-Assertion": {"x"}}}
	if err := a.Authenticate(r); err == nil {
		t.Errorf("JWKS 取得失敗時は拒否されるはず")
	}
}

// JWKS 取得が詰まっている間、他の検証が待たされないこと (#9)。
func TestCFAccessAuthSlowRefreshDoesNotBlock(t *testing.T) {
	a := NewCFAccessAuthWithURL("http://example.invalid/certs", "aud")
	release := make(chan struct{})
	var calls atomic.Int32
	a.fetch = func(string) (map[string]*rsa.PublicKey, error) {
		calls.Add(1)
		<-release
		return map[string]*rsa.PublicKey{}, nil
	}
	// 期限切れの鍵を用意する (猶予内)。
	a.keys = map[string]*rsa.PublicKey{"kid": {}}
	a.fetchedAt = time.Now().Add(-a.ttl - time.Second)

	slow := make(chan struct{})
	go func() {
		defer close(slow)
		if _, err := a.cachedKeys(); err != nil {
			t.Errorf("取得側で失敗: %v", err)
		}
	}()
	// 取得が始まるまで待つ。
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := a.cachedKeys(); err != nil {
			t.Errorf("待ち側で失敗: %v", err)
		}
	}()
	select {
	case <-done: // 期限切れの鍵で即座に通る
	case <-time.After(2 * time.Second):
		t.Fatal("JWKS 取得中の検証がブロックされた")
	}
	close(release)
	<-slow
	if n := calls.Load(); n != 1 {
		t.Errorf("fetch 呼び出し = %d (1 本だけのはず)", n)
	}
}

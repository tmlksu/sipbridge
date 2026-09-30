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
	if p, err := a.Authenticate(ok); err != nil || p != "" {
		t.Errorf("正しいトークンが拒否 (principal は空のはず): %q %v", p, err)
	}
	for name, h := range map[string]http.Header{
		"不一致":   {"Authorization": {"Bearer wrong"}},
		"接頭辞無し": {"Authorization": {"secret"}},
		"空":     {},
	} {
		if _, err := a.Authenticate(&http.Request{Header: h}); err == nil {
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

// testIssuer はテストで期待する iss (https://<team>) である。
const testIssuer = "https://example.cloudflareaccess.com"

func sign(t *testing.T, priv *rsa.PrivateKey, kid, aud string, exp time.Time) string {
	t.Helper()
	return signClaims(t, priv, kid, jwt.MapClaims{
		"aud": aud, "exp": jwt.NewNumericDate(exp), "iss": testIssuer,
		"sub": "", "common_name": "client-id.access",
	})
}

// signClaims は任意の claim で RS256 署名する。
func signClaims(t *testing.T, priv *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
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

	a := NewCFAccessAuthWithURL(srv.URL, "my-aud", testIssuer)
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
	if p, err := a.Authenticate(req(valid)); err != nil || p != "cn:client-id.access" {
		t.Errorf("正しい JWT が拒否 / principal 不正: %q %v", p, err)
	}
	// 2 回目はキャッシュ経路 (サーバを閉じても通るはず)。
	srv.Close()
	if _, err := a.Authenticate(req(valid)); err != nil {
		t.Errorf("キャッシュ経路の JWT が拒否: %v", err)
	}
	exp := jwt.NewNumericDate(time.Now().Add(time.Hour))

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
		{"iss 不一致", signClaims(t, priv, "k1", jwt.MapClaims{"aud": "my-aud", "exp": exp, "iss": "https://attacker.example.com"})},
		{"iss 末尾スラッシュ", signClaims(t, priv, "k1", jwt.MapClaims{"aud": "my-aud", "exp": exp, "iss": testIssuer + "/"})},
		{"iss 無し", signClaims(t, priv, "k1", jwt.MapClaims{"aud": "my-aud", "exp": exp})},
		{"exp 無し", signClaims(t, priv, "k1", jwt.MapClaims{"aud": "my-aud", "iss": testIssuer})},
		{"HS256 (alg 混同)", hs256(t, "k1", jwt.MapClaims{"aud": "my-aud", "exp": exp, "iss": testIssuer})},
		{"alg none", noneAlg(t, jwt.MapClaims{"aud": "my-aud", "exp": exp, "iss": testIssuer})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// キャッシュ済みのためサーバ不要。
			if _, err := a.Authenticate(req(tc.token)); err == nil {
				t.Errorf("拒否されるはず")
			}
		})
	}
}

// hs256 は HS256 で署名したトークンを作る (鍵は適当。alg 混同攻撃の確認用)。
func hs256(t *testing.T, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString([]byte("secret"))
	if err != nil {
		t.Fatalf("署名失敗: %v", err)
	}
	return s
}

// noneAlg は alg=none の未署名トークンを作る。
func noneAlg(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	s, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("署名失敗: %v", err)
	}
	return s
}

// TestCFAccessPrincipal は Service Token (sub 空 + common_name) と
// ユーザー認証 (sub + email) の JWT から principal を取り出せることを確認する。
func TestCFAccessPrincipal(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("鍵生成失敗: %v", err)
	}
	a := NewCFAccessAuthWithURL("http://example.invalid/certs", "my-aud", testIssuer)
	a.fetch = func(string) (map[string]*rsa.PublicKey, error) {
		return map[string]*rsa.PublicKey{"k1": &priv.PublicKey}, nil
	}
	exp := jwt.NewNumericDate(time.Now().Add(time.Hour))
	base := func(extra jwt.MapClaims) jwt.MapClaims {
		c := jwt.MapClaims{"aud": []string{"my-aud"}, "exp": exp, "iss": testIssuer, "iat": jwt.NewNumericDate(time.Now())}
		for k, v := range extra {
			c[k] = v
		}
		return c
	}
	cases := []struct {
		name   string
		claims jwt.MapClaims
		want   string
	}{
		{"service token", base(jwt.MapClaims{"sub": "", "common_name": "0123abcd.access", "type": "app"}), "cn:0123abcd.access"},
		{"ユーザー認証", base(jwt.MapClaims{"sub": "7335d417-61da-459d-899c-0a01c76a2f94", "email": "user@example.com", "type": "app"}), "sub:7335d417-61da-459d-899c-0a01c76a2f94"},
		{"sub/common_name 無し", base(nil), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &http.Request{Header: http.Header{}}
			r.Header.Set("Cf-Access-Jwt-Assertion", signClaims(t, priv, "k1", tc.claims))
			p, err := a.Authenticate(r)
			if err != nil {
				t.Fatalf("拒否された: %v", err)
			}
			if p != tc.want {
				t.Errorf("principal = %q, want %q", p, tc.want)
			}
		})
	}
}

// TestNewCFAccessAuthURLs はチームドメインから JWKS URL と iss を組み立てることを確認する。
func TestNewCFAccessAuthURLs(t *testing.T) {
	a := NewCFAccessAuth("example.cloudflareaccess.com", "aud")
	if a.jwksURL != "https://example.cloudflareaccess.com/cdn-cgi/access/certs" {
		t.Errorf("jwksURL = %q", a.jwksURL)
	}
	if a.issuer != "https://example.cloudflareaccess.com" {
		t.Errorf("issuer = %q", a.issuer)
	}
}

func TestCFAccessAuthFetchFailure(t *testing.T) {
	a := NewCFAccessAuthWithURL("http://127.0.0.1:1/certs", "aud", testIssuer)
	r := &http.Request{Header: http.Header{"Cf-Access-Jwt-Assertion": {"x"}}}
	if _, err := a.Authenticate(r); err == nil {
		t.Errorf("JWKS 取得失敗時は拒否されるはず")
	}
}

// JWKS 取得が詰まっている間、他の検証が待たされないこと (#9)。
func TestCFAccessAuthSlowRefreshDoesNotBlock(t *testing.T) {
	a := NewCFAccessAuthWithURL("http://example.invalid/certs", "aud", testIssuer)
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

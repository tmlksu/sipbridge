// Package auth は WebSocket 接続前の HTTP 認証を行う。
// AUTH_MODE=token では開発用共有トークン、cf-access では
// Cloudflare Access の JWT (RS256, JWKS 検証, aud/exp 検証) を使う。
package auth

import (
	"crypto/rsa"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Authenticator は HTTP リクエストを認証する。
type Authenticator interface {
	Authenticate(r *http.Request) error
	Mode() string
}

// ---- 開発用共有トークン ----

// TokenAuth は Authorization: Bearer <token> を検証する。
type TokenAuth struct {
	Token string
}

// NewTokenAuth は TokenAuth を作る。
func NewTokenAuth(token string) *TokenAuth {
	return &TokenAuth{Token: token}
}

// Mode は "token" を返す。
func (a *TokenAuth) Mode() string { return "token" }

// Authenticate は Bearer トークンが一致すれば nil を返す。
func (a *TokenAuth) Authenticate(r *http.Request) error {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return fmt.Errorf("Authorization: Bearer トークンが必要")
	}
	got := strings.TrimPrefix(h, prefix)
	if subtle.ConstantTimeCompare([]byte(got), []byte(a.Token)) != 1 {
		return fmt.Errorf("トークンが不一致")
	}
	return nil
}

// ---- Cloudflare Access JWT ----

// jwksURLPath はチームドメイン配下の証明書エンドポイント。
const jwksURLPath = "/cdn-cgi/access/certs"

// jwk は JWKS の1鍵 (RSA のみ対応)。
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwksDoc struct {
	Keys []jwk `json:"keys"`
}

// rsaPublicKey は n/e から RSA 公開鍵を復元する。
func (k jwk) rsaPublicKey() (*rsa.PublicKey, error) {
	if k.Kty != "RSA" {
		return nil, fmt.Errorf("kid %q は RSA ではない (%q)", k.Kid, k.Kty)
	}
	nb, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("kid %q の n の decode 失敗: %w", k.Kid, err)
	}
	eb, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("kid %q の e の decode 失敗: %w", k.Kid, err)
	}
	e := 0
	for _, b := range eb {
		e = e<<8 + int(b)
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}, nil
}

// CFAccessAuth は Cf-Access-Jwt-Assertion を JWKS で検証する。
type CFAccessAuth struct {
	jwksURL string
	aud     string

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey // kid → 公開鍵
	fetchedAt time.Time
	ttl       time.Duration
	fetch     func(url string) (map[string]*rsa.PublicKey, error) // 差し替え可能 (テスト用)
}

// NewCFAccessAuth はチームドメインと AUD から作る。
func NewCFAccessAuth(teamDomain, aud string) *CFAccessAuth {
	return NewCFAccessAuthWithURL("https://"+teamDomain+jwksURLPath, aud)
}

// NewCFAccessAuthWithURL は JWKS URL を直接指定する (テスト用)。
func NewCFAccessAuthWithURL(jwksURL, aud string) *CFAccessAuth {
	a := &CFAccessAuth{jwksURL: jwksURL, aud: aud, ttl: 10 * time.Minute}
	a.fetch = fetchJWKS
	return a
}

// Mode は "cf-access" を返す。
func (a *CFAccessAuth) Mode() string { return "cf-access" }

// Authenticate は Cf-Access-Jwt-Assertion を検証する。
func (a *CFAccessAuth) Authenticate(r *http.Request) error {
	raw := r.Header.Get("Cf-Access-Jwt-Assertion")
	if raw == "" {
		return fmt.Errorf("Cf-Access-Jwt-Assertion ヘッダが必要")
	}
	keys, err := a.cachedKeys()
	if err != nil {
		return err
	}
	tok, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, fmt.Errorf("想定外の署名方式 %q", t.Method.Alg())
		}
		kid, _ := t.Header["kid"].(string)
		pub, ok := keys[kid]
		if !ok {
			return nil, fmt.Errorf("未知の kid %q", kid)
		}
		return pub, nil
	}, jwt.WithAudience(a.aud))
	if err != nil {
		return fmt.Errorf("JWT 検証失敗: %w", err)
	}
	if !tok.Valid {
		return fmt.Errorf("JWT が無効")
	}
	return nil
}

// cachedKeys は JWKS を ttl の間キャッシュして返す。
func (a *CFAccessAuth) cachedKeys() (map[string]*rsa.PublicKey, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.keys != nil && time.Since(a.fetchedAt) < a.ttl {
		return a.keys, nil
	}
	keys, err := a.fetch(a.jwksURL)
	if err != nil {
		return nil, err
	}
	a.keys = keys
	a.fetchedAt = time.Now()
	return keys, nil
}

// fetchJWKS は JWKS 文書を取得して kid→公開鍵の表にする。
func fetchJWKS(url string) (map[string]*rsa.PublicKey, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("JWKS 取得失敗: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS 取得失敗: HTTP %d", resp.StatusCode)
	}
	var doc jwksDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("JWKS 解析失敗: %w", err)
	}
	out := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		pub, err := k.rsaPublicKey()
		if err != nil {
			return nil, err
		}
		out[k.Kid] = pub
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("JWKS に鍵が無い")
	}
	return out, nil
}

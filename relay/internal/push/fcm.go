// Package push の FCM 送信実装 (T5)。
//
// FCM HTTP v1 (https://fcm.googleapis.com/v1/projects/<id>/messages:send) で
// data message を送る。OAuth2 アクセストークンはサービスアカウント JSON から
// golang.org/x/oauth2/google で取得する。
//
// 無効トークン (404/UNREGISTERED) は TokenRemover (internal/state.Store) から
// 自動削除する。トークン削除だけの小さなインタフェースに依存するため、
// 状態ファイルの形式が変わっても push 側の変更は不要である。
package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	// DefaultFCMEndpoint は本番の FCM 基底 URL である。
	// テストでは httptest の URL を FCMConfig.Endpoint に指定する。
	DefaultFCMEndpoint = "https://fcm.googleapis.com"
	// fcmScope は FCM HTTP v1 送信に必要な OAuth2 スコープである。
	fcmScope = "https://www.googleapis.com/auth/firebase.messaging"
	// fcmAndroidTTL は着信 push の有効期限 (PROTOCOL.md の 25 秒応答待ちより少し長い) である。
	fcmAndroidTTL = "30s"
	// maxSendConcurrency は同時送信数の上限である。着信 push は 25 秒の応答待ちに
	// 間に合わせる必要があり、逐次だと 1 台の遅延 (HTTP タイムアウト 10 秒) が
	// 後続の端末にそのまま積み上がる。FCM 側への同時接続を増やしすぎない程度に並列化する。
	maxSendConcurrency = 8
)

// FCMConfig は NewFCM の設定である。
type FCMConfig struct {
	// ProjectID は Firebase プロジェクト ID (FCM_PROJECT_ID)。必須。
	ProjectID string
	// ServiceAccountFile はサービスアカウント JSON のパス
	// (FCM_SERVICE_ACCOUNT_FILE)。ServiceAccountJSON・TokenSource が
	// 無い場合に読み込む。
	ServiceAccountFile string
	// ServiceAccountJSON はサービスアカウント JSON の中身。
	// ファイルの代わりに直接渡したい場合に使う (テスト等)。
	ServiceAccountJSON []byte
	// TokenSource は OAuth2 トークン源の差し替え (テスト用)。
	// nil ならサービスアカウント JSON から作る。
	TokenSource oauth2.TokenSource
	// Endpoint は FCM 基底 URL の差し替え (テスト用)。
	// 空なら DefaultFCMEndpoint を使う。
	Endpoint string
	// HTTPClient の差し替え (テスト用)。nil なら 10 秒タイムアウトの既定品を使う。
	HTTPClient *http.Client
	// Store は無効トークンの削除先。nil でなければ、UNREGISTERED と判定した
	// トークンを保持デバイスから削除する (配線例は relay/README.md 参照)。
	Store TokenRemover
}

// FCM は push.Pusher の FCM HTTP v1 実装である。
type FCM struct {
	projectID   string
	endpoint    string
	tokenSource oauth2.TokenSource
	httpClient  *http.Client
	store       TokenRemover
}

// NewFCM は FCM 送信器を作る。Pusher インタフェースを満たす。
func NewFCM(cfg FCMConfig) (*FCM, error) {
	if cfg.ProjectID == "" {
		return nil, fmt.Errorf("FCM ProjectID が空 (FCM_PROJECT_ID を設定)")
	}
	ts := cfg.TokenSource
	if ts == nil {
		credJSON := cfg.ServiceAccountJSON
		if len(credJSON) == 0 {
			if cfg.ServiceAccountFile == "" {
				return nil, fmt.Errorf("サービスアカウントが未指定 (FCM_SERVICE_ACCOUNT_FILE を設定)")
			}
			data, err := os.ReadFile(cfg.ServiceAccountFile)
			if err != nil {
				return nil, fmt.Errorf("サービスアカウント読み込み失敗: %w", err)
			}
			credJSON = data
		}
		jwtCfg, err := google.JWTConfigFromJSON(credJSON, fcmScope)
		if err != nil {
			return nil, fmt.Errorf("サービスアカウント解析失敗: %w", err)
		}
		// TokenSource はトークンをキャッシュし、期限切れ前に再取得する。
		ts = jwtCfg.TokenSource(context.Background())
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = DefaultFCMEndpoint
	}
	endpoint = strings.TrimSuffix(endpoint, "/")
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &FCM{
		projectID:   cfg.ProjectID,
		endpoint:    endpoint,
		tokenSource: ts,
		httpClient:  httpClient,
		store:       cfg.Store,
	}, nil
}

// fcmSendRequest は messages:send のリクエスト体である。
type fcmSendRequest struct {
	Message fcmMessage `json:"message"`
}

type fcmMessage struct {
	Token   string            `json:"token"`
	Data    map[string]string `json:"data,omitempty"`
	Android *fcmAndroidConfig `json:"android,omitempty"`
}

type fcmAndroidConfig struct {
	Priority string `json:"priority,omitempty"`
	TTL      string `json:"ttl,omitempty"`
}

// fcmErrorResponse は FCM のエラー体である。
// 無効トークン時は status が UNREGISTERED、HTTP 404 になる。
type fcmErrorResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Type      string `json:"@type"`
			ErrorCode string `json:"errorCode"`
		} `json:"details"`
	} `json:"error"`
}

// InvalidTokenError はトークン無効 (UNREGISTERED) を表す。
// Send はこのエラーをトークン削除に使った後、呼び出し側には返さない。
type InvalidTokenError struct {
	Token  string
	Status int
	Reason string
}

func (e *InvalidTokenError) Error() string {
	return fmt.Sprintf("FCM トークン無効 (status=%d): %s", e.Status, e.Reason)
}

// Send は全トークンに data message を送る。Pusher インタフェースの実装である。
//
//   - data には {type, callId, caller, display} (空は省略) を載せ、
//     android.priority=HIGH、ttl=30s を付ける。
//   - UNREGISTERED と判定したトークンは削除先から消し、エラーに含めない。
//     それ以外の失敗は errors.Join でまとめて返す。
//   - 送信は maxSendConcurrency まで並列に行う (端末数分の遅延を積み上げないため)。
func (f *FCM) Send(ctx context.Context, tokens []string, p Payload) error {
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	sem := make(chan struct{}, maxSendConcurrency)
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(tok string) {
			defer wg.Done()
			defer func() { <-sem }()
			err := f.sendOne(ctx, tok, p)
			var invErr *InvalidTokenError
			if errors.As(err, &invErr) {
				f.removeToken(tok)
				return
			}
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("トークン %q への送信失敗: %w", maskToken(tok), err))
				mu.Unlock()
			}
		}(tok)
	}
	wg.Wait()
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// sendOne は 1 トークンへの送信である。
func (f *FCM) sendOne(ctx context.Context, token string, p Payload) error {
	tok, err := f.tokenSource.Token()
	if err != nil {
		return fmt.Errorf("OAuth2 トークン取得失敗: %w", err)
	}
	if tok == nil || tok.AccessToken == "" {
		return fmt.Errorf("OAuth2 トークンが空")
	}
	data := map[string]string{"type": p.Type}
	if p.Type == "" {
		data["type"] = "incoming"
	}
	if p.CallID != "" {
		data["callId"] = p.CallID
	}
	if p.From != "" {
		// FCM data payload では "from" が予約語のため (HTTP v1 で 400)、"caller" を使う。
		data["caller"] = p.From
	}
	if p.Display != "" {
		data["display"] = p.Display
	}
	body, err := json.Marshal(fcmSendRequest{
		Message: fcmMessage{
			Token: token,
			Data:  data,
			Android: &fcmAndroidConfig{
				Priority: "HIGH",
				TTL:      fcmAndroidTTL,
			},
		},
	})
	if err != nil {
		return err
	}
	url := f.endpoint + "/v1/projects/" + f.projectID + "/messages:send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("応答読み込み失敗: %w", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if isUnregistered(resp.StatusCode, respBody) {
		return &InvalidTokenError{Token: token, Status: resp.StatusCode, Reason: string(respBody)}
	}
	return fmt.Errorf("FCM が %d を返した: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
}

// isUnregistered は応答がトークン無効 (UNREGISTERED) かを判定する。
func isUnregistered(status int, body []byte) bool {
	var er fcmErrorResponse
	if err := json.Unmarshal(body, &er); err != nil {
		// JSON でなければ本文の文字列で判定する。
		return status == http.StatusNotFound && strings.Contains(string(body), "UNREGISTERED")
	}
	if er.Error.Status == "UNREGISTERED" {
		return true
	}
	for _, d := range er.Error.Details {
		if d.ErrorCode == "UNREGISTERED" {
			return true
		}
	}
	if status == http.StatusNotFound &&
		(strings.Contains(er.Error.Status, "UNREGISTERED") ||
			strings.Contains(er.Error.Message, "UNREGISTERED")) {
		return true
	}
	return false
}

// removeToken は無効トークンを削除先に伝える。
func (f *FCM) removeToken(token string) {
	if f.store == nil {
		return
	}
	_ = f.store.RemoveToken(token)
}

// maskToken はエラーメッセージ用にトークンを伏せる。
func maskToken(tok string) string {
	if len(tok) <= 8 {
		return "***"
	}
	return tok[:4] + "..." + tok[len(tok)-4:]
}

// Package push は push 送信の抽象化を行う。実送信 (FCM) は fcm.go にある。
// 端末の push トークンの永続化は internal/state (Store) が担当する。
package push

import "context"

// Payload は push で送る内容である。
type Payload struct {
	Type    string `json:"type"`
	CallID  string `json:"callId,omitempty"`
	From    string `json:"from,omitempty"`
	Display string `json:"display,omitempty"`
}

// Pusher は push 送信のインタフェースである。
type Pusher interface {
	Send(ctx context.Context, tokens []string, p Payload) error
}

// TokenRemover は無効になった push トークンの削除先である。
// internal/state.Store が実装する (RemoveToken)。
type TokenRemover interface {
	RemoveToken(token string) error
}

// Noop は何もしない Pusher (FCM 未設定時) である。
type Noop struct{}

// Send は常に nil を返す。
func (Noop) Send(_ context.Context, _ []string, _ Payload) error { return nil }

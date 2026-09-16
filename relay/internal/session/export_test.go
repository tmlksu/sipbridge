package session

// テスト専用のヘルパ (外部テストパッケージ session_test から
// 非公開の内部状態を確認するため)。

// SessionCount は現在の WS 接続総数である。
func (h *Hub) SessionCount() int { return h.sessionCount() }

// HasAccount は account のグループが起動中かを返す。
func (h *Hub) HasAccount(account string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.groups[account]
	return ok
}

// AccountRegistered は account の REGISTER 状態を返す。
func (h *Hub) AccountRegistered(account string) bool {
	h.mu.Lock()
	g := h.groups[account]
	h.mu.Unlock()
	return g != nil && g.registered()
}

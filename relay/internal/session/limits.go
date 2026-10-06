package session

// 乱用対策 (#30, #31): X-Device-Id の形式検証、オンライン端末数の上限、
// sip_account の試行回数制限 (全体トークンバケット)、端末 ID と認証主体
// (principal) の TOFU 結び付け。

import (
	"context"
	"crypto/subtle"
	"net/http"
	"sync"
	"time"
)

// 乱用対策の既定値。
const (
	DefaultMaxAccounts      = 16
	DefaultMaxStoredDevices = 64
	DefaultMaxOnlineDevices = 32
	// DefaultMaxConnsPerDevice は 1 つの端末 ID が同時に持てる WS 接続 (受け入れ途中を含む)
	// の上限である (#51)。正規の端末は新しい接続で古い接続を置換するので通常は 1 本だが、
	// 応答しない古い接続は close 待ち (最大 5 秒程度) の間残るため、張り直しが続いても
	// 弾かないよう 1 より大きくしておく。
	DefaultMaxConnsPerDevice = 4
	// DefaultAuthFailureDelay は sip_account のパスワード誤り (と試行制限) の
	// 応答を遅らせる時間である。1 接続は逐次処理なので、これが試行速度の上限になる。
	DefaultAuthFailureDelay = time.Second
	// DefaultMaxAuthFailures は 1 接続あたりの sip_account 失敗の許容回数である。
	// これに達したら接続を閉じる (アプリは 1 接続につき 1 回しか送らない)。
	DefaultMaxAuthFailures = 3
	// DefaultAttemptBurst / DefaultAttemptInterval は relay 全体の試行バケットである。
	// 「結び付いていない端末からの既存 account への sip_account」と
	// 「新規 account の作成」だけがトークンを消費する。既定は 10 回まで連続、
	// 以後 6 秒に 1 回 (= 毎分 10 回) 回復する。
	DefaultAttemptBurst    = 10
	DefaultAttemptInterval = 6 * time.Second
	// DefaultProvisionalGrace は仮の資格情報を認証拒否で取り消すまでの最短時間である
	// (最初の拒否から)。アプリに新しい内線を先に設定し、後から Asterisk 側に追加する
	// 運用の猶予。
	DefaultProvisionalGrace = 2 * time.Minute
	// maxDeviceIDLen は X-Device-Id の最大長である。
	maxDeviceIDLen = 64
	// pushUpdateMinInterval は保存済み端末の push トークン変更を受け付ける最小間隔である
	// (FCM トークンの更新は稀。連続更新で状態ファイル (microSD) を書かせない)。
	pushUpdateMinInterval = 10 * time.Second
)

// DeviceBindingMismatchBody は DEVICE_BINDING=enforce で端末 ID の principal が
// 記録と異なるときの HTTP 409 の本文である (docs/PROTOCOL.md)。
const DeviceBindingMismatchBody = "device_binding_mismatch"

// DEVICE_BINDING の値。
const (
	DeviceBindingOff     = "off"
	DeviceBindingWarn    = "warn"
	DeviceBindingEnforce = "enforce"
)

// error.code (docs/PROTOCOL.md)。
const (
	codePasswordMismatch = "account_password_mismatch"
	codeRateLimited      = "rate_limited"
	codeTooManyAccounts  = "too_many_accounts"
	codeTooManyDevices   = "too_many_devices"
)

// closeReasonTooManyFailures は sip_account の失敗が続いて閉じるときの理由である
// (クローズコードは 1008 policy violation)。
const closeReasonTooManyFailures = "too many sip_account failures"

// ValidDeviceID は X-Device-Id が ^[A-Za-z0-9._:-]{1,64}$ に合うかを返す。
// アプリは UUID v4、wsprobe は UUID か "wsprobe-150405.000"、ops は "e2e-reg"。
// ログ・状態ファイルに載るため、制御文字や長大な値を受け付けない。
func ValidDeviceID(id string) bool {
	if len(id) < 1 || len(id) > maxDeviceIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		b := id[i]
		switch {
		case 'a' <= b && b <= 'z', 'A' <= b && b <= 'Z', '0' <= b && b <= '9':
		case b == '.', b == '_', b == ':', b == '-':
		default:
			return false
		}
	}
	return true
}

// secretEqual はパスワードを定数時間で比較する。
func secretEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ---- principal (認証主体) ----

type principalCtxKey struct{}

// WithPrincipal は認証で得た principal を context に載せる (main の mux が
// Authenticate の結果を ServeWS へ渡すのに使う)。空なら ctx をそのまま返す。
func WithPrincipal(ctx context.Context, principal string) context.Context {
	if principal == "" {
		return ctx
	}
	return context.WithValue(ctx, principalCtxKey{}, principal)
}

// PrincipalFromContext は WithPrincipal で載せた principal を返す (無ければ空)。
func PrincipalFromContext(ctx context.Context) string {
	p, _ := ctx.Value(principalCtxKey{}).(string)
	return p
}

// ClientIP はログ用の接続元である。Cloudflare 経由なら CF-Connecting-IP、
// そうでなければ RemoteAddr。PII のため認証失敗・principal 不一致のときだけ出す。
func ClientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	return r.RemoteAddr
}

// checkDeviceBinding は端末 ID と principal の TOFU 結び付けを検査する。
// 拒否するなら HTTP ステータスと本文を返す (受け入れるなら 0)。
//
// 記録済み principal と異なる主体からの接続は、warn ならログのみ、enforce なら
// 409 device_binding_mismatch で拒否する (403 はアプリが Access の認証失敗と
// みなすため使わない)。拒否は addConn より前に行う: conns に入るとその端末は
// オンライン扱いになり、正規端末への着信 push が止まるため。未記録で端末が
// 保存済み (account / push あり) なら、この接続の principal を記録する。
//
// principal の無い接続は、RequirePrincipal (cf-access) かつ enforce なら 401 で
// 拒否する (sub も common_name も無い JWT では端末を区別できない)。
// AUTH_MODE=token / dev は RequirePrincipal が偽なので検査しない。
func (h *Hub) checkDeviceBinding(deviceID, principal, ip string) (status int, body string) {
	if h.cfg.DeviceBinding == DeviceBindingOff {
		return 0, ""
	}
	enforce := h.cfg.DeviceBinding == DeviceBindingEnforce
	if principal == "" {
		if enforce && h.cfg.RequirePrincipal {
			h.log.Warn("認証主体 (sub / common_name) の無い JWT を拒否", "device", deviceID, "ip", ip)
			return http.StatusUnauthorized, "unauthorized"
		}
		return 0, ""
	}
	pinned, err := h.store.PinDevicePrincipal(deviceID, principal)
	if err != nil {
		h.log.Warn("principal の保存に失敗", "device", deviceID, "err", err)
	}
	if pinned == "" || pinned == principal {
		return 0, ""
	}
	h.log.Warn("端末 ID の principal が記録と不一致", "device", deviceID,
		"principal", principal, "pinned", pinned, "ip", ip, "rejected", enforce,
		"hint", "正当な変更 (Service Token の作り直し等) なら docs/SECURITY.md §5 の手順で記録を消す")
	if enforce {
		return http.StatusConflict, DeviceBindingMismatchBody
	}
	return 0, ""
}

// pinPrincipal は端末が保存された直後 (sip_account / register_push) に
// principal を記録する (未記録のときだけ書く)。
func (h *Hub) pinPrincipal(c *Conn) {
	if c.principal == "" || h.cfg.DeviceBinding == DeviceBindingOff {
		return
	}
	if _, err := h.store.PinDevicePrincipal(c.deviceID, c.principal); err != nil {
		h.log.Warn("principal の保存に失敗", "device", c.deviceID, "err", err)
	}
}

// ---- オンライン端末数 ----

// reserveOnline の拒否理由。
type reserveResult int

const (
	reserveOK reserveResult = iota
	// reserveTooManyDevices は同時接続の端末数が MaxOnlineDevices に達している。
	reserveTooManyDevices
	// reserveTooManyConns はこの端末 ID の同時接続数が MaxConnsPerDevice に達している (#51)。
	reserveTooManyConns
)

// reserveOnline は接続を受け入れる前に端末の枠を予約する。既に接続中・
// 予約中・保存済み (既知) の端末は端末数の上限に達していても通す (再接続や
// replaceDuplicateConns による張り替えを弾かない)。ただし 1 つの端末 ID の
// 同時接続数 (接続中 + 受け入れ途中) は既知の端末でも MaxConnsPerDevice までに
// 制限する (#51: 保存済みの ID を名乗って WS を大量に張る FD 枯渇を防ぐ)。
// reserveOK なら addConn の後で release を呼ぶこと (Accept 失敗時も)。
func (h *Hub) reserveOnline(deviceID string) (release func(), res reserveResult) {
	_, known := h.store.Device(deviceID)
	h.mu.Lock()
	defer h.mu.Unlock()
	set, online := h.conns[deviceID]
	if len(set)+h.pending[deviceID] >= h.cfg.MaxConnsPerDevice {
		return nil, reserveTooManyConns
	}
	if !online && !known && h.pending[deviceID] == 0 && h.onlineCountLocked() >= h.cfg.MaxOnlineDevices {
		return nil, reserveTooManyDevices
	}
	h.pending[deviceID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.pending[deviceID]--; h.pending[deviceID] <= 0 {
				delete(h.pending, deviceID)
			}
		})
	}, reserveOK
}

// onlineCountLocked は接続中と受け入れ途中の端末 (deviceID) 数である (hub.mu 保持)。
func (h *Hub) onlineCountLocked() int {
	n := len(h.conns)
	for dev := range h.pending {
		if _, ok := h.conns[dev]; !ok {
			n++
		}
	}
	return n
}

// deviceRoomAvailable は deviceID を状態ファイルに保存できる見込みがあるか
// (既存端末・上限に空き・追い出せる端末がある) を返す。状態は変えない。
func (h *Hub) deviceRoomAvailable(deviceID string) bool {
	if h.store.CanAddDevice(deviceID) {
		return true
	}
	if h.cfg.DefaultAccount != "" {
		return false
	}
	online := h.onlineDevices()
	return h.store.HasEvictableUnbound(func(id string) bool { return online[id] || id == deviceID })
}

// ensureDeviceRoom は deviceID を状態ファイルに保存できるようにする。既存端末か
// 上限に空きがあれば true。満杯なら、既定アカウントが無い構成に限り、account の
// 無い (push 登録だけの) 最も古い端末を 1 台追い出して空きを作る。
// account の無い端末には着信 push が飛ばない (既定アカウントがある構成を除く)
// ため、ランダムな端末 ID の register_push で保存枠を恒久的に埋められないようにする。
// 接続中の端末と deviceID 自身は追い出さない。
func (h *Hub) ensureDeviceRoom(deviceID string) bool {
	if h.store.CanAddDevice(deviceID) {
		return true
	}
	if h.cfg.DefaultAccount != "" {
		return false // 既定アカウントでは account の無い端末にも push が飛ぶ
	}
	online := h.onlineDevices()
	evicted, ok := h.store.EvictOldestUnbound(func(id string) bool { return online[id] || id == deviceID })
	if !ok {
		return false
	}
	h.log.Info("保存端末数が上限のため account の無い古い端末を削除", "evicted", evicted, "device", deviceID)
	return h.store.CanAddDevice(deviceID)
}

// allowPushChange は保存済み端末の push トークン変更を受け付けてよいかを返す
// (同じ端末は pushUpdateMinInterval に 1 回まで)。受け付けるなら時刻を記録する。
func (h *Hub) allowPushChange(deviceID string) bool {
	h.pushMu.Lock()
	defer h.pushMu.Unlock()
	now := time.Now()
	if last, ok := h.pushChanged[deviceID]; ok && now.Sub(last) < pushUpdateMinInterval {
		return false
	}
	if len(h.pushChanged) > 256 {
		for id, t := range h.pushChanged {
			if now.Sub(t) >= pushUpdateMinInterval {
				delete(h.pushChanged, id)
			}
		}
	}
	h.pushChanged[deviceID] = now
	return true
}

// accountCount は起動中または保存済みの account 数である。
func (h *Hub) accountCount() int {
	names := make(map[string]struct{})
	for user := range h.store.Accounts() {
		names[user] = struct{}{}
	}
	h.mu.Lock()
	for user := range h.groups {
		names[user] = struct{}{}
	}
	h.mu.Unlock()
	return len(names)
}

// ---- 試行バケット ----

// tokenBucket は relay 全体の sip_account 試行を絞るトークンバケットである。
type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	burst    float64
	interval time.Duration // 1 トークンの回復時間
	last     time.Time
	now      func() time.Time
}

func newTokenBucket(burst int, interval time.Duration) *tokenBucket {
	return &tokenBucket{
		tokens: float64(burst), burst: float64(burst), interval: interval,
		last: time.Now(), now: time.Now,
	}
}

// take はトークンを 1 つ消費できれば true を返す。
func (b *tokenBucket) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if el := now.Sub(b.last); el > 0 {
		b.tokens += float64(el) / float64(b.interval)
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

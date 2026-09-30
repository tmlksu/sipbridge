// Package state は relay の永続状態 (SIP アカウントと端末の結び付け、
// push トークン) を 1 つの JSON ファイルで管理する。
//
// ファイル形式 (version 2, docs/PROTOCOL.md「SIP アカウント」節):
//
//	{
//	  "version": 2,
//	  "accounts": {"101": {"password": "…", "display": "…"}},
//	  "devices":  {"<deviceId>": {"account": "101", "push": {"provider":"fcm","token":"…"}, "principal": "…"}}
//	}
//
// 旧形式 (`{"<deviceId>": {"provider": "fcm", "token": "…"}}`, T1/T5 の
// PUSH_STATE_FILE) は読み込み時に自動移行する。
//
// 書き込みは 0600 の一時ファイル → rename で原子的に行う。
// path が空ならメモリのみで動作する (テスト用)。
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Version は現在の状態ファイル形式のバージョンである。
const Version = 2

// Account は SIP アカウント (内線) の資格情報である。
type Account struct {
	Password string `json:"password"`
	Display  string `json:"display,omitempty"`
}

// Push は端末の push 登録である。
type Push struct {
	Provider string `json:"provider"`
	Token    string `json:"token"`
}

// Device は端末ごとの状態 (結び付いた account と push 登録) である。
type Device struct {
	Account string `json:"account,omitempty"`
	Push    *Push  `json:"push,omitempty"`
	// Principal はこの端末 ID を最初に使った認証主体 (Cloudflare Access JWT の
	// sub、Service Token なら common_name) である (TOFU, DEVICE_BINDING)。
	// 端末の記録 (account / push) と一緒に消える。
	Principal string `json:"principal,omitempty"`
	// Created は端末の記録を作った時刻 (Unix 秒) である。保存数の上限で
	// account の無い古い端末を追い出す順序に使う (0 = 記録前からある端末)。
	Created int64 `json:"created,omitempty"`
}

// ErrTooManyDevices は新しい端末を保存しようとして上限 (SetMaxDevices) を
// 超えたときに返る。既存端末の更新では返らない。
var ErrTooManyDevices = errors.New("保存できる端末数の上限に達した")

// fileFormat は状態ファイルの JSON 表現である。
type fileFormat struct {
	Version  int                `json:"version"`
	Accounts map[string]Account `json:"accounts"`
	Devices  map[string]Device  `json:"devices"`
}

// Store は状態を保持し、変更のたびにファイルへ書き戻す。
// すべてのメソッドは並行に呼んでよい。
type Store struct {
	path string

	mu       sync.Mutex
	accounts map[string]Account
	devices  map[string]Device
	migrated bool // 旧形式から移行した (起動ログ用)
	// maxDevices は新規に保存できる端末数の上限 (0 = 無制限)。
	// 起動時の読み込みには適用しない (超過していても全件読む)。
	maxDevices int
}

// New は Store を作り、既存ファイルがあれば読み込む (旧形式は移行する)。
func New(path string) (*Store, error) {
	s := &Store{
		path:     path,
		accounts: make(map[string]Account),
		devices:  make(map[string]Device),
	}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("状態ファイル読み込み失敗: %w", err)
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := s.load(data); err != nil {
		return nil, err
	}
	if s.migrated {
		// 旧形式を読んだら新形式で書き戻しておく (次回起動は移行不要)。
		s.mu.Lock()
		err := s.saveLocked()
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

// SetMaxDevices は新規に保存できる端末数の上限を設定する (0 以下 = 無制限)。
// 既に保存済みの端末は上限を超えていても消さない。
func (s *Store) SetMaxDevices(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 0 {
		n = 0
	}
	s.maxDevices = n
}

// CanAddDevice は deviceID を (新規または既存として) 保存できるかを返す。
// 既存端末なら常に true。新規なら上限に空きがあるときだけ true。
func (s *Store) CanAddDevice(deviceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.canAddLocked(deviceID)
}

func (s *Store) canAddLocked(deviceID string) bool {
	if _, ok := s.devices[deviceID]; ok {
		return true
	}
	return s.maxDevices <= 0 || len(s.devices) < s.maxDevices
}

// Migrated は旧形式 (push トークンのみ) から移行したかを返す。
func (s *Store) Migrated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.migrated
}

// load は JSON を解釈する。version が無ければ旧形式として移行する。
func (s *Store) load(data []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("状態ファイル解析失敗: %w", err)
	}
	if _, ok := probe["version"]; !ok {
		return s.loadLegacy(data)
	}
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("状態ファイル解析失敗: %w", err)
	}
	if f.Version != Version {
		return fmt.Errorf("未知の状態ファイル version %d (対応は %d)", f.Version, Version)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range f.Accounts {
		s.accounts[k] = v
	}
	for k, v := range f.Devices {
		s.devices[k] = v
	}
	return nil
}

// loadLegacy は旧形式 ({deviceId: {provider, token}}) を読み込む。
// account の結び付けは存在しないため push 登録だけを引き継ぐ。
func (s *Store) loadLegacy(data []byte) error {
	var old map[string]Push
	if err := json.Unmarshal(data, &old); err != nil {
		return fmt.Errorf("旧形式の状態ファイル解析失敗: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for dev, p := range old {
		if p.Token == "" {
			continue
		}
		cp := p
		s.devices[dev] = Device{Push: &cp}
	}
	s.migrated = true
	return nil
}

// ---- account ----

// Accounts は全 account の複製を返す。
func (s *Store) Accounts() map[string]Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Account, len(s.accounts))
	for k, v := range s.accounts {
		out[k] = v
	}
	return out
}

// Account は user の account を返す。
func (s *Store) Account(user string) (Account, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[user]
	return a, ok
}

// SetAccount は account を保存する。
func (s *Store) SetAccount(user string, a Account) error {
	if user == "" {
		return fmt.Errorf("account の user が空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 変化が無ければ書かない (R2S は microSD なので無駄な書き込みを避ける)。
	if cur, ok := s.accounts[user]; ok && cur == a {
		return nil
	}
	s.accounts[user] = a
	return s.saveLocked()
}

// RemoveAccount は account を削除し、結び付いていた端末の account を空にする。
func (s *Store) RemoveAccount(user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.accounts, user)
	for id, d := range s.devices {
		if d.Account == user {
			d.Account = ""
			s.devices[id] = d
		}
	}
	return s.saveLocked()
}

// ---- device ----

// Devices は全端末の複製を返す。
func (s *Store) Devices() map[string]Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.devicesLocked()
}

func (s *Store) devicesLocked() map[string]Device {
	out := make(map[string]Device, len(s.devices))
	for k, v := range s.devices {
		out[k] = cloneDevice(v)
	}
	return out
}

func cloneDevice(d Device) Device {
	if d.Push != nil {
		p := *d.Push
		d.Push = &p
	}
	return d
}

// Device は端末の状態を返す。
func (s *Store) Device(deviceID string) (Device, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[deviceID]
	return cloneDevice(d), ok
}

// DevicesForAccount は指定 account に結び付いた端末の複製を返す。
func (s *Store) DevicesForAccount(user string) map[string]Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Device)
	if user == "" {
		return out
	}
	for id, d := range s.devices {
		if d.Account == user {
			out[id] = cloneDevice(d)
		}
	}
	return out
}

// CountDevicesForAccount は指定 account に結び付いた端末数を返す。
func (s *Store) CountDevicesForAccount(user string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if user == "" {
		return 0
	}
	n := 0
	for _, d := range s.devices {
		if d.Account == user {
			n++
		}
	}
	return n
}

// SetDeviceAccount は端末と account の結び付けを保存する (user="" で解除)。
func (s *Store) SetDeviceAccount(deviceID, user string) error {
	if deviceID == "" {
		return fmt.Errorf("deviceId が空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.devices[deviceID]
	if d.Account == user {
		return nil
	}
	if user != "" && !s.canAddLocked(deviceID) {
		return ErrTooManyDevices
	}
	if _, ok := s.devices[deviceID]; !ok {
		d.Created = time.Now().Unix()
	}
	d.Account = user
	if d.Account == "" && d.Push == nil {
		delete(s.devices, deviceID)
	} else {
		s.devices[deviceID] = d
	}
	return s.saveLocked()
}

// SetDevicePush は端末の push 登録を保存する。
func (s *Store) SetDevicePush(deviceID string, p Push) error {
	if deviceID == "" {
		return fmt.Errorf("deviceId が空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.devices[deviceID]
	if d.Push != nil && *d.Push == p {
		return nil
	}
	if !s.canAddLocked(deviceID) {
		return ErrTooManyDevices
	}
	if _, ok := s.devices[deviceID]; !ok {
		d.Created = time.Now().Unix()
	}
	cp := p
	d.Push = &cp
	s.devices[deviceID] = d
	return s.saveLocked()
}

// PinDevicePrincipal は端末の principal を TOFU で記録し、記録済みの値を返す。
//
//   - principal が空、または端末の記録 (account / push) が無ければ何もしない ("" を返す)。
//     接続しただけの端末 (wsprobe の使い捨て ID 等) で状態ファイルを増やさないため、
//     記録は account の結び付けか push 登録で端末が保存されたときから始める。
//   - 記録済みならそれを返す (上書きしない。呼び出し側が不一致を判定する)。
//   - 未記録なら principal を記録して保存し、それを返す。
//
// 書き込みは値が変わるときだけ行う (microSD 配慮)。
func (s *Store) PinDevicePrincipal(deviceID, principal string) (string, error) {
	if deviceID == "" || principal == "" {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[deviceID]
	if !ok {
		return "", nil
	}
	if d.Principal != "" {
		return d.Principal, nil
	}
	d.Principal = principal
	s.devices[deviceID] = d
	return principal, s.saveLocked()
}

// EvictOldestUnbound は account に結び付いていない端末のうち最も古い
// (Created が小さい、同じなら ID が小さい) 1 台を削除し、その ID を返す。
// skip が true を返す端末は対象外。対象が無ければ ("", false)。
func (s *Store) EvictOldestUnbound(skip func(id string) bool) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	victim := s.oldestUnboundLocked(skip)
	if victim == "" {
		return "", false
	}
	delete(s.devices, victim)
	_ = s.saveLocked() // 失敗してもメモリ上は削除済み (次の書き込みで反映される)
	return victim, true
}

// HasEvictableUnbound は EvictOldestUnbound で追い出せる端末があるかを返す (状態は変えない)。
func (s *Store) HasEvictableUnbound(skip func(id string) bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.oldestUnboundLocked(skip) != ""
}

// oldestUnboundLocked は account の無い最も古い端末の ID を返す (無ければ "")。
func (s *Store) oldestUnboundLocked(skip func(id string) bool) string {
	victim := ""
	var victimAt int64
	for id, d := range s.devices {
		if d.Account != "" || (skip != nil && skip(id)) {
			continue
		}
		if victim == "" || d.Created < victimAt || (d.Created == victimAt && id < victim) {
			victim, victimAt = id, d.Created
		}
	}
	return victim
}

// RemoveDevice は端末の情報をすべて削除する。
func (s *Store) RemoveDevice(deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.devices[deviceID]; !ok {
		return nil
	}
	delete(s.devices, deviceID)
	return s.saveLocked()
}

// RemoveToken は指定 push トークンを持つ端末の push 登録を削除する。
// FCM が UNREGISTERED を返したときの掃除用 (push.TokenRemover の実装)。
func (s *Store) RemoveToken(token string) error {
	if token == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for id, d := range s.devices {
		if d.Push == nil || d.Push.Token != token {
			continue
		}
		d.Push = nil
		if d.Account == "" {
			delete(s.devices, id)
		} else {
			s.devices[id] = d
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return s.saveLocked()
}

// ---- 永続化 ----

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("状態ディレクトリ作成失敗: %w", err)
		}
	}
	f := fileFormat{
		Version:  Version,
		Accounts: s.accounts,
		Devices:  s.devices,
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("状態ファイル書き込み失敗: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("状態ファイル確定失敗: %w", err)
	}
	return nil
}

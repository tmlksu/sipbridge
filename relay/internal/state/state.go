// Package state は relay の永続状態 (SIP アカウントと端末の結び付け、
// push トークン) を 1 つの JSON ファイルで管理する。
//
// ファイル形式 (version 2, docs/PROTOCOL.md「SIP アカウント」節):
//
//	{
//	  "version": 2,
//	  "accounts": {"101": {"password": "…", "display": "…"}},
//	  "devices":  {"<deviceId>": {"account": "101", "push": {"provider":"fcm","token":"…"}}}
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
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
}

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
	cp := p
	d.Push = &cp
	s.devices[deviceID] = d
	return s.saveLocked()
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

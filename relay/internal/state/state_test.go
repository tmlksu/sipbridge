package state_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tmlksu/sipbridge/relay/internal/state"
)

func TestMemoryStore(t *testing.T) {
	s, err := state.New("")
	if err != nil {
		t.Fatalf("New 失敗: %v", err)
	}
	if err := s.SetAccount("101", state.Account{Password: "pw", Display: "居間"}); err != nil {
		t.Fatalf("SetAccount 失敗: %v", err)
	}
	if err := s.SetDeviceAccount("dev-A", "101"); err != nil {
		t.Fatalf("SetDeviceAccount 失敗: %v", err)
	}
	if err := s.SetDevicePush("dev-A", state.Push{Provider: "fcm", Token: "tok-A"}); err != nil {
		t.Fatalf("SetDevicePush 失敗: %v", err)
	}
	a, ok := s.Account("101")
	if !ok || a.Password != "pw" || a.Display != "居間" {
		t.Errorf("Account が不正: %+v %v", a, ok)
	}
	if n := s.CountDevicesForAccount("101"); n != 1 {
		t.Errorf("CountDevicesForAccount = %d (1 のはず)", n)
	}
	devs := s.DevicesForAccount("101")
	if d, ok := devs["dev-A"]; !ok || d.Push == nil || d.Push.Token != "tok-A" {
		t.Errorf("DevicesForAccount が不正: %+v", devs)
	}
	// 結び付け解除しても push は残る。
	if err := s.SetDeviceAccount("dev-A", ""); err != nil {
		t.Fatalf("解除失敗: %v", err)
	}
	if n := s.CountDevicesForAccount("101"); n != 0 {
		t.Errorf("解除後の端末数 = %d", n)
	}
	if d, ok := s.Device("dev-A"); !ok || d.Push == nil || d.Account != "" {
		t.Errorf("解除後の device が不正: %+v %v", d, ok)
	}
	// RemoveToken は push を消し、account も無ければ端末ごと消える。
	if err := s.RemoveToken("tok-A"); err != nil {
		t.Fatalf("RemoveToken 失敗: %v", err)
	}
	if _, ok := s.Device("dev-A"); ok {
		t.Errorf("RemoveToken 後も device が残っている")
	}
}

func TestRemoveAccountClearsBinding(t *testing.T) {
	s, _ := state.New("")
	_ = s.SetAccount("102", state.Account{Password: "pw"})
	_ = s.SetDeviceAccount("dev-B", "102")
	if err := s.RemoveAccount("102"); err != nil {
		t.Fatalf("RemoveAccount 失敗: %v", err)
	}
	if _, ok := s.Account("102"); ok {
		t.Errorf("account が残っている")
	}
	if d, ok := s.Device("dev-B"); ok && d.Account != "" {
		t.Errorf("端末の結び付けが残っている: %+v", d)
	}
}

func TestPersistAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json")
	s, err := state.New(path)
	if err != nil {
		t.Fatalf("New 失敗: %v", err)
	}
	_ = s.SetAccount("101", state.Account{Password: "pw101", Display: "居間"})
	_ = s.SetDeviceAccount("dev-A", "101")
	if err := s.SetDevicePush("dev-A", state.Push{Provider: "fcm", Token: "tok-A"}); err != nil {
		t.Fatalf("SetDevicePush 失敗: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat 失敗: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("パーミッション = %v (0600 のはず)", fi.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("一時ファイルが残っている")
	}

	s2, err := state.New(path)
	if err != nil {
		t.Fatalf("再読み込み失敗: %v", err)
	}
	if s2.Migrated() {
		t.Errorf("version 2 を読んで移行扱いになっている")
	}
	a, ok := s2.Account("101")
	if !ok || a.Password != "pw101" {
		t.Errorf("account が永続化されていない: %+v", a)
	}
	d, ok := s2.Device("dev-A")
	if !ok || d.Account != "101" || d.Push == nil || d.Push.Token != "tok-A" {
		t.Errorf("device が永続化されていない: %+v", d)
	}
}

// TestLegacyMigration は旧形式 ({deviceId: {provider, token}}) の自動移行である。
func TestLegacyMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push_state.json")
	legacy := `{"dev-A":{"provider":"fcm","token":"tok-A"},"dev-B":{"provider":"fcm","token":"tok-B"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("旧ファイル作成失敗: %v", err)
	}
	s, err := state.New(path)
	if err != nil {
		t.Fatalf("New 失敗: %v", err)
	}
	if !s.Migrated() {
		t.Errorf("移行フラグが立っていない")
	}
	for dev, tok := range map[string]string{"dev-A": "tok-A", "dev-B": "tok-B"} {
		d, ok := s.Device(dev)
		if !ok || d.Push == nil || d.Push.Token != tok || d.Account != "" {
			t.Errorf("%s の移行が不正: %+v %v", dev, d, ok)
		}
	}
	if len(s.Accounts()) != 0 {
		t.Errorf("旧形式に account は無いはず: %+v", s.Accounts())
	}
	// 新形式で書き戻されている。
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("読み込み失敗: %v", err)
	}
	var f struct {
		Version int                       `json:"version"`
		Devices map[string]map[string]any `json:"devices"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("書き戻しの解析失敗: %v", err)
	}
	if f.Version != state.Version {
		t.Errorf("version = %d (%d のはず)", f.Version, state.Version)
	}
	if len(f.Devices) != 2 {
		t.Errorf("devices = %+v", f.Devices)
	}
	// 再読み込みでは移行扱いにならない。
	s2, err := state.New(path)
	if err != nil {
		t.Fatalf("再読み込み失敗: %v", err)
	}
	if s2.Migrated() {
		t.Errorf("2 回目も移行扱いになっている")
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := state.New(broken); err == nil {
		t.Errorf("壊れた JSON でもエラーにならない")
	}
	future := filepath.Join(dir, "future.json")
	if err := os.WriteFile(future, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := state.New(future); err == nil {
		t.Errorf("未知の version でもエラーにならない")
	}
	// 存在しないファイルは空の Store になる。
	s, err := state.New(filepath.Join(dir, "missing.json"))
	if err != nil {
		t.Fatalf("存在しないファイルでエラー: %v", err)
	}
	if len(s.Accounts()) != 0 || len(s.Devices()) != 0 {
		t.Errorf("空でない: %+v %+v", s.Accounts(), s.Devices())
	}
}

// TestSkipUnchangedWrite は内容が変わらない保存でファイルを書かないことの確認である。
// 保存後にファイルを消し、同じ値で呼んでも再作成されなければ書いていない。
func TestSkipUnchangedWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := state.New(path)
	if err != nil {
		t.Fatalf("New 失敗: %v", err)
	}
	acc := state.Account{Password: "pw101", Display: "居間"}
	push := state.Push{Provider: "fcm", Token: "tok-A"}
	if err := s.SetAccount("101", acc); err != nil {
		t.Fatalf("SetAccount 失敗: %v", err)
	}
	if err := s.SetDevicePush("dev-A", push); err != nil {
		t.Fatalf("SetDevicePush 失敗: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove 失敗: %v", err)
	}

	if err := s.SetAccount("101", acc); err != nil {
		t.Fatalf("SetAccount (同値) 失敗: %v", err)
	}
	if err := s.SetDevicePush("dev-A", push); err != nil {
		t.Fatalf("SetDevicePush (同値) 失敗: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("同じ値の保存でファイルが書かれた (err=%v)", err)
	}

	// 値が変われば書く。
	if err := s.SetDevicePush("dev-A", state.Push{Provider: "fcm", Token: "tok-B"}); err != nil {
		t.Fatalf("SetDevicePush (変更) 失敗: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("変更後にファイルが無い: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove 失敗: %v", err)
	}
	if err := s.SetAccount("101", state.Account{Password: "pw101", Display: "台所"}); err != nil {
		t.Fatalf("SetAccount (変更) 失敗: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("account 変更後にファイルが無い: %v", err)
	}
}

// TestMaxDevices は新規端末の保存だけが上限で拒否され、既存端末の更新と
// 起動時の読み込み (上限超過のファイル) は通ることを確認する。
func TestMaxDevices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := state.New(path)
	if err != nil {
		t.Fatalf("New 失敗: %v", err)
	}
	for _, id := range []string{"dev-1", "dev-2", "dev-3"} {
		if err := s.SetDevicePush(id, state.Push{Provider: "fcm", Token: "tok-" + id}); err != nil {
			t.Fatalf("SetDevicePush %s 失敗: %v", id, err)
		}
	}
	s.SetMaxDevices(2) // 既に 3 台 (上限超過) の状態で上限を設定する
	if err := s.SetDevicePush("dev-4", state.Push{Provider: "fcm", Token: "tok-4"}); !errors.Is(err, state.ErrTooManyDevices) {
		t.Errorf("新規端末の push 保存 err = %v (ErrTooManyDevices のはず)", err)
	}
	if err := s.SetDeviceAccount("dev-4", "101"); !errors.Is(err, state.ErrTooManyDevices) {
		t.Errorf("新規端末の結び付け err = %v (ErrTooManyDevices のはず)", err)
	}
	if s.CanAddDevice("dev-4") {
		t.Errorf("CanAddDevice(新規) は false のはず")
	}
	// 既存端末の更新は上限超過でも通る。
	if !s.CanAddDevice("dev-1") {
		t.Errorf("CanAddDevice(既存) は true のはず")
	}
	if err := s.SetDeviceAccount("dev-1", "101"); err != nil {
		t.Errorf("既存端末の結び付けが拒否された: %v", err)
	}
	if err := s.SetDevicePush("dev-2", state.Push{Provider: "fcm", Token: "tok-new"}); err != nil {
		t.Errorf("既存端末の push 更新が拒否された: %v", err)
	}
	// 解除 (user="") は未知の端末でもエラーにしない (何も保存しない)。
	if err := s.SetDeviceAccount("dev-9", ""); err != nil {
		t.Errorf("未知端末の解除がエラー: %v", err)
	}

	// 上限超過のファイルでも起動時は全件読む。
	s2, err := state.New(path)
	if err != nil {
		t.Fatalf("再読み込み失敗: %v", err)
	}
	s2.SetMaxDevices(1)
	if n := len(s2.Devices()); n != 3 {
		t.Errorf("読み込んだ端末数 = %d (3 のはず)", n)
	}
}

// TestPinDevicePrincipal は TOFU の記録規則 (記録の無い端末は記録しない、
// 記録済みは上書きしない、同値では書かない) を確認する。
func TestPinDevicePrincipal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := state.New(path)
	if err != nil {
		t.Fatalf("New 失敗: %v", err)
	}
	// 記録の無い端末 (接続しただけ) は記録しない。
	if got, err := s.PinDevicePrincipal("dev-A", "alice"); err != nil || got != "" {
		t.Fatalf("記録の無い端末: got=%q err=%v", got, err)
	}
	if _, ok := s.Device("dev-A"); ok {
		t.Fatalf("記録の無い端末が作られた")
	}
	if err := s.SetDevicePush("dev-A", state.Push{Provider: "fcm", Token: "tok"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PinDevicePrincipal("dev-A", "alice"); err != nil || got != "alice" {
		t.Fatalf("初回記録: got=%q err=%v", got, err)
	}
	// 記録済みは上書きしない。
	if got, _ := s.PinDevicePrincipal("dev-A", "mallory"); got != "alice" {
		t.Errorf("記録済み principal が上書きされた: %q", got)
	}
	// principal 空 (AUTH_MODE=token) は何もしない。
	if got, _ := s.PinDevicePrincipal("dev-A", ""); got != "" {
		t.Errorf("空 principal で %q が返った", got)
	}
	// 同値なら書かない。
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PinDevicePrincipal("dev-A", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("同じ principal でファイルが書かれた (err=%v)", err)
	}
	// 永続化されている。
	if err := s.SetDevicePush("dev-A", state.Push{Provider: "fcm", Token: "tok2"}); err != nil {
		t.Fatal(err)
	}
	s2, err := state.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := s2.Device("dev-A"); d.Principal != "alice" {
		t.Errorf("principal が永続化されていない: %+v", d)
	}
}

// TestEvictOldestUnbound は account に結び付いていない最も古い端末だけを
// 追い出すことを確認する (skip は対象外、account 付きは対象外)。
func TestEvictOldestUnbound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := state.New(path)
	if err != nil {
		t.Fatal(err)
	}
	// 作成時刻を制御するため、Created 付きの状態ファイルを読み込ませる。
	data := `{"version":2,"accounts":{"101":{"password":"pw"}},"devices":{
		"dev-old":   {"push":{"provider":"fcm","token":"a"},"created":100},
		"dev-older": {"push":{"provider":"fcm","token":"b"},"created":50},
		"dev-legacy":{"push":{"provider":"fcm","token":"c"}},
		"dev-bound": {"account":"101","created":1}
	}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err = state.New(path); err != nil {
		t.Fatal(err)
	}
	skip := func(id string) bool { return id == "dev-legacy" }
	if id, ok := s.EvictOldestUnbound(skip); !ok || id != "dev-older" {
		t.Fatalf("追い出し = %q %v (dev-older のはず)", id, ok)
	}
	if id, ok := s.EvictOldestUnbound(nil); !ok || id != "dev-legacy" {
		t.Fatalf("追い出し = %q %v (created 無しの dev-legacy が最古のはず)", id, ok)
	}
	if id, ok := s.EvictOldestUnbound(nil); !ok || id != "dev-old" {
		t.Fatalf("追い出し = %q %v (dev-old のはず)", id, ok)
	}
	if id, ok := s.EvictOldestUnbound(nil); ok {
		t.Fatalf("account 付きの端末が追い出された: %q", id)
	}
	// 永続化されている。
	s2, err := state.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(s2.Devices()); n != 1 {
		t.Errorf("残った端末数 = %d (1 のはず)", n)
	}
	// 新しく作った端末には作成時刻が入る。
	if err := s2.SetDevicePush("dev-new", state.Push{Provider: "fcm", Token: "n"}); err != nil {
		t.Fatal(err)
	}
	if d, _ := s2.Device("dev-new"); d.Created == 0 {
		t.Errorf("Created が入っていない: %+v", d)
	}
}

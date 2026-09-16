package state_test

import (
	"encoding/json"
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

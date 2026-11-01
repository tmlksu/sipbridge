package sipua_test

import (
	"testing"

	"github.com/tmlksu/sipbridge/relay/sipua"
	"github.com/tmlksu/sipbridge/relay/sipua/sipuatest"
)

// 公開窓口の型が満たすべき契約をコンパイル時に確かめる。
var (
	_ sipua.Backend = (*sipua.UA)(nil)
	_ sipua.Backend = (*sipuatest.Fake)(nil)
	_ sipua.Event   = sipua.EvIncoming{}
	_ sipua.Event   = sipua.EvRegistered{}
	_ sipua.Event   = sipua.EvRinging{}
	_ sipua.Event   = sipua.EvAnswered{}
	_ sipua.Event   = sipua.EvEnded{}
)

func TestValidateUser(t *testing.T) {
	if err := sipua.ValidateUser("2180"); err != nil {
		t.Fatalf("ValidateUser(2180): %v", err)
	}
	if err := sipua.ValidateUser("21\r\n80"); err == nil {
		t.Fatal("改行入りのユーザー名を通した")
	}
}

func TestParseTrustedSources(t *testing.T) {
	p, err := sipua.ParseTrustedSources("172.17.0.0/16")
	if err != nil || len(p) != 1 {
		t.Fatalf("ParseTrustedSources = %v, %v", p, err)
	}
}

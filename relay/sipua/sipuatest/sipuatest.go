// Package sipuatest は sipua を使う側のテスト用に、偽の Backend を出す。
// 外から InjectIncoming で着信を起こせ、応答後の RTP パイプは送ったものをそのまま返す (エコー)。
package sipuatest

import "github.com/tmlksu/sipbridge/relay/internal/fakebackend"

// Fake は偽の Backend (sipua.Backend を満たす)。
type Fake = fakebackend.Fake

// NewFake は登録済みの状態で始まる偽の Backend を作る。
func NewFake() *Fake { return fakebackend.New() }

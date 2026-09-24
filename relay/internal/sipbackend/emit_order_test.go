// Package sipbackend の emit 順序テスト (R6)。
// emit を Incoming → Ended の順に大量に連続で呼び、受信側で順序が保たれることを確かめる。
package sipbackend

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tmlksu/sipbridge/relay/internal/call"
)

// TestEmitPreservesOrder は 1000 組の Incoming → Ended を連続で積み、
// FIFO 順に届くことを確認する。旧実装 (イベントごとに goroutine) では
// 速い Ended が Incoming を追い越して順序が崩れた。
func TestEmitPreservesOrder(t *testing.T) {
	be, err := New(Config{
		SIPHost:    "127.0.0.1",
		SIPPort:    5060,
		User:       "101",
		RTPPortMin: 35000,
		RTPPortMax: 35020,
	}, nil)
	if err != nil {
		t.Fatalf("New 失敗: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev := make(chan call.Event, 4096)
	be.mu.Lock()
	be.ev = ev
	be.ctx = ctx
	be.mu.Unlock()
	go be.emitLoop(ev, ctx)

	const pairs = 1000
	for i := 0; i < pairs; i++ {
		id := fmt.Sprintf("order-%d", i)
		be.emit(call.EvIncoming{CallID: id, From: "tester"})
		be.emit(call.EvEnded{CallID: id, Reason: "cancel", Code: 487})
	}

	deadline := time.After(15 * time.Second)
	for i := 0; i < pairs; i++ {
		id := fmt.Sprintf("order-%d", i)
		select {
		case got := <-ev:
			inc, ok := got.(call.EvIncoming)
			if !ok {
				t.Fatalf("#%d: Incoming のはずが %T (%+v)", i, got, got)
			}
			if inc.CallID != id {
				t.Fatalf("#%d: Incoming.CallID = %q (%q のはず)", i, inc.CallID, id)
			}
		case <-deadline:
			t.Fatalf("#%d: Incoming が届かない", i)
		}
		select {
		case got := <-ev:
			end, ok := got.(call.EvEnded)
			if !ok {
				t.Fatalf("#%d: Ended のはずが %T (%+v)", i, got, got)
			}
			if end.CallID != id {
				t.Fatalf("#%d: Ended.CallID = %q (%q のはず)", i, end.CallID, id)
			}
		case <-deadline:
			t.Fatalf("#%d: Ended が届かない", i)
		}
	}
	select {
	case extra := <-ev:
		t.Fatalf("余計なイベント: %+v", extra)
	default:
	}
}

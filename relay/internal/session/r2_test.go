// R2 のテスト (同一端末の重複接続の置換 + 半死に WS の push 救済)。
// 既存の流儀 (fakebackend + httptest + session_test ヘルパ) に合わせる。
package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tmlksu/sipbridge/relay/internal/proto"
	"github.com/tmlksu/sipbridge/relay/internal/push"
	"github.com/tmlksu/sipbridge/relay/internal/session"
)

// TestDuplicateConnectionReplacesOld は、同じ device で 2 本目を接続すると
// 1 本目がコード 4001/replaced で閉じられ、2 本目は引き続き incoming を
// 受け取れることを確認する。
func TestDuplicateConnectionReplacesOld(t *testing.T) {
	fx := newFixture(t)
	ws1 := dial(t, fx.url, "dev-DUP")
	if _, ok := readJSON(t, ws1).(*proto.Hello); !ok {
		t.Fatalf("1 本目に hello が来ない")
	}

	ws2 := dial(t, fx.url, "dev-DUP")
	defer ws2.Close(websocket.StatusNormalClosure, "")
	if _, ok := readJSON(t, ws2).(*proto.Hello); !ok {
		t.Fatalf("2 本目に hello が来ない")
	}

	// 1 本目は置換クローズされる。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := ws1.Read(ctx)
	if err == nil {
		t.Fatalf("1 本目が閉じられていない")
	}
	if got := websocket.CloseStatus(err); got != websocket.StatusCode(4001) {
		t.Fatalf("1 本目の close コード = %v (4001 のはず, err=%v)", got, err)
	}
	waitSessions(t, fx, 1, 3*time.Second)

	// 2 本目は生きて着信を受け取れる。
	id := fx.fb(t).InjectIncoming("102", "Bob", 0)
	inc, ok := readJSON(t, ws2).(*proto.Incoming)
	if !ok || inc.CallID != id {
		t.Fatalf("2 本目に incoming が来ない: %+v", inc)
	}
}

// TestDuplicateConnectionDuringCallKeepsCall は、通話中 (answer 済み) の勝者
// デバイスが再接続しても、1 本目の切断で ended/timeout にならず、2 本目で
// バイナリ往復できることを確認する。ResumeTimeout を過ぎても切れないこと。
func TestDuplicateConnectionDuringCallKeepsCall(t *testing.T) {
	const resume = 300 * time.Millisecond
	fx := newFixtureCfg(t, push.Noop{}, nil, session.Config{
		DefaultAccount:  defaultTestAccount,
		DefaultPassword: defaultTestPassword,
		ResumeTimeout:   resume,
	})
	fx.waitRegistered(t, defaultTestAccount)

	ws1 := dial(t, fx.url, "dev-WIN")
	if _, ok := readJSON(t, ws1).(*proto.Hello); !ok {
		t.Fatalf("1 本目に hello が来ない")
	}
	id := fx.fb(t).InjectIncoming("102", "Bob", 0)
	inc, ok := readJSON(t, ws1).(*proto.Incoming)
	if !ok || inc.CallID != id {
		t.Fatalf("1 本目に incoming が来ない: %+v", inc)
	}
	writeJSON(t, ws1, &proto.Answer{T: proto.TAnswer, CallID: id})
	if ans, ok := readJSON(t, ws1).(*proto.Answered); !ok || ans.CallID != id {
		t.Fatalf("1 本目に answered が来ない: %+v", ans)
	}

	// 同じ device で張り替え。
	ws2 := dial(t, fx.url, "dev-WIN")
	defer ws2.Close(websocket.StatusNormalClosure, "")
	hello, ok := readJSON(t, ws2).(*proto.Hello)
	if !ok {
		t.Fatalf("2 本目に hello が来ない")
	}
	if hello.Call == nil || hello.Call.CallID != id || hello.Call.State != "active" {
		t.Fatalf("2 本目の hello.call が通話継続になっていない: %+v", hello.Call)
	}

	// 1 本目は置換で閉じられる。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err := ws1.Read(ctx)
	cancel()
	if err == nil {
		t.Fatalf("1 本目が閉じられていない")
	}
	if got := websocket.CloseStatus(err); got != websocket.StatusCode(4001) {
		t.Fatalf("1 本目の close コード = %v (4001 のはず, err=%v)", got, err)
	}

	// 2 本目でバイナリ往復 (fake はエコー)。
	rtp := []byte{0x80, 0x00, 0x12, 0x34, 1, 2, 3, 4}
	wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wcancel()
	if err := ws2.Write(wctx, websocket.MessageBinary, rtp); err != nil {
		t.Fatalf("2 本目のバイナリ送信失敗: %v", err)
	}
	typ, echo := readMsg(t, ws2)
	if typ != websocket.MessageBinary || string(echo) != string(rtp) {
		t.Fatalf("2 本目のエコーが戻らない: typ=%v data=%v", typ, echo)
	}

	// 猶予を過ぎても切れないこと (古い接続の detach でタイマが張られていない)。
	// 注意: coder/websocket は期限切れの Read で接続を閉じるため、
	// 張り替え後の ws2 をフォアグラウンドでタイムアウト読みすると再利用できない。
	// 以降の読みはバックグラウンドに任せ、フォアグラウンドは書きだけにする。
	textCh := make(chan any, 32)
	binCh := make(chan []byte, 32)
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			typ, data, err := ws2.Read(ctx)
			cancel()
			if err != nil {
				return
			}
			switch typ {
			case websocket.MessageText:
				msg, derr := proto.Decode(data)
				if derr != nil {
					continue
				}
				select {
				case textCh <- msg:
				default:
				}
			case websocket.MessageBinary:
				select {
				case binCh <- data:
				default:
				}
			}
		}
	}()
	time.Sleep(resume * 3)
	select {
	case m := <-textCh:
		t.Fatalf("猶予超過で余計なメッセージ (切断された?): %+v", m)
	default:
	}

	// まだ生きているので往復できる。
	if err := ws2.Write(wctx, websocket.MessageBinary, rtp); err != nil {
		t.Fatalf("猶予後のバイナリ送信失敗: %v", err)
	}
	select {
	case echo := <-binCh:
		if string(echo) != string(rtp) {
			t.Fatalf("猶予後のエコーが不正: %v", echo)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("猶予後のエコーが戻らない")
	}

	// 後片付け: 2 本目から切断できる。猶予タイマで切れていれば
	// hangup は失敗し ended(bye) は来ない。
	writeJSON(t, ws2, &proto.Hangup{T: proto.THangup, CallID: id})
	select {
	case m := <-textCh:
		end, ok := m.(*proto.Ended)
		if !ok || end.Reason != "bye" || end.CallID != id {
			t.Fatalf("2 本目に ended(bye) が来ない: %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("2 本目の ended が来ない")
	}
}

// TestPushToHalfDeadConnection は、トークン登録済みだが WS が応答しない端末に
// push が飛び、生きている端末には飛ばないことを確認する。
// 「応答しない」状態はクライアント側の読み取り停止で作る
// (coder/websocket は Read しないと pong を返さない)。
func TestPushToHalfDeadConnection(t *testing.T) {
	rp := newRecordingPusher()
	fx := newFixtureCfg(t, rp, nil, session.Config{
		DefaultAccount:       defaultTestAccount,
		DefaultPassword:      defaultTestPassword,
		LivenessProbeTimeout: 500 * time.Millisecond,
	})
	fx.waitRegistered(t, defaultTestAccount)

	// 応答しなくなる端末。
	dead := dial(t, fx.url, "dev-DEAD")
	defer dead.Close(websocket.StatusNormalClosure, "")
	if _, ok := readJSON(t, dead).(*proto.Hello); !ok {
		t.Fatalf("dead に hello が来ない")
	}
	registerPush(t, dead, "tok-DEAD", 1)
	// ここから dead 側は一切 Read しない (pong が返らなくなる)。

	// 生きている端末。バックグラウンドで読み続ける (ping に pong を返す)。
	alive := dial(t, fx.url, "dev-ALIVE")
	defer alive.Close(websocket.StatusNormalClosure, "")
	if _, ok := readJSON(t, alive).(*proto.Hello); !ok {
		t.Fatalf("alive に hello が来ない")
	}
	registerPush(t, alive, "tok-ALIVE", 2)
	aliveMsgs := make(chan any, 32)
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			typ, data, err := alive.Read(ctx)
			cancel()
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			msg, derr := proto.Decode(data)
			if derr != nil {
				continue
			}
			select {
			case aliveMsgs <- msg:
			default:
			}
		}
	}()

	callID := fx.fb(t).InjectIncoming("102", "Bob", 0)
	// 生きている端末には incoming が届く。
	select {
	case m := <-aliveMsgs:
		inc, ok := m.(*proto.Incoming)
		if !ok || inc.CallID != callID {
			t.Fatalf("alive の incoming が不正: %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("alive に incoming が来ない")
	}

	// 半死にの端末のトークンに push が飛ぶ。生きている端末には飛ばない。
	c := rp.wait(t, 5*time.Second)
	if len(c.tokens) != 1 || c.tokens[0] != "tok-DEAD" {
		t.Fatalf("push 先が不正: %v (tok-DEAD のみのはず)", c.tokens)
	}
	if c.payload.CallID != callID || c.payload.Type != "incoming" {
		t.Fatalf("payload が不正: %+v", c.payload)
	}
}

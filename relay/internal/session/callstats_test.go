package session_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tmlksu/sipbridge/relay/internal/proto"
	"github.com/tmlksu/sipbridge/relay/internal/push"
	"github.com/tmlksu/sipbridge/relay/internal/session"
)

// logBuf はテスト用のログ出力先 (並行書き込み可)。
type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// lines は msg=<msg> の行を返す。
func (b *logBuf) lines(msg string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, l := range strings.Split(b.buf.String(), "\n") {
		if strings.Contains(l, " msg="+msg+" ") {
			out = append(out, l)
		}
	}
	return out
}

func (b *logBuf) waitLines(t *testing.T, msg string, n int, within time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if ls := b.lines(msg); len(ls) >= n {
			return ls
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s が %d 行出ない: %q", msg, n, b.lines(msg))
	return nil
}

const statsWait = 150 * time.Millisecond

func newStatsFixture(t *testing.T) (*fixture, *logBuf) {
	t.Helper()
	lb := &logBuf{}
	log := slog.New(slog.NewTextHandler(lb, &slog.HandlerOptions{Level: slog.LevelInfo}))
	fx := newFixtureLog(t, push.Noop{}, nil, session.Config{
		DefaultAccount:  defaultTestAccount,
		DefaultPassword: defaultTestPassword,
		CallStatsWait:   statsWait,
	}, log)
	fx.waitRegistered(t, defaultTestAccount)
	return fx, lb
}

func rtpPkt(seq uint16) []byte {
	b := make([]byte, 12+160)
	b[0] = 0x80
	binary.BigEndian.PutUint16(b[2:4], seq)
	binary.BigEndian.PutUint32(b[4:8], uint32(seq)*160)
	binary.BigEndian.PutUint32(b[8:12], 0xabcdef)
	return b
}

// answeredCall は着信→応答し、seqs の RTP を上りに送ってエコーを読み切る。
func answeredCall(t *testing.T, fx *fixture, ws *websocket.Conn, seqs []uint16) string {
	t.Helper()
	readJSON(t, ws) // hello
	id := fx.fb(t).InjectIncoming("0312345678", "山田", 0)
	readJSON(t, ws) // incoming
	writeJSON(t, ws, &proto.Answer{T: proto.TAnswer, CallID: id})
	if _, ok := readJSON(t, ws).(*proto.Answered); !ok {
		t.Fatalf("answered のはず")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range seqs {
		if err := ws.Write(ctx, websocket.MessageBinary, rtpPkt(s)); err != nil {
			t.Fatalf("RTP 送信失敗: %v", err)
		}
		if typ, _ := readMsg(t, ws); typ != websocket.MessageBinary {
			t.Fatalf("エコーのはずが %v", typ)
		}
	}
	return id
}

func appStats(callID string) string {
	return `{"t":"call_stats","callId":"` + callID + `","dur":4000,"net":"wifi",` +
		`"rx":{"pkts":5,"gaps":0,"reorder":0,"jitterMs":1.5,"maxGapMs":40,"stall100":0,"stall200":0,"stall500":0,"reconnects":0},` +
		`"jb":{"underrun":0,"overflow":0},"playUnderrun":0,"tx":{"pkts":5,"drop":0,"lateMs":3},"rttMs":[80,-1]}`
}

func writeRaw(t *testing.T, ws *websocket.Conn, s string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageText, []byte(s)); err != nil {
		t.Fatalf("WS 書き込み失敗: %v", err)
	}
}

// pingPong は応答が pong だけであること (call_stats に応答が無いこと) を確かめる。
func pingPong(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	writeJSON(t, ws, &proto.Ping{T: proto.TPing, Ts: 99})
	if m, ok := readJSON(t, ws).(*proto.Pong); !ok || m.Ts != 99 {
		t.Fatalf("pong のはずが %#v", m)
	}
}

// TestCallStatsWithApp: 通話終了後にアプリの call_stats を受けたら 1 行出し、
// 待ち時間を過ぎても relay 分だけの行を重ねて出さない。
func TestCallStatsWithApp(t *testing.T) {
	fx, lb := newStatsFixture(t)
	ws := dial(t, fx.url, "dev-A")
	defer ws.Close(websocket.StatusNormalClosure, "")

	id := answeredCall(t, fx, ws, []uint16{10, 11, 13, 14, 12}) // 12 が欠け→逆行
	writeJSON(t, ws, &proto.Hangup{T: proto.THangup, CallID: id})
	if _, ok := readJSON(t, ws).(*proto.Ended); !ok {
		t.Fatalf("ended のはず")
	}
	writeRaw(t, ws, appStats(id))
	pingPong(t, ws)

	ls := lb.waitLines(t, "call_stats", 1, 2*time.Second)
	time.Sleep(statsWait * 3)
	if n := len(lb.lines("call_stats")); n != 1 {
		t.Fatalf("call_stats は 1 行のはずが %d 行: %q", n, lb.lines("call_stats"))
	}
	l := ls[0]
	t.Log(l)
	for _, want := range []string{
		"callId=" + id, "account=101", "dev=dev-A", "net=wifi",
		"up.pkts=5", "up.gaps=1", "up.reorder=1",
		"ast.pkts=5", "ast.gaps=1", "ast.qdrop=0", "txDrop=0",
		`app="{\"dur\":4000,\"jb\":`, // t/callId は除き、キー順に整列
		`\"rttMs\":[80,-1]`,
	} {
		if !strings.Contains(l, want) {
			t.Errorf("%q を含まない", want)
		}
	}
	for _, pii := range []string{"0312345678", "山田", `\"t\":`} {
		if strings.Contains(l, pii) {
			t.Errorf("%q を含んではいけない", pii)
		}
	}
}

// TestCallStatsRelayOnly: アプリの call_stats が来なければ待ち時間後に relay 分だけ出す。
// その後に遅れて届いたものは call_stats_orphan として別に出す (二重に数えない)。
func TestCallStatsRelayOnly(t *testing.T) {
	fx, lb := newStatsFixture(t)
	ws := dial(t, fx.url, "dev-A")
	defer ws.Close(websocket.StatusNormalClosure, "")

	id := answeredCall(t, fx, ws, []uint16{1, 2, 3})
	fx.fb(t).InjectRemoteHangup(id)
	if _, ok := readJSON(t, ws).(*proto.Ended); !ok {
		t.Fatalf("ended のはず")
	}
	start := time.Now()
	ls := lb.waitLines(t, "call_stats", 1, 2*time.Second)
	if el := time.Since(start); el < statsWait/2 {
		t.Fatalf("待ち時間より早く出た: %v", el)
	}
	l := ls[0]
	t.Log(l)
	if !strings.Contains(l, "up.pkts=3") || strings.Contains(l, "app=") || strings.Contains(l, "net=") {
		t.Fatalf("relay 分だけの行のはず: %s", l)
	}

	writeRaw(t, ws, appStats(id)) // 遅着
	pingPong(t, ws)
	lb.waitLines(t, "call_stats_orphan", 1, 2*time.Second)
	if n := len(lb.lines("call_stats")); n != 1 {
		t.Fatalf("call_stats は 1 行のはずが %d 行", n)
	}
}

// TestCallStatsBeforeEnded: 通話終了の処理より先に call_stats が届いても、
// 終了時に 1 行だけ出す。
func TestCallStatsBeforeEnded(t *testing.T) {
	fx, lb := newStatsFixture(t)
	ws := dial(t, fx.url, "dev-A")
	defer ws.Close(websocket.StatusNormalClosure, "")

	id := answeredCall(t, fx, ws, []uint16{1, 2})
	writeRaw(t, ws, appStats(id))
	pingPong(t, ws) // call_stats の処理完了を待つ
	if n := len(lb.lines("call_stats")); n != 0 {
		t.Fatalf("通話中に出してはいけない: %q", lb.lines("call_stats"))
	}
	fx.fb(t).InjectRemoteHangup(id)
	readJSON(t, ws) // ended
	ls := lb.waitLines(t, "call_stats", 1, statsWait/2)
	if !strings.Contains(ls[0], "app=") || !strings.Contains(ls[0], "up.pkts=2") {
		t.Fatalf("app と relay の両方を含むはず: %s", ls[0])
	}
	time.Sleep(statsWait * 2)
	if n := len(lb.lines("call_stats")); n != 1 {
		t.Fatalf("call_stats は 1 行のはずが %d 行", n)
	}
}

// TestCallStatsRejectsOversizeAndUnknown: 上限超えは捨て、不明な callId はエラーにしない。
func TestCallStatsRejectsOversizeAndUnknown(t *testing.T) {
	fx, lb := newStatsFixture(t)
	ws := dial(t, fx.url, "dev-A")
	defer ws.Close(websocket.StatusNormalClosure, "")
	readJSON(t, ws) // hello

	big := `{"t":"call_stats","callId":"x","pad":"` + strings.Repeat("a", proto.MaxCallStatsSize) + `"}`
	writeRaw(t, ws, big)
	writeRaw(t, ws, `{"t":"call_stats","callId":"nope","net":"cellular"}`)
	writeRaw(t, ws, `{"t":"call_stats","callId":123}`)
	pingPong(t, ws) // error 応答が挟まらない

	if ls := lb.lines("call_stats_orphan"); len(ls) != 2 {
		t.Fatalf("orphan は 2 行 (nope と数値 callId) のはず: %q", ls)
	}
	if ls := lb.lines("call_stats"); len(ls) != 0 {
		t.Fatalf("call_stats を出してはいけない: %q", ls)
	}
}

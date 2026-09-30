package session

import (
	"testing"
	"time"
)

// TestTokenBucket はバースト分だけ連続で取れ、以後は interval ごとに 1 つ回復し、
// 上限 (burst) を超えて貯まらないことを確認する。
func TestTokenBucket(t *testing.T) {
	now := time.Unix(1000, 0)
	b := newTokenBucket(3, 10*time.Second)
	b.now = func() time.Time { return now }
	b.last = now
	for i := 0; i < 3; i++ {
		if !b.take() {
			t.Fatalf("%d 個目が取れない", i+1)
		}
	}
	if b.take() {
		t.Fatalf("バースト超過で取れた")
	}
	now = now.Add(5 * time.Second)
	if b.take() {
		t.Fatalf("回復前に取れた")
	}
	now = now.Add(5 * time.Second)
	if !b.take() {
		t.Fatalf("interval 経過で回復しない")
	}
	now = now.Add(time.Hour) // 長時間放置しても burst まで
	for i := 0; i < 3; i++ {
		if !b.take() {
			t.Fatalf("回復後 %d 個目が取れない", i+1)
		}
	}
	if b.take() {
		t.Fatalf("burst を超えて貯まった")
	}
}

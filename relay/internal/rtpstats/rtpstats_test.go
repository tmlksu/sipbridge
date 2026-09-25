package rtpstats

import (
	"encoding/binary"
	"testing"
	"time"
)

func pkt(seq uint16, ts, ssrc uint32) []byte {
	b := make([]byte, 12+160)
	b[0] = 0x80
	binary.BigEndian.PutUint16(b[2:4], seq)
	binary.BigEndian.PutUint32(b[4:8], ts)
	binary.BigEndian.PutUint32(b[8:12], ssrc)
	return b
}

const frame = 20 * time.Millisecond

func TestSteadyStream(t *testing.T) {
	var s Stats
	for i := 0; i < 100; i++ {
		s.ObserveAt(pkt(uint16(i), uint32(i*160), 1), time.Duration(i)*frame)
	}
	got := s.Snapshot()
	want := Snapshot{Pkts: 100, MaxGapMs: 20}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestGapsReorderAndWrap(t *testing.T) {
	var s Stats
	at := time.Duration(0)
	obs := func(seq uint16) {
		s.ObserveAt(pkt(seq, uint32(seq)*160, 7), at)
		at += frame
	}
	obs(65533)
	obs(65534)
	obs(0) // 65535 が欠け (折り返しをまたぐ)
	obs(1)
	obs(4) // 2,3 が欠け
	obs(3) // 逆行
	obs(4) // 重複
	obs(5)
	got := s.Snapshot()
	if got.Pkts != 8 || got.Gaps != 3 || got.Reorder != 2 {
		t.Fatalf("pkts/gaps/reorder = %d/%d/%d, want 8/3/2", got.Pkts, got.Gaps, got.Reorder)
	}
}

func TestStallsAndMaxGap(t *testing.T) {
	var s Stats
	arr := []time.Duration{0, 20, 40, 190, 210, 450, 470, 1100, 1120} // ms
	for i, a := range arr {
		s.ObserveAt(pkt(uint16(i), uint32(i*160), 1), a*time.Millisecond)
	}
	got := s.Snapshot()
	// 間隔: 20,20,150,20,240,20,630,20
	if got.Stall100 != 3 || got.Stall200 != 2 || got.Stall500 != 1 {
		t.Fatalf("stall = %d/%d/%d, want 3/2/1", got.Stall100, got.Stall200, got.Stall500)
	}
	if got.MaxGapMs != 630 {
		t.Fatalf("maxGapMs = %d, want 630", got.MaxGapMs)
	}
}

func TestJitterConverges(t *testing.T) {
	// 到着が ±5 ms で交互に揺れる → |D| は 10 ms が続き、jitter は 10 ms に近づく。
	var s Stats
	for i := 0; i < 500; i++ {
		off := 5 * time.Millisecond
		if i%2 == 1 {
			off = -off
		}
		s.ObserveAt(pkt(uint16(i), uint32(i*160), 1), time.Duration(i)*frame+off+time.Second)
	}
	got := s.Snapshot().JitterMs
	if got < 9.5 || got > 10.5 {
		t.Fatalf("jitterMs = %v, want ≈10", got)
	}
}

func TestSSRCChangeResetsBaseline(t *testing.T) {
	var s Stats
	s.ObserveAt(pkt(100, 16000, 1), 0)
	s.ObserveAt(pkt(101, 16160, 1), frame)
	s.ObserveAt(pkt(5000, 999, 2), 2*frame) // 送信元が作り直された
	s.ObserveAt(pkt(5001, 1159, 2), 3*frame)
	got := s.Snapshot()
	if got.Gaps != 0 || got.Reorder != 0 || got.JitterMs != 0 || got.Pkts != 4 {
		t.Fatalf("got %+v", got)
	}
}

func TestIgnoresNonRTP(t *testing.T) {
	var s Stats
	s.ObserveAt([]byte{0x80, 0}, 0)
	b := pkt(1, 1, 1)
	b[0] = 0x40 // version 1
	s.ObserveAt(b, 0)
	if s.Pkts() != 0 {
		t.Fatalf("pkts = %d", s.Pkts())
	}
}

func TestSnapshotAdd(t *testing.T) {
	a := Snapshot{Pkts: 1, Gaps: 2, JitterMs: 3, MaxGapMs: 40, Stall100: 1}
	b := Snapshot{Pkts: 10, Reorder: 1, JitterMs: 1, MaxGapMs: 400, Stall100: 2, Stall500: 1}
	got := a.Add(b)
	want := Snapshot{Pkts: 11, Gaps: 2, Reorder: 1, JitterMs: 3, MaxGapMs: 400, Stall100: 3, Stall500: 1}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestObserveNoAlloc(t *testing.T) {
	var s Stats
	p := pkt(1, 160, 1)
	i := uint16(0)
	n := testing.AllocsPerRun(1000, func() {
		i++
		binary.BigEndian.PutUint16(p[2:4], i)
		s.Observe(p)
	})
	if n != 0 {
		t.Fatalf("Observe がアロケーションしている: %v", n)
	}
}

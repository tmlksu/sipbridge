package proto

import (
	"reflect"
	"testing"
)

func intp(v int) *int { return &v }

func TestRoundTrip(t *testing.T) {
	ci := &CallInfo{CallID: "c1", Direction: "in", State: "ringing", From: "102", Display: "Bob", PT: 0, StartedAt: 1700000000}
	cases := []struct {
		name string
		msg  any
		want any // Decode 後の型
	}{
		{"hello", &Hello{T: THello, RelayVersion: "0.2.0", Extension: "101", Account: "101", Registered: true, Call: ci, ServerTime: 1}, &Hello{}},
		{"hello null call", &Hello{T: THello, RelayVersion: "0.2.0", Extension: "101", Account: "101"}, &Hello{}},
		{"hello account 無し", &Hello{T: THello, RelayVersion: "0.2.0"}, &Hello{}},
		{"registration", &Registration{T: TRegistration, OK: true, Detail: "ok"}, &Registration{}},
		{"incoming", &Incoming{T: TIncoming, CallID: "c1", From: "102", Display: "Bob", PT: 8}, &Incoming{}},
		{"ringing", &Ringing{T: TRinging, CallID: "c1", Early: true}, &Ringing{}},
		{"answered", &Answered{T: TAnswered, CallID: "c1", PT: 0}, &Answered{}},
		{"ended", &Ended{T: TEnded, CallID: "c1", Reason: "bye", Code: 200}, &Ended{}},
		{"error", &Error{T: TError, Code: "x", Message: "y"}, &Error{}},
		{"pong", &Pong{T: TPong, Ts: 42}, &Pong{}},
		{"answer", &Answer{T: TAnswer, CallID: "c1", PT: intp(8)}, &Answer{}},
		{"answer pt 省略", &Answer{T: TAnswer, CallID: "c1"}, &Answer{}},
		{"reject", &Reject{T: TReject, CallID: "c1", Code: intp(486)}, &Reject{}},
		{"hangup", &Hangup{T: THangup, CallID: "c1"}, &Hangup{}},
		{"dial", &Dial{T: TDial, To: "102"}, &Dial{}},
		{"dtmf", &Dtmf{T: TDtmf, CallID: "c1", Digits: "123#"}, &Dtmf{}},
		{"register_push", &RegisterPush{T: TRegisterPush, Provider: "fcm", Token: "tok"}, &RegisterPush{}},
		{"sip_account", &SipAccount{T: TSipAccount, User: "101", Password: "pw", Display: "居間"}, &SipAccount{}},
		{"sip_account 解除", &SipAccount{T: TSipAccount}, &SipAccount{}},
		{"ping", &Ping{T: TPing, Ts: 7}, &Ping{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := Encode(tc.msg)
			if err != nil {
				t.Fatalf("Encode 失敗: %v", err)
			}
			got, err := Decode(data)
			if err != nil {
				t.Fatalf("Decode 失敗: %v", err)
			}
			if reflect.TypeOf(got) != reflect.TypeOf(tc.want) {
				t.Fatalf("型が不一致: got %T want %T", got, tc.want)
			}
			if !reflect.DeepEqual(got, tc.msg) {
				t.Errorf("往復で不一致:\n got %+v\nwant %+v", got, tc.msg)
			}
		})
	}
}

func TestDecodeErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"t 無し", `{"callId":"c1"}`},
		{"未知の t", `{"t":"explode"}`},
		{"JSON 不正", `{oops`},
		{"空", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Decode([]byte(tc.raw)); err == nil {
				t.Errorf("エラーになるはず")
			}
		})
	}
}

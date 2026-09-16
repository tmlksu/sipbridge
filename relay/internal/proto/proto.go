// Package proto は docs/PROTOCOL.md の JSON 制御メッセージの型と
// encode/decode を提供する。テキストフレームは必ず "t" を持つ。
package proto

import (
	"encoding/json"
	"fmt"
)

// メッセージ種別 (t の値)。
const (
	THello        = "hello"
	TRegistration = "registration"
	TIncoming     = "incoming"
	TRinging      = "ringing"
	TAnswered     = "answered"
	TEnded        = "ended"
	TError        = "error"
	TPong         = "pong"

	TAnswer       = "answer"
	TReject       = "reject"
	THangup       = "hangup"
	TDial         = "dial"
	TDtmf         = "dtmf"
	TRegisterPush = "register_push"
	TSipAccount   = "sip_account"
	TPing         = "ping"
)

// CallInfo は通話の概要 (hello.call ほかで使用)。
type CallInfo struct {
	CallID    string `json:"callId"`
	Direction string `json:"direction"` // "in" | "out"
	State     string `json:"state"`     // "ringing" | "active"
	From      string `json:"from,omitempty"`
	Display   string `json:"display,omitempty"`
	PT        int    `json:"pt"`
	StartedAt int64  `json:"startedAt"` // Unix 秒
}

// --- relay → app ---

type Hello struct {
	T            string `json:"t"`
	RelayVersion string `json:"relayVersion"`
	Extension    string `json:"extension"`
	// Account は結び付いている SIP アカウント (v1.1)。Extension と同値で、
	// 結び付けが無ければ空文字である。
	Account    string    `json:"account"`
	Registered bool      `json:"registered"`
	Call       *CallInfo `json:"call"`
	ServerTime int64     `json:"serverTime"`
}

type Registration struct {
	T      string `json:"t"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type Incoming struct {
	T       string `json:"t"`
	CallID  string `json:"callId"`
	From    string `json:"from"`
	Display string `json:"display,omitempty"`
	PT      int    `json:"pt"`
}

type Ringing struct {
	T      string `json:"t"`
	CallID string `json:"callId"`
	Early  bool   `json:"early"`
}

type Answered struct {
	T      string `json:"t"`
	CallID string `json:"callId"`
	PT     int    `json:"pt"`
}

type Ended struct {
	T      string `json:"t"`
	CallID string `json:"callId"`
	Reason string `json:"reason"` // bye/cancel/reject/timeout/error/answered_elsewhere
	Code   int    `json:"code"`
}

type Error struct {
	T       string `json:"t"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Pong struct {
	T  string `json:"t"`
	Ts int64  `json:"ts"`
}

// --- app → relay ---

type Answer struct {
	T      string `json:"t"`
	CallID string `json:"callId"`
	PT     *int   `json:"pt,omitempty"` // 省略時は incoming.pt
}

type Reject struct {
	T      string `json:"t"`
	CallID string `json:"callId"`
	Code   *int   `json:"code,omitempty"` // 省略時 486
}

type Hangup struct {
	T      string `json:"t"`
	CallID string `json:"callId"`
}

type Dial struct {
	T  string `json:"t"`
	To string `json:"to"`
}

type Dtmf struct {
	T      string `json:"t"`
	CallID string `json:"callId"`
	Digits string `json:"digits"`
}

type RegisterPush struct {
	T        string `json:"t"`
	Provider string `json:"provider"` // "fcm"
	Token    string `json:"token"`
}

// SipAccount はこの端末が使う SIP アカウント (内線) の登録である (v1.1)。
// User="" は結び付けの解除を意味する。
type SipAccount struct {
	T        string `json:"t"`
	User     string `json:"user"`
	Password string `json:"password,omitempty"`
	Display  string `json:"display,omitempty"`
}

type Ping struct {
	T  string `json:"t"`
	Ts int64  `json:"ts"`
}

// envelope は t だけを先読みするための型。
type envelope struct {
	T string `json:"t"`
}

// Encode はメッセージを JSON に変換する。
func Encode(v any) ([]byte, error) {
	return json.Marshal(v)
}

// Decode は JSON 制御メッセージを t に応じて具体型へ変換する。
func Decode(data []byte) (any, error) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("JSON 解析失敗: %w", err)
	}
	var v any
	switch env.T {
	case THello:
		v = &Hello{}
	case TRegistration:
		v = &Registration{}
	case TIncoming:
		v = &Incoming{}
	case TRinging:
		v = &Ringing{}
	case TAnswered:
		v = &Answered{}
	case TEnded:
		v = &Ended{}
	case TError:
		v = &Error{}
	case TPong:
		v = &Pong{}
	case TAnswer:
		v = &Answer{}
	case TReject:
		v = &Reject{}
	case THangup:
		v = &Hangup{}
	case TDial:
		v = &Dial{}
	case TDtmf:
		v = &Dtmf{}
	case TRegisterPush:
		v = &RegisterPush{}
	case TSipAccount:
		v = &SipAccount{}
	case TPing:
		v = &Ping{}
	case "":
		return nil, fmt.Errorf("フィールド t が無い")
	default:
		return nil, fmt.Errorf("未知のメッセージ種別 %q", env.T)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return nil, fmt.Errorf("種別 %q の解析失敗: %w", env.T, err)
	}
	return v, nil
}

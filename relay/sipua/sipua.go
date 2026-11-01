// Package sipua は relay の SIP UA (Asterisk に普通の内線として REGISTER し、
// 着信・発信・RTP を扱う) を relay の外から使うための公開窓口である。
//
// relay とは別に動く SIP 内線アプリ (AI 内線など) から使う。中身は
// internal/sipbackend と internal/call の型の別名と薄い関数だけで、relay の挙動は変えない。
// 互換性: ここに出した名前は、使う側を壊さないよう消したり意味を変えたりしない。
package sipua

import (
	"log/slog"
	"net/netip"

	"github.com/tmlksu/sipbridge/relay/internal/call"
	"github.com/tmlksu/sipbridge/relay/internal/sipbackend"
)

// 通話制御の抽象 (relay の Manager と同じ契約)。
type (
	// Backend は SIP 側とのやり取り (REGISTER・着信・発信・切断)。
	Backend = call.Backend
	// MediaPipe は 1 通話の RTP の送受信。
	MediaPipe = call.MediaPipe
	// Event は Backend が上げるイベント (以下の Ev* のどれか)。
	Event = call.Event

	EvRegistered = call.EvRegistered
	EvIncoming   = call.EvIncoming
	EvRinging    = call.EvRinging
	EvAnswered   = call.EvAnswered
	EvEnded      = call.EvEnded
)

// UA は sipgo による Backend の実装、Config はその設定である。
type (
	UA     = sipbackend.Backend
	Config = sipbackend.Config
)

// New は SIP UA を作る。Start で REGISTER を始める。
func New(cfg Config, log *slog.Logger) (*UA, error) { return sipbackend.New(cfg, log) }

// ValidateUser は SIP ユーザー名 (内線番号) を検証する。
func ValidateUser(user string) error { return sipbackend.ValidateUser(user) }

// ParseTrustedSources は SIP_TRUSTED_SOURCES (SIP_HOST 以外に受け入れる送信元) を読む。
func ParseTrustedSources(s string) ([]netip.Prefix, error) {
	return sipbackend.ParseTrustedSources(s)
}

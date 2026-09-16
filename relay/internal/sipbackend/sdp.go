// Package sipbackend の SDP 生成・解析ヘルパー。
// Asterisk (chan_pjsip, G.711 ulaw/alaw) とのやり取りに必要な最小限の
// SDP (RFC 8866) だけを扱う。外部依存なし。
package sipbackend

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// 対応ペイロードタイプ。
const (
	PTPCMU = 0 // G.711 μ-law
	PTPCMA = 8 // G.711 A-law
)

// codecName は PT に対応する RTP マッピング名を返す。
func codecName(pt int) (string, bool) {
	switch pt {
	case PTPCMU:
		return "PCMU/8000", true
	case PTPCMA:
		return "PCMA/8000", true
	}
	return "", false
}

// buildAnswerSDP は着信応答 (200 OK)・re-INVITE 応答用の SDP を作る。
// 単一 PT のみ提示する。
func buildAnswerSDP(localIP string, port, pt int) []byte {
	name, ok := codecName(pt)
	if !ok {
		name = "PCMU/8000"
		pt = PTPCMU
	}
	return buildSDP(localIP, port, []int{pt}, map[int]string{pt: name})
}

// buildOfferSDP は発信 (INVITE) 用の SDP を作る。PCMU/PCMA の両方を提示する。
func buildOfferSDP(localIP string, port int) []byte {
	return buildSDP(localIP, port,
		[]int{PTPCMU, PTPCMA},
		map[int]string{PTPCMU: "PCMU/8000", PTPCMA: "PCMA/8000"})
}

func buildSDP(localIP string, port int, pts []int, names map[int]string) []byte {
	// sessid は衝突しにくい値であれば何でもよい。
	sessID := port
	fmts := make([]string, 0, len(pts))
	for _, pt := range pts {
		fmts = append(fmts, strconv.Itoa(pt))
	}
	var b strings.Builder
	b.WriteString("v=0\r\n")
	fmt.Fprintf(&b, "o=- %d %d IN IP4 %s\r\n", sessID, sessID, localIP)
	b.WriteString("s=sipbridge\r\n")
	fmt.Fprintf(&b, "c=IN IP4 %s\r\n", localIP)
	b.WriteString("t=0 0\r\n")
	fmt.Fprintf(&b, "m=audio %d RTP/AVP %s\r\n", port, strings.Join(fmts, " "))
	for _, pt := range pts {
		fmt.Fprintf(&b, "a=rtpmap:%d %s\r\n", pt, names[pt])
	}
	b.WriteString("a=ptime:20\r\n")
	b.WriteString("a=sendrecv\r\n")
	return []byte(b.String())
}

// remoteOffer は相手 SDP から取り出した RTP 宛先と PT である。
type remoteOffer struct {
	addr *net.UDPAddr
	pt   int
}

// parseOffer は相手 SDP を解析し、RTP 宛先と使う PT を決める。
// PCMU/PCMA のどちらも無ければエラーを返す (UAS は 488 を返送する)。
func parseOffer(body []byte) (remoteOffer, error) {
	var out remoteOffer
	if len(body) == 0 {
		return out, fmt.Errorf("SDP が空")
	}
	// c= 行はセッションレベルとメディアレベルがあり得る。
	// メディアレベルがあればそちらを優先する。
	var sessionIP, mediaIP, originIP string
	var mediaPort int
	var mediaPortSeen bool
	var fmts []int
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		switch {
		case strings.HasPrefix(line, "c=IN IP4 "):
			ip := strings.TrimPrefix(line, "c=IN IP4 ")
			ip = strings.Fields(ip)[0]
			if mediaPortSeen {
				mediaIP = ip
			} else if sessionIP == "" {
				sessionIP = ip
			}
		case strings.HasPrefix(line, "o="):
			// o=- sess ver IN IP4 addr
			if f := strings.Fields(line); len(f) >= 6 {
				originIP = f[5]
			}
		case strings.HasPrefix(line, "m=audio "):
			f := strings.Fields(strings.TrimPrefix(line, "m=audio "))
			if len(f) >= 3 {
				if p, err := strconv.Atoi(f[0]); err == nil {
					mediaPort = p
				}
				mediaPortSeen = true
				fmts = nil
				for _, s := range f[2:] {
					// "0" のような数値だけ拾う ("RTP/AVP" は無視される)。
					if n, err := strconv.Atoi(s); err == nil {
						fmts = append(fmts, n)
					}
				}
			}
		}
	}
	if !mediaPortSeen || mediaPort <= 0 || mediaPort > 65535 {
		return out, fmt.Errorf("m=audio のポートが不正 (%d)", mediaPort)
	}
	for _, pt := range fmts {
		if pt == PTPCMU || pt == PTPCMA {
			out.pt = pt
			break
		}
	}
	// out.pt のゼロ値は PCMU (0) と区別が付かないため、明示的に有無を判定する。
	found := false
	for _, pt := range fmts {
		if pt == PTPCMU || pt == PTPCMA {
			found = true
			break
		}
	}
	if !found {
		return out, fmt.Errorf("対応コーデック (PCMU/PCMA) が無い")
	}
	ipStr := mediaIP
	if ipStr == "" {
		ipStr = sessionIP
	}
	if ipStr == "" {
		ipStr = originIP
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return out, fmt.Errorf("接続先 IP が不正 (%q)", ipStr)
	}
	out.addr = &net.UDPAddr{IP: ip, Port: mediaPort}
	return out, nil
}

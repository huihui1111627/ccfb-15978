package protocol

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	HopAlias = "alias"
	HopRelay = "relay"
	HopQ     = "q"

	QDirect  = "qDIR" // gateway directly heard the originating station
	QRelayed = "qRLY" // gateway heard the packet through a relay
	QNetwork = "qNET" // packet arrived from another server/peer

	SourceDirect  = "direct"
	SourceRelayed = "relayed"
	SourceNetwork = "network"
	SourceClient  = "client"
	SourceUnknown = "unknown"
)

type Hop struct {
	Kind      string `json:"kind"`
	Alias     string `json:"alias,omitempty"`
	Call      string `json:"call,omitempty"`
	Remaining int    `json:"remaining,omitempty"`
	Done      bool   `json:"done,omitempty"`
	Marker    string `json:"marker,omitempty"`
	Via       string `json:"via,omitempty"`
}

type Frame struct {
	Src        string `json:"src"`
	Dst        string `json:"dst"`
	Path       []Hop  `json:"path,omitempty"`
	Body       string `json:"body"`
	Raw        string `json:"raw,omitempty"`
	ThirdParty *Frame `json:"third_party,omitempty"`
}

func (h Hop) String() string {
	switch h.Kind {
	case HopQ:
		return h.Marker + "=" + h.Via
	case HopRelay:
		if h.Alias != "" {
			return h.Call + "[" + h.Alias + "]*"
		}
		return h.Call + "*"
	default:
		return fmt.Sprintf("%s#%d", h.Alias, h.Remaining)
	}
}

func (f *Frame) String() string {
	var b strings.Builder
	b.WriteString(f.Src)
	b.WriteByte('>')
	b.WriteString(f.Dst)
	if len(f.Path) > 0 {
		b.WriteByte(',')
		for i, h := range f.Path {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(h.String())
		}
	}
	b.WriteByte(':')
	b.WriteString(f.Body)
	return b.String()
}

func (f *Frame) Clone() *Frame {
	if f == nil {
		return nil
	}
	cp := *f
	if f.Path != nil {
		cp.Path = append([]Hop(nil), f.Path...)
	}
	if f.ThirdParty != nil {
		cp.ThirdParty = f.ThirdParty.Clone()
	}
	return &cp
}

func ParseFrame(line string) (*Frame, error) {
	return parseFrame(line, 0)
}

func parseFrame(line string, depth int) (*Frame, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, fmt.Errorf("empty frame")
	}
	if len(line) > 1024 {
		return nil, fmt.Errorf("frame too long")
	}
	colon := strings.IndexByte(line, ':')
	if colon <= 0 {
		return nil, fmt.Errorf("missing colon/body")
	}
	header := line[:colon]
	body := line[colon+1:]
	gt := strings.IndexByte(header, '>')
	if gt <= 0 {
		return nil, fmt.Errorf("missing source/destination separator")
	}
	src := NormalizeCall(header[:gt])
	rest := header[gt+1:]
	if !ValidCall(src) {
		return nil, fmt.Errorf("invalid source callsign %q", src)
	}
	dst := rest
	var pathText string
	if comma := strings.IndexByte(rest, ','); comma >= 0 {
		dst = rest[:comma]
		pathText = rest[comma+1:]
	}
	dst = NormalizeCall(dst)
	if !ValidCall(dst) {
		return nil, fmt.Errorf("invalid destination callsign %q", dst)
	}
	f := &Frame{Src: src, Dst: dst, Body: body, Raw: line}
	if pathText != "" {
		for _, tok := range strings.Split(pathText, ",") {
			h, err := parseHop(tok)
			if err != nil {
				return nil, err
			}
			f.Path = append(f.Path, h)
		}
	}
	if strings.HasPrefix(body, "}") {
		if depth >= 3 {
			return nil, fmt.Errorf("third-party nesting too deep")
		}
		inner, err := parseFrame(body[1:], depth+1)
		if err != nil {
			return nil, fmt.Errorf("bad third-party frame: %w", err)
		}
		f.ThirdParty = inner
	}
	return f, nil
}

func parseHop(tok string) (Hop, error) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return Hop{}, fmt.Errorf("empty path token")
	}
	if strings.HasPrefix(tok, "q") && strings.Contains(tok, "=") {
		parts := strings.SplitN(tok, "=", 2)
		if parts[0] == "" || parts[1] == "" {
			return Hop{}, fmt.Errorf("bad q marker %q", tok)
		}
		return Hop{Kind: HopQ, Marker: parts[0], Via: NormalizeCall(parts[1])}, nil
	}
	if i := strings.IndexByte(tok, '#'); i >= 0 {
		alias := NormalizeCall(tok[:i])
		n, err := strconv.Atoi(tok[i+1:])
		if err != nil || n < 0 || n > 15 || alias == "" {
			return Hop{}, fmt.Errorf("bad alias hop %q", tok)
		}
		return Hop{Kind: HopAlias, Alias: alias, Remaining: n}, nil
	}
	done := strings.HasSuffix(tok, "*")
	if done {
		tok = strings.TrimSuffix(tok, "*")
	}
	h := Hop{Kind: HopRelay, Done: done}
	if ob := strings.IndexByte(tok, '['); ob >= 0 {
		cb := strings.IndexByte(tok, ']')
		if cb < ob {
			return Hop{}, fmt.Errorf("bad rewritten hop %q", tok)
		}
		h.Call = NormalizeCall(tok[:ob])
		h.Alias = NormalizeCall(tok[ob+1 : cb])
	} else {
		h.Call = NormalizeCall(tok)
	}
	if !ValidCall(h.Call) {
		return Hop{}, fmt.Errorf("invalid relay call %q", h.Call)
	}
	return h, nil
}

// RewriteForRelay applies the generic alias/hop-count protocol rule. It returns
// a new frame and true when this relay consumed one generic hop.
func RewriteForRelay(in *Frame, relayCall string, aliases []string) (*Frame, bool) {
	relayCall = NormalizeCall(relayCall)
	allowed := map[string]bool{}
	for _, a := range aliases {
		allowed[NormalizeCall(a)] = true
	}
	for _, h := range in.Path {
		if h.Kind == HopRelay && h.Call == relayCall {
			return nil, false // loop suppression
		}
	}
	idx := -1
	for i, h := range in.Path {
		if h.Kind == HopAlias && h.Remaining > 0 && allowed[NormalizeCall(h.Alias)] {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, false
	}
	out := in.Clone()
	old := out.Path[idx]
	used := Hop{Kind: HopRelay, Call: relayCall, Alias: old.Alias, Done: true}
	old.Remaining--
	newPath := make([]Hop, 0, len(out.Path)+1)
	newPath = append(newPath, out.Path[:idx]...)
	newPath = append(newPath, used, old)
	newPath = append(newPath, out.Path[idx+1:]...)
	out.Path = newPath
	out.Raw = out.String()
	return out, true
}

func (f *Frame) RelayCalls() []string {
	var out []string
	if f == nil {
		return out
	}
	for _, h := range f.Path {
		if h.Kind == HopRelay && h.Call != "" {
			out = append(out, h.Call)
		}
	}
	return out
}

func (f *Frame) HasRelayHop() bool {
	return len(f.RelayCalls()) > 0
}

func (f *Frame) QMarker() (marker, via string) {
	if f == nil {
		return "", ""
	}
	for i := len(f.Path) - 1; i >= 0; i-- {
		if f.Path[i].Kind == HopQ {
			return f.Path[i].Marker, f.Path[i].Via
		}
	}
	return "", ""
}

func (f *Frame) SourceClass() string {
	marker, _ := f.QMarker()
	switch marker {
	case QDirect:
		return SourceDirect
	case QRelayed:
		return SourceRelayed
	case QNetwork:
		return SourceNetwork
	}
	if f.HasRelayHop() {
		return SourceRelayed
	}
	return SourceUnknown
}

func WithQMarker(in *Frame, marker, via string) *Frame {
	out := in.Clone()
	out.Path = append(out.Path, Hop{Kind: HopQ, Marker: marker, Via: NormalizeCall(via)})
	out.Raw = out.String()
	return out
}

func WrapThirdParty(gatewayCall string, inner *Frame) *Frame {
	wrapped := &Frame{
		Src:        NormalizeCall(gatewayCall),
		Dst:        "APGW",
		Body:       "}" + inner.String(),
		ThirdParty: inner.Clone(),
	}
	wrapped.Raw = wrapped.String()
	return wrapped
}

func (f *Frame) Innermost() *Frame {
	cur := f
	for cur != nil && cur.ThirdParty != nil {
		cur = cur.ThirdParty
	}
	return cur
}

type Fields struct {
	Type   string
	Values map[string]string
}

func ParseFields(body string) (Fields, error) {
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, "}") {
		return Fields{}, fmt.Errorf("third-party body must be unwrapped before field decode")
	}
	parts := strings.Split(body, ";")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return Fields{}, fmt.Errorf("missing packet type")
	}
	res := Fields{Type: strings.ToUpper(strings.TrimSpace(parts[0])), Values: map[string]string{}}
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		eq := strings.IndexByte(p, '=')
		if eq <= 0 {
			return Fields{}, fmt.Errorf("bad field %q", p)
		}
		k := strings.ToLower(strings.TrimSpace(p[:eq]))
		v := strings.TrimSpace(p[eq+1:])
		res.Values[k] = v
	}
	return res, nil
}

func EncodeFields(typ string, kv ...string) string {
	var b strings.Builder
	b.WriteString(strings.ToUpper(typ))
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteByte(';')
		b.WriteString(kv[i])
		b.WriteByte('=')
		b.WriteString(kv[i+1])
	}
	return b.String()
}

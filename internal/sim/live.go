package sim

import (
	"time"

	"amsgw/internal/core"
	"amsgw/internal/protocol"
)

// RouteLiveChat applies the same recent-heard gateway rule used by the
// deterministic run to an operator message submitted from the web page.
func RouteLiveChat(res *Result, m *core.Message, at time.Time) *core.Downlink {
	target := protocol.NormalizeCall(m.Target)
	bestGW := ""
	bestHeard := time.Time{}
	for gw, heard := range res.Heard {
		t, ok := heard[target]
		if !ok || at.Sub(t) > res.Scenario.Cfg.HeardTTL {
			continue
		}
		if bestGW == "" || t.After(bestHeard) {
			bestGW, bestHeard = gw, t
		}
	}
	if bestGW == "" {
		dl := core.Downlink{At: at, Recipient: target}
		res.Hub.AddDownlink(m.ID, dl)
		return &dl
	}
	ts := protocol.FormatMinuteSecond(at)
	inner := &protocol.Frame{
		Src: m.Source, Dst: "APMSG",
		Body: protocol.EncodeFields("MSG", "mid", m.ID, "ts", ts, "to", target, "text", m.Text),
	}
	wrapped := protocol.WrapThirdParty(bestGW, inner)
	fate := FateFor(res.Scenario.Cfg.Seed, "LIVE:"+m.ID, target, res.Scenario.Cfg)
	dl := core.Downlink{
		At: at, Gateway: bestGW, HeardAt: bestHeard,
		AgeSeconds: at.Sub(bestHeard).Seconds(), Recipient: target,
		Frame: wrapped.String(), DeliveredRF: fate.Receive,
	}
	res.Hub.AddDownlink(m.ID, dl)
	return &dl
}

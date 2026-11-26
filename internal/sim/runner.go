package sim

import (
	"container/heap"
	"fmt"
	"sort"
	"time"

	"amsgw/internal/core"
	"amsgw/internal/protocol"
)

type Receipt struct {
	At        time.Time `json:"at"`
	Call      string    `json:"call"`
	MessageID string    `json:"message_id"`
	Via       string    `json:"via"`
	From      string    `json:"from"`
	Downlink  bool      `json:"downlink"`
}

type Result struct {
	Scenario       *Scenario                       `json:"scenario"`
	Hub            *core.Hub                       `json:"hub"`
	StationInboxes map[string][]string             `json:"station_inboxes"`
	Receipts       []Receipt                       `json:"receipts"`
	Heard          map[string]map[string]time.Time `json:"-"`
	RelayForwards  int                             `json:"relay_forwards"`
	Transmissions  int                             `json:"transmissions"`
}

type simEvent struct {
	at       time.Time
	order    int
	kind     string // tx, rx, client, peer, bad
	txKey    string
	from     string
	to       string
	frame    *protocol.Frame
	downlink bool
	client   *ClientSendSpec
	peer     *PeerSpec
	bad      *MalformedSpec
}

type eventHeap []*simEvent

func (h eventHeap) Len() int { return len(h) }
func (h eventHeap) Less(i, j int) bool {
	if h[i].at.Equal(h[j].at) {
		return h[i].order < h[j].order
	}
	return h[i].at.Before(h[j].at)
}
func (h eventHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *eventHeap) Push(x any)   { *h = append(*h, x.(*simEvent)) }
func (h *eventHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

type runner struct {
	sc            *Scenario
	hub           *core.Hub
	heap          eventHeap
	order         int
	heard         map[string]map[string]time.Time
	relaySeen     map[string]map[string]bool
	stationSeen   map[string]map[string]bool
	inboxes       map[string][]string
	receipts      []Receipt
	relayForwards int
	transmissions int
}

func Run(sc *Scenario) *Result {
	rn := &runner{
		sc: sc, hub: core.NewHub(sc.Start),
		heard:       map[string]map[string]time.Time{},
		relaySeen:   map[string]map[string]bool{},
		stationSeen: map[string]map[string]bool{},
		inboxes:     map[string][]string{},
	}
	for _, n := range sc.Nodes {
		rn.hub.EnsureStation(core.Station{
			Call: n.Call, Name: n.Name, Kind: n.Kind, Aliases: n.Aliases,
			RangeKm: n.RangeKm, Lat: n.Lat, Lon: n.Lon,
			SpeedKmh: n.SpeedKmh, Heading: n.Heading,
		})
	}
	for _, c := range sc.Clients {
		rn.hub.RegisterClient(c.ID, c.Call, c.PasscodeOK, c.Subs, sc.Start)
	}

	for _, op := range sc.Originals {
		mid := messageIDOrEmpty(op.Frame)
		rn.push(&simEvent{
			at: op.At, kind: "tx", from: op.Frame.Src, frame: op.Frame,
			txKey: "T:" + mid + ":0",
		})
	}
	for i := range sc.ClientSends {
		cs := sc.ClientSends[i]
		rn.push(&simEvent{at: cs.At, kind: "client", client: &cs})
	}
	for i := range sc.Peer {
		p := sc.Peer[i]
		rn.push(&simEvent{at: p.At, kind: "peer", peer: &p})
	}
	for i := range sc.Malformed {
		b := sc.Malformed[i]
		rn.push(&simEvent{at: b.At, kind: "bad", bad: &b})
	}

	for rn.heap.Len() > 0 {
		ev := heap.Pop(&rn.heap).(*simEvent)
		rn.hub.SetNow(ev.at)
		switch ev.kind {
		case "tx":
			rn.handleTx(ev)
		case "rx":
			rn.handleRx(ev)
		case "client":
			rn.hub.ClientSend(ev.client.ClientID, ev.client.MessageID, ev.client.To, ev.client.Text, ev.at)
		case "peer":
			qf := protocol.WithQMarker(ev.peer.Frame, protocol.QNetwork, ev.peer.Via)
			rn.hub.IngestWire(qf.String(), core.Ingress{At: ev.at, Kind: "peer", Via: ev.peer.Via})
		case "bad":
			rn.hub.IngestWire(ev.bad.Raw, core.Ingress{At: ev.at, Kind: "gateway", Via: ev.bad.Via})
		}
		rn.drainOutbox(ev.at)
	}
	rn.hub.FinalizeTracks()

	return &Result{
		Scenario: sc, Hub: rn.hub, StationInboxes: rn.inboxes,
		Receipts: rn.receipts, Heard: rn.heard,
		RelayForwards: rn.relayForwards,
		Transmissions: rn.transmissions,
	}
}

func (r *runner) push(ev *simEvent) {
	r.order++
	ev.order = r.order
	heap.Push(&r.heap, ev)
}

func (r *runner) handleTx(ev *simEvent) {
	r.transmissions++
	sender := r.sc.NodeByCall[ev.from]
	if sender == nil {
		return
	}
	senderPos := sender.PositionAt(ev.at)
	for _, rx := range r.sc.Nodes {
		if rx.Call == sender.Call {
			continue
		}
		rxPos := rx.PositionAt(ev.at)
		if distKm(senderPos, rxPos) > sender.RangeKm {
			continue
		}
		fate := FateFor(r.sc.Cfg.Seed, ev.txKey, rx.Call, r.sc.Cfg)
		if !fate.Receive {
			continue
		}
		frame := ev.frame
		r.push(&simEvent{
			at: ev.at.Add(fate.Delay), kind: "rx", txKey: ev.txKey,
			from: ev.from, to: rx.Call, frame: frame, downlink: ev.downlink,
		})
		if fate.Duplicate {
			r.push(&simEvent{
				at: ev.at.Add(fate.Delay + fate.DuplicateDelay), kind: "rx", txKey: ev.txKey,
				from: ev.from, to: rx.Call, frame: frame, downlink: ev.downlink,
			})
		}
	}
}

func (r *runner) handleRx(ev *simEvent) {
	node := r.sc.NodeByCall[ev.to]
	if node == nil {
		return
	}
	mid := messageIDOrEmpty(ev.frame)
	switch node.Kind {
	case "gateway":
		if ev.downlink {
			return
		}
		if ev.from == ev.frame.Src {
			if r.heard[node.Call] == nil {
				r.heard[node.Call] = map[string]time.Time{}
			}
			r.heard[node.Call][ev.frame.Src] = ev.at
		}
		marker := protocol.QDirect
		if ev.frame.HasRelayHop() {
			marker = protocol.QRelayed
		}
		qf := protocol.WithQMarker(ev.frame, marker, node.Call)
		r.hub.IngestWire(qf.String(), core.Ingress{At: ev.at, Kind: "gateway", Via: node.Call})

	case "relay":
		if mid == "" {
			return
		}
		if r.relaySeen[node.Call] == nil {
			r.relaySeen[node.Call] = map[string]bool{}
		}
		if r.relaySeen[node.Call][mid] {
			return
		}
		newFrame, acted := protocol.RewriteForRelay(ev.frame, node.Call, node.Aliases)
		if !acted {
			return
		}
		r.relaySeen[node.Call][mid] = true
		r.relayForwards++
		stage := len(newFrame.RelayCalls())
		r.push(&simEvent{
			at: ev.at, kind: "tx", from: node.Call, frame: newFrame,
			txKey: fmt.Sprintf("R:%s:%s:%d", mid, node.Call, stage),
		})

	case "station":
		r.receiveStationMessage(ev, mid)
	}
}

func (r *runner) receiveStationMessage(ev *simEvent, mid string) {
	content := ev.frame.Innermost()
	fields, err := protocol.ParseFields(content.Body)
	if err != nil || fields.Type != "MSG" {
		return
	}
	target := protocol.NormalizeCall(fields.Values["to"])
	if target != ev.to {
		return
	}
	if mid == "" {
		mid = fields.Values["mid"]
	}
	if r.stationSeen[ev.to] == nil {
		r.stationSeen[ev.to] = map[string]bool{}
	}
	if r.stationSeen[ev.to][mid] {
		return
	}
	r.stationSeen[ev.to][mid] = true
	r.inboxes[ev.to] = append(r.inboxes[ev.to], mid)
	via := "direct-rf"
	if ev.downlink {
		via = ev.from
		r.hub.MarkDownlinkDelivered(mid, ev.from)
	}
	r.receipts = append(r.receipts, Receipt{
		At: ev.at, Call: ev.to, MessageID: mid, Via: via, From: ev.from, Downlink: ev.downlink,
	})
}

func (r *runner) drainOutbox(at time.Time) {
	for _, m := range r.hub.DrainOutbox() {
		r.routeChat(m, at)
	}
}

func (r *runner) routeChat(m *core.Message, at time.Time) {
	target := protocol.NormalizeCall(m.Target)
	bestGW := ""
	bestHeard := time.Time{}
	for gw, heard := range r.heard {
		t, ok := heard[target]
		if !ok {
			continue
		}
		if at.Sub(t) > r.sc.Cfg.HeardTTL {
			continue
		}
		if bestGW == "" || t.After(bestHeard) {
			bestGW, bestHeard = gw, t
		}
	}
	if bestGW == "" {
		r.hub.AddDownlink(m.ID, core.Downlink{
			At: at, Recipient: target, DeliveredRF: false,
		})
		return
	}
	ts := protocol.FormatMinuteSecond(at)
	if m.InferredAt != nil {
		ts = protocol.FormatMinuteSecond(*m.InferredAt)
	}
	inner := &protocol.Frame{
		Src: m.Source, Dst: "APMSG",
		Body: protocol.EncodeFields("MSG", "mid", m.ID, "ts", ts, "to", target, "text", m.Text),
	}
	wrapped := protocol.WrapThirdParty(bestGW, inner)
	r.hub.AddDownlink(m.ID, core.Downlink{
		At: at, Gateway: bestGW, HeardAt: bestHeard,
		AgeSeconds: at.Sub(bestHeard).Seconds(), Recipient: target,
		Frame: wrapped.String(), DeliveredRF: false,
	})
	r.push(&simEvent{
		at: at, kind: "tx", from: bestGW, frame: wrapped, downlink: true,
		txKey: "D:" + m.ID + ":" + bestGW,
	})
}

func messageIDOrEmpty(f *protocol.Frame) string {
	if f == nil {
		return ""
	}
	content := f.Innermost()
	fields, err := protocol.ParseFields(content.Body)
	if err != nil {
		return ""
	}
	return fields.Values["mid"]
}

// PositionAt returns deterministic position history for mobile nodes. Static
// infrastructure returns its configured location.
func (n *Node) PositionAt(at time.Time) GeoPoint {
	if len(n.History) == 0 {
		return GeoPoint{Lat: n.Lat, Lon: n.Lon}
	}
	i := sort.Search(len(n.History), func(i int) bool {
		return n.History[i].At.After(at)
	}) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(n.History) {
		i = len(n.History) - 1
	}
	return GeoPoint{Lat: n.History[i].Lat, Lon: n.History[i].Lon}
}

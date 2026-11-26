package reference

import (
	"container/heap"
	"fmt"
	"math"
	"strings"
	"time"

	"amsgw/internal/protocol"
	"amsgw/internal/sim"
)

type ObjectState struct {
	Name         string
	Owner        string
	LastReporter string
	Active       bool
	Version      int
}

type Report struct {
	ClientSets  map[string]map[string]bool
	Objects     map[string]*ObjectState
	StationSets map[string]map[string]bool
	BadCount    int
	DeniedCount int
	SourceClass map[string]string
	QMarker     map[string]string
	RouteCalls  map[string][]string
	DownlinkGW  map[string]string
	RFDelivered map[string]bool
	Accepted    map[string]bool
}

type refClient struct {
	id, call string
	canSend  bool
	subs     map[string]bool // kinds
	calls    map[string]bool
	inbox    map[string]bool
}

type refMessage struct {
	id, kind, source, target string
	public                   bool
}

type refEvent struct {
	at       time.Time
	order    int
	kind     string
	txKey    string
	from, to string
	frame    *protocol.Frame
	downlink bool
	client   *sim.ClientSendSpec
	peer     *sim.PeerSpec
	bad      *sim.MalformedSpec
}

type eventHeap []*refEvent

func (h eventHeap) Len() int { return len(h) }
func (h eventHeap) Less(i, j int) bool {
	if h[i].at.Equal(h[j].at) {
		return h[i].order < h[j].order
	}
	return h[i].at.Before(h[j].at)
}
func (h eventHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *eventHeap) Push(x any)   { *h = append(*h, x.(*refEvent)) }
func (h *eventHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

type engine struct {
	sc    *sim.Scenario
	h     eventHeap
	order int

	clients map[string]*refClient
	objects map[string]*ObjectState
	msgs    map[string]*refMessage
	outbox  []string

	heard       map[string]map[string]time.Time
	relaySeen   map[string]map[string]bool
	stationSeen map[string]map[string]bool
	stations    map[string]map[string]bool

	badCount    int
	deniedCount int

	sourceClass map[string]string
	qMarker     map[string]string
	routeCalls  map[string][]string
	downlinkGW  map[string]string
	rfDelivered map[string]bool
}

func Run(sc *sim.Scenario) *Report {
	e := &engine{
		sc:          sc,
		clients:     map[string]*refClient{},
		objects:     map[string]*ObjectState{},
		msgs:        map[string]*refMessage{},
		heard:       map[string]map[string]time.Time{},
		relaySeen:   map[string]map[string]bool{},
		stationSeen: map[string]map[string]bool{},
		stations:    map[string]map[string]bool{},
		sourceClass: map[string]string{},
		qMarker:     map[string]string{},
		routeCalls:  map[string][]string{},
		downlinkGW:  map[string]string{},
		rfDelivered: map[string]bool{},
	}
	for _, cs := range sc.Clients {
		e.clients[cs.ID] = &refClient{
			id: cs.ID, call: cs.Call, canSend: cs.PasscodeOK,
			subs: cs.Subs.Kinds, calls: cs.Subs.Calls, inbox: map[string]bool{},
		}
	}
	for _, op := range sc.Originals {
		mid := messageID(op.Frame)
		e.push(&refEvent{at: op.At, kind: "tx", from: op.Frame.Src, frame: op.Frame, txKey: "T:" + mid + ":0"})
	}
	for i := range sc.ClientSends {
		cs := sc.ClientSends[i]
		e.push(&refEvent{at: cs.At, kind: "client", client: &cs})
	}
	for i := range sc.Peer {
		p := sc.Peer[i]
		e.push(&refEvent{at: p.At, kind: "peer", peer: &p})
	}
	for i := range sc.Malformed {
		b := sc.Malformed[i]
		e.push(&refEvent{at: b.At, kind: "bad", bad: &b})
	}

	for e.h.Len() > 0 {
		ev := heap.Pop(&e.h).(*refEvent)
		switch ev.kind {
		case "tx":
			e.tx(ev)
		case "rx":
			e.rx(ev)
		case "client":
			c := e.clients[ev.client.ClientID]
			if c == nil {
				continue
			}
			if !c.canSend {
				e.deniedCount++
				continue
			}
			m := &refMessage{id: ev.client.MessageID, kind: "chat", source: c.call, target: protocol.NormalizeCall(ev.client.To)}
			if e.acceptLocal(m, ev.at) {
				e.outbox = append(e.outbox, m.id)
			}
		case "peer":
			qf := withQ(ev.peer.Frame, protocol.QNetwork, ev.peer.Via)
			e.acceptFrame(qf, ev.at, "peer", ev.peer.Via)
		case "bad":
			_, err := protocol.ParseFrame(ev.bad.Raw)
			if err == nil {
				// The generator's semantic malformed cases may parse framing but
				// fail field validation. Run the same field gate.
				f, perr := protocol.ParseFrame(ev.bad.Raw)
				if perr == nil {
					content := f.Innermost()
					fields, ferr := protocol.ParseFields(content.Body)
					if ferr == nil {
						_, verr := validateSemantic(fields, ev.bad.At)
						if verr == nil {
							// A template unexpectedly became valid; count it as accepted
							// rather than bad. This should not happen with fixed templates.
							continue
						}
					}
				}
			}
			e.badCount++
		}
		e.drain(ev.at)
	}

	rep := &Report{
		ClientSets: map[string]map[string]bool{},
		Objects:    e.objects, StationSets: e.stations,
		BadCount: e.badCount, DeniedCount: e.deniedCount,
		SourceClass: e.sourceClass, QMarker: e.qMarker, RouteCalls: e.routeCalls,
		DownlinkGW: e.downlinkGW, RFDelivered: e.rfDelivered, Accepted: map[string]bool{},
	}
	for id, c := range e.clients {
		rep.ClientSets[id] = c.inbox
	}
	for mid := range e.msgs {
		rep.Accepted[mid] = true
	}
	return rep
}

func (e *engine) push(ev *refEvent) {
	e.order++
	ev.order = e.order
	heap.Push(&e.h, ev)
}

func (e *engine) tx(ev *refEvent) {
	sender := e.sc.NodeByCall[ev.from]
	if sender == nil {
		return
	}
	sp := sender.PositionAt(ev.at)
	for _, rx := range e.sc.Nodes {
		if rx.Call == sender.Call {
			continue
		}
		rp := rx.PositionAt(ev.at)
		if refDist(sp.Lat, sp.Lon, rp.Lat, rp.Lon) > sender.RangeKm {
			continue
		}
		fate := sim.FateFor(e.sc.Cfg.Seed, ev.txKey, rx.Call, e.sc.Cfg)
		if !fate.Receive {
			continue
		}
		e.push(&refEvent{at: ev.at.Add(fate.Delay), kind: "rx", from: ev.from, to: rx.Call, frame: ev.frame, downlink: ev.downlink, txKey: ev.txKey})
		if fate.Duplicate {
			e.push(&refEvent{at: ev.at.Add(fate.Delay + fate.DuplicateDelay), kind: "rx", from: ev.from, to: rx.Call, frame: ev.frame, downlink: ev.downlink, txKey: ev.txKey})
		}
	}
}

func (e *engine) rx(ev *refEvent) {
	node := e.sc.NodeByCall[ev.to]
	if node == nil {
		return
	}
	mid := messageID(ev.frame)
	switch node.Kind {
	case "gateway":
		if ev.downlink {
			return
		}
		if ev.from == ev.frame.Src {
			if e.heard[node.Call] == nil {
				e.heard[node.Call] = map[string]time.Time{}
			}
			e.heard[node.Call][ev.frame.Src] = ev.at
		}
		marker := protocol.QDirect
		if hasRelay(ev.frame) {
			marker = protocol.QRelayed
		}
		qf := withQ(ev.frame, marker, node.Call)
		e.acceptFrame(qf, ev.at, "gateway", node.Call)
	case "relay":
		if mid == "" {
			return
		}
		if e.relaySeen[node.Call] == nil {
			e.relaySeen[node.Call] = map[string]bool{}
		}
		if e.relaySeen[node.Call][mid] {
			return
		}
		nf, ok := rewrite(ev.frame, node.Call, node.Aliases)
		if !ok {
			return
		}
		e.relaySeen[node.Call][mid] = true
		stage := len(relayCalls(nf))
		e.push(&refEvent{at: ev.at, kind: "tx", from: node.Call, frame: nf, txKey: fmt.Sprintf("R:%s:%s:%d", mid, node.Call, stage)})
	case "station":
		e.stationMessage(ev, mid)
	}
}

func (e *engine) stationMessage(ev *refEvent, mid string) {
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
	if e.stationSeen[ev.to] == nil {
		e.stationSeen[ev.to] = map[string]bool{}
	}
	if e.stationSeen[ev.to][mid] {
		return
	}
	e.stationSeen[ev.to][mid] = true
	if e.stations[ev.to] == nil {
		e.stations[ev.to] = map[string]bool{}
	}
	e.stations[ev.to][mid] = true
	if ev.downlink {
		e.rfDelivered[mid] = true
	}
}

func (e *engine) acceptLocal(m *refMessage, at time.Time) bool {
	if _, ok := e.msgs[m.id]; ok {
		return false
	}
	e.msgs[m.id] = m
	e.fanout(m)
	return true
}

func (e *engine) acceptFrame(f *protocol.Frame, at time.Time, ingress, via string) bool {
	content := f.Innermost()
	fields, err := protocol.ParseFields(content.Body)
	if err != nil {
		e.badCount++
		return false
	}
	mid := fields.Values["mid"]
	if mid == "" {
		e.badCount++
		return false
	}
	if _, exists := e.msgs[mid]; exists {
		return false
	}
	validated, err := validateSemantic(fields, at)
	if err != nil {
		e.badCount++
		return false
	}
	source := content.Src
	kind := validated.kind
	msg := &refMessage{id: mid, kind: kind, source: source}
	class := "unknown"
	marker, _ := qMarker(f)
	if ingress == "peer" {
		class = protocol.SourceNetwork
		if marker == "" {
			marker = protocol.QNetwork
		}
	} else {
		class = classify(f)
	}
	e.sourceClass[mid] = class
	e.qMarker[mid] = marker
	e.routeCalls[mid] = relayCalls(f)

	switch kind {
	case "chat":
		msg.target = protocol.NormalizeCall(fields.Values["to"])
	case "object":
		name := fields.Values["name"]
		msg.public = true
		obj, ok := e.objects[name]
		if !ok {
			obj = &ObjectState{Name: name, Owner: source, Active: true}
			e.objects[name] = obj
		}
		obj.LastReporter = source
		obj.Active = true
		obj.Version++
	case "object-remove":
		name := fields.Values["name"]
		msg.kind = "object-remove"
		msg.target = ""
		msg.public = false
		// Record the command before the authorization branch so duplicated RF
		// copies cannot create repeated denials/NAKs.
		e.msgs[mid] = msg
		obj, ok := e.objects[name]
		if !ok || !obj.Active || obj.Owner != source {
			e.deniedCount++
			nakID := mid + ":NAK"
			nak := &refMessage{id: nakID, kind: "notice", source: "SRV", target: source}
			e.msgs[nakID] = nak
			e.fanout(nak)
			return false
		}
		obj.Active = false
		obj.Version++
		msg.public = true
	case "position", "weather":
		msg.public = true
	}

	e.msgs[mid] = msg
	e.fanout(msg)
	if kind == "chat" {
		msg.target = protocol.NormalizeCall(fields.Values["to"])
		e.outbox = append(e.outbox, mid)
	}
	return true
}

type semantic struct {
	kind string
}

func validateSemantic(f protocol.Fields, at time.Time) (semantic, error) {
	if ts := f.Values["ts"]; ts != "" {
		if _, err := protocol.ParseStamp(ts, at); err != nil {
			return semantic{}, err
		}
	}
	switch f.Type {
	case "LOC":
		if _, err := parseFloat(f.Values["lat"]); err != nil {
			return semantic{}, err
		}
		if _, err := parseFloat(f.Values["lon"]); err != nil {
			return semantic{}, err
		}
		return semantic{kind: "position"}, nil
	case "WX":
		if _, err := parseFloat(f.Values["temp"]); err != nil {
			return semantic{}, err
		}
		h, err := parseFloat(f.Values["hum"])
		if err != nil || h < 0 || h > 100 {
			return semantic{}, fmt.Errorf("bad humidity")
		}
		return semantic{kind: "weather"}, nil
	case "MSG":
		to := protocol.NormalizeCall(f.Values["to"])
		if !protocol.ValidCall(to) {
			return semantic{}, fmt.Errorf("bad to")
		}
		return semantic{kind: "chat"}, nil
	case "OBJ":
		if f.Values["name"] == "" {
			return semantic{}, fmt.Errorf("missing name")
		}
		act := strings.ToLower(f.Values["act"])
		if act == "" || act == "report" || act == "update" {
			if _, err := parseFloat(f.Values["lat"]); err != nil {
				return semantic{}, err
			}
			if _, err := parseFloat(f.Values["lon"]); err != nil {
				return semantic{}, err
			}
			return semantic{kind: "object"}, nil
		}
		if act == "remove" || act == "delete" {
			return semantic{kind: "object-remove"}, nil
		}
		return semantic{}, fmt.Errorf("bad action")
	default:
		return semantic{}, fmt.Errorf("unknown type")
	}
}

func parseFloat(s string) (float64, error) {
	var v float64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%f", &v)
	if err != nil {
		return 0, err
	}
	return v, nil
}

func (e *engine) fanout(m *refMessage) {
	for _, c := range e.clients {
		if match(c, m) {
			c.inbox[m.id] = true
		}
	}
}

func match(c *refClient, m *refMessage) bool {
	if m.kind == "chat" || m.kind == "notice" {
		return c.call == m.source || c.call == m.target
	}
	if !m.public {
		return false
	}
	if len(c.subs) > 0 && !c.subs[m.kind] {
		return false
	}
	if len(c.calls) > 0 && !c.calls[m.source] {
		return false
	}
	return true
}

func (e *engine) drain(at time.Time) {
	ids := e.outbox
	e.outbox = nil
	for _, id := range ids {
		m := e.msgs[id]
		if m == nil {
			continue
		}
		e.route(m, at)
	}
}

func (e *engine) route(m *refMessage, at time.Time) {
	target := protocol.NormalizeCall(m.target)
	bestGW := ""
	best := time.Time{}
	for gw, heard := range e.heard {
		t, ok := heard[target]
		if !ok || at.Sub(t) > e.sc.Cfg.HeardTTL {
			continue
		}
		if bestGW == "" || t.After(best) {
			bestGW, best = gw, t
		}
	}
	e.downlinkGW[m.id] = bestGW
	if bestGW == "" {
		return
	}
	inner := &protocol.Frame{Src: m.source, Dst: "APMSG", Body: protocol.EncodeFields("MSG", "mid", m.id, "to", target)}
	wrapped := protocol.WrapThirdParty(bestGW, inner)
	e.push(&refEvent{
		at: at, kind: "tx", from: bestGW, frame: wrapped, downlink: true,
		txKey: "D:" + m.id + ":" + bestGW,
	})
}

// rewrite is the reference expression of the alias/hop rule. It intentionally
// does not call protocol.RewriteForRelay.
func rewrite(in *protocol.Frame, relayCall string, aliases []string) (*protocol.Frame, bool) {
	allowed := map[string]bool{}
	for _, a := range aliases {
		allowed[protocol.NormalizeCall(a)] = true
	}
	for _, h := range in.Path {
		if h.Kind == protocol.HopRelay && h.Call == relayCall {
			return nil, false
		}
	}
	idx := -1
	for i, h := range in.Path {
		if h.Kind == protocol.HopAlias && h.Remaining > 0 && allowed[protocol.NormalizeCall(h.Alias)] {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, false
	}
	out := in.Clone()
	old := out.Path[idx]
	used := protocol.Hop{Kind: protocol.HopRelay, Call: relayCall, Alias: old.Alias, Done: true}
	old.Remaining--
	tail := append([]protocol.Hop{used, old}, out.Path[idx+1:]...)
	out.Path = append(append([]protocol.Hop{}, out.Path[:idx]...), tail...)
	return out, true
}

func messageID(f *protocol.Frame) string {
	if f == nil {
		return ""
	}
	fields, err := protocol.ParseFields(f.Innermost().Body)
	if err != nil {
		return ""
	}
	return fields.Values["mid"]
}

func relayCalls(f *protocol.Frame) []string {
	var out []string
	for _, h := range f.Path {
		if h.Kind == protocol.HopRelay {
			out = append(out, h.Call)
		}
	}
	return out
}

func hasRelay(f *protocol.Frame) bool { return len(relayCalls(f)) > 0 }

func qMarker(f *protocol.Frame) (string, string) {
	for i := len(f.Path) - 1; i >= 0; i-- {
		if f.Path[i].Kind == protocol.HopQ {
			return f.Path[i].Marker, f.Path[i].Via
		}
	}
	return "", ""
}

func classify(f *protocol.Frame) string {
	if hasRelay(f) {
		return protocol.SourceRelayed
	}
	return protocol.SourceDirect
}

func withQ(f *protocol.Frame, marker, via string) *protocol.Frame {
	out := f.Clone()
	out.Path = append(out.Path, protocol.Hop{Kind: protocol.HopQ, Marker: marker, Via: protocol.NormalizeCall(via)})
	return out
}

func refDist(lat1, lon1, lat2, lon2 float64) float64 {
	const km = 111.32
	mid := (lat1 + lat2) / 2
	dx := (lon2 - lon1) * km * math.Cos(mid*math.Pi/180)
	dy := (lat2 - lat1) * 110.574
	return math.Hypot(dx, dy)
}

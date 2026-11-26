package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"amsgw/internal/protocol"
)

var (
	ErrUnknownClient = errors.New("unknown client")
	ErrSendDenied    = errors.New("read-only login: wrong passcode")
	ErrUnauthorized  = errors.New("unauthorized")
)

type Ingress struct {
	At   time.Time
	Kind string // gateway, peer
	Via  string
}

type AcceptResult struct {
	Message   *Message
	Duplicate bool
	Rejected  bool
}

func (h *Hub) EnsureStation(s Station) {
	h.mu.Lock()
	defer h.mu.Unlock()
	call := protocol.NormalizeCall(s.Call)
	if old, ok := h.Stations[call]; ok {
		if s.Name != "" {
			old.Name = s.Name
		}
		if s.Kind != "" {
			old.Kind = s.Kind
		}
		if len(s.Aliases) > 0 {
			old.Aliases = append([]string(nil), s.Aliases...)
		}
		if s.RangeKm > 0 {
			old.RangeKm = s.RangeKm
		}
		return
	}
	s.Call = call
	if s.Track == nil {
		s.Track = []TrackPoint{}
	}
	h.Stations[call] = &s
	h.StationOrder = append(h.StationOrder, call)
}

func (h *Hub) RecordBad(at time.Time, via, raw, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.BadPackets = append(h.BadPackets, BadPacket{At: at, Via: via, Raw: raw, Reason: reason})
}

// IngestWire parses and accepts one frame arriving from a gateway or peer.
func (h *Hub) IngestWire(raw string, ing Ingress) (*AcceptResult, error) {
	at := ing.At
	if at.IsZero() {
		h.mu.RLock()
		at = h.Now
		h.mu.RUnlock()
	}
	frame, err := protocol.ParseFrame(raw)
	if err != nil {
		h.RecordBad(at, ing.Via, raw, err.Error())
		return nil, err
	}
	return h.AcceptFrame(frame, ing), nil
}

func (h *Hub) AcceptFrame(frame *protocol.Frame, ing Ingress) *AcceptResult {
	at := ing.At
	if at.IsZero() {
		h.mu.RLock()
		at = h.Now
		h.mu.RUnlock()
	}
	raw := frame.Raw
	if raw == "" {
		raw = frame.String()
	}
	content := frame.Innermost()
	fields, err := protocol.ParseFields(content.Body)
	if err != nil {
		h.RecordBad(at, ing.Via, raw, err.Error())
		return &AcceptResult{Rejected: true}
	}
	mid := fields.Values["mid"]
	if mid == "" {
		err := errors.New("missing mid")
		h.RecordBad(at, ing.Via, raw, err.Error())
		return &AcceptResult{Rejected: true}
	}

	stampRaw := fields.Values["ts"]
	var parsed protocol.ParsedStamp
	if stampRaw != "" {
		parsed, err = protocol.ParseStamp(stampRaw, at)
		if err != nil {
			h.RecordBad(at, ing.Via, raw, err.Error())
			return &AcceptResult{Rejected: true}
		}
	} else {
		parsed = protocol.ParsedStamp{Time: at, Precision: "received"}
	}

	source := content.Src
	msg := &Message{
		ID:             mid,
		Source:         source,
		WireStamp:      stampRaw,
		StampPrecision: string(parsed.Precision),
		ReceivedAt:     at,
		Path:           append([]protocol.Hop(nil), frame.Path...),
		RouteCalls:     frame.RelayCalls(),
		Raw:            raw,
	}
	if !parsed.Time.IsZero() {
		t := parsed.Time
		msg.InferredAt = &t
		msg.SkewSeconds = at.Sub(t).Seconds()
		msg.TimeBad = parsed.BadSkew
	}
	marker, markerVia := frame.QMarker()
	msg.QMarker = marker
	if markerVia != "" && ing.Via == "" {
		msg.Via = markerVia
	} else {
		msg.Via = ing.Via
	}
	msg.SourceClass = frame.SourceClass()
	if ing.Kind == "peer" {
		msg.SourceClass = protocol.SourceNetwork
		if msg.QMarker == "" {
			msg.QMarker = protocol.QNetwork
		}
	}
	if frame.ThirdParty != nil {
		msg.WrappedBy = frame.Src
	}

	switch fields.Type {
	case "LOC":
		msg.Kind = "position"
		msg.Lat, err = parseFloatField(fields.Values, "lat", -90, 90)
		if err != nil {
			return h.badField(raw, at, ing, mid, err)
		}
		msg.Lon, err = parseFloatField(fields.Values, "lon", -180, 180)
		if err != nil {
			return h.badField(raw, at, ing, mid, err)
		}
		msg.HasLocation = true
		msg.SpeedKmh, _ = strconv.ParseFloat(fields.Values["spd"], 64)
		msg.Heading, _ = strconv.ParseFloat(fields.Values["hdg"], 64)
		msg.ReportReason = fields.Values["reason"]
		msg.Public = true
	case "WX":
		msg.Kind = "weather"
		msg.TempC, err = strconv.ParseFloat(fields.Values["temp"], 64)
		if err != nil {
			return h.badField(raw, at, ing, mid, fmt.Errorf("bad temp: %w", err))
		}
		msg.Humidity, err = strconv.ParseFloat(fields.Values["hum"], 64)
		if err != nil || msg.Humidity < 0 || msg.Humidity > 100 {
			return h.badField(raw, at, ing, mid, fmt.Errorf("bad humidity"))
		}
		msg.WindKmh, _ = strconv.ParseFloat(fields.Values["wind"], 64)
		msg.Public = true
	case "MSG":
		msg.Kind = "chat"
		msg.Target = protocol.NormalizeCall(fields.Values["to"])
		if !protocol.ValidCall(msg.Target) {
			return h.badField(raw, at, ing, mid, fmt.Errorf("bad message recipient"))
		}
		msg.Text = fields.Values["text"]
	case "OBJ":
		action := strings.ToLower(fields.Values["act"])
		if action == "" {
			action = "report"
		}
		name := strings.TrimSpace(fields.Values["name"])
		if name == "" {
			return h.badField(raw, at, ing, mid, fmt.Errorf("missing object name"))
		}
		msg.Object = name
		msg.ObjectAction = action
		switch action {
		case "report", "update":
			msg.Kind = "object"
			msg.ObjectAction = "report"
			msg.Lat, err = parseFloatField(fields.Values, "lat", -90, 90)
			if err != nil {
				return h.badField(raw, at, ing, mid, err)
			}
			msg.Lon, err = parseFloatField(fields.Values, "lon", -180, 180)
			if err != nil {
				return h.badField(raw, at, ing, mid, err)
			}
			msg.HasLocation = true
			msg.Public = true
		case "remove", "delete":
			msg.Kind = "object-remove"
			msg.ObjectAction = "remove"
			msg.Public = true // changed only after authorization check
		default:
			return h.badField(raw, at, ing, mid, fmt.Errorf("unknown object action %q", action))
		}
	default:
		return h.badField(raw, at, ing, mid, fmt.Errorf("unknown packet type %q", fields.Type))
	}

	rec := Reception{
		At: at, Via: msg.Via, Ingress: ing.Kind, SourceClass: msg.SourceClass,
		QMarker: marker, Path: append([]protocol.Hop(nil), frame.Path...),
		RouteCalls: append([]string(nil), msg.RouteCalls...), Raw: raw,
	}
	msg.Receptions = []Reception{rec}

	h.mu.Lock()
	if old, ok := h.ByID[mid]; ok {
		old.Receptions = append(old.Receptions, rec)
		h.mu.Unlock()
		return &AcceptResult{Message: old, Duplicate: true}
	}

	// Object deletion is an authorization rule, not a geometry/state update.
	if msg.Kind == "object-remove" {
		obj, exists := h.Objects[msg.Object]
		if !exists || !obj.Active {
			h.Denials = append(h.Denials, Denial{At: at, Call: msg.Source, Action: "object-remove:" + msg.Object, Reason: "object does not exist"})
			msg.Public = false
			h.appendLocked(msg)
			nak := h.noticeLocked(mid+":NAK", msg.Source, at, "cannot remove "+msg.Object+": not active")
			h.appendLocked(nak)
			h.fanoutLocked(nak)
			h.mu.Unlock()
			return &AcceptResult{Message: msg, Rejected: true}
		}
		if obj.Owner != msg.Source {
			h.Denials = append(h.Denials, Denial{At: at, Call: msg.Source, Action: "object-remove:" + msg.Object, Reason: ErrUnauthorized.Error()})
			msg.Public = false
			h.appendLocked(msg)
			nak := h.noticeLocked(mid+":NAK", msg.Source, at, "only "+obj.Owner+" can remove "+msg.Object)
			h.appendLocked(nak)
			h.fanoutLocked(nak)
			h.mu.Unlock()
			return &AcceptResult{Message: msg, Rejected: true}
		}
		obj.Active = false
		obj.UpdatedAt = at
		obj.Version++
	}

	h.appendLocked(msg)
	h.applyStateLocked(msg)
	h.fanoutLocked(msg)
	if msg.Kind == "chat" {
		h.outbox = append(h.outbox, msg.ID)
	}
	h.mu.Unlock()
	return &AcceptResult{Message: msg}
}

func (h *Hub) badField(raw string, at time.Time, ing Ingress, mid string, err error) *AcceptResult {
	h.RecordBad(at, ing.Via, raw, err.Error())
	return &AcceptResult{Rejected: true}
}

func parseFloatField(vals map[string]string, key string, min, max float64) (float64, error) {
	s := vals[key]
	if s == "" {
		return 0, fmt.Errorf("missing %s", key)
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("bad %s: %w", key, err)
	}
	if v < min || v > max {
		return 0, fmt.Errorf("%s out of range", key)
	}
	return v, nil
}

func (h *Hub) appendLocked(m *Message) {
	h.ByID[m.ID] = m
	h.Messages = append(h.Messages, m)
}

func (h *Hub) noticeLocked(id, to string, at time.Time, text string) *Message {
	return &Message{
		ID: id, Kind: "notice", Source: "SRV", Target: to, Text: text,
		ReceivedAt: at, SourceClass: "server", Public: false,
	}
}

func (h *Hub) applyStateLocked(m *Message) {
	switch m.Kind {
	case "position":
		call := m.Source
		when := m.ReceivedAt
		if m.InferredAt != nil {
			when = *m.InferredAt
		}
		st, ok := h.Stations[call]
		if !ok {
			st = &Station{Call: call, Kind: "station"}
			h.Stations[call] = st
			h.StationOrder = append(h.StationOrder, call)
		}
		st.Lat, st.Lon = m.Lat, m.Lon
		st.SpeedKmh, st.Heading = m.SpeedKmh, m.Heading
		st.LastSeen = when
		st.LastReportID = m.ID
		st.ReportReason = m.ReportReason
		st.Track = append(st.Track, TrackPoint{
			At: m.ReceivedAt, Lat: m.Lat, Lon: m.Lon,
			Speed: m.SpeedKmh, Heading: m.Heading,
			Reason: m.ReportReason, MessageID: m.ID,
		})
	case "weather":
		h.Weather[m.Source] = &Weather{
			Call: m.Source, At: m.ReceivedAt, TempC: m.TempC,
			Humidity: m.Humidity, WindKmh: m.WindKmh, MessageID: m.ID,
		}
	case "object":
		obj, ok := h.Objects[m.Object]
		if !ok {
			obj = &Object{Name: m.Object, Owner: m.Source, CreatedAt: m.ReceivedAt, Active: true, Version: 0}
			h.Objects[m.Object] = obj
			h.ObjectOrder = append(h.ObjectOrder, m.Object)
		}
		obj.Lat, obj.Lon = m.Lat, m.Lon
		obj.LastReporter = m.Source
		obj.UpdatedAt = m.ReceivedAt
		obj.Active = true
		obj.Version++
		obj.LatestMessage = m.ID
	case "object-remove":
		// Handled before fanout because ownership determines visibility.
	}
}

func (h *Hub) fanoutLocked(m *Message) {
	for _, id := range h.ClientOrder {
		c := h.Clients[id]
		if subscriptionMatches(c, m) {
			c.Inbox = append(c.Inbox, m.ID)
		}
	}
}

func subscriptionMatches(c *Client, m *Message) bool {
	if m.Kind == "chat" || m.Kind == "notice" {
		return c.Call == m.Source || c.Call == m.Target
	}
	if !m.Public {
		return false
	}
	if len(c.Subs.Kinds) > 0 && !c.Subs.Kinds[m.Kind] {
		return false
	}
	if len(c.Subs.Calls) > 0 && !c.Subs.Calls[m.Source] {
		return false
	}
	return true
}

func (h *Hub) DrainOutbox() []*Message {
	h.mu.Lock()
	ids := h.outbox
	h.outbox = nil
	out := make([]*Message, 0, len(ids))
	for _, id := range ids {
		if m, ok := h.ByID[id]; ok {
			out = append(out, m)
		}
	}
	h.mu.Unlock()
	return out
}

func (h *Hub) AddDownlink(id string, d Downlink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m, ok := h.ByID[id]; ok {
		m.Downlinks = append(m.Downlinks, d)
	}
}

func (h *Hub) ClientSend(clientID, messageID, to, text string, at time.Time) (*Message, error) {
	h.mu.Lock()
	c, ok := h.Clients[clientID]
	if !ok {
		h.mu.Unlock()
		return nil, ErrUnknownClient
	}
	if !c.CanSend {
		h.Denials = append(h.Denials, Denial{At: at, Call: c.Call, Action: "send", Reason: ErrSendDenied.Error()})
		h.mu.Unlock()
		return nil, ErrSendDenied
	}
	m := &Message{
		ID: messageID, Kind: "chat", Source: c.Call, Target: protocol.NormalizeCall(to),
		Text: text, ReceivedAt: at, SourceClass: protocol.SourceClient, Public: false,
	}
	if !protocol.ValidCall(m.Target) {
		h.mu.Unlock()
		return nil, fmt.Errorf("bad recipient")
	}
	if _, exists := h.ByID[messageID]; exists {
		h.mu.Unlock()
		return nil, fmt.Errorf("duplicate message id")
	}
	h.appendLocked(m)
	h.fanoutLocked(m)
	h.outbox = append(h.outbox, m.ID)
	h.mu.Unlock()
	return m, nil
}

// AuthenticatedSend is used by the HTTP API where no simulated client ID is
// available. A short-lived client record is not required for authorization.
func (h *Hub) AuthenticatedSend(call, passcode, messageID, to, text string, at time.Time) (*Message, error) {
	call = protocol.NormalizeCall(call)
	if !protocol.CheckPasscode(call, passcode) {
		h.mu.Lock()
		h.Denials = append(h.Denials, Denial{At: at, Call: call, Action: "send", Reason: ErrSendDenied.Error()})
		h.mu.Unlock()
		return nil, ErrSendDenied
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	m := &Message{
		ID: messageID, Kind: "chat", Source: call, Target: protocol.NormalizeCall(to),
		Text: text, ReceivedAt: at, SourceClass: protocol.SourceClient, Public: false,
	}
	if !protocol.ValidCall(m.Target) {
		return nil, fmt.Errorf("bad recipient")
	}
	if _, exists := h.ByID[messageID]; exists {
		return nil, fmt.Errorf("duplicate message id")
	}
	h.appendLocked(m)
	h.fanoutLocked(m)
	h.outbox = append(h.outbox, m.ID)
	return m, nil
}

func (h *Hub) RemoveObjectHTTP(call, passcode, name, messageID string, at time.Time) (*Message, error) {
	call = protocol.NormalizeCall(call)
	if !protocol.CheckPasscode(call, passcode) {
		h.mu.Lock()
		h.Denials = append(h.Denials, Denial{At: at, Call: call, Action: "object-remove:" + name, Reason: ErrSendDenied.Error()})
		h.mu.Unlock()
		return nil, ErrSendDenied
	}
	m := &Message{
		ID: messageID, Kind: "object-remove", Source: call, Object: name,
		ObjectAction: "remove", ReceivedAt: at, SourceClass: protocol.SourceClient,
		Raw: "WEB OBJECT REMOVE " + name,
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.ByID[messageID]; exists {
		return nil, fmt.Errorf("duplicate message id")
	}
	obj, ok := h.Objects[name]
	if !ok || !obj.Active {
		h.Denials = append(h.Denials, Denial{At: at, Call: call, Action: "object-remove:" + name, Reason: "not found"})
		m.Public = false
		h.appendLocked(m)
		nak := h.noticeLocked(messageID+":NAK", call, at, "object not found")
		h.appendLocked(nak)
		h.fanoutLocked(nak)
		return m, ErrUnauthorized
	}
	if obj.Owner != call {
		h.Denials = append(h.Denials, Denial{At: at, Call: call, Action: "object-remove:" + name, Reason: ErrUnauthorized.Error()})
		m.Public = false
		h.appendLocked(m)
		nak := h.noticeLocked(messageID+":NAK", call, at, "only "+obj.Owner+" can remove "+name)
		h.appendLocked(nak)
		h.fanoutLocked(nak)
		return m, ErrUnauthorized
	}
	obj.Active = false
	obj.UpdatedAt = at
	obj.Version++
	h.appendLocked(m)
	h.applyStateLocked(m)
	h.fanoutLocked(m)
	return m, nil
}

func (h *Hub) Snapshot() map[string]any {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return map[string]any{
		"now":           h.Now,
		"messages":      h.Messages,
		"clients":       h.Clients,
		"client_order":  h.ClientOrder,
		"stations":      h.Stations,
		"station_order": h.StationOrder,
		"objects":       h.Objects,
		"object_order":  h.ObjectOrder,
		"weather":       h.Weather,
		"bad_packets":   h.BadPackets,
		"denials":       h.Denials,
	}
}

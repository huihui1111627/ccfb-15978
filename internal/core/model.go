package core

import (
	"sort"
	"sync"
	"time"

	"amsgw/internal/protocol"
)

type GeoPoint struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type TrackPoint struct {
	At        time.Time `json:"at"`
	Lat       float64   `json:"lat"`
	Lon       float64   `json:"lon"`
	Speed     float64   `json:"speed_kmh,omitempty"`
	Heading   float64   `json:"heading,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	MessageID string    `json:"message_id"`
}

type Station struct {
	Call         string       `json:"call"`
	Name         string       `json:"name,omitempty"`
	Kind         string       `json:"kind"`
	Aliases      []string     `json:"aliases,omitempty"`
	RangeKm      float64      `json:"range_km,omitempty"`
	Lat          float64      `json:"lat"`
	Lon          float64      `json:"lon"`
	SpeedKmh     float64      `json:"speed_kmh"`
	Heading      float64      `json:"heading"`
	LastSeen     time.Time    `json:"last_seen"`
	LastReportID string       `json:"last_report_id,omitempty"`
	ReportReason string       `json:"report_reason,omitempty"`
	Track        []TrackPoint `json:"track"`
}

type Weather struct {
	Call      string    `json:"call"`
	At        time.Time `json:"at"`
	TempC     float64   `json:"temp_c"`
	Humidity  float64   `json:"humidity_pct"`
	WindKmh   float64   `json:"wind_kmh,omitempty"`
	MessageID string    `json:"message_id"`
}

type Object struct {
	Name          string    `json:"name"`
	Lat           float64   `json:"lat"`
	Lon           float64   `json:"lon"`
	Owner         string    `json:"owner"`
	LastReporter  string    `json:"last_reporter"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Active        bool      `json:"active"`
	Version       int       `json:"version"`
	LatestMessage string    `json:"latest_message"`
}

type Reception struct {
	At          time.Time      `json:"at"`
	Via         string         `json:"via"`
	Ingress     string         `json:"ingress"`
	SourceClass string         `json:"source_class"`
	QMarker     string         `json:"q_marker,omitempty"`
	Path        []protocol.Hop `json:"path,omitempty"`
	RouteCalls  []string       `json:"route_calls,omitempty"`
	Raw         string         `json:"raw"`
}

type Downlink struct {
	At          time.Time `json:"at"`
	Gateway     string    `json:"gateway"`
	HeardAt     time.Time `json:"heard_at"`
	AgeSeconds  float64   `json:"heard_age_seconds"`
	Recipient   string    `json:"recipient"`
	DeliveredRF bool      `json:"delivered_rf"`
	Frame       string    `json:"frame"`
}

type Message struct {
	ID             string         `json:"id"`
	Kind           string         `json:"kind"`
	Source         string         `json:"source"`
	Target         string         `json:"target,omitempty"`
	Object         string         `json:"object,omitempty"`
	ObjectAction   string         `json:"object_action,omitempty"`
	Text           string         `json:"text,omitempty"`
	Lat            float64        `json:"lat,omitempty"`
	Lon            float64        `json:"lon,omitempty"`
	HasLocation    bool           `json:"has_location,omitempty"`
	SpeedKmh       float64        `json:"speed_kmh,omitempty"`
	Heading        float64        `json:"heading,omitempty"`
	TempC          float64        `json:"temp_c,omitempty"`
	Humidity       float64        `json:"humidity_pct,omitempty"`
	WindKmh        float64        `json:"wind_kmh,omitempty"`
	ReportReason   string         `json:"report_reason,omitempty"`
	WireStamp      string         `json:"wire_stamp,omitempty"`
	StampPrecision string         `json:"stamp_precision,omitempty"`
	InferredAt     *time.Time     `json:"inferred_at,omitempty"`
	ReceivedAt     time.Time      `json:"received_at"`
	SkewSeconds    float64        `json:"skew_seconds,omitempty"`
	TimeBad        bool           `json:"time_bad"`
	Path           []protocol.Hop `json:"path,omitempty"`
	RouteCalls     []string       `json:"route_calls,omitempty"`
	SourceClass    string         `json:"source_class"`
	Via            string         `json:"via,omitempty"`
	QMarker        string         `json:"q_marker,omitempty"`
	WrappedBy      string         `json:"wrapped_by,omitempty"`
	Raw            string         `json:"raw"`
	Receptions     []Reception    `json:"receptions,omitempty"`
	Downlinks      []Downlink     `json:"downlinks,omitempty"`
	Public         bool           `json:"public"`
}

type Subscription struct {
	Kinds map[string]bool `json:"kinds,omitempty"`
	Calls map[string]bool `json:"calls,omitempty"`
}

type Client struct {
	ID          string       `json:"id"`
	Call        string       `json:"call"`
	CanSend     bool         `json:"can_send"`
	PasscodeOK  bool         `json:"passcode_ok"`
	Subs        Subscription `json:"subs"`
	ConnectedAt time.Time    `json:"connected_at"`
	Inbox       []string     `json:"inbox"`
}

type BadPacket struct {
	At     time.Time `json:"at"`
	Via    string    `json:"via"`
	Raw    string    `json:"raw"`
	Reason string    `json:"reason"`
}

type Denial struct {
	At     time.Time `json:"at"`
	Call   string    `json:"call"`
	Action string    `json:"action"`
	Reason string    `json:"reason"`
}

type Hub struct {
	mu sync.RWMutex

	Now time.Time `json:"now"`

	Messages     []*Message          `json:"messages"`
	ByID         map[string]*Message `json:"-"`
	Clients      map[string]*Client  `json:"clients"`
	ClientOrder  []string            `json:"client_order"`
	Stations     map[string]*Station `json:"stations"`
	StationOrder []string            `json:"station_order"`
	Objects      map[string]*Object  `json:"objects"`
	ObjectOrder  []string            `json:"object_order"`
	Weather      map[string]*Weather `json:"weather"`
	BadPackets   []BadPacket         `json:"bad_packets"`
	Denials      []Denial            `json:"denials"`

	outbox []string
}

func NewHub(now time.Time) *Hub {
	return &Hub{
		Now:      now,
		ByID:     map[string]*Message{},
		Clients:  map[string]*Client{},
		Stations: map[string]*Station{},
		Objects:  map[string]*Object{},
		Weather:  map[string]*Weather{},
	}
}

func (h *Hub) SetNow(t time.Time) {
	h.mu.Lock()
	h.Now = t
	h.mu.Unlock()
}

func (h *Hub) NowTime() time.Time {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.Now
}

func (h *Hub) RegisterClient(id, call string, passcodeOK bool, subs Subscription, at time.Time) *Client {
	call = protocol.NormalizeCall(call)
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.Clients[id]; ok {
		return existing
	}
	c := &Client{
		ID: id, Call: call, PasscodeOK: passcodeOK, CanSend: passcodeOK,
		Subs: subs, ConnectedAt: at, Inbox: []string{},
	}
	h.Clients[id] = c
	h.ClientOrder = append(h.ClientOrder, id)
	return c
}

func (h *Hub) ClientByCall(call string) *Client {
	call = protocol.NormalizeCall(call)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, id := range h.ClientOrder {
		if c := h.Clients[id]; c.Call == call {
			return c
		}
	}
	return nil
}

func (h *Hub) SortedClientIDs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := append([]string(nil), h.ClientOrder...)
	sort.Strings(out)
	return out
}

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"amsgw/internal/core"
	"amsgw/internal/protocol"
	"amsgw/internal/reference"
	"amsgw/internal/sim"
)

type Server struct {
	Res *sim.Result
	Hub *core.Hub
	Sc  *sim.Scenario

	baseSets     map[string]map[string]bool
	baseObjects  map[string]core.Object
	baseMessages int
	baseBad      int
	baseDenials  int

	verifyMu sync.RWMutex
	verify   VerifyReport
}

type VerifyReport struct {
	Running       bool      `json:"running"`
	Done          bool      `json:"done"`
	OK            bool      `json:"ok"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
	Summary       string    `json:"summary"`
	Mismatches    []string  `json:"mismatches,omitempty"`
	Clients       int       `json:"clients"`
	Originals     int       `json:"originals"`
	HubMessages   int       `json:"hub_messages"`
	RelayForwards int       `json:"relay_forwards"`
}

func NewServer(res *sim.Result) *Server {
	return &Server{
		Res: res, Hub: res.Hub, Sc: res.Scenario,
		baseSets:     res.Hub.ClientInboxSets(),
		baseObjects:  res.Hub.ObjectOwners(),
		baseMessages: len(res.Hub.Messages),
		baseBad:      len(res.Hub.BadPackets),
		baseDenials:  len(res.Hub.Denials),
		verify:       VerifyReport{Running: true, StartedAt: time.Now().UTC()},
	}
}

func (s *Server) StartVerification() {
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				s.verifyMu.Lock()
				s.verify.Running = false
				s.verify.Done = true
				s.verify.OK = false
				s.verify.Summary = fmt.Sprintf("参照实现运行失败: %v", rec)
				s.verify.FinishedAt = time.Now().UTC()
				s.verifyMu.Unlock()
			}
		}()
		want := reference.Run(s.Sc)
		rep := VerifyReport{
			Running: false, Done: true, StartedAt: s.verify.StartedAt,
			FinishedAt: time.Now().UTC(), Clients: len(want.ClientSets),
			Originals: len(s.Sc.Originals), HubMessages: s.baseMessages,
			RelayForwards: s.Res.RelayForwards,
		}
		var mismatches []string
		for id, expected := range want.ClientSets {
			actual := s.baseSets[id]
			var missing, extra []string
			for mid := range expected {
				if !actual[mid] {
					missing = append(missing, mid)
				}
			}
			for mid := range actual {
				if !expected[mid] {
					extra = append(extra, mid)
				}
			}
			if len(missing)+len(extra) > 0 {
				sort.Strings(missing)
				sort.Strings(extra)
				mismatches = append(mismatches, fmt.Sprintf("%s missing=%d extra=%d firstMissing=%v firstExtra=%v",
					id, len(missing), len(extra), firstN(missing, 5), firstN(extra, 5)))
			}
		}
		if s.baseBad != want.BadCount {
			mismatches = append(mismatches, fmt.Sprintf("bad packets got=%d want=%d", s.baseBad, want.BadCount))
		}
		if s.baseDenials != want.DeniedCount {
			mismatches = append(mismatches, fmt.Sprintf("denials got=%d want=%d", s.baseDenials, want.DeniedCount))
		}
		for name, wo := range want.Objects {
			ao := s.baseObjects[name]
			if ao.Owner != wo.Owner || ao.Active != wo.Active || ao.Version != wo.Version {
				mismatches = append(mismatches, fmt.Sprintf("object %s mismatch", name))
			}
		}
		rep.Mismatches = mismatches
		rep.OK = len(mismatches) == 0
		if rep.OK {
			rep.Summary = fmt.Sprintf("固定种子参照实现完全一致：%d 个客户端集合、%d 个物体状态、%d 条畸形报文全部对上", rep.Clients, len(want.Objects), want.BadCount)
		} else {
			rep.Summary = fmt.Sprintf("发现 %d 类不一致", len(mismatches))
		}
		s.verifyMu.Lock()
		s.verify = rep
		s.verifyMu.Unlock()
	}()
}

func firstN(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return in[:n]
}

func (s *Server) verification() VerifyReport {
	s.verifyMu.RLock()
	defer s.verifyMu.RUnlock()
	return s.verify
}

func (s *Server) Handler(static http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/overview", s.handleOverview)
	mux.HandleFunc("/api/map", s.handleMap)
	mux.HandleFunc("/api/messages", s.handleMessages)
	mux.HandleFunc("/api/message", s.handleMessage)
	mux.HandleFunc("/api/clients", s.handleClients)
	mux.HandleFunc("/api/heard", s.handleHeard)
	mux.HandleFunc("/api/client", s.handleClient)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/send", s.handleSend)
	mux.HandleFunc("/api/object/remove", s.handleObjectRemove)
	mux.HandleFunc("/api/verify", s.handleVerify)
	if static != nil {
		mux.Handle("/", static)
	}
	return logging(mux)
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	var msgCount, positionCount, chatCount, objectCount, badTime, activeObjects, badCount, denialCount, stationCount, clientCount int
	var now time.Time
	s.Hub.ReadLocked(func() {
		now = s.Hub.Now
		msgCount = len(s.Hub.Messages)
		stationCount = len(s.Hub.StationOrder)
		clientCount = len(s.Hub.ClientOrder)
		badCount = len(s.Hub.BadPackets)
		denialCount = len(s.Hub.Denials)
		for _, m := range s.Hub.Messages {
			switch m.Kind {
			case "position":
				positionCount++
			case "chat":
				chatCount++
			case "object", "object-remove":
				objectCount++
			}
			if m.TimeBad {
				badTime++
			}
		}
		for _, o := range s.Hub.Objects {
			if o.Active {
				activeObjects++
			}
		}
	})
	writeJSON(w, 200, map[string]any{
		"config": s.Sc.Cfg,
		"start":  s.Sc.Start,
		"now":    now,
		"counts": map[string]int{
			"stations": stationCount, "clients": clientCount,
			"messages": msgCount, "positions": positionCount, "chats": chatCount,
			"object_events": objectCount, "objects_active": activeObjects,
			"bad_packets": badCount, "denials": denialCount,
			"time_bad": badTime, "relay_forwards": s.Res.RelayForwards,
			"transmissions": s.Res.Transmissions, "rf_receipts": len(s.Res.Receipts),
		},
		"verify": s.verification(),
	})
}

func (s *Server) handleMap(w http.ResponseWriter, r *http.Request) {
	s.Hub.ViewMapSnapshot(func(snap map[string]any) {
		writeJSON(w, 200, snap)
	})
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	q := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("q")))
	type summary struct {
		ID           string    `json:"id"`
		Kind         string    `json:"kind"`
		Source       string    `json:"source"`
		Target       string    `json:"target,omitempty"`
		Object       string    `json:"object,omitempty"`
		Text         string    `json:"text,omitempty"`
		ReceivedAt   time.Time `json:"received_at"`
		TimeBad      bool      `json:"time_bad"`
		SourceClass  string    `json:"source_class"`
		Via          string    `json:"via,omitempty"`
		RouteCalls   []string  `json:"route_calls,omitempty"`
		ReceptionN   int       `json:"reception_count"`
		DownlinkGW   string    `json:"downlink_gateway,omitempty"`
		DeliveredRF  bool      `json:"delivered_rf,omitempty"`
		ReportReason string    `json:"report_reason,omitempty"`
	}
	var out []summary
	s.Hub.ReadLocked(func() {
		for i := len(s.Hub.Messages) - 1; i >= 0 && len(out) < limit; i-- {
			m := s.Hub.Messages[i]
			if kind != "" && m.Kind != kind {
				continue
			}
			if q != "" && !strings.Contains(strings.ToUpper(m.Source+" "+m.Target+" "+m.Object+" "+m.ID+" "+m.Text), q) {
				continue
			}
			gw, delivered := "", false
			if len(m.Downlinks) > 0 {
				gw = m.Downlinks[0].Gateway
				delivered = m.Downlinks[0].DeliveredRF
			}
			out = append(out, summary{
				ID: m.ID, Kind: m.Kind, Source: m.Source, Target: m.Target,
				Object: m.Object, Text: m.Text, ReceivedAt: m.ReceivedAt,
				TimeBad: m.TimeBad, SourceClass: m.SourceClass, Via: m.Via,
				RouteCalls: m.RouteCalls, ReceptionN: len(m.Receptions),
				DownlinkGW: gw, DeliveredRF: delivered, ReportReason: m.ReportReason,
			})
		}
	})
	writeJSON(w, 200, map[string]any{"messages": out})
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	m := s.Hub.GetMessage(id)
	if m == nil {
		writeJSON(w, 404, map[string]string{"error": "message not found"})
		return
	}
	writeJSON(w, 200, m)
}

func (s *Server) handleClients(w http.ResponseWriter, r *http.Request) {
	type row struct {
		ID      string            `json:"id"`
		Call    string            `json:"call"`
		CanSend bool              `json:"can_send"`
		PassOK  bool              `json:"passcode_ok"`
		Subs    core.Subscription `json:"subs"`
		InboxN  int               `json:"inbox_count"`
	}
	var rows []row
	s.Hub.ReadLocked(func() {
		for _, id := range s.Hub.ClientOrder {
			c := s.Hub.Clients[id]
			rows = append(rows, row{ID: c.ID, Call: c.Call, CanSend: c.CanSend, PassOK: c.PasscodeOK, Subs: c.Subs, InboxN: len(c.Inbox)})
		}
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	writeJSON(w, 200, map[string]any{"clients": rows})
}

func (s *Server) handleHeard(w http.ResponseWriter, r *http.Request) {
	type heardRow struct {
		Call      string    `json:"call"`
		Gateway   string    `json:"gateway"`
		HeardAt   time.Time `json:"heard_at"`
		AgeSecond float64   `json:"age_seconds"`
		Eligible  bool      `json:"eligible"`
	}
	now := s.Hub.NowTime()
	best := map[string]heardRow{}
	for gw, heard := range s.Res.Heard {
		for call, at := range heard {
			row := heardRow{Call: call, Gateway: gw, HeardAt: at, AgeSecond: now.Sub(at).Seconds(),
				Eligible: now.Sub(at) <= s.Sc.Cfg.HeardTTL}
			if old, ok := best[call]; !ok || at.After(old.HeardAt) {
				best[call] = row
			}
		}
	}
	rows := make([]heardRow, 0, len(best))
	for _, row := range best {
		if row.Eligible {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].AgeSecond < rows[j].AgeSecond })
	if len(rows) > 200 {
		rows = rows[:200]
	}
	writeJSON(w, 200, map[string]any{"heard": rows, "ttl_seconds": s.Sc.Cfg.HeardTTL.Seconds()})
}

func (s *Server) handleClient(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	found := false
	s.Hub.ReadLocked(func() {
		if c, ok := s.Hub.Clients[id]; ok {
			found = true
			writeJSON(w, 200, c)
		}
	})
	if !found {
		writeJSON(w, 404, map[string]string{"error": "client not found"})
	}
}

type loginReq struct {
	Call     string `json:"call"`
	Passcode string `json:"passcode"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	call := protocol.NormalizeCall(req.Call)
	ok := protocol.CheckPasscode(call, req.Passcode)
	writeJSON(w, 200, map[string]any{
		"call": call, "passcode_ok": ok, "can_send": ok,
		"derived_passcode": protocol.Passcode(call),
	})
}

type sendReq struct {
	Call     string `json:"call"`
	Passcode string `json:"passcode"`
	To       string `json:"to"`
	Text     string `json:"text"`
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req sendReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	now := s.Hub.NowTime().Add(time.Second)
	s.Hub.SetNow(now)
	mid := fmt.Sprintf("WEB-%d", now.UnixNano())
	m, err := s.Hub.AuthenticatedSend(req.Call, req.Passcode, mid, req.To, req.Text, now)
	if err != nil {
		code := 400
		if err == core.ErrSendDenied {
			code = 403
		}
		writeJSON(w, code, map[string]any{"error": err.Error(), "derived_passcode": protocol.Passcode(req.Call)})
		return
	}
	dl := sim.RouteLiveChat(s.Res, m, now)
	writeJSON(w, 200, map[string]any{"message": m, "downlink": dl})
}

type removeReq struct {
	Call     string `json:"call"`
	Passcode string `json:"passcode"`
	Name     string `json:"name"`
}

func (s *Server) handleObjectRemove(w http.ResponseWriter, r *http.Request) {
	var req removeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	now := s.Hub.NowTime().Add(time.Second)
	s.Hub.SetNow(now)
	mid := fmt.Sprintf("WEBOBJ-%d", now.UnixNano())
	m, err := s.Hub.RemoveObjectHTTP(req.Call, req.Passcode, req.Name, mid, now)
	if err != nil {
		writeJSON(w, 403, map[string]any{"error": err.Error(), "message": m})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": m})
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.verification())
}

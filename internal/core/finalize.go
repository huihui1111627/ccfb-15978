package core

import "sort"

// FinalizeTracks orders potentially out-of-order delayed reports and makes the
// latest point correspond to the newest inferred report time.
func (h *Hub) FinalizeTracks() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, st := range h.Stations {
		sort.SliceStable(st.Track, func(i, j int) bool {
			return st.Track[i].At.Before(st.Track[j].At)
		})
		if len(st.Track) > 0 {
			last := st.Track[len(st.Track)-1]
			st.Lat, st.Lon = last.Lat, last.Lon
			st.SpeedKmh, st.Heading = last.Speed, last.Heading
			st.LastSeen = last.At
			st.LastReportID = last.MessageID
			for _, m := range h.Messages {
				if m.ID == last.MessageID {
					st.ReportReason = m.ReportReason
					break
				}
			}
		}
	}
}

func (h *Hub) MarkDownlinkDelivered(messageID, gateway string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m, ok := h.ByID[messageID]
	if !ok {
		return
	}
	for i := range m.Downlinks {
		if m.Downlinks[i].Gateway == gateway {
			m.Downlinks[i].DeliveredRF = true
		}
	}
}

func (h *Hub) ClientInboxSets() map[string]map[string]bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := map[string]map[string]bool{}
	for id, c := range h.Clients {
		set := map[string]bool{}
		for _, mid := range c.Inbox {
			set[mid] = true
		}
		out[id] = set
	}
	return out
}

func (h *Hub) ObjectOwners() map[string]Object {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := map[string]Object{}
	for k, v := range h.Objects {
		out[k] = *v
	}
	return out
}

func (h *Hub) GetMessage(id string) *Message {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.ByID[id]
}

func (h *Hub) ReadLocked(fn func()) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	fn()
}

func (h *Hub) MapSnapshot() map[string]any {
	var out map[string]any
	h.ViewMapSnapshot(func(snap map[string]any) { out = snap })
	return out
}

// ViewMapSnapshot keeps the read lock while fn encodes the snapshot, avoiding
// concurrent mutation of nested tracks/objects during JSON serialization.
func (h *Hub) ViewMapSnapshot(fn func(map[string]any)) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	fn(map[string]any{
		"now":           h.Now,
		"stations":      h.Stations,
		"station_order": h.StationOrder,
		"objects":       h.Objects,
		"object_order":  h.ObjectOrder,
		"weather":       h.Weather,
	})
}

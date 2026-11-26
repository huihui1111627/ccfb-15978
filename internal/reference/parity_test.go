package reference_test

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"amsgw/internal/reference"
	"amsgw/internal/sim"
)

func smallConfig() sim.Config {
	cfg := sim.DefaultConfig()
	cfg.Seed = 424242
	cfg.Stations = 48
	cfg.Relays = 6
	cfg.Gateways = 4
	cfg.Clients = 12
	cfg.Weather = 3
	cfg.Duration = 75 * time.Minute
	cfg.Malformed = 12
	cfg.RadioMessages = 8
	cfg.ClientSends = 6
	return cfg
}

func TestParitySmall(t *testing.T) {
	cfg := smallConfig()
	sc := sim.BuildScenario(cfg)
	prod := sim.Run(sc)
	want := reference.Run(sc)
	assertParity(t, prod, want)
}

func TestParityFixedSeedAcceptance(t *testing.T) {
	if testing.Short() {
		t.Skip("full acceptance parity skipped in short mode")
	}
	cfg := sim.AcceptanceConfig()
	sc := sim.BuildScenario(cfg)
	prod := sim.Run(sc)
	want := reference.Run(sc)
	assertParity(t, prod, want)

	duplicateReceptions, timeBad, downlinks := 0, 0, 0
	for _, m := range prod.Hub.Messages {
		if len(m.Receptions) > 1 {
			duplicateReceptions++
		}
		if m.TimeBad {
			timeBad++
		}
		downlinks += len(m.Downlinks)
	}
	if duplicateReceptions == 0 {
		t.Fatal("fixed chaos should produce duplicated/multiple gateway receptions")
	}
	if timeBad == 0 {
		t.Fatal("scenario should include timestamp skew examples")
	}
	if downlinks == 0 {
		t.Fatal("scenario should produce gateway third-party downlinks")
	}
	t.Logf("chaos evidence: duplicate_or_multi_reception=%d time_bad=%d downlinks=%d", duplicateReceptions, timeBad, downlinks)
	t.Logf("stations=%d originals=%d messages=%d clients=%d relayForwards=%d transmissions=%d bad=%d",
		cfg.Stations, len(sc.Originals), len(prod.Hub.Messages), len(sc.Clients),
		prod.RelayForwards, prod.Transmissions, len(prod.Hub.BadPackets))
}

func assertParity(t *testing.T, prod *sim.Result, want *reference.Report) {
	t.Helper()
	hubSets := prod.Hub.ClientInboxSets()
	for id, expected := range want.ClientSets {
		actual := hubSets[id]
		if actual == nil {
			t.Fatalf("client %s missing in production", id)
		}
		if diff := setDiff(expected, actual); diff != "" {
			for _, mid := range sortedMissing(expected, actual)[:min(5, len(sortedMissing(expected, actual)))] {
				if m := prod.Hub.ByID[mid]; m != nil {
					t.Logf("missing %s kind=%s source=%s public=%v", mid, m.Kind, m.Source, m.Public)
				}
			}
			t.Logf("client %s subs=%+v actualCount=%d wantCount=%d", id, prod.Hub.Clients[id].Subs, len(actual), len(expected))
			t.Fatalf("client %s inbox mismatch (-want +got):\n%s", id, diff)
		}
	}
	for id := range hubSets {
		if _, ok := want.ClientSets[id]; !ok {
			t.Fatalf("unexpected production client %s", id)
		}
	}

	if len(prod.Hub.BadPackets) != want.BadCount {
		t.Fatalf("bad packet count: got %d want %d", len(prod.Hub.BadPackets), want.BadCount)
	}
	if len(prod.Hub.Denials) != want.DeniedCount {
		gotReasons := []string{}
		for _, d := range prod.Hub.Denials {
			gotReasons = append(gotReasons, d.Call+":"+d.Action)
		}
		t.Fatalf("denial count: got %d (%v) want %d", len(prod.Hub.Denials), gotReasons, want.DeniedCount)
	}

	for name, wo := range want.Objects {
		ao, ok := prod.Hub.Objects[name]
		if !ok {
			t.Fatalf("object %s missing production", name)
		}
		if ao.Owner != wo.Owner || ao.Active != wo.Active || ao.Version != wo.Version || ao.LastReporter != wo.LastReporter {
			t.Fatalf("object %s mismatch: prod=%+v want=%+v", name, ao, wo)
		}
	}

	for call, expected := range want.StationSets {
		actual := map[string]bool{}
		for _, mid := range prod.StationInboxes[call] {
			actual[mid] = true
		}
		if diff := setDiff(expected, actual); diff != "" {
			t.Fatalf("station %s RF inbox mismatch (-want +got):\n%s", call, diff)
		}
	}

	for mid, class := range want.SourceClass {
		m, ok := prod.Hub.ByID[mid]
		if !ok {
			t.Fatalf("message %s missing production", mid)
		}
		if m.SourceClass != class {
			t.Fatalf("message %s source class got %s want %s", mid, m.SourceClass, class)
		}
		if want.QMarker[mid] != "" && m.QMarker != want.QMarker[mid] {
			t.Fatalf("message %s q marker got %s want %s", mid, m.QMarker, want.QMarker[mid])
		}
		if stringsJoin(m.RouteCalls) != stringsJoin(want.RouteCalls[mid]) {
			t.Fatalf("message %s route got %v want %v", mid, m.RouteCalls, want.RouteCalls[mid])
		}
	}

	for mid, gw := range want.DownlinkGW {
		m, ok := prod.Hub.ByID[mid]
		if !ok {
			t.Fatalf("downlink message %s missing", mid)
		}
		gotGW := ""
		delivered := false
		if len(m.Downlinks) > 0 {
			gotGW = m.Downlinks[0].Gateway
			delivered = m.Downlinks[0].DeliveredRF
		}
		if gotGW != gw {
			t.Fatalf("message %s gateway got %q want %q (all=%+v)", mid, gotGW, gw, m.Downlinks)
		}
		if gw != "" && delivered != want.RFDelivered[mid] {
			t.Fatalf("message %s RF delivery got %v want %v", mid, delivered, want.RFDelivered[mid])
		}
	}
}

func sortedMissing(want, got map[string]bool) []string {
	var missing []string
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	return missing
}

func setDiff(want, got map[string]bool) string {
	var missing, extra []string
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return ""
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return fmt.Sprintf("missing=%v extra=%v", missing, extra)
}

func stringsJoin(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += "|"
		}
		out += s
	}
	return out
}

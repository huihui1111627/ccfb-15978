package protocol

import (
	"testing"
	"time"
)

func TestPasscodeDerivedFromCallsign(t *testing.T) {
	if Passcode("BG0001") != Passcode("bg0001-7") {
		t.Fatal("SSID should not change derived base passcode")
	}
	if !CheckPasscode("BG0001", "12708") {
		t.Fatal("expected deterministic derived passcode 12708")
	}
	if CheckPasscode("BG0001", "wrong") {
		t.Fatal("invalid passcode accepted")
	}
}

func TestTimestampCrossesDayAndMonth(t *testing.T) {
	received := time.Date(2026, 11, 1, 0, 10, 0, 0, time.UTC)
	p, err := ParseStamp("312359z", received)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC)
	if !p.Time.Equal(want) {
		t.Fatalf("month rollover got %s want %s", p.Time, want)
	}
	if p.BadSkew {
		t.Fatal("11 minute skew should not be marked bad")
	}

	lateNight := time.Date(2026, 11, 1, 0, 3, 0, 0, time.UTC)
	p, err = ParseStamp("235800", lateNight)
	if err != nil {
		t.Fatal(err)
	}
	want = time.Date(2026, 10, 31, 23, 58, 0, 0, time.UTC)
	if !p.Time.Equal(want) {
		t.Fatalf("day rollover got %s want %s", p.Time, want)
	}

	old, err := ParseStamp("151200z", received)
	if err != nil {
		t.Fatal(err)
	}
	if !old.BadSkew {
		t.Fatalf("stamp inferred to %s should be flagged as skew", old.Time)
	}
}

func TestAliasRewriteDecrementsToZero(t *testing.T) {
	f, err := ParseFrame("BG0001>APLOC,WIDE#3:LOC;mid=X;lat=23;lon=113")
	if err != nil {
		t.Fatal(err)
	}
	relays := []string{"RPT01", "RPT02", "RPT03", "RPT04"}
	for i, call := range relays {
		nf, acted := RewriteForRelay(f, call, []string{"WIDE"})
		if i < 3 {
			if !acted {
				t.Fatalf("relay %s did not act", call)
			}
			f = nf
			if got := f.RelayCalls(); len(got) != i+1 {
				t.Fatalf("after %s relay calls=%v", call, got)
			}
		} else if acted {
			t.Fatal("zero hop packet must not be forwarded again")
		}
	}
	last := f.Path[len(f.Path)-1]
	if last.Kind != HopAlias || last.Remaining != 0 {
		t.Fatalf("expected terminal WIDE#0, got %+v", last)
	}
}

func TestUnrelatedAliasNotRewritten(t *testing.T) {
	f, _ := ParseFrame("BG1>X,MTN#2:LOC;mid=Y")
	if _, ok := RewriteForRelay(f, "GW1", []string{"CITY", "WIDE"}); ok {
		t.Fatal("relay should only consume matching aliases")
	}
}

func TestQMarkerAndThirdPartyRoundTrip(t *testing.T) {
	inner := &Frame{Src: "BG0001", Dst: "APMSG", Body: "MSG;mid=M1;to=BG0002;text=hi"}
	wrapped := WrapThirdParty("GW01", inner)
	line := wrapped.String()
	parsed, err := ParseFrame(line)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Src != "GW01" || parsed.ThirdParty == nil || parsed.ThirdParty.Src != "BG0001" {
		t.Fatalf("third party origin lost: %s", line)
	}
	q := WithQMarker(parsed, QRelayed, "GW09")
	parsedQ, err := ParseFrame(q.String())
	if err != nil {
		t.Fatal(err)
	}
	marker, via := parsedQ.QMarker()
	if marker != QRelayed || via != "GW09" || parsedQ.SourceClass() != SourceRelayed {
		t.Fatalf("bad q marker %s/%s class=%s", marker, via, parsedQ.SourceClass())
	}
}

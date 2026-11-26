package protocol

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CallRe matches a callsign with an optional SSID. The simulation intentionally
// uses short synthetic callsigns such as R-MT01 and GW-CITY1; hyphens are part
// of the base identifier for those infrastructure names.
var CallRe = regexp.MustCompile(`^[A-Z0-9]{1,8}(?:-[A-Z0-9]{1,2})?$`)

func NormalizeCall(call string) string {
	return strings.ToUpper(strings.TrimSpace(call))
}

func ValidCall(call string) bool {
	call = NormalizeCall(call)
	return CallRe.MatchString(call)
}

// BaseCall removes a terminal numeric/SSID suffix for the APRS-style passcode.
// Synthetic infrastructure calls such as GW-01 keep their first hyphenated
// component because the suffix after the final hyphen is only removed when it
// is a 1-2 character SSID.
func BaseCall(call string) string {
	call = NormalizeCall(call)
	if i := strings.LastIndex(call, "-"); i >= 0 {
		suffix := call[i+1:]
		if len(suffix) <= 2 {
			if _, err := strconv.Atoi(suffix); err == nil {
				return call[:i]
			}
		}
	}
	return call
}

// Passcode is the standard 15-bit hash derived from a callsign.
func Passcode(call string) int {
	root := BaseCall(call)
	hash := uint16(0x73e2)
	for i := 0; i < len(root); i++ {
		ch := root[i]
		if i%2 == 0 {
			hash ^= uint16(ch) << 8
		} else {
			hash ^= uint16(ch)
		}
	}
	return int(hash & 0x7fff)
}

func CheckPasscode(call, supplied string) bool {
	call = NormalizeCall(call)
	supplied = strings.TrimSpace(supplied)
	if call == "" || supplied == "" {
		return false
	}
	n, err := strconv.Atoi(supplied)
	if err != nil {
		return false
	}
	return n == Passcode(call)
}

const TimestampSkewLimit = 20 * time.Minute

type StampPrecision string

const (
	StampDayMinute StampPrecision = "ddhhmm"
	StampMinuteSec StampPrecision = "hhmmss"
)

type ParsedStamp struct {
	Time      time.Time
	Precision StampPrecision
	Raw       string
	Skew      time.Duration
	BadSkew   bool
}

// ParseStamp understands the two compact timestamp forms used by the network:
//   - DDHHMMz: day of month, hour and minute UTC
//   - HHMMSS:  hour, minute and second UTC
//
// Partial timestamps are reconstructed near receivedAt. Candidates in adjacent
// months/days make month and year rollover unambiguous for normal delayed
// packets.
func ParseStamp(raw string, receivedAt time.Time) (ParsedStamp, error) {
	s := strings.TrimSpace(raw)
	now := receivedAt.UTC()
	if strings.HasSuffix(s, "z") || strings.HasSuffix(s, "Z") {
		digits := s[:len(s)-1]
		if len(digits) != 6 || !allDigits(digits) {
			return ParsedStamp{}, fmt.Errorf("bad day/time stamp %q", raw)
		}
		day, _ := strconv.Atoi(digits[0:2])
		hour, _ := strconv.Atoi(digits[2:4])
		minute, _ := strconv.Atoi(digits[4:6])
		if day < 1 || day > 31 || hour > 23 || minute > 59 {
			return ParsedStamp{}, fmt.Errorf("invalid day/time stamp %q", raw)
		}
		best, err := nearestMonthDay(now, day, hour, minute, 0)
		if err != nil {
			return ParsedStamp{}, err
		}
		return finishStamp(raw, StampDayMinute, best, now), nil
	}

	// Separated form: DD/HHMM or DD/HH:MM.
	if strings.Contains(s, "/") {
		parts := strings.SplitN(s, "/", 2)
		if len(parts[0]) != 2 || !allDigits(parts[0]) {
			return ParsedStamp{}, fmt.Errorf("bad day stamp %q", raw)
		}
		day, _ := strconv.Atoi(parts[0])
		clock := strings.ReplaceAll(parts[1], ":", "")
		if len(clock) != 4 || !allDigits(clock) {
			return ParsedStamp{}, fmt.Errorf("bad day clock %q", raw)
		}
		hour, _ := strconv.Atoi(clock[0:2])
		minute, _ := strconv.Atoi(clock[2:4])
		if day < 1 || day > 31 || hour > 23 || minute > 59 {
			return ParsedStamp{}, fmt.Errorf("invalid day stamp %q", raw)
		}
		best, err := nearestMonthDay(now, day, hour, minute, 0)
		if err != nil {
			return ParsedStamp{}, err
		}
		return finishStamp(raw, StampDayMinute, best, now), nil
	}

	clock := strings.ReplaceAll(s, ":", "")
	if len(clock) != 6 || !allDigits(clock) {
		return ParsedStamp{}, fmt.Errorf("bad clock stamp %q", raw)
	}
	hour, _ := strconv.Atoi(clock[0:2])
	minute, _ := strconv.Atoi(clock[2:4])
	second, _ := strconv.Atoi(clock[4:6])
	if hour > 23 || minute > 59 || second > 59 {
		return ParsedStamp{}, fmt.Errorf("invalid clock stamp %q", raw)
	}
	best := nearestDayClock(now, hour, minute, second)
	return finishStamp(raw, StampMinuteSec, best, now), nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func validDate(y, m, d int) bool {
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	return t.Year() == y && int(t.Month()) == m && t.Day() == d
}

func nearestMonthDay(now time.Time, day, hour, minute, second int) (time.Time, error) {
	var best time.Time
	bestDist := time.Duration(math.MaxInt64)
	found := false
	base := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for _, off := range []int{-1, 0, 1, -2, 2} {
		first := base.AddDate(0, off, 0)
		y, m := first.Year(), first.Month()
		if !validDate(y, int(m), day) {
			continue
		}
		cand := time.Date(y, m, day, hour, minute, second, 0, time.UTC)
		d := absDuration(cand.Sub(now))
		if d < bestDist {
			bestDist = d
			best = cand
			found = true
		}
	}
	if !found {
		return time.Time{}, fmt.Errorf("day %d does not occur near %s", day, now.Format("2006-01"))
	}
	return best, nil
}

func nearestDayClock(now time.Time, hour, minute, second int) time.Time {
	var best time.Time
	bestDist := time.Duration(math.MaxInt64)
	for _, dayOff := range []int{-1, 0, 1} {
		d := now.AddDate(0, 0, dayOff)
		cand := time.Date(d.Year(), d.Month(), d.Day(), hour, minute, second, 0, time.UTC)
		dist := absDuration(cand.Sub(now))
		if dist < bestDist {
			bestDist = dist
			best = cand
		}
	}
	return best
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func finishStamp(raw string, precision StampPrecision, t, now time.Time) ParsedStamp {
	skew := t.Sub(now)
	abs := absDuration(skew)
	return ParsedStamp{
		Time:      t,
		Precision: precision,
		Raw:       raw,
		Skew:      skew,
		BadSkew:   abs > TimestampSkewLimit,
	}
}

func FormatDayMinute(t time.Time) string {
	return fmt.Sprintf("%02d%02d%02dz", t.Day(), t.Hour(), t.Minute())
}

func FormatMinuteSecond(t time.Time) string {
	return fmt.Sprintf("%02d%02d%02d", t.Hour(), t.Minute(), t.Second())
}

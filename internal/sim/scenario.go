package sim

import (
	"fmt"
	"math"
	"math/rand"
	"time"

	"amsgw/internal/core"
	"amsgw/internal/protocol"
)

type Original struct {
	At    time.Time       `json:"at"`
	Frame *protocol.Frame `json:"frame"`
}

type PeerSpec struct {
	At    time.Time       `json:"at"`
	Via   string          `json:"via"`
	Frame *protocol.Frame `json:"frame"`
}

type MalformedSpec struct {
	At  time.Time `json:"at"`
	Via string    `json:"via"`
	Raw string    `json:"raw"`
}

type ClientSendSpec struct {
	At        time.Time `json:"at"`
	ClientID  string    `json:"client_id"`
	MessageID string    `json:"message_id"`
	To        string    `json:"to"`
	Text      string    `json:"text"`
}

type ClientSpec struct {
	ID         string            `json:"id"`
	Call       string            `json:"call"`
	Passcode   string            `json:"passcode"`
	PasscodeOK bool              `json:"passcode_ok"`
	Subs       core.Subscription `json:"subs"`
}

type Scenario struct {
	Cfg         Config            `json:"cfg"`
	Start       time.Time         `json:"start"`
	Nodes       []*Node           `json:"nodes"`
	NodeByCall  map[string]*Node  `json:"-"`
	Clients     []ClientSpec      `json:"clients"`
	Originals   []Original        `json:"originals"`
	Peer        []PeerSpec        `json:"peer"`
	Malformed   []MalformedSpec   `json:"malformed"`
	ClientSends []ClientSendSpec  `json:"client_sends"`
	ObjectOwner map[string]string `json:"object_owner"`
}

type mover struct {
	node            *Node
	target          GeoPoint
	speed           float64
	nextAt          time.Time
	reportedHeading float64
	hasReported     bool
	dwellUntil      time.Time
}

func BuildScenario(cfg Config) *Scenario {
	if cfg.Stations < 10 {
		cfg.Stations = 10
	}
	if cfg.Clients > cfg.Stations {
		cfg.Clients = cfg.Stations
	}
	r := rand.New(rand.NewSource(cfg.Seed))
	start := time.Date(2026, 10, 31, 23, 40, 0, 0, time.UTC)

	sc := &Scenario{
		Cfg:         cfg,
		Start:       start,
		NodeByCall:  map[string]*Node{},
		ObjectOwner: map[string]string{},
	}
	addNode := func(n *Node) {
		sc.Nodes = append(sc.Nodes, n)
		sc.NodeByCall[n.Call] = n
	}

	for i := 1; i <= cfg.Stations; i++ {
		p := randomPoint(r, true)
		mobile := i%10 != 0 && i%11 != 0 // roughly 80% mobile, with fixed base stations
		n := &Node{
			Call:    fmt.Sprintf("BG%04d", i),
			Name:    fmt.Sprintf("会员台 %03d", i),
			Kind:    "station",
			Lat:     p.Lat,
			Lon:     p.Lon,
			RangeKm: 10 + r.Float64()*8,
			Mobile:  mobile,
			Heading: r.Float64() * 360,
		}
		if mobile {
			n.SpeedKmh = 20 + r.Float64()*70
		}
		addNode(n)
	}
	for i := 1; i <= cfg.Weather; i++ {
		p := randomPoint(r, false)
		n := &Node{
			Call:    fmt.Sprintf("WX%02d", i),
			Name:    fmt.Sprintf("气象站 %02d", i),
			Kind:    "weather",
			Lat:     p.Lat,
			Lon:     p.Lon,
			RangeKm: 24 + r.Float64()*18,
		}
		addNode(n)
	}

	cols := 6
	rows := (cfg.Relays + cols - 1) / cols
	for i := 0; i < cfg.Relays; i++ {
		col := i % cols
		row := i / cols
		lat := minLat + 0.18 + (float64(row)+0.5)/float64(rows)*(maxLat-minLat-0.36)
		lon := minLon + 0.15 + (float64(col)+0.5)/float64(cols)*(maxLon-minLon-0.30)
		lat += (r.Float64() - 0.5) * 0.10
		lon += (r.Float64() - 0.5) * 0.10
		aliases := []string{"WIDE"}
		if lat >= 23.05 {
			aliases = append(aliases, "MTN")
		} else {
			aliases = append(aliases, "CITY")
		}
		n := &Node{
			Call:    fmt.Sprintf("RPT%02d", i+1),
			Name:    fmt.Sprintf("%s中继 %02d", map[bool]string{true: "北山", false: "城区"}[lat >= 23.05], i+1),
			Kind:    "relay",
			Aliases: aliases,
			Lat:     lat,
			Lon:     lon,
			RangeKm: 24 + r.Float64()*14,
		}
		addNode(n)
	}
	for i := 1; i <= cfg.Gateways; i++ {
		var p GeoPoint
		if i <= cfg.Gateways*2/3 {
			p = GeoPoint{Lat: 23.12 + (r.Float64()-0.5)*0.34, Lon: 113.30 + (r.Float64()-0.5)*0.42}
		} else {
			p = randomPoint(r, false)
		}
		n := &Node{
			Call:    fmt.Sprintf("GW%02d", i),
			Name:    fmt.Sprintf("接入网关 %02d", i),
			Kind:    "gateway",
			Lat:     p.Lat,
			Lon:     p.Lon,
			RangeKm: 18 + r.Float64()*14,
		}
		addNode(n)
	}

	for i := 0; i < cfg.Clients; i++ {
		call := sc.Nodes[i].Call
		pc := protocol.Passcode(call)
		ok := i%5 != 4
		supplied := fmt.Sprintf("%05d", pc)
		if !ok {
			wrong := pc + 1
			if wrong > 32767 {
				wrong = 1
			}
			supplied = fmt.Sprintf("%05d", wrong)
		}
		subs := core.Subscription{}
		switch i % 4 {
		case 0:
			subs.Kinds = map[string]bool{"position": true}
		case 1:
			subs.Calls = map[string]bool{call: true, sc.Nodes[(i+7)%cfg.Stations].Call: true}
		}
		sc.Clients = append(sc.Clients, ClientSpec{
			ID: fmt.Sprintf("client-%02d", i+1), Call: call, Passcode: supplied,
			PasscodeOK: ok, Subs: subs,
		})
	}

	midSeq := 0
	newMID := func() string {
		midSeq++
		return fmt.Sprintf("SIM%06d", midSeq)
	}
	addOriginal := func(at time.Time, src, dst string, body string) {
		f := &protocol.Frame{Src: src, Dst: dst, Path: initialPath(r), Body: body}
		f.Raw = f.String()
		sc.Originals = append(sc.Originals, Original{At: at, Frame: f})
	}

	// Smart-beaconing movement and report cadence.
	var movers []*mover
	for _, n := range sc.Nodes {
		if n.Kind != "station" || !n.Mobile {
			continue
		}
		mv := &mover{
			node: n, target: randomPoint(r, true), speed: n.SpeedKmh,
			nextAt: start.Add(time.Duration(r.Intn(240)) * time.Second),
		}
		n.History = []HistPoint{{At: start, Lat: n.Lat, Lon: n.Lon, Speed: n.SpeedKmh, Heading: n.Heading}}
		movers = append(movers, mv)
	}
	steps := int(cfg.Duration / cfg.Tick)
	for step := 0; step <= steps; step++ {
		now := start.Add(cfg.Tick * time.Duration(step))
		for _, mv := range movers {
			n := mv.node
			if now.Before(mv.dwellUntil) {
				n.SpeedKmh = 0
			} else {
				d := distKm(n, mv.target)
				stepDist := n.SpeedKmh * cfg.Tick.Seconds() / 3600
				if d <= math.Max(0.08, stepDist) {
					n.Lat, n.Lon = mv.target.Lat, mv.target.Lon
					if r.Float64() < 0.24 {
						secs := 300 + r.Intn(1500)
						mv.dwellUntil = now.Add(time.Duration(secs) * time.Second)
						n.SpeedKmh = 0
						mv.speed = 0
					} else {
						mv.target = randomPoint(r, true)
						mv.speed = 25 + r.Float64()*85
						n.SpeedKmh = mv.speed
					}
				} else {
					n.Heading = bearing(GeoPoint{Lat: n.Lat, Lon: n.Lon}, mv.target)
					p := moveToward(GeoPoint{Lat: n.Lat, Lon: n.Lon}, n.Heading, mv.speed, cfg.Tick.Seconds())
					n.Lat, n.Lon = p.Lat, p.Lon
					n.SpeedKmh = mv.speed
				}
			}
			n.History = append(n.History, HistPoint{
				At: now, Lat: n.Lat, Lon: n.Lon, Speed: n.SpeedKmh, Heading: n.Heading,
			})

			turn := mv.hasReported && n.SpeedKmh >= 5 && angleDelta(n.Heading, mv.reportedHeading) >= 28
			due := !now.Before(mv.nextAt)
			if !turn && !due {
				continue
			}
			reason := "interval"
			switch {
			case !mv.hasReported:
				reason = "startup"
			case turn:
				reason = "turn"
			case n.SpeedKmh < 5:
				reason = "stationary"
			}
			mid := newMID()
			ts := protocol.FormatMinuteSecond(now)
			body := protocol.EncodeFields("LOC",
				"mid", mid, "ts", ts,
				"lat", fmt.Sprintf("%.6f", n.Lat), "lon", fmt.Sprintf("%.6f", n.Lon),
				"spd", fmt.Sprintf("%.1f", n.SpeedKmh), "hdg", fmt.Sprintf("%.0f", n.Heading),
				"reason", reason,
			)
			addOriginal(now, n.Call, "APLOC", body)
			mv.hasReported = true
			mv.reportedHeading = n.Heading
			interval := 900.0
			if n.SpeedKmh >= 5 {
				interval = math.Max(45, math.Min(300, 7200/n.SpeedKmh))
			}
			if turn {
				// A turn supplement resets the distance/time cadence slightly.
				interval = math.Min(interval, 60)
			}
			mv.nextAt = now.Add(time.Duration(interval) * time.Second)
		}
	}

	// Fixed stations still report on a slow heartbeat.
	for i := 1; i <= cfg.Stations; i++ {
		n := sc.NodeByCall[fmt.Sprintf("BG%04d", i)]
		if n.Mobile {
			continue
		}
		for mins := 2 + r.Intn(8); mins < int(cfg.Duration/time.Minute)-2; mins += 12 + r.Intn(8) {
			now := start.Add(time.Duration(mins) * time.Minute)
			mid := newMID()
			body := protocol.EncodeFields("LOC", "mid", mid,
				"ts", protocol.FormatMinuteSecond(now),
				"lat", fmt.Sprintf("%.6f", n.Lat), "lon", fmt.Sprintf("%.6f", n.Lon),
				"spd", "0.0", "hdg", fmt.Sprintf("%.0f", n.Heading), "reason", "stationary")
			addOriginal(now, n.Call, "APLOC", body)
		}
	}

	// Weather stations use the day/hour/minute form.
	wxIndex := 0
	for _, n := range sc.Nodes {
		if n.Kind != "weather" {
			continue
		}
		wxIndex++
		for mins := 4 + wxIndex; mins < int(cfg.Duration/time.Minute)-1; mins += 10 {
			now := start.Add(time.Duration(mins) * time.Minute)
			temp := 14 + r.Float64()*17
			hum := 38 + r.Float64()*58
			ts := protocol.FormatDayMinute(now)
			if (mins/10+wxIndex)%17 == 0 {
				old := now.AddDate(0, -1, -12)
				ts = protocol.FormatDayMinute(old)
			}
			mid := newMID()
			body := protocol.EncodeFields("WX", "mid", mid, "ts", ts,
				"temp", fmt.Sprintf("%.1f", temp), "hum", fmt.Sprintf("%.0f", hum),
				"wind", fmt.Sprintf("%.1f", r.Float64()*18))
			addOriginal(now, n.Call, "APWX", body)
		}
	}

	objectNames := []string{"北峰测量标", "湖心浮标", "城东枢纽石", "西河闸标尺", "南山瞭望点", "旧塔坐标", "水库水尺", "垭口反光标"}
	objectBases := map[string]GeoPoint{}
	for i, name := range objectNames {
		objectBases[name] = GeoPoint{Lat: 22.8 + float64(i%4)*0.16 + r.Float64()*0.05, Lon: 113.0 + float64(i/4)*0.28 + r.Float64()*0.06}
	}
	for i := 0; i < 18; i++ {
		name := objectNames[i%len(objectNames)]
		base := objectBases[name]
		lat := base.Lat + (r.Float64()-0.5)*0.012
		lon := base.Lon + (r.Float64()-0.5)*0.012
		src := sc.Nodes[r.Intn(cfg.Stations)].Call
		if _, ok := sc.ObjectOwner[name]; !ok {
			sc.ObjectOwner[name] = src
		}
		now := start.Add(time.Duration(12+r.Intn(int(cfg.Duration/time.Minute)-35)) * time.Minute)
		mid := newMID()
		body := protocol.EncodeFields("OBJ", "mid", mid, "ts", protocol.FormatMinuteSecond(now),
			"act", "report", "name", name,
			"lat", fmt.Sprintf("%.6f", lat), "lon", fmt.Sprintf("%.6f", lon))
		addOriginal(now, src, "APOBJ", body)
	}
	// Two unauthorized withdrawal attempts; objects must remain owned by first reporter.
	for i := 0; i < 3; i++ {
		name := objectNames[i]
		owner := sc.ObjectOwner[name]
		src := sc.Nodes[r.Intn(cfg.Stations)].Call
		for src == owner {
			src = sc.Nodes[r.Intn(cfg.Stations)].Call
		}
		now := start.Add(cfg.Duration - 24*time.Minute + time.Duration(i*3)*time.Minute)
		mid := newMID()
		body := protocol.EncodeFields("OBJ", "mid", mid, "ts", protocol.FormatMinuteSecond(now),
			"act", "remove", "name", name)
		addOriginal(now, src, "APOBJ", body)
	}
	// Two owner-authorized withdrawals.
	for i := 3; i < 5; i++ {
		name := objectNames[i]
		owner := sc.ObjectOwner[name]
		now := start.Add(cfg.Duration - 10*time.Minute + time.Duration(i)*time.Minute)
		mid := newMID()
		body := protocol.EncodeFields("OBJ", "mid", mid, "ts", protocol.FormatMinuteSecond(now),
			"act", "remove", "name", name)
		addOriginal(now, owner, "APOBJ", body)
	}

	// Station-to-station radio messages between simulated client callsigns.
	for i := 0; i < cfg.RadioMessages; i++ {
		a := r.Intn(cfg.Clients)
		b := r.Intn(cfg.Clients - 1)
		if b >= a {
			b++
		}
		src := sc.Clients[a].Call
		target := sc.Clients[b].Call
		now := start.Add(time.Duration(18+r.Intn(int(cfg.Duration/time.Minute)-35)) * time.Minute)
		mid := newMID()
		text := fmt.Sprintf("HELLO-%03d FROM MOUNTAIN", i+1)
		body := protocol.EncodeFields("MSG", "mid", mid, "ts", protocol.FormatMinuteSecond(now),
			"to", target, "text", text)
		addOriginal(now, src, "APMSG", body)
	}

	for i := 0; i < cfg.ClientSends; i++ {
		ci := i % cfg.Clients
		toIdx := (ci + 3) % cfg.Clients
		at := start.Add(time.Duration(25+r.Intn(int(cfg.Duration/time.Minute)-45)) * time.Minute)
		sc.ClientSends = append(sc.ClientSends, ClientSendSpec{
			At: at, ClientID: sc.Clients[ci].ID, MessageID: fmt.Sprintf("WEB%04d", i+1),
			To: sc.Clients[toIdx].Call, Text: fmt.Sprintf("CLIENT DIRECT %d", i+1),
		})
	}

	// Federated packets from another association server.
	for i := 0; i < 18; i++ {
		now := start.Add(time.Duration(5+r.Intn(int(cfg.Duration/time.Minute)-10)) * time.Minute)
		call := fmt.Sprintf("BD%04d", 1000+i)
		p := randomPoint(r, false)
		mid := newMID()
		var body string
		if i%3 == 0 {
			body = protocol.EncodeFields("WX", "mid", mid, "ts", protocol.FormatDayMinute(now),
				"temp", fmt.Sprintf("%.1f", 10+r.Float64()*20), "hum", fmt.Sprintf("%.0f", 40+r.Float64()*50))
		} else {
			body = protocol.EncodeFields("LOC", "mid", mid, "ts", protocol.FormatMinuteSecond(now),
				"lat", fmt.Sprintf("%.6f", p.Lat), "lon", fmt.Sprintf("%.6f", p.Lon),
				"spd", fmt.Sprintf("%.1f", r.Float64()*90), "hdg", fmt.Sprintf("%.0f", r.Float64()*360),
				"reason", "federated")
		}
		f := &protocol.Frame{Src: call, Dst: "APNET", Body: body}
		f.Raw = f.String()
		sc.Peer = append(sc.Peer, PeerSpec{At: now, Via: "SRVPEER", Frame: f})
	}

	badTemplates := []func(int) string{
		func(i int) string { return "@@@ BROKEN FRAME WITHOUT HEADER" },
		func(i int) string { return "BAD CALL>APLOC:LOC;mid=BAD" + fmt.Sprint(i) },
		func(i int) string {
			return fmt.Sprintf("BG0001>APLOC:LOC;mid=BAD%03d;lat=not-a-number;lon=113.3", i)
		},
		func(i int) string { return fmt.Sprintf("BG0002>APWX:WX;mid=BAD%03d;temp=hot;hum=50", i) },
		func(i int) string { return fmt.Sprintf("BG0003>APLOC:LOC;mid=BAD%03d;ts=999999z;lat=23;lon=113", i) },
		func(i int) string { return fmt.Sprintf("BG0004>APMSG:MSG;mid=BAD%03d", i) },
		func(i int) string { return ":missing-source" },
		func(i int) string { return fmt.Sprintf("BG0005>APOBJ:OBJ;mid=BAD%03d;act=report;name=X", i) },
	}
	for i := 0; i < cfg.Malformed; i++ {
		now := start.Add(time.Duration(r.Intn(int(cfg.Duration/time.Second))) * time.Second)
		gw := fmt.Sprintf("GW%02d", 1+r.Intn(cfg.Gateways))
		raw := badTemplates[i%len(badTemplates)](i)
		sc.Malformed = append(sc.Malformed, MalformedSpec{At: now, Via: gw, Raw: raw})
	}

	return sc
}

func initialPath(r *rand.Rand) []protocol.Hop {
	roll := r.Float64()
	n := 1
	switch {
	case roll < 0.62:
		n = 1
	case roll < 0.92:
		n = 2
	default:
		n = 3
	}
	return []protocol.Hop{{Kind: protocol.HopAlias, Alias: "WIDE", Remaining: n}}
}

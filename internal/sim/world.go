package sim

import (
	"math"
	"math/rand"
	"time"
)

const (
	minLat = 22.55
	maxLat = 23.55
	minLon = 112.85
	maxLon = 113.95
)

type GeoPoint struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type Node struct {
	Call    string   `json:"call"`
	Name    string   `json:"name,omitempty"`
	Kind    string   `json:"kind"` // station, weather, relay, gateway
	Aliases []string `json:"aliases,omitempty"`
	Lat     float64  `json:"lat"`
	Lon     float64  `json:"lon"`
	RangeKm float64  `json:"range_km"`
	Mobile  bool     `json:"mobile"`

	SpeedKmh float64 `json:"speed_kmh,omitempty"`
	Heading  float64 `json:"heading,omitempty"`

	// History is tick-sampled and intentionally omitted from scenario JSON.
	History []HistPoint `json:"-"`
}

type HistPoint struct {
	At      time.Time `json:"at"`
	Lat     float64   `json:"lat"`
	Lon     float64   `json:"lon"`
	Speed   float64   `json:"speed_kmh"`
	Heading float64   `json:"heading"`
}

func distKm(a LatLon, b LatLon) float64 {
	const kmPerDeg = 111.32
	latMid := (a.GetLat() + b.GetLat()) / 2
	dx := (b.GetLon() - a.GetLon()) * kmPerDeg * math.Cos(deg2rad(latMid))
	dy := (b.GetLat() - a.GetLat()) * 110.574
	return math.Hypot(dx, dy)
}

type LatLon interface {
	GetLat() float64
	GetLon() float64
}

func (n Node) GetLat() float64     { return n.Lat }
func (n Node) GetLon() float64     { return n.Lon }
func (p GeoPoint) GetLat() float64 { return p.Lat }
func (p GeoPoint) GetLon() float64 { return p.Lon }

func deg2rad(d float64) float64 { return d * math.Pi / 180 }
func rad2deg(r float64) float64 { return r * 180 / math.Pi }

func bearing(from, to GeoPoint) float64 {
	lat1 := deg2rad(from.Lat)
	lat2 := deg2rad(to.Lat)
	dLon := deg2rad(to.Lon - from.Lon)
	y := math.Sin(dLon) * math.Cos(lat2)
	x := math.Cos(lat1)*math.Sin(lat2) - math.Sin(lat1)*math.Cos(lat2)*math.Cos(dLon)
	b := rad2deg(math.Atan2(y, x))
	return math.Mod(b+360, 360)
}

func angleDelta(a, b float64) float64 {
	d := math.Abs(math.Mod(a-b, 360))
	if d > 180 {
		d = 360 - d
	}
	return d
}

func randomPoint(r *rand.Rand, cityBias bool) GeoPoint {
	if cityBias && r.Float64() < 0.72 {
		// Urban cluster around the fictional downtown.
		lat := 23.12 + r.NormFloat64()*0.16
		lon := 113.30 + r.NormFloat64()*0.20
		lat = clampFloat(lat, minLat+0.03, maxLat-0.03)
		lon = clampFloat(lon, minLon+0.03, maxLon-0.03)
		return GeoPoint{Lat: lat, Lon: lon}
	}
	return GeoPoint{
		Lat: minLat + r.Float64()*(maxLat-minLat),
		Lon: minLon + r.Float64()*(maxLon-minLon),
	}
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func moveToward(p GeoPoint, heading, speedKmh, dtSeconds float64) GeoPoint {
	distKm := speedKmh * dtSeconds / 3600
	latDelta := (distKm / 110.574) * math.Cos(deg2rad(heading))
	lonDelta := (distKm / (111.32 * math.Cos(deg2rad(p.Lat)))) * math.Sin(deg2rad(heading))
	return GeoPoint{Lat: p.Lat + latDelta, Lon: p.Lon + lonDelta}
}

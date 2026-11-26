package sim

import (
	"hash/fnv"
	"time"
)

type Config struct {
	Seed          int64         `json:"seed"`
	Stations      int           `json:"stations"`
	Relays        int           `json:"relays"`
	Gateways      int           `json:"gateways"`
	Clients       int           `json:"clients"`
	Weather       int           `json:"weather"`
	Duration      time.Duration `json:"duration"`
	Tick          time.Duration `json:"tick"`
	LossRate      float64       `json:"loss_rate"`
	DuplicateRate float64       `json:"duplicate_rate"`
	MaxDelay      time.Duration `json:"max_delay"`
	HeardTTL      time.Duration `json:"heard_ttl"`
	Malformed     int           `json:"malformed"`
	RadioMessages int           `json:"radio_messages"`
	ClientSends   int           `json:"client_sends"`
}

func DefaultConfig() Config {
	return Config{
		Seed:          20261127,
		Stations:      300,
		Relays:        24,
		Gateways:      12,
		Clients:       30,
		Weather:       12,
		Duration:      120 * time.Minute,
		Tick:          5 * time.Second,
		LossRate:      0.10,
		DuplicateRate: 0.06,
		MaxDelay:      90 * time.Second,
		HeardTTL:      45 * time.Minute,
		Malformed:     42,
		RadioMessages: 28,
		ClientSends:   12,
	}
}

func AcceptanceConfig() Config {
	cfg := DefaultConfig()
	cfg.Seed = 987654321
	return cfg
}

type Fate struct {
	Receive        bool
	Delay          time.Duration
	Duplicate      bool
	DuplicateDelay time.Duration
}

// FateFor derives a stable per-link impairment. Keying by transmission key and
// receiver means the production runtime and the independent reference can ask
// the same question without sharing mutable RNG call order.
func FateFor(masterSeed int64, txKey, receiver string, cfg Config) Fate {
	h := fnv.New64a()
	var buf [8]byte
	v := uint64(masterSeed)
	for i := 0; i < 8; i++ {
		buf[i] = byte(v)
		v >>= 8
	}
	h.Write(buf[:])
	h.Write([]byte{0})
	h.Write([]byte(txKey))
	h.Write([]byte{0})
	h.Write([]byte(receiver))
	x := splitMix64(h.Sum64() + 0x9e3779b97f4a7c15)

	// Use the top 53 bits as a [0,1) float.
	unit := func(y uint64) float64 { return float64(y>>11) / float64(uint64(1)<<53) }
	if unit(x) < cfg.LossRate {
		return Fate{Receive: false}
	}
	x = splitMix64(x)
	maxDelay := int(cfg.MaxDelay / time.Second)
	if maxDelay < 1 {
		maxDelay = 1
	}
	delaySec := 1 + int(splitMix64(x+0x1111)%uint64(maxDelay))
	f := Fate{Receive: true, Delay: time.Duration(delaySec) * time.Second}
	x = splitMix64(x + 0x2222)
	if unit(x) < cfg.DuplicateRate {
		f.Duplicate = true
		f.DuplicateDelay = time.Duration(1+int(splitMix64(x+0x3333)%12)) * time.Second
	}
	return f
}

func splitMix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

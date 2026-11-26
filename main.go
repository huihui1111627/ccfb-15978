package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"time"

	"amsgw/internal/api"
	"amsgw/internal/sim"
)

//go:embed web
var webFS embed.FS

func main() {
	addr := flag.String("addr", ":8088", "HTTP listen address")
	seed := flag.Int64("seed", 987654321, "fixed simulation seed")
	stations := flag.Int("stations", 300, "number of simulated member stations")
	relays := flag.Int("relays", 24, "number of digipeaters")
	gateways := flag.Int("gateways", 12, "number of RF-to-server gateways")
	clients := flag.Int("clients", 30, "number of online clients")
	duration := flag.Duration("duration", 120*time.Minute, "simulated duration")
	flag.Parse()

	cfg := sim.AcceptanceConfig()
	cfg.Seed = *seed
	cfg.Stations = *stations
	cfg.Relays = *relays
	cfg.Gateways = *gateways
	cfg.Clients = *clients
	cfg.Duration = *duration

	log.Printf("building deterministic scenario seed=%d stations=%d relays=%d gateways=%d", cfg.Seed, cfg.Stations, cfg.Relays, cfg.Gateways)
	sc := sim.BuildScenario(cfg)
	log.Printf("simulating RF, relays, gateways and server aggregation...")
	res := sim.Run(sc)
	log.Printf("simulation complete: originals=%d aggregated=%d relay_forwards=%d transmissions=%d bad=%d",
		len(sc.Originals), len(res.Hub.Messages), res.RelayForwards, res.Transmissions, len(res.Hub.BadPackets))

	server := api.NewServer(res)
	server.StartVerification()

	staticSub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux := server.Handler(http.FileServer(http.FS(staticSub)))
	log.Printf("web map ready at http://localhost%s", *addr)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

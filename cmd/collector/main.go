// Command collector is the long-running service that records bus arrivals.
//
// Every POLL_INTERVAL it fetches the live trip-updates feed and updates one row
// per stop visit in the database (see migrations/002_stop_visits.sql for why).
// Leave it running; the dashboard reports on whatever it has collected.
//
// Run:  go run ./cmd/collector
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	// Embeds a copy of the time zone database in the binary. It's only used if
	// the machine has none of its own (common in slim Docker images), so
	// time.LoadLocation("America/Edmonton") can't fail in production.
	_ "time/tzdata"

	"github.com/raraia/calgary-transit-tracker/internal/collector"
	"github.com/raraia/calgary-transit-tracker/internal/gtfs"
	"github.com/raraia/calgary-transit-tracker/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	dbURL := env("DATABASE_URL", "postgres://ctt:ctt@localhost:5432/ctt")
	feedURL := env("GTFS_RT_TRIP_UPDATES", "https://data.calgary.ca/download/gs4m-mdc2/application%2Foctet-stream")
	interval, err := time.ParseDuration(env("POLL_INTERVAL", "30s"))
	if err != nil || interval < 10*time.Second {
		log.Error("POLL_INTERVAL must be a duration of at least 10s, e.g. 30s or 1m")
		os.Exit(1)
	}

	// ctx is cancelled on Ctrl+C or a stop signal, so everything shuts down cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.New(ctx, dbURL)
	if err != nil {
		log.Error("connecting to database", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		log.Error("migrating database", "err", err)
		os.Exit(1)
	}

	loc, err := time.LoadLocation("America/Edmonton")
	if err != nil {
		log.Error("loading time zone", "err", err)
		os.Exit(1)
	}

	fetch := func(ctx context.Context) ([]byte, error) { return gtfs.Fetch(ctx, feedURL) }
	c := collector.New(fetch, st, loc, log)

	log.Info("collector started", "interval", interval)
	c.Run(ctx, interval)
	log.Info("collector stopped")
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

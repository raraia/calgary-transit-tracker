// Command collector is the long-running service. Every POLL_INTERVAL it fetches
// the live trip-updates feed and stores each delay reading. Leave it running and
// the stop_time_updates table fills up with the data the dashboard reports on.
//
// Run:  go run ./cmd/collector
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/raraia/calgary-transit-tracker/internal/gtfs"
	"github.com/raraia/calgary-transit-tracker/internal/store"
)

const pollInterval = 45 * time.Second

func main() {
	dbURL := env("DATABASE_URL", "postgres://ctt:ctt@localhost:5432/ctt")
	feedURL := env("GTFS_RT_TRIP_UPDATES",
		"https://data.calgary.ca/download/gs4m-mdc2/application%2Foctet-stream")

	// ctx is cancelled when you press Ctrl+C, so the loop can stop cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("connecting to db: %v", err)
	}
	defer st.Close()

	log.Printf("collector started, polling every %s", pollInterval)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	pollOnce(ctx, st, feedURL) // fetch immediately, don't wait a full interval
	for {
		select {
		case <-ctx.Done():
			log.Println("shutting down")
			return
		case <-ticker.C:
			pollOnce(ctx, st, feedURL)
		}
	}
}

// pollOnce does one fetch-and-store cycle. Errors are logged, not fatal: one bad
// fetch (network blip, feed hiccup) shouldn't kill a service meant to run for days.
func pollOnce(ctx context.Context, st *store.Store, feedURL string) {
	now := time.Now()
	delays, err := gtfs.FetchTripUpdates(ctx, feedURL)
	if err != nil {
		log.Printf("fetch error: %v", err)
		return
	}
	if err := st.InsertDelays(ctx, delays, now); err != nil {
		log.Printf("insert error: %v", err)
		return
	}
	log.Printf("stored %d readings", len(delays))
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

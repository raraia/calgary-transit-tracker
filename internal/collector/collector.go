// Package collector turns feed snapshots into stored stop visits, on a timer.
//
// The logic lives here rather than in cmd/collector/main.go so it can be tested:
// the collector depends on a function that fetches bytes and an interface that
// stores visits, and tests swap in fakes for both. No network or database needed.
package collector

import (
	"context"
	"log/slog"
	"time"

	"github.com/raraia/calgary-transit-tracker/internal/gtfs"
	"github.com/raraia/calgary-transit-tracker/internal/store"
)

// FetchFunc returns the raw bytes of one trip-updates feed snapshot.
type FetchFunc func(ctx context.Context) ([]byte, error)

// Sink is where visits get stored. *store.Store satisfies it; tests use a fake.
type Sink interface {
	UpsertStopVisits(ctx context.Context, visits []store.StopVisit, seenAt time.Time) (int, error)
}

type Collector struct {
	fetch FetchFunc
	sink  Sink
	loc   *time.Location // Calgary's time zone, for working out service dates
	log   *slog.Logger
	now   func() time.Time

	// lastGenerated is the header timestamp of the last snapshot we stored.
	// The feed only changes every 30s or so; if we poll and get the same
	// snapshot again we skip it rather than count it twice.
	lastGenerated time.Time
}

func New(fetch FetchFunc, sink Sink, loc *time.Location, log *slog.Logger) *Collector {
	return &Collector{fetch: fetch, sink: sink, loc: loc, log: log, now: time.Now}
}

// Result summarises one poll, for logging and tests.
type Result struct {
	Skipped bool // the snapshot was identical to the previous one
	Events  int  // usable stop events in the snapshot
	Stored  int  // distinct visits written
}

// PollOnce fetches one snapshot and stores it.
func (c *Collector) PollOnce(ctx context.Context) (Result, error) {
	data, err := c.fetch(ctx)
	if err != nil {
		return Result{}, err
	}
	snap, err := gtfs.DecodeTripUpdates(data, c.now())
	if err != nil {
		return Result{}, err
	}
	if snap.GeneratedAt.Equal(c.lastGenerated) {
		return Result{Skipped: true, Events: len(snap.Events)}, nil
	}

	visits := make([]store.StopVisit, 0, len(snap.Events))
	for _, e := range snap.Events {
		visits = append(visits, store.StopVisit{
			ServiceDate:  gtfs.ServiceDate(e.StartDate, snap.GeneratedAt, c.loc),
			TripID:       e.TripID,
			StopSequence: int32(e.StopSequence),
			RouteID:      e.RouteID,
			StopID:       e.StopID,
			HasDelay:     e.HasDelay,
			Delay:        e.Delay,
			Predicted:    e.Predicted,
		})
	}

	stored, err := c.sink.UpsertStopVisits(ctx, visits, snap.GeneratedAt)
	if err != nil {
		return Result{}, err
	}
	// Only remember the snapshot once it's safely stored, so a failed write
	// gets retried on the next poll instead of being skipped as a duplicate.
	c.lastGenerated = snap.GeneratedAt
	return Result{Events: len(snap.Events), Stored: stored}, nil
}

// Run polls every interval until ctx is cancelled (Ctrl+C in main).
//
// A failed poll is logged and the loop carries on: a network blip or one bad
// snapshot shouldn't kill a service that's meant to run for weeks.
func (c *Collector) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		res, err := c.PollOnce(ctx)
		switch {
		case err != nil:
			c.log.Error("poll failed", "err", err)
		case res.Skipped:
			c.log.Debug("feed unchanged; skipped")
		default:
			c.log.Info("stored snapshot", "events", res.Events, "visits", res.Stored)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

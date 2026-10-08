// Package store is the thin layer between the app and PostgreSQL.
package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/raraia/calgary-transit-tracker/internal/gtfs"
)

type Store struct {
	pool *pgxpool.Pool
}

// New opens a connection pool to the database at databaseURL.
func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// InsertDelays writes a batch of delay readings in one round trip.
// observedAt is passed in (not the DB's NOW()) so every row in a batch shares
// the exact fetch time, which keeps the hourly aggregation clean.
func (s *Store) InsertDelays(ctx context.Context, delays []gtfs.StopDelay, observedAt time.Time) error {
	if len(delays) == 0 {
		return nil
	}

	// CopyFrom is pgx's fast bulk-insert path: it streams all the rows to the
	// server at once instead of running one INSERT per row. pgx.CopyFromSlice
	// adapts our slice into the row-by-row form CopyFrom wants.
	_, err := s.pool.CopyFrom(
		ctx,
		pgx.Identifier{"stop_time_updates"},
		[]string{"observed_at", "trip_id", "route_id", "stop_id", "stop_sequence", "delay_seconds"},
		pgx.CopyFromSlice(len(delays), func(i int) ([]any, error) {
			d := delays[i]
			return []any{observedAt, d.TripID, d.RouteID, d.StopID, d.StopSequence, d.DelaySeconds}, nil
		}),
	)
	return err
}

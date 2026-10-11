package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// TableLoad describes rows to bulk-load into one schedule table.
//
// Rows is anything with Next/Values/Err methods (pgx.CopyFromSource). That lets
// the schedule package stream rows straight out of the GTFS zip file without
// first holding a million stop_times rows in memory.
type TableLoad struct {
	Table   string
	Columns []string
	Rows    pgx.CopyFromSource
}

// ReplaceSchedule swaps the whole static schedule for a new one, atomically.
//
// Everything happens in one transaction: empty the four schedule tables, then
// stream each table in with COPY (Postgres's fast bulk-load path). If any row
// fails, the transaction rolls back and the old schedule stays untouched. Other
// connections keep seeing the old schedule until the commit, never a mix.
//
// stop_visits is deliberately not touched: collected history survives a
// schedule refresh. Loads must be ordered so referenced tables come first
// (routes before trips).
func (s *Store) ReplaceSchedule(ctx context.Context, loads []TableLoad) (map[string]int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	// Rollback after a successful Commit does nothing, so deferring it is a
	// simple way to guarantee cleanup on every error path below.
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "TRUNCATE routes, stops, trips, stop_times"); err != nil {
		return nil, err
	}

	counts := make(map[string]int64, len(loads))
	for _, l := range loads {
		n, err := tx.CopyFrom(ctx, pgx.Identifier{l.Table}, l.Columns, l.Rows)
		if err != nil {
			return nil, fmt.Errorf("loading %s: %w", l.Table, err)
		}
		counts[l.Table] = n
	}
	return counts, tx.Commit(ctx)
}

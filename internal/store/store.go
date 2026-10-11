// Package store is the only part of the app that talks to PostgreSQL.
//
// Everything else (collector, API, schedule loader) calls methods on Store, so
// SQL lives in one package and can be tested against a real database in one
// place (see store_test.go).
package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// transitTZ is Calgary's time zone. Hour-of-day stats are reported in local
// time, because "8 am" means 8 am in Calgary, not in UTC.
const transitTZ = "America/Edmonton"

type Store struct {
	pool *pgxpool.Pool
}

// New opens a connection pool to the database at databaseURL.
//
// A pool keeps a handful of connections open and lends them out, because
// opening a new database connection for every query is slow.
func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Ping checks the database is reachable. The API's /healthz endpoint uses it.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

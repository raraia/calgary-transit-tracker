package store

import (
	"context"
	"fmt"
	"io/fs"
	"sort"

	"github.com/raraia/calgary-transit-tracker/migrations"
)

// migrationLockID is an arbitrary number that names our Postgres advisory lock.
// Any process that wants to migrate must hold this lock first.
const migrationLockID int64 = 72_041_903

// Migrate brings the database schema up to date by applying, in filename order,
// every migrations/*.sql file that hasn't been applied yet.
//
// Applied versions are recorded in a schema_migrations table, so each file runs
// once per database. Every service calls Migrate on startup, which means a
// fresh database sets itself up and an old one upgrades itself.
func (s *Store) Migrate(ctx context.Context) error {
	// Advisory locks belong to a single connection, so take one connection from
	// the pool and use it for the whole run.
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	// If the collector and API start at the same moment, both would try to
	// migrate. The lock makes the second one wait until the first is done,
	// at which point it finds nothing left to apply.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("taking migration lock: %w", err)
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockID)

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return err
	}

	applied := map[string]bool{}
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return err
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return err
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files) // 001_..., 002_..., in order

	for _, name := range files {
		if applied[name] {
			continue
		}
		sql, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return err
		}

		// Run each file inside a transaction, so a migration that fails halfway
		// leaves the database exactly as it was, instead of half-upgraded.
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		// Exec with no arguments uses Postgres's simple query protocol, which
		// accepts a whole file of statements separated by semicolons.
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("applying %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", name); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

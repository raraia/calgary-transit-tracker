// Package migrations bundles the SQL schema files into the compiled binary.
//
// The //go:embed directive below tells the Go compiler to copy every .sql file
// in this folder into the program itself. That means the collector and API can
// set up or upgrade the database on startup without needing the SQL files to
// exist on the machine they run on. See internal/store/migrate.go for how they
// are applied.
package migrations

import "embed"

// FS holds the .sql files, named so they sort in the order they must run
// (001_..., 002_..., ...).
//
//go:embed *.sql
var FS embed.FS

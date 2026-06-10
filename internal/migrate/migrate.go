// Package migrate runs embedded SQL migrations against the caretd schema using goose.
//
// All migrations live under migrations/*.sql (embedded at compile time via the
// migrations package's go:embed directive). The goose version table is stored in
// caretd.goose_db_version, never in public. Goose is given a *sql.DB adapter over the
// pgx pool so it participates in the same connection pool and honours the
// caretd search_path set by store.Open.
package migrate

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/Samandar-Komilov/caretd/migrations"
)

func init() {
	// Suppress goose's default stdout logger; callers that want verbose migration
	// output can set their own logger via goose.SetLogger before calling Up/Down.
	goose.SetLogger(goose.NopLogger())
}

// Up applies all pending migrations to the database. It is idempotent: already-applied
// migrations are skipped. Migrations are scoped entirely to the caretd schema; nothing
// in public is touched.
//
// The goose version table lives at caretd.goose_db_version, so even the migration
// bookkeeping stays out of public.
//
// Up blocks until all migrations are applied or ctx is cancelled.
func Up(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	// sqlDB wraps the pool; closing it does NOT close the pool itself, so we close
	// this wrapper when done to release any sql.DB-internal state.
	defer func() { _ = sqlDB.Close() }()

	provider, err := newProvider(sqlDB)
	if err != nil {
		return err
	}
	defer func() { _ = provider.Close() }()

	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrate up: apply migrations: %w", err)
	}

	for _, r := range results {
		if r.Error != nil {
			return fmt.Errorf("migrate up: migration %d failed: %w", r.Source.Version, r.Error)
		}
	}

	return nil
}

// Down rolls back all applied migrations to version 0. After Down the caretd schema is
// dropped (the migration's Down section runs DROP SCHEMA caretd CASCADE), leaving the
// host application's public schema untouched.
//
// Intended for testing and development; do not call in production without understanding
// the consequences.
func Down(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()

	provider, err := newProvider(sqlDB)
	if err != nil {
		return err
	}
	defer func() { _ = provider.Close() }()

	if _, err := provider.DownTo(ctx, 0); err != nil {
		return fmt.Errorf("migrate down: roll back migrations: %w", err)
	}

	return nil
}

// OpenSQLDB returns a *sql.DB backed by the given pool, for callers that need a
// database/sql interface (e.g. testing helpers that verify migration results).
// The returned *sql.DB does NOT own the pool's lifetime; close the pool, not the
// returned *sql.DB.
func OpenSQLDB(pool *pgxpool.Pool) *sql.DB {
	return stdlib.OpenDBFromPool(pool)
}

// newProvider constructs a goose.Provider configured for the caretd schema.
func newProvider(sqlDB *sql.DB) (*goose.Provider, error) {
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		sqlDB,
		migrations.FS,
		// Keep the goose version table inside the caretd schema so it never
		// touches public, and so DROP SCHEMA caretd CASCADE removes it cleanly.
		goose.WithTableName("caretd.goose_db_version"),
	)
	if err != nil {
		return nil, fmt.Errorf("migrate: create goose provider: %w", err)
	}
	return provider, nil
}

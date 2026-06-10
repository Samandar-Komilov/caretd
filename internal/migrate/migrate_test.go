// Package migrate_test contains integration tests for the migrate package.
// Tests requiring a live database are skipped when CARETD_DSN is unset.
package migrate_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Samandar-Komilov/caretd/internal/migrate"
	"github.com/Samandar-Komilov/caretd/internal/store"
)

// requireDSN returns the DSN from CARETD_DSN or skips the test.
func requireDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("CARETD_DSN")
	if dsn == "" {
		t.Skip("CARETD_DSN not set; skipping database integration test")
	}
	return dsn
}

// TestUpAndDown verifies the full migration lifecycle:
//  1. Up creates caretd.domains and caretd.endpoints.
//  2. Nothing in public is created by caretd migrations.
//  3. Down drops the caretd schema entirely, leaving public untouched.
func TestUpAndDown(t *testing.T) {
	dsn := requireDSN(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer pool.Close()

	// Apply migrations.
	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("migrate.Up: %v", err)
	}

	// Verify caretd.domains and caretd.endpoints exist.
	sqlDB := migrate.OpenSQLDB(pool)
	defer sqlDB.Close()

	tables := []string{"caretd.domains", "caretd.endpoints"}
	for _, tbl := range tables {
		var exists bool
		row := sqlDB.QueryRowContext(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'caretd' AND table_name = $1)",
			tableNameOnly(tbl),
		)
		if err := row.Scan(&exists); err != nil {
			t.Fatalf("check table %s: %v", tbl, err)
		}
		if !exists {
			t.Errorf("table %s does not exist after Up", tbl)
		}
	}

	// Verify caretd migration bookkeeping is NOT in public.
	var gooseInPublic bool
	row := sqlDB.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'goose_db_version')",
	)
	if err := row.Scan(&gooseInPublic); err != nil {
		t.Fatalf("check goose in public: %v", err)
	}
	if gooseInPublic {
		t.Error("goose_db_version found in public schema; should be in caretd schema only")
	}

	// Roll back all migrations.
	if err := migrate.Down(ctx, pool); err != nil {
		t.Fatalf("migrate.Down: %v", err)
	}

	// After Down, caretd schema should no longer exist.
	var schemaExists bool
	row = sqlDB.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = 'caretd')",
	)
	if err := row.Scan(&schemaExists); err != nil {
		t.Fatalf("check caretd schema after Down: %v", err)
	}
	if schemaExists {
		t.Error("caretd schema still exists after Down")
	}

	// Re-apply so subsequent test runs start clean.
	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("migrate.Up (re-apply after Down): %v", err)
	}
}

// tableNameOnly returns the table name portion of a schema.table string.
func tableNameOnly(qualified string) string {
	for i := len(qualified) - 1; i >= 0; i-- {
		if qualified[i] == '.' {
			return qualified[i+1:]
		}
	}
	return qualified
}

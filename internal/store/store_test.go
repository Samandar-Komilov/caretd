// Package store_test contains integration tests for the store package.
// Tests that require a live database are skipped when CARETD_DSN is not set or the
// database is unreachable.
package store_test

import (
	"context"
	"os"
	"testing"
	"time"

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

// TestOpenAndPing verifies that Open succeeds and Ping works against a live database.
func TestOpenAndPing(t *testing.T) {
	dsn := requireDSN(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer pool.Close()

	if err := store.Ping(ctx, pool); err != nil {
		t.Fatalf("store.Ping: %v", err)
	}
}

// TestSearchPathPinned verifies that search_path is set to caretd on every connection
// acquired from the pool.
func TestSearchPathPinned(t *testing.T) {
	dsn := requireDSN(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer pool.Close()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()

	var searchPath string
	if err := conn.QueryRow(ctx, "SHOW search_path").Scan(&searchPath); err != nil {
		t.Fatalf("query search_path: %v", err)
	}

	if searchPath != "caretd" {
		t.Errorf("search_path = %q, want %q", searchPath, "caretd")
	}
}

// TestOpenBadDSN verifies that a malformed DSN is rejected with a wrapped error.
func TestOpenBadDSN(t *testing.T) {
	ctx := context.Background()
	_, err := store.Open(ctx, "not-a-valid-dsn://??!!")
	if err == nil {
		t.Fatal("expected error for bad DSN, got nil")
	}
}

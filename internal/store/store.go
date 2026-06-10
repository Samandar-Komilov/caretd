// Package store opens and manages the pgx connection pool for caretd.
//
// Every connection in the pool has search_path=caretd set at connect time so that
// unqualified table references (used in goose and sqlc-generated queries) resolve to
// the caretd schema without callers needing to qualify every identifier.
//
// caretd is a guest in the host application's database.  This package never touches
// public schema objects and never creates tables outside caretd.
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open parses dsn, creates a pgx connection pool, and verifies connectivity with a
// Ping.  search_path is set to caretd on every connection so unqualified identifiers
// resolve to the caretd schema.
//
// The caller must call pool.Close() when done.  Use context cancellation to bound
// the initial connection attempt.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("store: parse DSN: %w", err)
	}

	// Pin search_path=caretd on every new connection.  This ensures that all
	// queries — whether from goose, sqlc-generated code, or hand-written SQL —
	// resolve unqualified names to the caretd schema, never public.
	cfg.ConnConfig.RuntimeParams["search_path"] = "caretd"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: create pool: %w", err)
	}

	if err := Ping(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}

	return pool, nil
}

// Ping verifies that the pool can reach the database server.  It acquires one
// connection, runs a trivial query, and releases the connection back.
// Returns a wrapped error if the database is unreachable.
func Ping(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store: acquire connection for ping: %w", err)
	}
	defer conn.Release()

	if err := conn.Ping(ctx); err != nil {
		return fmt.Errorf("store: ping database: %w", err)
	}

	return nil
}

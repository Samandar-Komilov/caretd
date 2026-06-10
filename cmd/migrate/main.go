// Command migrate is a CLI tool for applying and rolling back caretd database
// migrations.  It is not the production daemon; use it during development and CI.
//
// Usage:
//
//	migrate up    — apply all pending migrations
//	migrate down  — roll back all applied migrations
//
// The database DSN is read from CARETD_DSN (or -dsn flag).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/Samandar-Komilov/caretd/internal/config"
	"github.com/Samandar-Komilov/caretd/internal/migrate"
	"github.com/Samandar-Komilov/caretd/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	flag.StringVar(&cfg.DatabaseDSN, "dsn", cfg.DatabaseDSN, "PostgreSQL DSN")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		return fmt.Errorf("usage: migrate up|down")
	}

	ctx := context.Background()

	pool, err := store.Open(ctx, cfg.DatabaseDSN)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	switch args[0] {
	case "up":
		fmt.Println("applying migrations...")
		if err := migrate.Up(ctx, pool); err != nil {
			return fmt.Errorf("up: %w", err)
		}
		fmt.Println("done")
	case "down":
		fmt.Println("rolling back migrations...")
		if err := migrate.Down(ctx, pool); err != nil {
			return fmt.Errorf("down: %w", err)
		}
		fmt.Println("done")
	default:
		return fmt.Errorf("unknown command %q: use up or down", args[0])
	}

	return nil
}

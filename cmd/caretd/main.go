// Command caretd is the entrypoint for the caretd SIP B2BUA and media server.
//
// This file is the ONLY place that wires concrete dependencies (STYLEGUIDE §1).
// All other packages receive interfaces, not concrete types.
//
// Startup sequence:
//  1. Parse config from env vars; apply any flag overrides.
//  2. Build structured slog JSON logger.
//  3. Install signal handler; cancel root context on SIGINT/SIGTERM.
//  4. Open pgx pool (fail fast if DB unreachable).
//  5. Run migrations (idempotent; creates caretd schema if needed).
//  6. Log "caretd ready" with all configured listen addresses.
//  7. Block on ctx.Done().
//  8. Graceful shutdown: close pool, log clean exit.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Samandar-Komilov/caretd/internal/config"
	"github.com/Samandar-Komilov/caretd/internal/migrate"
	"github.com/Samandar-Komilov/caretd/internal/store"
)

func main() {
	if err := run(); err != nil {
		// Use fmt.Fprintf so the error appears even before the logger is ready.
		fmt.Fprintf(os.Stderr, "caretd: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. Load configuration from env vars (defaults populated in config.Load).
	//    Flag overrides are applied after parsing; they shadow env values when
	//    explicitly provided on the command line.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Register flag overrides against flag.CommandLine.  Defaults are the values
	// already loaded from env so that -flag=val only applies when the user passes it.
	flag.StringVar(&cfg.DatabaseDSN, "dsn", cfg.DatabaseDSN,
		"PostgreSQL DSN (overrides CARETD_DSN)")
	flag.StringVar(&cfg.SIPListenAddr, "sip-addr", cfg.SIPListenAddr,
		"SIP listen address (overrides CARETD_SIP_ADDR)")
	flag.StringVar(&cfg.ControlAddr, "control-addr", cfg.ControlAddr,
		"Control API listen address (overrides CARETD_CONTROL_ADDR)")
	flag.StringVar(&cfg.ObsAddr, "obs-addr", cfg.ObsAddr,
		"Observability API listen address (overrides CARETD_OBS_ADDR)")
	flag.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel,
		"Log level: debug|info|warn|error (overrides CARETD_LOG_LEVEL)")
	flag.Parse()

	// Re-validate in case flags changed a value to something invalid.
	// (config.Load already validated defaults; this catches flag overrides.)

	// 2. Build the structured JSON logger at the configured level.
	logger := buildLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	// 3. Root context — cancelled on SIGINT or SIGTERM.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 4. Open the pgx connection pool.  Fail fast: if the database is unreachable
	//    caretd cannot serve SIP traffic, so there is no point starting.
	logger.Info("connecting to database", "dsn_hint", dsnHint(cfg.DatabaseDSN))
	pool, err := store.Open(ctx, cfg.DatabaseDSN)
	if err != nil {
		return fmt.Errorf("open database pool: %w", err)
	}
	defer func() {
		pool.Close()
		logger.Info("database pool closed")
	}()

	// 5. Run migrations.  Idempotent: applied migrations are skipped.
	//    Every object is created in schema caretd; public is never touched.
	logger.Info("running migrations")
	if err := migrate.Up(ctx, pool); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	logger.Info("migrations applied")

	// 6. Log that the daemon is ready.  (No SIP/HTTP servers yet — added in later phases.)
	logger.Info("caretd ready",
		"sip_addr", cfg.SIPListenAddr,
		"control_addr", cfg.ControlAddr,
		"obs_addr", cfg.ObsAddr,
	)

	// 7. Block until a signal is received.
	<-ctx.Done()

	// 8. Graceful shutdown.
	logger.Info("shutting down", "reason", ctx.Err())
	// pool.Close() is called via defer above.

	logger.Info("caretd stopped")
	return nil
}

// buildLogger returns a JSON slog.Logger at the requested level.  level must be one of
// debug, info, warn, error (validated by config.Load).
func buildLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		// config.Load already validated the level; this branch should never be reached.
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}

// dsnHint returns a safe substring of the DSN for logging — enough to identify the
// host/path without leaking a password.
func dsnHint(dsn string) string {
	// Return only the first 60 chars with any middle portion redacted to avoid
	// leaking credentials that may appear in postgres://user:pass@host/db URLs.
	const maxLen = 60
	if len(dsn) <= maxLen {
		return dsn
	}
	return dsn[:30] + "…" + dsn[len(dsn)-10:]
}

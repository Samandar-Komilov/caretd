// Package config loads caretd runtime configuration from environment variables.
// Flags are registered on the default flag.CommandLine set; the caller (main.go)
// calls flag.Parse() exactly once after program startup. There are no config files:
// all knobs come from the environment or command-line flags, matching the "no static
// config" principle of caretd (PLAN §1).
//
// Environment variables (all prefixed CARETD_):
//
//	CARETD_DSN          PostgreSQL DSN.
//	                    Default: "postgres:///caretd?host=/tmp" (local Unix-socket peer auth
//	                    on systems where the socket lives under /tmp, e.g. Fedora/RHEL).
//	                    For Debian/Ubuntu the socket is usually /var/run/postgresql; set the
//	                    env var accordingly:
//	                      CARETD_DSN="postgres:///caretd?host=/var/run/postgresql"
//	                    For password auth:
//	                      CARETD_DSN="postgres://user:pass@localhost/caretd"
//	CARETD_SIP_ADDR     SIP UDP/TCP listen address. Default: ":5060"
//	CARETD_CONTROL_ADDR REST control-plane listen address. Default: ":8080"
//	CARETD_OBS_ADDR     Observability listen address. Default: ":9090"
//	CARETD_LOG_LEVEL    Structured log level: debug|info|warn|error. Default: "info"
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Config holds all caretd runtime configuration. All fields are populated by Load.
type Config struct {
	// DatabaseDSN is the PostgreSQL connection string used to open the pgx pool.
	// The store package will pin search_path=caretd on every connection.
	DatabaseDSN string

	// SIPListenAddr is the address the SIP transport listens on (Phase 2+).
	SIPListenAddr string

	// ControlAddr is the address the REST control API listens on (Phase 8+).
	ControlAddr string

	// ObsAddr is the address the observability API listens on (Phase 9+).
	ObsAddr string

	// LogLevel controls slog output verbosity. One of: debug, info, warn, error.
	LogLevel string
}

// Load reads configuration from environment variables.
// Environment variables take precedence over compiled-in defaults.
// Returns an error if any required field fails validation.
//
// Callers that also want flag overrides should register flags against
// flag.CommandLine (using RegisterFlags) and call flag.Parse() before Load.
func Load() (Config, error) {
	cfg := Config{
		DatabaseDSN:   envOrDefault("CARETD_DSN", "postgres:///caretd?host=/tmp"),
		SIPListenAddr: envOrDefault("CARETD_SIP_ADDR", ":5060"),
		ControlAddr:   envOrDefault("CARETD_CONTROL_ADDR", ":8080"),
		ObsAddr:       envOrDefault("CARETD_OBS_ADDR", ":9090"),
		LogLevel:      envOrDefault("CARETD_LOG_LEVEL", "info"),
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validate returns an error if any field fails a basic sanity check.
func (c *Config) validate() error {
	if strings.TrimSpace(c.DatabaseDSN) == "" {
		return errors.New("database DSN must not be empty (set CARETD_DSN)")
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "warning", "error":
		// Normalise "warning" → "warn" for slog compatibility.
		if strings.ToLower(c.LogLevel) == "warning" {
			c.LogLevel = "warn"
		}
	default:
		return fmt.Errorf("invalid log level %q: must be one of debug, info, warn, error", c.LogLevel)
	}
	return nil
}

// envOrDefault returns the value of the environment variable name if set and non-empty,
// otherwise returns def.
func envOrDefault(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

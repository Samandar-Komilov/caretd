package config_test

import (
	"os"
	"testing"

	"github.com/Samandar-Komilov/caretd/internal/config"
)

// TestLoadDefaults verifies that Load returns sensible defaults when no env vars or
// flags are set.  We clear relevant env vars and re-invoke via a subprocess-friendly
// approach by temporarily unsetting them.
func TestLoadDefaults(t *testing.T) {
	// Unset all CARETD_ env vars for this test.
	vars := []string{
		"CARETD_DSN", "CARETD_SIP_ADDR", "CARETD_CONTROL_ADDR",
		"CARETD_OBS_ADDR", "CARETD_LOG_LEVEL",
	}
	saved := make(map[string]string, len(vars))
	for _, v := range vars {
		saved[v] = os.Getenv(v)
		os.Unsetenv(v)
	}
	t.Cleanup(func() {
		for k, v := range saved {
			if v != "" {
				os.Setenv(k, v)
			}
		}
	})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.SIPListenAddr != ":5060" {
		t.Errorf("SIPListenAddr = %q, want :5060", cfg.SIPListenAddr)
	}
	if cfg.ControlAddr != ":8080" {
		t.Errorf("ControlAddr = %q, want :8080", cfg.ControlAddr)
	}
	if cfg.ObsAddr != ":9090" {
		t.Errorf("ObsAddr = %q, want :9090", cfg.ObsAddr)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
}

// TestLoadFromEnv verifies that env vars override the defaults.
func TestLoadFromEnv(t *testing.T) {
	t.Setenv("CARETD_DSN", "postgres://user:pass@localhost/testdb")
	t.Setenv("CARETD_SIP_ADDR", ":5061")
	t.Setenv("CARETD_CONTROL_ADDR", ":9001")
	t.Setenv("CARETD_OBS_ADDR", ":9002")
	t.Setenv("CARETD_LOG_LEVEL", "debug")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.DatabaseDSN != "postgres://user:pass@localhost/testdb" {
		t.Errorf("DatabaseDSN = %q", cfg.DatabaseDSN)
	}
	if cfg.SIPListenAddr != ":5061" {
		t.Errorf("SIPListenAddr = %q, want :5061", cfg.SIPListenAddr)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}

// TestLoadInvalidLogLevel verifies that an invalid log level is rejected.
func TestLoadInvalidLogLevel(t *testing.T) {
	t.Setenv("CARETD_LOG_LEVEL", "verbose")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid log level, got nil")
	}
}

// TestLoadEmptyDSN verifies that an empty DSN is rejected.
func TestLoadEmptyDSN(t *testing.T) {
	t.Setenv("CARETD_DSN", "   ")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for empty DSN, got nil")
	}
}

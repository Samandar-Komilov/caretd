// Package migrations embeds the caretd SQL migration files so that the
// internal/migrate package can access them at compile time via go:embed.
// All SQL objects are created in schema caretd; nothing in public is touched.
package migrations

import "embed"

// FS holds all *.sql files in this directory.
//
//go:embed *.sql
var FS embed.FS

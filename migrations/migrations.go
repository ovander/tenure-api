// Package migrations embeds the SQL migrations applied by `tenure migrate`
// (RENT-BE-001 §2): six-digit zero-padded files with goose Up and Down
// sections, each reversible or explicitly marked not.
package migrations

import "embed"

// FS holds the migration files.
//
//go:embed *.sql
var FS embed.FS

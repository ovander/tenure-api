// Package migrate applies the embedded SQL migrations with goose as the
// tenure_migrate role (RENT-BE-001 §2, OPEN-QUESTIONS Q-21).
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"github.com/pressly/goose/v3"

	// Registers the pgx database/sql driver used by the migrations.
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/ovander/tenure-api/migrations"
)

// Migrator applies the embedded migrations as tenure_migrate (Q-21).
type Migrator struct {
	db *sql.DB
	p  *goose.Provider
}

// NewMigrator connects to url, which must be the tenure_migrate role.
func NewMigrator(url string) (*Migrator, error) {
	s, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, s, migrations.FS)
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	return &Migrator{db: s, p: p}, nil
}

// Close closes the connection.
func (m *Migrator) Close() error { return m.db.Close() }

// Up applies every pending migration.
func (m *Migrator) Up(ctx context.Context, out io.Writer) error {
	res, err := m.p.Up(ctx)
	for _, r := range res {
		_, _ = fmt.Fprintln(out, r)
	}
	return err
}

// UpTo applies the pending migrations up to and including version, so a test
// can stand a database at an earlier schema.
func (m *Migrator) UpTo(ctx context.Context, version int64, out io.Writer) error {
	res, err := m.p.UpTo(ctx, version)
	for _, r := range res {
		_, _ = fmt.Fprintln(out, r)
	}
	return err
}

// Down reverts the latest migration.
func (m *Migrator) Down(ctx context.Context, out io.Writer) error {
	r, err := m.p.Down(ctx)
	if r != nil {
		_, _ = fmt.Fprintln(out, r)
	}
	return err
}

// Status prints the state of every migration.
func (m *Migrator) Status(ctx context.Context, out io.Writer) error {
	st, err := m.p.Status(ctx)
	if err != nil {
		return err
	}
	for _, s := range st {
		_, _ = fmt.Fprintf(out, "%06d %-8s %s\n", s.Source.Version, s.State, s.Source.Path)
	}
	return nil
}

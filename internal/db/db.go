// Package db opens PostgreSQL through gorm on the pgx driver and runs every
// unit of work in a transaction that carries the row-level-security context
// (RENT-BE-001 §2, §4): app.owner_id, the active owner scope, and
// app.actor_sub, the Socrate subject of the caller. Both are set with
// set_config(…, true), so they end with the transaction. Repositories take the
// transaction from the context with Tx and never add an owner filter
// themselves; a repository called outside InScope fails rather than running
// without a scope.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ovander/tenure-api/internal/invariant"
)

// TypePeriodLocked is the problem of a write the period lock refused
// (migration 000025, Q-127): the database raises SQLSTATE codePeriodLocked
// and InScope returns it as this business rule. internal/ledger registers
// its titles.
const TypePeriodLocked = "ledger/period-locked"

const codePeriodLocked = "TL001"

// lockErr turns the period lock's refusal into its business rule.
func lockErr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == codePeriodLocked {
		return invariant.New(TypePeriodLocked, pg.Message)
	}
	return err
}

// ErrNoScope is returned by Tx outside InScope.
var ErrNoScope = errors.New("db: no scoped transaction in context")

// Scope is the security context of a transaction.
type Scope struct {
	// OwnerID is the active owner scope; uuid.Nil for none (the platform
	// scope of admin actions, or scope resolution before one is chosen).
	OwnerID uuid.UUID
	// ActorSub is the caller's Socrate subject; empty for jobs.
	ActorSub string
	// Role is the caller's role in the scope (owner, co_owner, accountant,
	// admin, job), recorded in the audit trail.
	Role      string
	RequestID string
}

type txKey struct{}

type txValue struct {
	tx    *gorm.DB
	scope Scope
}

// DB is a connection pool.
type DB struct {
	gorm *gorm.DB
	sql  *sql.DB
}

// Open connects to url and checks the connection.
func Open(ctx context.Context, url string) (*DB, error) {
	g, err := gorm.Open(postgres.Open(url), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
		NowFunc:                func() time.Time { return time.Now().UTC() },
		TranslateError:         true,
	})
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	s, err := g.DB()
	if err != nil {
		return nil, err
	}
	s.SetMaxOpenConns(20)
	s.SetConnMaxIdleTime(5 * time.Minute)
	if err := s.PingContext(ctx); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return &DB{gorm: g, sql: s}, nil
}

// Close closes the pool.
func (d *DB) Close() error { return d.sql.Close() }

// Ping checks the database is reachable (used by /readyz).
func (d *DB) Ping(ctx context.Context) error { return d.sql.PingContext(ctx) }

// InScope runs fn in one transaction carrying scope; fn's error rolls it back.
// Inside fn, ctx carries the transaction for Tx and the scope for ScopeFrom. A
// nested call with the same scope joins the outer transaction; a nested call
// with another scope is refused.
func (d *DB) InScope(ctx context.Context, scope Scope, fn func(ctx context.Context) error) error {
	if v, ok := ctx.Value(txKey{}).(txValue); ok {
		if v.scope != scope {
			return errors.New("db: nested transaction with a different scope")
		}
		return fn(ctx)
	}
	return lockErr(d.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		owner := ""
		if scope.OwnerID != uuid.Nil {
			owner = scope.OwnerID.String()
		}
		if err := tx.Exec("SELECT set_config('app.owner_id', ?, true), set_config('app.actor_sub', ?, true)",
			owner, scope.ActorSub).Error; err != nil {
			return fmt.Errorf("db: set scope: %w", err)
		}
		return fn(context.WithValue(ctx, txKey{}, txValue{tx: tx, scope: scope}))
	}))
}

// Tx returns the scoped transaction of ctx, bound to ctx.
func Tx(ctx context.Context) (*gorm.DB, error) {
	v, ok := ctx.Value(txKey{}).(txValue)
	if !ok {
		return nil, ErrNoScope
	}
	return v.tx.WithContext(ctx), nil
}

// ScopeFrom returns the scope of the transaction in ctx.
func ScopeFrom(ctx context.Context) (Scope, bool) {
	v, ok := ctx.Value(txKey{}).(txValue)
	return v.scope, ok
}

// SQL returns the underlying database/sql pool (migrations only).
func (d *DB) SQL() *sql.DB { return d.sql }

// IsUniqueViolation reports whether err is a unique-constraint violation.
func IsUniqueViolation(err error) bool { return errors.Is(err, gorm.ErrDuplicatedKey) }

// IsNotFound reports whether err is gorm's record-not-found.
func IsNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }

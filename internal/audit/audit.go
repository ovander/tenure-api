// Package audit records the append-only, hash-chained audit trail
// (RENT-SPEC-022, RENT-BE-001 §8b). Every service write calls Record after the
// write and before commit, inside the same scoped transaction, so a write
// without its event — or an event without its write — cannot commit.
//
// Each owner has one chain; owner_id NULL is the platform chain (admin
// parameter changes, OPEN-QUESTIONS Q-36). An event's hash is
//
//	sha256(prev_hash ‖ canonical JSON of the event)
//
// where prev_hash is the previous event's hash in hex (64 zeros for the first)
// and the canonical JSON has sorted keys and no insignificant space. It is
// computed in Go; inserts on one chain are serialised by a transaction-scoped
// advisory lock.
package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/db"
)

// Genesis is the prev_hash of a chain's first event.
var Genesis = strings.Repeat("0", 64)

// Now is the clock of the audit trail; tests replace it.
var Now = func() time.Time { return time.Now().UTC() }

// Entity identifies what an event is about.
type Entity struct {
	Type string
	ID   string
}

// Event is a stored audit event.
type Event struct {
	ID         int64           `json:"id" gorm:"column:id;primaryKey"`
	OwnerID    *uuid.UUID      `json:"owner_id" gorm:"column:owner_id"`
	ActorSub   string          `json:"actor_sub" gorm:"column:actor_sub"`
	Scope      string          `json:"scope" gorm:"column:scope"`
	Action     string          `json:"action" gorm:"column:action"`
	EntityType string          `json:"entity_type" gorm:"column:entity_type"`
	EntityID   string          `json:"entity_id" gorm:"column:entity_id"`
	Before     json.RawMessage `json:"before" gorm:"column:before;type:jsonb" swaggertype:"object"`
	After      json.RawMessage `json:"after" gorm:"column:after;type:jsonb" swaggertype:"object"`
	RequestID  string          `json:"request_id" gorm:"column:request_id"`
	OccurredAt time.Time       `json:"occurred_at" gorm:"column:occurred_at"`
	PrevHash   string          `json:"prev_hash" gorm:"column:prev_hash"`
	Hash       string          `json:"hash" gorm:"column:hash"`
}

// TableName maps Event to audit_events.
func (Event) TableName() string { return "audit_events" }

// Record appends one event to the chain of the transaction's scope. before and
// after are marshalled to JSON (nil for a creation or a read action).
func Record(ctx context.Context, action string, entity Entity, before, after any) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	sc, _ := db.ScopeFrom(ctx)
	if action == "" || entity.Type == "" || entity.ID == "" {
		return errors.New("audit: action and entity are required")
	}
	actor := sc.ActorSub
	if actor == "" {
		actor = "system:" + sc.Role
	}
	e := Event{ActorSub: actor, Scope: sc.Role, Action: action, EntityType: entity.Type, EntityID: entity.ID,
		RequestID: sc.RequestID, OccurredAt: Now().Truncate(time.Microsecond)}
	if sc.OwnerID != uuid.Nil {
		id := sc.OwnerID
		e.OwnerID = &id
	}
	if e.Before, err = toJSON(before); err != nil {
		return err
	}
	if e.After, err = toJSON(after); err != nil {
		return err
	}
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", chainKey(e.OwnerID)).Error; err != nil {
		return fmt.Errorf("audit: lock: %w", err)
	}
	var prev *string
	if err := tx.Raw("SELECT audit_last_hash()").Scan(&prev).Error; err != nil {
		return fmt.Errorf("audit: head: %w", err)
	}
	e.PrevHash = Genesis
	if prev != nil && *prev != "" {
		e.PrevHash = *prev
	}
	if e.Hash, err = Hash(e); err != nil {
		return err
	}
	// A plain INSERT, without RETURNING: a caller allowed to append to a chain
	// (a co-owner downloading a document, an admin on the platform chain) need
	// not be allowed to read it, and RETURNING would apply the read policy.
	err = tx.Exec(`INSERT INTO audit_events (owner_id, actor_sub, scope, action, entity_type, entity_id, before, after,
		request_id, occurred_at, prev_hash, hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.OwnerID, e.ActorSub, e.Scope, e.Action, e.EntityType, e.EntityID, nullJSON(e.Before), nullJSON(e.After),
		e.RequestID, e.OccurredAt, e.PrevHash, e.Hash).Error
	if err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}
	return nil
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

func chainKey(owner *uuid.UUID) string {
	if owner == nil {
		return "audit:platform"
	}
	return "audit:" + owner.String()
}

func toJSON(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("audit: marshal: %w", err)
	}
	return b, nil
}

// Canonical returns the canonical JSON of the hashed fields of e: sorted keys,
// numbers kept as written, before/after normalised the same way whether they
// come from Go or back from jsonb.
func Canonical(e Event) ([]byte, error) {
	norm := func(raw json.RawMessage) (any, error) {
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			return nil, nil
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("audit: canonical: %w", err)
		}
		return v, nil
	}
	before, err := norm(e.Before)
	if err != nil {
		return nil, err
	}
	after, err := norm(e.After)
	if err != nil {
		return nil, err
	}
	var owner any
	if e.OwnerID != nil {
		owner = e.OwnerID.String()
	}
	m := map[string]any{
		"owner_id": owner, "actor_sub": e.ActorSub, "scope": e.Scope, "action": e.Action,
		"entity_type": e.EntityType, "entity_id": e.EntityID, "before": before, "after": after,
		"request_id": e.RequestID, "occurred_at": e.OccurredAt.UTC().Format(time.RFC3339Nano), "prev_hash": e.PrevHash,
	}
	return json.Marshal(m) // map keys are sorted
}

// Hash returns sha256(prev_hash ‖ canonical JSON) in hex.
func Hash(e Event) (string, error) {
	c, err := Canonical(e)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(e.PrevHash))
	h.Write(c)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Result is the outcome of verifying one chain.
type Result struct {
	Events int    `json:"events"`
	OK     bool   `json:"ok"`
	BadID  int64  `json:"bad_id,omitempty"`
	Reason string `json:"reason,omitempty"`
	Head   string `json:"head,omitempty"`
}

// Verify recomputes the chain of the transaction's scope, in id order, and
// reports the first event whose link or hash does not match.
func Verify(ctx context.Context) (Result, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("audit: %w", err)
	}
	sc, _ := db.ScopeFrom(ctx)
	q := tx.Model(&Event{}).Order("id")
	if sc.OwnerID == uuid.Nil {
		q = q.Where("owner_id IS NULL")
	} else {
		q = q.Where("owner_id = ?", sc.OwnerID)
	}
	var events []Event
	if err := q.Find(&events).Error; err != nil {
		return Result{}, fmt.Errorf("audit: read chain: %w", err)
	}
	r := Result{Events: len(events), OK: true}
	prev := Genesis
	for _, e := range events {
		if e.PrevHash != prev {
			return Result{Events: r.Events, BadID: e.ID, Reason: "prev_hash does not link to the previous event"}, nil
		}
		h, err := Hash(e)
		if err != nil {
			return Result{}, err
		}
		if h != e.Hash {
			return Result{Events: r.Events, BadID: e.ID, Reason: "hash does not match the event"}, nil
		}
		prev = e.Hash
	}
	r.Head = prev
	return r, nil
}

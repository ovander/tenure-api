package params

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of the params module.
const (
	TypeUnknownKey    = "params/unknown-key"
	TypePastEffective = "params/effective-date-in-past"
	TypeDuplicate     = "params/duplicate-entry"
	TypeInvalidValue  = "params/invalid-value"
	TypeIRLExists     = "params/irl-quarter-exists"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeUnknownKey:    problem.M(422, "Paramètre légal inconnu.", "Unknown legal parameter."),
		TypePastEffective: problem.M(422, "Un paramètre ne peut pas prendre effet à une date passée.", "A parameter cannot take effect on a past date."),
		TypeDuplicate:     problem.M(409, "Une valeur existe déjà pour ce paramètre à cette date.", "A value already exists for this parameter on that date."),
		TypeInvalidValue:  problem.M(422, "Valeur de paramètre invalide.", "Invalid parameter value."),
		TypeIRLExists:     problem.M(409, "L’indice de ce trimestre est déjà enregistré.", "The index of this quarter is already recorded."),
	})
}

// Service serves legal parameters and IRL values.
type Service struct {
	DB   *db.DB
	Repo Repository
	Now  func() time.Time
}

// List returns every entry, or those of key, in force on asOf when set.
func (s Service) List(ctx context.Context, sc db.Scope, key string, asOf *period.Date) ([]Entry, error) {
	var out []Entry
	err := s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		set, err := s.Repo.Load(ctx)
		if err != nil {
			return err
		}
		for _, e := range set.Entries() {
			if key != "" && e.Key != key {
				continue
			}
			if asOf != nil {
				cur, err := set.Lookup(e.Key, *asOf)
				if err != nil || cur.EffectiveFrom != e.EffectiveFrom {
					continue
				}
			}
			out = append(out, e)
		}
		return nil
	})
	return out, err
}

// IRL returns the cached IRL series.
func (s Service) IRL(ctx context.Context, sc db.Scope) ([]IRL, error) {
	var out []IRL
	err := s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		var err error
		out, err = s.Repo.IRLSeries(ctx)
		return err
	})
	return out, err
}

// PutInput is a new effective-dated value.
type PutInput struct {
	EffectiveFrom period.Date     `json:"effective_from" swaggertype:"string" format:"date"`
	Value         json.RawMessage `json:"value" swaggertype:"object"`
	Source        string          `json:"source"`
	SourceURL     string          `json:"source_url"`
}

// Put appends a value of an existing key (admin, step-up; RENT-SPEC-021: a
// change is a reviewed entry, never an edit). It never rewrites a past value:
// the effective date must be today or later (Q-39). The event goes to the
// platform audit chain (Q-36).
func (s Service) Put(ctx context.Context, p *auth.Principal, requestID, key string, in PutInput) (Entry, error) {
	var f invariant.Fields
	if in.EffectiveFrom.IsZero() {
		f.Add("effective_from", "La date d’effet est obligatoire.")
	}
	if len(in.Value) == 0 || !json.Valid(in.Value) {
		f.Add("value", "La valeur doit être un JSON valide.")
	}
	if in.Source == "" {
		f.Add("source", "La source juridique est obligatoire.")
	}
	if err := f.Err(); err != nil {
		return Entry{}, err
	}
	today := period.FromTime(s.Now().In(paris()))
	if in.EffectiveFrom.Before(today) {
		return Entry{}, invariant.New(TypePastEffective, in.EffectiveFrom.String())
	}
	e := Entry{Key: key, EffectiveFrom: in.EffectiveFrom, Value: in.Value, Source: in.Source, SourceURL: in.SourceURL}
	err := s.DB.InScope(ctx, p.PlatformScope(requestID), func(ctx context.Context) error {
		set, err := s.Repo.Load(ctx)
		if err != nil {
			return err
		}
		known := false
		for _, x := range set.Entries() {
			known = known || x.Key == key
		}
		if !known {
			return invariant.New(TypeUnknownKey, key)
		}
		if _, err := set.With(e); err != nil {
			if errors.Is(err, ErrDuplicate) {
				return invariant.New(TypeDuplicate, key)
			}
			return invariant.New(TypeInvalidValue, err.Error())
		}
		if err := s.Repo.Insert(ctx, e, p.Sub); err != nil {
			if db.IsUniqueViolation(err) {
				return invariant.New(TypeDuplicate, key)
			}
			return err
		}
		return audit.Record(ctx, "params.put", audit.Entity{Type: "legal_param", ID: fmt.Sprintf("%s@%s", key, e.EffectiveFrom)}, nil, e)
	})
	var inv *invariant.Error
	if errors.As(err, &inv) {
		return Entry{}, inv
	}
	return e, err
}

// IRLInput is the body of PUT /params/irl/{quarter}.
type IRLInput struct {
	Value       int64       `json:"value" example:"14668"`
	PublishedOn period.Date `json:"published_on" swaggertype:"string" format:"date"`
	Source      string      `json:"source"`
}

// PutIRL records an IRL value by hand when INSEE cannot be reached (admin,
// step-up; RENT-SPEC-006 manual override). Like the fetch, it never replaces a
// recorded quarter (Q-53). The event goes to the platform chain.
func (s Service) PutIRL(ctx context.Context, p *auth.Principal, requestID, quarter string, in IRLInput) (IRL, error) {
	var f invariant.Fields
	if !quarterRe.MatchString(quarter) {
		f.Add("quarter", "Trimestre attendu au format AAAA-Qn.")
	}
	if in.Value <= 0 {
		f.Add("value", "L’indice doit être positif (en centièmes).")
	}
	if in.PublishedOn.IsZero() {
		f.Add("published_on", "La date de publication est obligatoire.")
	}
	if in.Source == "" {
		f.Add("source", "La source est obligatoire.")
	}
	if err := f.Err(); err != nil {
		return IRL{}, err
	}
	out := IRL{Quarter: quarter, Value: in.Value, PublishedOn: in.PublishedOn}
	err := s.DB.InScope(ctx, p.PlatformScope(requestID), func(ctx context.Context) error {
		ok, err := s.Repo.InsertIRL(ctx, quarter, in.Value, in.PublishedOn.Time(), "manual: "+in.Source)
		if err != nil {
			return err
		}
		if !ok {
			return invariant.New(TypeIRLExists, quarter)
		}
		return audit.Record(ctx, "irl.override", audit.Entity{Type: "irl_index", ID: quarter}, nil, map[string]any{
			"value": in.Value, "published_on": in.PublishedOn, "source": in.Source})
	})
	return out, err
}

func fieldErr(field, msg string) error {
	var f invariant.Fields
	f.Add(field, msg)
	return f
}

func paris() *time.Location {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return time.UTC
	}
	return loc
}

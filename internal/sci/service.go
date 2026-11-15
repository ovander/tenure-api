package sci

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/portfolio"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of the register.
const (
	TypeCapitalExceeded = "sci/capital-exceeded"
	TypeHoldingClosed   = "sci/holding-closed"
	TypeHoldingParts    = "sci/holding-parts"
	TypeSameAssociate   = "sci/same-associate"
	TypeAssociateLinked = "sci/associate-linked"
	TypeLinkGrant       = "sci/link-grant"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeCapitalExceeded: problem.M(422, "Les parts détenues dépasseraient le capital de la SCI.", "The parts held would exceed the SCI's capital."),
		TypeHoldingClosed:   problem.M(409, "Ces parts ne sont plus détenues à cette date : la détention est close ou commence après.", "These parts are no longer held on that day: the holding is closed or starts later."),
		TypeHoldingParts:    problem.M(422, "On ne cède pas plus de parts que la détention n’en compte.", "No more parts can be transferred than the holding has."),
		TypeSameAssociate:   problem.M(422, "Les parts vont à un autre associé que celui qui les détient.", "The parts go to an associate other than the one holding them."),
		TypeAssociateLinked: problem.M(409, "Cette personne est déjà associée de la SCI.", "This person is already an associate of the SCI."),
		TypeLinkGrant:       problem.M(422, "Le lien se fait avec vous-même ou avec une personne ayant un accès en cours à la SCI.", "The link is to yourself or to a person with live access to the SCI."),
	})
}

// Reasons of a movement of parts (Q-133). A subscription creates parts
// within the capital; the others move parts from one associate to another.
const (
	ReasonSubscription = "subscription"
	ReasonTransfer     = "transfer"
	ReasonGift         = "gift"
	ReasonSuccession   = "succession"
)

// Service runs the register of an SCI.
type Service struct {
	DB    *db.DB
	Repo  Repository
	Now   func() time.Time
	NewID func() uuid.UUID
}

func (s Service) now() time.Time { return s.Now().UTC().Truncate(time.Microsecond) }

func (s Service) today() period.Date {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return period.FromTime(s.Now().UTC())
	}
	return period.FromTime(s.Now().In(loc))
}

func scope(p *auth.Principal, requestID string) (db.Scope, error) {
	if p.Active == nil {
		return db.Scope{}, invariant.ErrForbidden
	}
	if !p.Active.IsSCI() {
		return db.Scope{}, invariant.New(portfolio.TypeEntityNotSCI, "")
	}
	return p.DBScope(requestID), nil
}

// AssociateInput is the body of POST /associates and PUT /associates/{id}.
// Link "self" links the associate to the caller; a grant_id links them to
// the person holding that live grant on the SCI; neither leaves a name only.
type AssociateInput struct {
	Name    string     `json:"name"`
	Link    string     `json:"link" enums:"self"`
	GrantID *uuid.UUID `json:"grant_id"`
}

// HoldingView is a holding of parts: from from_on, up to the day before
// closed_on.
type HoldingView struct {
	ID       uuid.UUID    `json:"id"`
	Parts    int          `json:"parts"`
	FromOn   period.Date  `json:"from_on" swaggertype:"string" format:"date"`
	ClosedOn *period.Date `json:"closed_on" swaggertype:"string" format:"date"`
}

// AssociateView is an associate and their holdings.
type AssociateView struct {
	ID        uuid.UUID     `json:"id"`
	Name      string        `json:"name"`
	PersonSub *string       `json:"person_sub"`
	Me        bool          `json:"me"`
	Version   int           `json:"version"`
	PartsOn   int           `json:"parts_on"`
	Holdings  []HoldingView `json:"holdings"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// Register is GET /associates: the SCI's capital, the parts held on the day
// asked (today by default) and its associates in register order.
type Register struct {
	On           period.Date     `json:"on" swaggertype:"string" format:"date"`
	CapitalParts int             `json:"capital_parts"`
	HeldParts    int             `json:"held_parts"`
	Associates   []AssociateView `json:"associates"`
}

func holdingOf(h holdingRow) Holding {
	v := Holding{ID: h.ID, Associate: h.AssociateID, Parts: h.Parts, From: period.FromTime(h.FromOn)}
	if h.ClosedOn != nil {
		c := period.FromTime(*h.ClosedOn)
		v.Closed = &c
	}
	return v
}

func associateView(a associateRow, hs []Holding, on period.Date, me string) AssociateView {
	v := AssociateView{ID: a.ID, Name: a.Name, PersonSub: a.PersonSub, Me: a.PersonSub != nil && *a.PersonSub == me,
		Version: a.Version, Holdings: []HoldingView{}, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
	for _, h := range hs {
		if h.Associate != a.ID {
			continue
		}
		v.Holdings = append(v.Holdings, HoldingView{ID: h.ID, Parts: h.Parts, FromOn: h.From, ClosedOn: h.Closed})
		if h.HeldOn(on) {
			v.PartsOn += h.Parts
		}
	}
	return v
}

// Register returns the SCI's register on a day (nil: today in Europe/Paris).
func (s Service) Register(ctx context.Context, p *auth.Principal, requestID string, on *period.Date) (Register, error) {
	sc, err := scope(p, requestID)
	if err != nil {
		return Register{}, err
	}
	day := s.today()
	if on != nil {
		day = *on
	}
	out := Register{On: day, Associates: []AssociateView{}}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		capital, _, err := s.Repo.Capital(ctx, sc.OwnerID)
		if err != nil {
			return err
		}
		as, err := s.Repo.Associates(ctx)
		if err != nil {
			return err
		}
		rows, err := s.Repo.Holdings(ctx)
		if err != nil {
			return err
		}
		hs := make([]Holding, len(rows))
		for i, h := range rows {
			hs[i] = holdingOf(h)
		}
		out.CapitalParts = capital
		_, out.HeldParts = PartsOn(hs, day)
		for _, a := range as {
			out.Associates = append(out.Associates, associateView(a, hs, day, p.Sub))
		}
		return nil
	})
	return out, err
}

func (s Service) link(ctx context.Context, p *auth.Principal, owner uuid.UUID, in AssociateInput) (*string, error) {
	switch {
	case in.Link == "self" && in.GrantID == nil:
		sub := p.Sub
		return &sub, nil
	case in.Link == "" && in.GrantID != nil:
		sub, ok, err := s.Repo.LiveGrantee(ctx, owner, *in.GrantID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, invariant.New(TypeLinkGrant, in.GrantID.String())
		}
		return &sub, nil
	case in.Link == "" && in.GrantID == nil:
		return nil, nil
	}
	return nil, invariant.New(TypeLinkGrant, "")
}

func validAssociate(in *AssociateInput) error {
	in.Name = strings.TrimSpace(in.Name)
	var f invariant.Fields
	if in.Name == "" || len([]rune(in.Name)) > 200 {
		f.Add("name", "Le nom de l’associé est obligatoire (200 caractères au plus).")
	}
	if in.Link != "" && in.Link != "self" {
		f.Add("link", "Lien attendu : self, ou un grant_id.")
	}
	return f.Err()
}

// CreateAssociate adds an associate to the register (POST /associates).
func (s Service) CreateAssociate(ctx context.Context, p *auth.Principal, requestID string, in AssociateInput) (AssociateView, error) {
	sc, err := scope(p, requestID)
	if err != nil {
		return AssociateView{}, err
	}
	if err := validAssociate(&in); err != nil {
		return AssociateView{}, err
	}
	var out AssociateView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		sub, err := s.link(ctx, p, sc.OwnerID, in)
		if err != nil {
			return err
		}
		now := s.now()
		a := associateRow{ID: s.NewID(), OwnerID: sc.OwnerID, Name: in.Name, PersonSub: sub, Version: 1,
			CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub}
		if err := s.Repo.InsertAssociate(ctx, a); err != nil {
			if db.IsUniqueViolation(err) {
				return invariant.New(TypeAssociateLinked, "")
			}
			return err
		}
		out = associateView(a, nil, s.today(), p.Sub)
		return audit.Record(ctx, "sci.associate.create", audit.Entity{Type: "sci_associate", ID: a.ID.String()}, nil, out)
	})
	return out, err
}

// UpdateAssociate changes an associate's name or link (PUT
// /associates/{id}, If-Match). Their holdings change only by movements.
func (s Service) UpdateAssociate(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in AssociateInput) (AssociateView, error) {
	sc, err := scope(p, requestID)
	if err != nil {
		return AssociateView{}, err
	}
	if err := validAssociate(&in); err != nil {
		return AssociateView{}, err
	}
	var out AssociateView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		cur, err := s.Repo.Associate(ctx, id)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return invariant.ErrStale
		}
		sub, err := s.link(ctx, p, sc.OwnerID, in)
		if err != nil {
			return err
		}
		before := associateView(cur, nil, s.today(), p.Sub)
		cur.Name, cur.PersonSub, cur.UpdatedAt, cur.UpdatedBy = in.Name, sub, s.now(), p.Sub
		if err := s.Repo.UpdateAssociate(ctx, cur, version); err != nil {
			if db.IsUniqueViolation(err) {
				return invariant.New(TypeAssociateLinked, "")
			}
			return err
		}
		cur.Version = version + 1
		out = associateView(cur, nil, s.today(), p.Sub)
		return audit.Record(ctx, "sci.associate.update", audit.Entity{Type: "sci_associate", ID: id.String()}, before, out)
	})
	return out, err
}

// MovementInput is the body of POST /sci/movements. A subscription gives new
// parts, within the capital, to an associate; a transfer, a gift or a
// succession moves parts of an open holding to another associate on its day:
// the holding is closed that day, the receiver's holding opens that day, and
// what the giver keeps opens again that day.
type MovementInput struct {
	OnDate        period.Date `json:"on_date" swaggertype:"string" format:"date"`
	Reason        string      `json:"reason" enums:"subscription,transfer,gift,succession"`
	Parts         int         `json:"parts"`
	FromHoldingID *uuid.UUID  `json:"from_holding_id"`
	ToAssociateID uuid.UUID   `json:"to_associate_id"`
	Note          string      `json:"note"`
}

// MovementView is a recorded movement and the holdings it opened and closed.
type MovementView struct {
	ID uuid.UUID `json:"id"`
	MovementInput
	Opened    []HoldingView `json:"opened"`
	Closed    []uuid.UUID   `json:"closed"`
	CreatedAt time.Time     `json:"created_at"`
}

func movementView(m movementRow) MovementView {
	return MovementView{ID: m.ID, CreatedAt: m.CreatedAt, Opened: []HoldingView{}, Closed: []uuid.UUID{},
		MovementInput: MovementInput{OnDate: period.FromTime(m.OnDate), Reason: m.Reason, Parts: m.Parts,
			FromHoldingID: m.FromHoldingID, ToAssociateID: m.ToAssociateID, Note: m.Note}}
}

// Movements lists the SCI's movements, oldest first (GET /sci/movements).
func (s Service) Movements(ctx context.Context, p *auth.Principal, requestID string) ([]MovementView, error) {
	sc, err := scope(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []MovementView{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		ms, err := s.Repo.Movements(ctx)
		if err != nil {
			return err
		}
		hs, err := s.Repo.Holdings(ctx)
		if err != nil {
			return err
		}
		for _, m := range ms {
			v := movementView(m)
			for _, h := range hs {
				if h.OpenedBy == m.ID {
					v.Opened = append(v.Opened, HoldingView{ID: h.ID, Parts: h.Parts, FromOn: period.FromTime(h.FromOn)})
				}
				if h.ClosedBy != nil && *h.ClosedBy == m.ID {
					v.Closed = append(v.Closed, h.ID)
				}
			}
			out = append(out, v)
		}
		return nil
	})
	return out, err
}

func validMovement(in *MovementInput) error {
	in.Note = strings.TrimSpace(in.Note)
	var f invariant.Fields
	if in.OnDate.IsZero() {
		f.Add("on_date", "La date du mouvement est obligatoire.")
	}
	switch in.Reason {
	case ReasonSubscription:
		if in.FromHoldingID != nil {
			f.Add("from_holding_id", "Une souscription crée des parts : elle ne part d’aucune détention.")
		}
	case ReasonTransfer, ReasonGift, ReasonSuccession:
		if in.FromHoldingID == nil {
			f.Add("from_holding_id", "La détention dont les parts sont cédées est obligatoire.")
		}
	default:
		f.Add("reason", "Motif attendu : subscription, transfer, gift ou succession.")
	}
	if in.Parts <= 0 {
		f.Add("parts", "Le nombre de parts est un entier positif.")
	}
	if in.ToAssociateID == uuid.Nil {
		f.Add("to_associate_id", "L’associé qui reçoit les parts est obligatoire.")
	}
	if len([]rune(in.Note)) > 500 {
		f.Add("note", "500 caractères au plus.")
	}
	return f.Err()
}

// RecordMovement records a movement of parts (POST /sci/movements) and the
// holdings it opens and closes, in one audited write. A holding is never
// edited afterwards; a movement is not undone (Q-133).
func (s Service) RecordMovement(ctx context.Context, p *auth.Principal, requestID string, in MovementInput) (MovementView, error) {
	sc, err := scope(p, requestID)
	if err != nil {
		return MovementView{}, err
	}
	if err := validMovement(&in); err != nil {
		return MovementView{}, err
	}
	var out MovementView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if err := s.Repo.LockRegister(ctx, sc.OwnerID); err != nil {
			return err
		}
		if _, err := s.Repo.Associate(ctx, in.ToAssociateID); err != nil {
			if errors.Is(err, invariant.ErrNotFound) {
				var f invariant.Fields
				f.Add("to_associate_id", "Associé inconnu dans cette SCI.")
				return f.Err()
			}
			return err
		}
		now := s.now()
		m := movementRow{ID: s.NewID(), OwnerID: sc.OwnerID, OnDate: in.OnDate.Time(), Reason: in.Reason, Parts: in.Parts,
			FromHoldingID: in.FromHoldingID, ToAssociateID: in.ToAssociateID, Note: in.Note, CreatedAt: now, CreatedBy: p.Sub}
		open := func(associate uuid.UUID, parts int) holdingRow {
			return holdingRow{ID: s.NewID(), OwnerID: sc.OwnerID, AssociateID: associate, Parts: parts, FromOn: in.OnDate.Time(),
				OpenedBy: m.ID, CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub}
		}
		var opened []holdingRow
		var closed *holdingRow
		if in.Reason == ReasonSubscription {
			capital, _, err := s.Repo.Capital(ctx, sc.OwnerID)
			if err != nil {
				return err
			}
			rows, err := s.Repo.Holdings(ctx)
			if err != nil {
				return err
			}
			hs := make([]Holding, len(rows))
			for i, h := range rows {
				hs[i] = holdingOf(h)
			}
			if MaxHeldFrom(hs, in.OnDate)+in.Parts > capital {
				return invariant.New(TypeCapitalExceeded, "")
			}
			opened = []holdingRow{open(in.ToAssociateID, in.Parts)}
		} else {
			h, err := s.Repo.Holding(ctx, *in.FromHoldingID)
			if err != nil {
				if errors.Is(err, invariant.ErrNotFound) {
					var f invariant.Fields
					f.Add("from_holding_id", "Détention inconnue dans cette SCI.")
					return f.Err()
				}
				return err
			}
			hv := holdingOf(h)
			// The day of the movement is the holding's first day not held: it
			// must fall after its first day and while it is open.
			if hv.Closed != nil || !hv.From.Before(in.OnDate) {
				return invariant.New(TypeHoldingClosed, h.ID.String())
			}
			if in.Parts > h.Parts {
				return invariant.New(TypeHoldingParts, "")
			}
			if h.AssociateID == in.ToAssociateID {
				return invariant.New(TypeSameAssociate, "")
			}
			day := in.OnDate.Time()
			h.ClosedOn, h.ClosedBy, h.UpdatedAt, h.UpdatedBy = &day, &m.ID, now, p.Sub
			closed = &h
			opened = []holdingRow{open(in.ToAssociateID, in.Parts)}
			if rest := h.Parts - in.Parts; rest > 0 {
				opened = append(opened, open(h.AssociateID, rest))
			}
		}
		if err := s.Repo.InsertMovement(ctx, m); err != nil {
			return err
		}
		out = movementView(m)
		if closed != nil {
			if err := s.Repo.CloseHolding(ctx, *closed); err != nil {
				return err
			}
			out.Closed = append(out.Closed, closed.ID)
		}
		for _, h := range opened {
			if err := s.Repo.InsertHolding(ctx, h); err != nil {
				return err
			}
			out.Opened = append(out.Opened, HoldingView{ID: h.ID, Parts: h.Parts, FromOn: in.OnDate})
		}
		return audit.Record(ctx, "sci.movement", audit.Entity{Type: "sci_movement", ID: m.ID.String()}, nil, out)
	})
	return out, err
}

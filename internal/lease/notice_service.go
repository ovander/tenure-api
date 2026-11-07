package lease

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/period"
)

// NoticeTemplate is the version of the owner's notice letter (templates/notice).
const NoticeTemplate = "v1"

// DocOwnerNotice is the lease document kind of the owner's notice letter.
const DocOwnerNotice = "owner_notice"

// NoticeLetterInput is the body of POST /leases/{id}/notice/letter: the
// owner's ground, and dry_run to preview the term it aims at.
type NoticeLetterInput struct {
	NoticeGround
	DryRun bool `json:"dry_run"`
}

// NoticeLetter is the owner's letter prepared, or previewed.
type NoticeLetter struct {
	Terms  NoticeTerms   `json:"terms"`
	Letter *DocumentView `json:"letter,omitempty"`
}

// NoticeInput is the body of POST /leases/{id}/notice: a notice received on
// a date. The tenant's names its reduced-notice ground, if any; the owner's
// is the letter generated for the lease (document_id), whose ground it
// carries.
type NoticeInput struct {
	GivenBy    string       `json:"given_by" enums:"tenant,owner"`
	Reason     string       `json:"reason,omitempty"`
	ReceivedOn period.Date  `json:"received_on" swaggertype:"string" format:"date"`
	Delivery   string       `json:"delivery" enums:"registered_letter,bailiff,hand_delivery"`
	SentOn     *period.Date `json:"sent_on,omitempty" swaggertype:"string" format:"date"`
	// TrackingRef is the registered letter's or the bailiff's reference.
	TrackingRef string `json:"tracking_ref,omitempty"`
	// DocumentID is the owner's letter, or the tenant's letter uploaded.
	DocumentID *uuid.UUID `json:"document_id,omitempty"`
	DryRun     bool       `json:"dry_run"`
}

// NoticeView is a recorded notice.
type NoticeView struct {
	ID          uuid.UUID    `json:"id"`
	LeaseID     uuid.UUID    `json:"lease_id"`
	GivenBy     string       `json:"given_by" enums:"tenant,owner"`
	Ground      NoticeGround `json:"ground"`
	ReceivedOn  period.Date  `json:"received_on" swaggertype:"string" format:"date"`
	Months      int          `json:"months"`
	EndOn       period.Date  `json:"end_on" swaggertype:"string" format:"date"`
	Delivery    string       `json:"delivery" enums:"registered_letter,bailiff,hand_delivery"`
	SentOn      *period.Date `json:"sent_on,omitempty" swaggertype:"string" format:"date"`
	TrackingRef string       `json:"tracking_ref,omitempty"`
	DocumentID  *uuid.UUID   `json:"document_id,omitempty"`
}

// NoticeResult is a notice recorded, or previewed: the terms, and once
// recorded the notice and the lease under notice.
type NoticeResult struct {
	Terms  NoticeTerms `json:"terms"`
	Notice *NoticeView `json:"notice,omitempty"`
	Lease  *View       `json:"lease,omitempty"`
}

func noticeView(r noticeRow) NoticeView {
	v := NoticeView{ID: r.ID, LeaseID: r.LeaseID, GivenBy: r.GivenBy, ReceivedOn: period.FromTime(r.ReceivedOn), Months: r.Months,
		EndOn: period.FromTime(r.EndOn), Delivery: r.Delivery, TrackingRef: r.TrackingRef, DocumentID: r.DocumentID}
	_ = json.Unmarshal(r.Ground, &v.Ground)
	v.Ground.Reason = r.Reason
	if r.SentOn != nil {
		d := period.FromTime(*r.SentOn)
		v.SentOn = &d
	}
	return v
}

func noticeFacts(l Lease) NoticeFacts {
	return NoticeFacts{Kind: l.Kind, StartOn: l.StartOn, TermMonths: l.TermMonths, TenseZone: l.TenseZone()}
}

// Notice returns a lease's recorded notice (RENT-SPEC-010). The accountant
// is refused: a notice names the beneficiary of a repossession.
func (s Service) Notice(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (NoticeView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return NoticeView{}, err
	}
	if p.Active.Role == auth.RoleAccountant {
		return NoticeView{}, invariant.ErrForbidden
	}
	var out NoticeView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if _, err := s.Repo.Get(ctx, id, false); err != nil {
			return err
		}
		r, ok, err := s.Repo.Notice(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return invariant.ErrNotFound
		}
		out = noticeView(r)
		return nil
	})
	return out, err
}

// noticeLetterData is what templates/notice renders.
type noticeLetterData struct {
	Number          string
	IssuedOn        period.Date
	Owner           document.Party
	Tenants         []string
	Premises        []string
	ContractNumber  string
	StartOn         period.Date
	EndOn           period.Date
	LatestReceiptOn period.Date
	Months          int
	Furnished       bool
	Ground          NoticeGround
	Relationship    string
}

var relationshipFR = map[string]string{
	"owner": "le bailleur lui-même", "spouse": "son conjoint", "pacs_partner": "son partenaire de PACS",
	"concubin": "son concubin notoire depuis au moins un an", "ascendant": "un ascendant du bailleur",
	"descendant": "un descendant du bailleur", "spouse_ascendant": "un ascendant de son conjoint, partenaire ou concubin",
	"spouse_descendant": "un descendant de son conjoint, partenaire ou concubin",
}

// NoticeLetter prepares the owner's notice letter (CG-<asset>-<YYYY>-<seq>)
// of an active lease on one of the legal grounds, for the end of the next
// term the notice can still reach (RENT-SPEC-010; loi 89-462 art. 15). The
// letter states the ground and what it requires, and the last day the
// tenant must receive it; a sale or repossession refers to the notice of
// information of the arrêté of 13 December 2017, to annex. A dry run only
// computes the term.
func (s Service) NoticeLetter(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, in NoticeLetterInput) (NoticeLetter, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return NoticeLetter{}, err
	}
	var out NoticeLetter
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		f, err := s.facts(ctx, sc, id, true)
		if err != nil {
			return err
		}
		if State(f.st.lease.State) != StateActive {
			return invariant.New(TypeInvalidTransition, "notice from "+f.st.lease.State)
		}
		r, err := f.ps.Notice(f.today)
		if err != nil {
			return err
		}
		in.Conditions, in.Grounds = strings.TrimSpace(in.Conditions), strings.TrimSpace(in.Grounds)
		g := in.NoticeGround
		terms, err := OwnerNotice(noticeFacts(f.lease), g, period.Date{}, f.today, r)
		if err != nil {
			return err
		}
		out.Terms = terms
		if in.DryRun {
			return nil
		}
		contract := ""
		if ct, ok, err := s.Repo.LatestLeaseDocument(ctx, id, DocContract); err != nil {
			return err
		} else if ok {
			contract = ct.Number
		}
		data := noticeLetterData{Owner: document.Party{Name: f.owner.LegalName, Address: f.owner.Address},
			Tenants: tenantNames(f.tenants), Premises: assetLabels(f.assets), ContractNumber: contract,
			StartOn: f.lease.StartOn, EndOn: terms.EndOn, LatestReceiptOn: *terms.LatestReceiptOn, Months: terms.Months,
			Furnished: f.lease.Kind == KindFurnished, Ground: g}
		if g.Beneficiary != nil {
			data.Relationship = relationshipFR[g.Beneficiary.Relationship]
		}
		snap := map[string]any{"template": "notice/" + NoticeTemplate, "lease": f.view, "ground": g, "terms": terms}
		lid := id
		iss, err := s.Issuer.Issue(ctx, document.Request{Class: document.ClassNotice, TemplateVersion: NoticeTemplate,
			AssetID: principalID(f.lease), LeaseID: &lid, Snapshot: snap,
			Data: func(number string, issued period.Date) any {
				data.Number, data.IssuedOn = number, issued
				return data
			}}, f.ps)
		if err != nil {
			return err
		}
		doc, err := s.recordLeaseDocument(ctx, sc, id, DocOwnerNotice, f.st.lease.Version, nil, iss, snap, p.Sub)
		if err != nil {
			return err
		}
		out.Letter = &doc
		return audit.Record(ctx, "lease.notice_letter", audit.Entity{Type: "lease", ID: id.String()}, nil, out)
	})
	return out, err
}

// letterSnapshot is the part of an owner letter's snapshot a notice reads.
type letterSnapshot struct {
	Ground NoticeGround `json:"ground"`
}

// GiveNotice records a notice received on a date and puts the lease under
// notice with the end tenure computes (RENT-SPEC-010, Q-118): the tenant's
// period by lease kind and ground, or the owner's notice for the end of the
// current term, on the letter generated for the lease, received at the
// latest its lead time before that term. One notice per lease, immutable.
// A dry run only computes the terms.
func (s Service) GiveNotice(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, in NoticeInput) (NoticeResult, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return NoticeResult{}, err
	}
	var f invariant.Fields
	if in.GivenBy != GivenByTenant && in.GivenBy != GivenByOwner {
		f.Add("given_by", "Attendu : tenant ou owner.")
	}
	if in.ReceivedOn.IsZero() {
		f.Add("received_on", "La date de réception est obligatoire.")
	}
	switch in.Delivery {
	case DeliveryRegistered, DeliveryBailiff, DeliveryHand:
	default:
		f.Add("delivery", "Attendu : registered_letter, bailiff ou hand_delivery.")
	}
	if in.SentOn != nil && in.SentOn.After(in.ReceivedOn) {
		f.Add("sent_on", "L’envoi ne peut pas suivre la réception.")
	}
	in.TrackingRef = strings.TrimSpace(in.TrackingRef)
	if len(in.TrackingRef) > 100 {
		f.Add("tracking_ref", "Au plus 100 caractères.")
	}
	if err := f.Err(); err != nil {
		return NoticeResult{}, err
	}
	var out NoticeResult
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		fc, err := s.facts(ctx, sc, id, true)
		if err != nil {
			return err
		}
		if _, err := Next(State(fc.st.lease.State), ActionNotice); err != nil {
			return err
		}
		if _, exists, err := s.Repo.Notice(ctx, id); err != nil {
			return err
		} else if exists {
			return invariant.New(TypeNoticeExists, id.String())
		}
		if in.ReceivedOn.After(fc.today) {
			return invariant.New(TypeNoticeInvalid, "received_on after today")
		}
		r, err := fc.ps.Notice(in.ReceivedOn)
		if err != nil {
			return err
		}
		var g NoticeGround
		var terms NoticeTerms
		if in.GivenBy == GivenByOwner {
			if in.DocumentID == nil {
				return invariant.New(TypeNoticeLetterMissing, "document_id")
			}
			ld, ok, err := s.Repo.LeaseDocumentOf(ctx, id, *in.DocumentID, DocOwnerNotice)
			if err != nil {
				return err
			}
			if !ok {
				return invariant.New(TypeNoticeLetterMissing, in.DocumentID.String())
			}
			var snap letterSnapshot
			if err := json.Unmarshal(ld.InputSnapshot, &snap); err != nil {
				return err
			}
			g = snap.Ground
			terms, err = OwnerNotice(noticeFacts(fc.lease), g, in.ReceivedOn, fc.today, r)
			if err != nil {
				return err
			}
		} else {
			g = NoticeGround{Reason: strings.TrimSpace(in.Reason)}
			if in.DocumentID != nil {
				ok, err := s.Repo.UploadOnLease(ctx, id, *in.DocumentID)
				if err != nil {
					return err
				}
				if !ok {
					return invariant.New(TypeNoticeInvalid, "document_id")
				}
			}
			terms, err = TenantNotice(noticeFacts(fc.lease), g, in.ReceivedOn, r)
			if err != nil {
				return err
			}
		}
		out.Terms = terms
		if in.DryRun {
			return nil
		}
		reason := g.Reason
		g.Reason = ""
		ground, err := json.Marshal(g)
		if err != nil {
			return err
		}
		g.Reason = reason
		now := s.now()
		row := noticeRow{ID: s.NewID(), OwnerID: sc.OwnerID, LeaseID: id, GivenBy: in.GivenBy, Reason: reason,
			ReceivedOn: in.ReceivedOn.Time(), Months: terms.Months, EndOn: terms.EndOn.Time(), Delivery: in.Delivery,
			TrackingRef: in.TrackingRef, Ground: ground, DocumentID: in.DocumentID, CreatedAt: now, CreatedBy: p.Sub}
		if in.SentOn != nil {
			t := in.SentOn.Time()
			row.SentOn = &t
		}
		if err := s.Repo.InsertNotice(ctx, row); err != nil {
			return err
		}
		before := fc.view
		next := fc.st
		end := terms.EndOn.Time()
		next.lease.EndOn = &end
		next.lease.State, next.lease.Version = string(StateNotice), fc.st.lease.Version+1
		next.lease.UpdatedAt, next.lease.UpdatedBy = now, p.Sub
		if err := s.Repo.SetState(ctx, next.lease); err != nil {
			return err
		}
		lv, nv := view(next), noticeView(row)
		out.Lease, out.Notice = &lv, &nv
		return audit.Record(ctx, "lease.notice", audit.Entity{Type: "lease", ID: id.String()}, before, out)
	})
	return out, err
}

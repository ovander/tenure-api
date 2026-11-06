package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of the completion routes (RENT-INV-001 §7, Q-114).
const (
	TypeCompletionUnavailable  = "inventory/completion-unavailable"
	TypeCompletionWindowClosed = "inventory/completion-window-closed"
	TypeCompletionDecided      = "inventory/completion-decided"
	TypeCompletionDocument     = "inventory/completion-document-invalid"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeCompletionUnavailable:  problem.M(409, "Un complément porte sur un état des lieux d’entrée émis et saisi dans Tenure.", "A completion is for an issued entry inventory captured in Tenure."),
		TypeCompletionWindowClosed: problem.M(409, "La demande de complément est hors du délai prévu par la loi.", "The completion request is outside the period the law allows."),
		TypeCompletionDecided:      problem.M(409, "Cette demande de complément est déjà tranchée.", "This completion request is already decided."),
		TypeCompletionDocument:     problem.M(422, "Le courrier du locataire doit être un document déposé, de classe inventory, de ce bail.", "The tenant’s letter must be an uploaded document of class inventory for this lease."),
	})
}

// CompletionTemplate is the version of templates/inventory_completion the EC
// document renders with; a template change adds a version (tenure-api#84).
const CompletionTemplate = "v1"

// completionTemplate is the template name of the EC document, numbered in
// class inventory.
const completionTemplate document.Class = "inventory_completion"

// Decisions of a completion request (completion_decision).
const (
	DecisionPending  = "pending"
	DecisionAccepted = "accepted"
	DecisionRefused  = "refused"
	DecisionPartial  = "partial"
)

// CompletionLineInput is a line of a completion request: an entry item and
// the state the tenant asks to record (a condition, whether it works, or a
// comment).
type CompletionLineInput struct {
	EntryItemID uuid.UUID `json:"entry_item_id"`
	Condition   Condition `json:"condition,omitempty" enums:"new,good,fair,poor,broken"`
	Works       Works     `json:"works,omitempty" enums:"ok,ko,n/a"`
	Comment     string    `json:"comment,omitempty"`
}

// CompletionInput is the body of POST /inventories/{id}/completions: the
// tenant's request as received, with their letter when uploaded.
type CompletionInput struct {
	Reason            string                `json:"reason" enums:"ten_day,heating"`
	RequestedOn       *period.Date          `json:"requested_on" swaggertype:"string" format:"date"`
	RequestDocumentID *uuid.UUID            `json:"request_document_id,omitempty"`
	Lines             []CompletionLineInput `json:"lines"`
}

// DecisionLineInput accepts or refuses one line of a request.
type DecisionLineInput struct {
	EntryItemID uuid.UUID `json:"entry_item_id"`
	Accepted    bool      `json:"accepted"`
}

// DecisionInput is the body of PUT /completions/{id}: every line decided,
// on decided_on (today when omitted).
type DecisionInput struct {
	DecidedOn *period.Date        `json:"decided_on,omitempty" swaggertype:"string" format:"date"`
	Lines     []DecisionLineInput `json:"lines"`
}

// CompletionLineView is a line with its entry item as issued.
type CompletionLineView struct {
	EntryItemID uuid.UUID `json:"entry_item_id"`
	Room        string    `json:"room"`
	Label       string    `json:"label"`
	Category    Category  `json:"category"`
	Entry       State     `json:"entry"`
	Requested   State     `json:"requested"`
	// Accepted is set once the request is decided.
	Accepted *bool `json:"accepted,omitempty"`
}

// CompletionView is a completion request and, once decided, its EC document.
type CompletionView struct {
	ID                uuid.UUID            `json:"id"`
	InventoryID       uuid.UUID            `json:"inventory_id"`
	Reason            string               `json:"reason" enums:"ten_day,heating"`
	RequestedOn       period.Date          `json:"requested_on" swaggertype:"string" format:"date"`
	RequestDocumentID *uuid.UUID           `json:"request_document_id,omitempty"`
	Decision          string               `json:"decision" enums:"pending,accepted,refused,partial"`
	DecidedOn         *period.Date         `json:"decided_on,omitempty" swaggertype:"string" format:"date"`
	DocumentID        *uuid.UUID           `json:"document_id,omitempty"`
	Lines             []CompletionLineView `json:"lines"`
	Version           int                  `json:"version"`
}

// CompletionsView is where an entry stands on its completions: the windows
// (once the entry is issued with its date), and every request.
type CompletionsView struct {
	InventoryID uuid.UUID        `json:"inventory_id"`
	DoneOn      *period.Date     `json:"done_on,omitempty" swaggertype:"string" format:"date"`
	Windows     *Windows         `json:"windows,omitempty"`
	Open        []string         `json:"open" enums:"ten_day,heating"`
	Completions []CompletionView `json:"completions"`
}

// completionEntry is an entry loaded for its completions.
type completionEntry struct {
	head  captureRow
	items map[uuid.UUID]itemRow
	order []itemRow
}

func (s Service) completionEntry(ctx context.Context, id uuid.UUID, lock bool) (completionEntry, error) {
	h, ok, err := s.Repo.Capture(ctx, id, lock)
	if err != nil {
		return completionEntry{}, err
	}
	if !ok {
		return completionEntry{}, invariant.ErrNotFound
	}
	if h.Kind != "entry" {
		return completionEntry{}, invariant.New(TypeCompletionUnavailable, h.Kind)
	}
	e := completionEntry{head: h, items: map[uuid.UUID]itemRow{}}
	if e.order, err = s.Repo.Items(ctx, id); err != nil {
		return completionEntry{}, err
	}
	for _, it := range e.order {
		e.items[it.ID] = it
	}
	return e, nil
}

// windows returns the entry's windows, nil before it has a date.
func (s Service) windows(ctx context.Context, h captureRow) (*Windows, error) {
	if h.DoneOn == nil {
		return nil, nil
	}
	ps, err := s.Lease.Params.Load(ctx)
	if err != nil {
		return nil, err
	}
	on := period.FromTime(*h.DoneOn)
	r, err := WindowRulesAt(ps, on)
	if err != nil {
		return nil, err
	}
	w := CompletionWindows(on, r)
	return &w, nil
}

func stateOf(it itemRow) State {
	st := State{Quantity: it.Quantity, Comment: it.Comment}
	if it.Condition != nil {
		st.Condition = Condition(*it.Condition)
	}
	if it.Works != nil {
		st.Works = Works(*it.Works)
	}
	return st
}

func (e completionEntry) view(r completionRow) (CompletionView, error) {
	var lines []Completion
	if err := json.Unmarshal(r.Lines, &lines); err != nil {
		return CompletionView{}, fmt.Errorf("inventory: completion %s: %w", r.ID, err)
	}
	v := CompletionView{ID: r.ID, InventoryID: r.InventoryID, Reason: r.Reason, RequestedOn: period.FromTime(r.RequestedOn),
		RequestDocumentID: r.RequestDocumentID, Decision: r.Decision, DecidedOn: dateOf(r.DecidedOn), DocumentID: r.DocumentID,
		Lines: []CompletionLineView{}, Version: r.Version}
	for _, l := range lines {
		id, _ := uuid.Parse(l.EntryItemID)
		it := e.items[id]
		lv := CompletionLineView{EntryItemID: id, Room: it.RoomLabel, Label: it.Label, Category: Category(it.Category), Entry: stateOf(it),
			Requested: State{Condition: l.Condition, Works: l.Works, Comment: l.Comment}}
		if r.Decision != DecisionPending {
			a := l.Accepted
			lv.Accepted = &a
		}
		v.Lines = append(v.Lines, lv)
	}
	return v, nil
}

func (s Service) completionsView(ctx context.Context, e completionEntry) (CompletionsView, error) {
	v := CompletionsView{InventoryID: e.head.ID, DoneOn: dateOf(e.head.DoneOn), Open: []string{}, Completions: []CompletionView{}}
	if e.head.State == StateIssued && e.head.Mode != "upload" {
		w, err := s.windows(ctx, e.head)
		if err != nil {
			return CompletionsView{}, err
		}
		v.Windows = w
		if w != nil {
			for _, r := range []string{ReasonTenDay, ReasonHeating} {
				if w.Open(r, *v.DoneOn, s.today()) {
					v.Open = append(v.Open, r)
				}
			}
		}
	}
	rows, err := s.Repo.Completions(ctx, e.head.ID)
	if err != nil {
		return CompletionsView{}, err
	}
	for _, r := range rows {
		cv, err := e.view(r)
		if err != nil {
			return CompletionsView{}, err
		}
		v.Completions = append(v.Completions, cv)
	}
	return v, nil
}

// Completions returns an entry's completion windows and requests. The
// co-owner reads them; the accountant does not.
func (s Service) Completions(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (CompletionsView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return CompletionsView{}, err
	}
	var out CompletionsView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		e, err := s.completionEntry(ctx, id, false)
		if err != nil {
			return err
		}
		out, err = s.completionsView(ctx, e)
		return err
	})
	return out, err
}

// validCompletion checks a request's fields that need no entry.
func (s Service) validCompletion(in CompletionInput) error {
	var f invariant.Fields
	if in.Reason != ReasonTenDay && in.Reason != ReasonHeating {
		f.Add("reason", "Motif attendu : ten_day ou heating.")
	}
	if in.RequestedOn == nil {
		f.Add("requested_on", "La date de la demande est obligatoire.")
	} else if s.today().Before(*in.RequestedOn) {
		f.Add("requested_on", "La date de la demande est dans le futur.")
	}
	if len(in.Lines) == 0 {
		f.Add("lines", "Au moins un élément à compléter.")
	}
	for i, l := range in.Lines {
		pre := fmt.Sprintf("lines[%d].", i)
		validItem(&f, pre, ItemInput{Condition: l.Condition, Works: l.Works, Comment: l.Comment})
		if l.Condition == "" && l.Works == "" && strings.TrimSpace(l.Comment) == "" {
			f.Add(pre+"comment", "Indiquez l’état, le fonctionnement ou une observation à ajouter.")
		}
	}
	return f.Err()
}

// RequestCompletion records a tenant's request to complete an issued entry
// (loi 89-462 art. 3-2, RENT-INV-001 §5 step 5): within the ten days after
// the entry, or within the first month of the heating season for the
// heating items. Each line names an item of the entry, once. The request
// waits for the owner's decision.
func (s Service) RequestCompletion(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, in CompletionInput) (CompletionView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return CompletionView{}, err
	}
	if err := s.validCompletion(in); err != nil {
		return CompletionView{}, err
	}
	var out CompletionView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		e, err := s.completionEntry(ctx, id, true)
		if err != nil {
			return err
		}
		if e.head.State != StateIssued || e.head.Mode == "upload" || e.head.DoneOn == nil {
			return invariant.New(TypeCompletionUnavailable, e.head.State)
		}
		w, err := s.windows(ctx, e.head)
		if err != nil {
			return err
		}
		if !w.Open(in.Reason, period.FromTime(*e.head.DoneOn), *in.RequestedOn) {
			return invariant.New(TypeCompletionWindowClosed, in.Reason+" "+in.RequestedOn.String())
		}
		var f invariant.Fields
		seen := map[uuid.UUID]bool{}
		lines := make([]Completion, 0, len(in.Lines))
		for i, l := range in.Lines {
			pre := fmt.Sprintf("lines[%d].", i)
			it, ok := e.items[l.EntryItemID]
			switch {
			case !ok || seen[l.EntryItemID]:
				f.Add(pre+"entry_item_id", "Un élément de cet état des lieux, une seule fois.")
			case in.Reason == ReasonHeating && Category(it.Category) != Heating:
				f.Add(pre+"entry_item_id", "Une demande pendant la période de chauffe porte sur les éléments de chauffage.")
			}
			seen[l.EntryItemID] = true
			lines = append(lines, Completion{EntryItemID: l.EntryItemID.String(), Condition: l.Condition, Works: l.Works, Comment: strings.TrimSpace(l.Comment)})
		}
		if err := f.Err(); err != nil {
			return err
		}
		if in.RequestDocumentID != nil {
			d, ok, err := s.Repo.DocumentInfo(ctx, *in.RequestDocumentID)
			if err != nil {
				return err
			}
			if !ok || d.Class != string(document.ClassInventory) || d.Source != document.SourceUpload || d.LeaseID == nil || *d.LeaseID != e.head.LeaseID {
				return invariant.New(TypeCompletionDocument, in.RequestDocumentID.String())
			}
		}
		raw, err := json.Marshal(lines)
		if err != nil {
			return err
		}
		now := s.now()
		r := completionRow{ID: s.NewID(), OwnerID: sc.OwnerID, InventoryID: id, Reason: in.Reason, RequestedOn: in.RequestedOn.Time(),
			RequestDocumentID: in.RequestDocumentID, Lines: raw, Decision: DecisionPending, Version: 1,
			CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub}
		if err := s.Repo.InsertCompletion(ctx, r); err != nil {
			return err
		}
		if out, err = e.view(r); err != nil {
			return err
		}
		return audit.Record(ctx, "inventory.completion.create", audit.Entity{Type: "inventory_completion", ID: r.ID.String()}, nil,
			map[string]any{"inventory_id": id, "reason": r.Reason, "requested_on": in.RequestedOn.String(), "lines": lines,
				"request_document_id": r.RequestDocumentID})
	})
	return out, err
}

// decision is accepted when every line is, refused when none is, partial
// otherwise.
func decision(lines []Completion) string {
	n := 0
	for _, l := range lines {
		if l.Accepted {
			n++
		}
	}
	switch n {
	case len(lines):
		return DecisionAccepted
	case 0:
		return DecisionRefused
	}
	return DecisionPartial
}

// DecideCompletion records the owner's decision on every line of a pending
// request (If-Match on its version) and issues the EC document in the same
// transaction: the issued entry is never edited, the accepted lines complete
// it in the exit pre-fill and the comparison. A decided request no longer
// changes.
func (s Service) DecideCompletion(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in DecisionInput) (CompletionView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return CompletionView{}, err
	}
	if in.DecidedOn != nil && s.today().Before(*in.DecidedOn) {
		return CompletionView{}, invariant.Fields{{Field: "decided_on", Message: "La date de la décision est dans le futur."}}
	}
	var out CompletionView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		r, ok, err := s.Repo.Completion(ctx, id, true)
		if err != nil {
			return err
		}
		if !ok {
			return invariant.ErrNotFound
		}
		e, err := s.completionEntry(ctx, r.InventoryID, false)
		if err != nil {
			return err
		}
		if r.Decision != DecisionPending {
			return invariant.New(TypeCompletionDecided, r.Decision)
		}
		if r.Version != version {
			return invariant.ErrStale
		}
		before, err := e.view(r)
		if err != nil {
			return err
		}
		var lines []Completion
		if err := json.Unmarshal(r.Lines, &lines); err != nil {
			return err
		}
		decided := map[string]bool{}
		var f invariant.Fields
		for i, l := range in.Lines {
			k := l.EntryItemID.String()
			if _, dup := decided[k]; dup || !hasLine(lines, k) {
				f.Add(fmt.Sprintf("lines[%d].entry_item_id", i), "Une ligne de cette demande, une seule fois.")
			}
			decided[k] = l.Accepted
		}
		if len(in.Lines) != len(lines) {
			f.Add("lines", "Chaque ligne de la demande est acceptée ou refusée.")
		}
		on := s.today()
		if in.DecidedOn != nil {
			on = *in.DecidedOn
		}
		if on.Before(period.FromTime(r.RequestedOn)) {
			f.Add("decided_on", "La décision ne peut pas précéder la demande.")
		}
		if err := f.Err(); err != nil {
			return err
		}
		for i := range lines {
			lines[i].Accepted = decided[lines[i].EntryItemID]
		}
		raw, err := json.Marshal(lines)
		if err != nil {
			return err
		}
		t := on.Time()
		r.Lines, r.Decision, r.DecidedOn = raw, decision(lines), &t
		docID, err := s.issueCompletion(ctx, sc, e, r)
		if err != nil {
			return err
		}
		r.DocumentID = &docID
		r.UpdatedAt, r.UpdatedBy = s.now(), p.Sub
		if err := s.Repo.DecideCompletion(ctx, r, version); err != nil {
			return err
		}
		r.Version = version + 1
		if out, err = e.view(r); err != nil {
			return err
		}
		return audit.Record(ctx, "inventory.completion.decide", audit.Entity{Type: "inventory_completion", ID: r.ID.String()}, before,
			map[string]any{"decision": r.Decision, "decided_on": on.String(), "lines": lines, "document_id": docID, "version": r.Version})
	})
	return out, err
}

func hasLine(lines []Completion, id string) bool {
	for _, l := range lines {
		if l.EntryItemID == id {
			return true
		}
	}
	return false
}

// CompletionLine is a line of the EC document.
type CompletionLine struct {
	Room, Label      string
	Entry, Requested string
	Accepted         bool
	EntryComment     string
	RequestedComment string
}

// CompletionData is the data of templates/inventory_completion/v1: the
// entry it completes, the request (reason, date) and the decision per line.
type CompletionData struct {
	Number       string
	IssuedOn     period.Date
	EntryNumber  string
	EntryDoneOn  period.Date
	Owner        document.Party
	Tenants      []string
	Premises     []string
	Heating      bool
	RequestedOn  period.Date
	DecidedOn    period.Date
	Decision     string
	Lines        []CompletionLine
	WindowEndsOn period.Date
}

var decisionFR = map[string]string{DecisionAccepted: "acceptée", DecisionRefused: "refusée", DecisionPartial: "acceptée en partie"}

func stateFR(st State) string {
	var parts []string
	if st.Condition != "" {
		parts = append(parts, conditionFR[st.Condition])
	}
	if st.Works != "" {
		parts = append(parts, worksFR[st.Works])
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, ", ")
}

// issueCompletion renders and archives the EC document of a decided request
// and returns its id.
func (s Service) issueCompletion(ctx context.Context, sc db.Scope, e completionEntry, r completionRow) (uuid.UUID, error) {
	facts, err := s.Lease.InventoryFactsInTx(ctx, sc, e.head.LeaseID)
	if err != nil {
		return uuid.Nil, err
	}
	v, err := e.view(r)
	if err != nil {
		return uuid.Nil, err
	}
	w, err := s.windows(ctx, e.head)
	if err != nil {
		return uuid.Nil, err
	}
	d := CompletionData{Owner: facts.Owner, Tenants: facts.Tenants, Heating: r.Reason == ReasonHeating, RequestedOn: v.RequestedOn,
		Decision: decisionFR[r.Decision], EntryDoneOn: period.FromTime(*e.head.DoneOn), Lines: []CompletionLine{}}
	if r.DecidedOn != nil {
		d.DecidedOn = period.FromTime(*r.DecidedOn)
	}
	if w != nil {
		d.WindowEndsOn = w.TenDayEndsOn
		if d.Heating && w.HeatingEndsOn != nil {
			d.WindowEndsOn = *w.HeatingEndsOn
		}
	}
	if e.head.DocumentID != nil {
		if di, ok, err := s.Repo.DocumentInfo(ctx, *e.head.DocumentID); err != nil {
			return uuid.Nil, err
		} else if ok {
			d.EntryNumber = strings.TrimSuffix(path.Base(di.ObjectKey), ".pdf")
		}
	}
	for _, a := range facts.Assets {
		d.Premises = append(d.Premises, a.Label)
	}
	for _, l := range v.Lines {
		d.Lines = append(d.Lines, CompletionLine{Room: l.Room, Label: l.Label, Entry: stateFR(l.Entry), EntryComment: l.Entry.Comment,
			Requested: stateFR(l.Requested), RequestedComment: l.Requested.Comment, Accepted: l.Accepted != nil && *l.Accepted})
	}
	lid := e.head.LeaseID
	iss, err := s.Issuer.Issue(ctx, document.Request{Class: document.ClassInventory, Template: completionTemplate,
		TemplateVersion: CompletionTemplate, Series: document.SeriesCompletion, AssetID: facts.Principal, LeaseID: &lid, Snapshot: d,
		Data: func(number string, issued period.Date) any {
			d.Number, d.IssuedOn = number, issued
			return d
		}}, facts.Params)
	if err != nil {
		return uuid.Nil, err
	}
	return iss.DocumentID, nil
}

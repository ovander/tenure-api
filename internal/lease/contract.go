package lease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/portfolio"
)

// Template versions of the lease documents (templates/<class>/v1).
const (
	ContractTemplate       = "v2"
	AmendmentTemplate      = "v1"
	DepositReceiptTemplate = "v1"
	SuretyTemplate         = "v1"
)

// Kinds of lease documents (lease_documents.kind).
const (
	DocContract       = "contract"
	DocAmendment      = "amendment"
	DocDepositReceipt = "deposit_receipt"
	DocSuretyAct      = "surety_act"
)

// Guarantor is what the documents read of a guarantee's guarantor: the
// person for a surety (name, address, legal person, solidary), the visa
// reference for Visale, the insurer and policy for a rent insurance.
type Guarantor struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	LegalPerson bool   `json:"legal_person"`
	// Solidary defaults to true (caution solidaire).
	Solidary   *bool  `json:"solidary"`
	VisaNumber string `json:"visa_number"`
	Insurer    string `json:"insurer"`
	Policy     string `json:"policy"`
}

func guarantorOf(raw json.RawMessage) Guarantor {
	var g Guarantor
	_ = json.Unmarshal(raw, &g)
	g.Name, g.Address = strings.TrimSpace(g.Name), strings.TrimSpace(g.Address)
	return g
}

func (g Guarantor) solidary() bool { return g.Solidary == nil || *g.Solidary }

// label is how the contract names a guarantee's guarantor.
func (g Guarantor) label(k GuaranteeKind) string {
	switch k {
	case GuaranteeVisale:
		return g.VisaNumber
	case GuaranteeInsurance:
		return strings.TrimSpace(g.Insurer + " " + g.Policy)
	}
	return g.Name
}

// DocumentView is a generated lease document as the API returns it.
type DocumentView struct {
	ID           uuid.UUID  `json:"id"`
	LeaseID      uuid.UUID  `json:"lease_id"`
	Kind         string     `json:"kind" enums:"contract,amendment,deposit_receipt,surety_act"`
	Number       string     `json:"number" example:"CT-0190A1B2-2026-0001"`
	LeaseVersion int        `json:"lease_version"`
	GuaranteeID  *uuid.UUID `json:"guarantee_id"`
	DocumentID   uuid.UUID  `json:"document_id"`
	IssuedAt     time.Time  `json:"issued_at"`
	SHA256       string     `json:"sha256"`
}

func leaseDocumentView(d leaseDocumentRow, sum string) DocumentView {
	return DocumentView{ID: d.ID, LeaseID: d.LeaseID, Kind: d.Kind, Number: d.Number, LeaseVersion: d.LeaseVersion,
		GuaranteeID: d.GuaranteeID, DocumentID: d.DocumentID, IssuedAt: d.IssuedAt, SHA256: sum}
}

// Blocker is one rule that blocks a step, or a warning.
type Blocker struct {
	Type   string `json:"type" example:"lease/diagnostic-expired"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// BlockersView lists what blocks contract generation and activation, and the
// warnings of the owner view (RENT-SPEC-008).
type BlockersView struct {
	Generation []Blocker `json:"generation"`
	Activation []Blocker `json:"activation"`
	Warnings   []Blocker `json:"warnings"`
}

// contractFacts is everything a contract and its blockers read, in one
// transaction.
type contractFacts struct {
	st         stored
	view       View
	lease      Lease
	assets     []portfolio.AssetView
	owner      portfolio.OwnerView
	tenants    []portfolio.TenantView
	diags      []portfolio.DiagnosticView
	guarantees []guaranteeRow
	ps         params.Set
	today      period.Date
}

func (s Service) today() period.Date { return period.FromTime(s.Now().In(parisTZ())) }

func parisTZ() *time.Location {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return time.UTC
	}
	return loc
}

func (s Service) facts(ctx context.Context, sc db.Scope, id uuid.UUID, lock bool) (contractFacts, error) {
	st, err := s.Repo.Get(ctx, id, lock)
	if err != nil {
		return contractFacts{}, err
	}
	f := contractFacts{st: st, view: view(st), today: s.today()}
	if f.lease, f.assets, err = s.domain(ctx, id, f.view.Input, State(st.lease.State)); err != nil {
		return contractFacts{}, err
	}
	if f.ps, err = s.Params.Load(ctx); err != nil {
		return contractFacts{}, err
	}
	if f.owner, err = s.Portfolio.OwnerInTx(ctx, sc.OwnerID); err != nil {
		return contractFacts{}, err
	}
	tids := make([]uuid.UUID, len(st.tenants))
	for i, t := range st.tenants {
		tids[i] = t.TenantID
	}
	if f.tenants, err = s.Portfolio.TenantsInTx(ctx, tids); err != nil {
		return contractFacts{}, err
	}
	if f.diags, err = s.Portfolio.DiagnosticsInTx(ctx, f.view.AssetIDs); err != nil {
		return contractFacts{}, err
	}
	if f.guarantees, err = s.Repo.Guarantees(ctx, id); err != nil {
		return contractFacts{}, err
	}
	return f, nil
}

func guaranteeFacts(gs []guaranteeRow) []GuaranteeFact {
	out := make([]GuaranteeFact, len(gs))
	for i, g := range gs {
		out[i] = GuaranteeFact{Kind: GuaranteeKind(g.Kind), Status: GuaranteeStatus(g.Status), AmountCents: money.Cents(g.AmountCents)}
	}
	return out
}

// studentsOnly reports whether every tenant is a student or an apprentice.
func studentsOnly(ts []portfolio.TenantView) bool {
	if len(ts) == 0 {
		return false
	}
	for _, t := range ts {
		if t.Occupation == nil || (*t.Occupation != portfolio.OccupationStudent && *t.Occupation != portfolio.OccupationApprentice) {
			return false
		}
	}
	return true
}

// generationBlockers lists what blocks generating the contract (RENT-SPEC-008):
// the lease rules at save, the owner profile, the mobility reason, the
// clauses, the guarantee rules, the guarantors of sureties, and the
// diagnostics of every asset as of today.
func (s Service) generationBlockers(ctx context.Context, f contractFacts) ([]*invariant.Error, error) {
	out, err := s.rules(ctx, f.lease, f.st.lease.ID, f.view.AssetIDs)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(f.owner.LegalName) == "" || strings.TrimSpace(f.owner.Address) == "" {
		out = append(out, invariant.New(TypeOwnerIncomplete, ""))
	}
	if f.lease.Kind == KindMobility && f.view.MobilityReason == nil {
		out = append(out, invariant.New(TypeMobilityReasonMissing, ""))
	}
	if _, e := Clauses(f.view.Clauses); e != nil {
		out = append(out, e)
	}
	gerrs, err := CheckGuarantees(f.lease, guaranteeFacts(f.guarantees), studentsOnly(f.tenants), true, f.ps)
	if err != nil {
		return nil, err
	}
	out = append(out, gerrs...)
	for _, g := range f.guarantees {
		if g.Kind == string(GuaranteeSurety) && GuaranteeFact(guaranteeFacts([]guaranteeRow{g})[0]).live() {
			if gg := guarantorOf(g.Guarantor); gg.Name == "" || gg.Address == "" {
				out = append(out, invariant.New(TypeGuarantorIncomplete, g.ID.String()))
			}
		}
	}
	derrs, err := CheckDiagnostics(f.assets, f.diags, f.today, f.ps)
	if err != nil {
		return nil, err
	}
	return append(out, derrs...), nil
}

// fingerprint identifies the guarantees a contract states: a change to any of
// them makes the contract stale, a generated document attached to one does not.
func fingerprint(gs []guaranteeRow) string {
	type g struct {
		ID        uuid.UUID       `json:"id"`
		Kind      string          `json:"kind"`
		Guarantor json.RawMessage `json:"guarantor"`
		Amount    int64           `json:"amount_cents"`
		From      *time.Time      `json:"valid_from"`
		To        *time.Time      `json:"valid_to"`
		Status    string          `json:"status"`
	}
	list := make([]g, len(gs))
	for i, x := range gs {
		var compact any
		_ = json.Unmarshal(x.Guarantor, &compact)
		b, _ := json.Marshal(compact)
		list[i] = g{x.ID, x.Kind, b, x.AmountCents, x.ValidFrom, x.ValidTo, x.Status}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID.String() < list[j].ID.String() })
	b, _ := json.Marshal(list)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type contractSnapshotHead struct {
	LeaseVersion          int    `json:"lease_version"`
	GuaranteesFingerprint string `json:"guarantees_fingerprint"`
}

// activationBlockers lists what activation needs beyond the generation
// blockers (RENT-SPEC-008, Q-22): a contract generated from the current
// version of the lease and its guarantees, its signed copy uploaded, and the
// entry inventory recorded.
func (s Service) activationBlockers(ctx context.Context, f contractFacts) ([]*invariant.Error, error) {
	var out []*invariant.Error
	ct, ok, err := s.Repo.LatestLeaseDocument(ctx, f.st.lease.ID, DocContract)
	if err != nil {
		return nil, err
	}
	var head contractSnapshotHead
	if ok {
		_ = json.Unmarshal(ct.InputSnapshot, &head)
	}
	switch {
	case !ok || ct.LeaseVersion != f.st.lease.Version || head.GuaranteesFingerprint != fingerprint(f.guarantees):
		out = append(out, invariant.New(TypeContractMissing, ""))
	default:
		signed, err := s.Repo.SignedCopyExists(ctx, ct.DocumentID)
		if err != nil {
			return nil, err
		}
		if !signed {
			out = append(out, invariant.New(TypeContractNotSigned, ct.Number))
		}
	}
	invs, err := s.Repo.Inventories(ctx, f.st.lease.ID)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(invs, func(i inventoryRow) bool { return i.Kind == "entry" }) {
		out = append(out, invariant.New(TypeEntryInventoryMissing, ""))
	}
	return out, nil
}

func (s Service) warnings(f contractFacts) ([]*invariant.Error, error) {
	to := make([]*period.Date, len(f.guarantees))
	for i, g := range f.guarantees {
		if g.ValidTo != nil {
			d := period.FromTime(*g.ValidTo)
			to[i] = &d
		}
	}
	return Warnings(f.lease, guaranteeFacts(f.guarantees), to, f.today, f.ps)
}

func blockers(errs []*invariant.Error) []Blocker {
	out := []Blocker{}
	for _, e := range errs {
		out = append(out, Blocker{Type: e.Type, Detail: e.Detail})
	}
	return out
}

// Blockers lists what blocks the lease's contract generation and activation,
// and its warnings. Only a draft has generation and activation blockers.
func (s Service) Blockers(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (BlockersView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return BlockersView{}, err
	}
	var out BlockersView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		f, err := s.facts(ctx, sc, id, false)
		if err != nil {
			return err
		}
		var gen, act []*invariant.Error
		if State(f.st.lease.State) == StateDraft {
			if gen, err = s.generationBlockers(ctx, f); err != nil {
				return err
			}
			if act, err = s.activationBlockers(ctx, f); err != nil {
				return err
			}
		}
		warn, err := s.warnings(f)
		if err != nil {
			return err
		}
		out = BlockersView{Generation: blockers(gen), Activation: blockers(act), Warnings: blockers(warn)}
		return nil
	})
	return out, err
}

func assetLabel(a portfolio.AssetView) string {
	label := a.Address
	if a.Lot != "" {
		label += ", lot " + a.Lot
	}
	return label
}

func tenantNames(ts []portfolio.TenantView) []string {
	var out []string
	for _, t := range ts {
		out = append(out, strings.Join(t.Names, " et "))
	}
	return out
}

func assetLabels(as []portfolio.AssetView) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = assetLabel(a)
	}
	return out
}

func principalID(l Lease) uuid.UUID {
	a, _ := l.PrincipalAsset()
	id, _ := uuid.Parse(a.ID)
	return id
}

func (s Service) recordLeaseDocument(ctx context.Context, sc db.Scope, leaseID uuid.UUID, kind string, version int,
	guarantee *uuid.UUID, iss document.Issued, snapshot any, by string) (DocumentView, error) {
	snap, err := json.Marshal(snapshot)
	if err != nil {
		return DocumentView{}, err
	}
	row := leaseDocumentRow{ID: s.NewID(), OwnerID: sc.OwnerID, LeaseID: leaseID, Kind: kind, Number: iss.Number,
		LeaseVersion: version, GuaranteeID: guarantee, DocumentID: iss.DocumentID, InputSnapshot: snap, IssuedAt: iss.IssuedAt, CreatedBy: by}
	if err := s.Repo.InsertLeaseDocument(ctx, row); err != nil {
		return DocumentView{}, err
	}
	return leaseDocumentView(row, iss.SHA256), nil
}

// contractSnapshot is the input snapshot of a contract (RENT-SPEC-022:
// reconstructability).
type contractSnapshot struct {
	contractSnapshotHead
	Template    string                     `json:"template"`
	Lease       View                       `json:"lease"`
	Owner       portfolio.OwnerView        `json:"owner"`
	Tenants     []portfolio.TenantView     `json:"tenants"`
	Assets      []portfolio.AssetView      `json:"assets"`
	Diagnostics []portfolio.DiagnosticView `json:"diagnostics"`
	Guarantees  []GuaranteeView            `json:"guarantees"`
	Ceiling     money.Cents                `json:"ceiling_cents,omitempty"`
	Previous    *money.Cents               `json:"previous_rent_cents,omitempty"`
}

// GenerateContract generates the contract of a draft lease
// (CT-<asset>-<YYYY>-<seq>, RENT-SPEC-008) once nothing blocks it, from the
// lease, its assets, owner, tenants, guarantees and diagnostics only. A
// later change to the lease or its guarantees needs a new contract before
// activation.
func (s Service) GenerateContract(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (DocumentView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return DocumentView{}, err
	}
	var out DocumentView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		f, err := s.facts(ctx, sc, id, true)
		if err != nil {
			return err
		}
		if State(f.st.lease.State) != StateDraft {
			return invariant.New(TypeContractNotDraft, f.st.lease.State)
		}
		errs, err := s.generationBlockers(ctx, f)
		if err != nil {
			return err
		}
		if len(errs) > 0 {
			return errs[0]
		}
		clauses, _ := Clauses(f.view.Clauses)
		ceiling, controlled, _, err := ZoneCeiling(f.lease, f.lease.StartOn, f.ps)
		if err != nil {
			return err
		}
		var previous *money.Cents
		if f.lease.TenseZone() {
			prev, ok, err := s.Repo.PreviousLease(ctx, f.view.AssetIDs, id, f.lease.StartOn.Time())
			if err != nil {
				return err
			}
			if ok {
				c := money.Cents(prev.RentCents)
				previous = &c
			}
		}
		rule, err := f.ps.Diagnostics(f.today)
		if err != nil {
			return err
		}
		gviews := make([]GuaranteeView, len(f.guarantees))
		for i, g := range f.guarantees {
			gviews[i] = guaranteeView(g, auth.RoleOwner)
		}
		snap := contractSnapshot{contractSnapshotHead: contractSnapshotHead{LeaseVersion: f.st.lease.Version, GuaranteesFingerprint: fingerprint(f.guarantees)},
			Template: "contract/" + ContractTemplate, Lease: f.view, Owner: f.owner, Tenants: f.tenants, Assets: f.assets,
			Diagnostics: f.diags, Guarantees: gviews, Ceiling: ceiling, Previous: previous}
		data := document.ContractData{Kind: string(f.lease.Kind), Owner: document.Party{Name: f.owner.LegalName, Address: f.owner.Address},
			StartOn: f.lease.StartOn, TermMonths: f.lease.TermMonths, RentCents: f.lease.RentCents, ProvisionCents: f.lease.ProvisionCents,
			DepositCents: f.lease.DepositCents, DueDay: f.lease.DueDay, IRLRefQuarter: f.lease.IRLRefQuarter, IRLRefValue: f.lease.IRLRefValue,
			RevisionMonth: int(f.lease.RevisionMonth), Controlled: controlled, Tense: f.lease.TenseZone(), CeilingCents: ceiling, PreviousRentCents: previous, Clauses: clauses}
		if f.view.MobilityReason != nil {
			data.MobilityReason = string(*f.view.MobilityReason)
		}
		solidary := map[uuid.UUID]bool{}
		for _, t := range f.st.tenants {
			solidary[t.TenantID] = t.Solidary
		}
		for _, t := range f.tenants {
			data.Tenants = append(data.Tenants, document.ContractTenant{Name: strings.Join(t.Names, " et "), Solidary: solidary[t.ID]})
		}
		labels := map[uuid.UUID]string{}
		for _, a := range f.assets {
			ca := document.ContractAsset{Kind: string(a.Kind), Address: a.Address, Lot: a.Lot}
			if a.SurfaceHundredthsM2 != nil {
				ca.SurfaceHundredthsM2 = *a.SurfaceHundredthsM2
			}
			if a.DPEClass != nil {
				ca.DPEClass = *a.DPEClass
			}
			data.Assets = append(data.Assets, ca)
			data.Copropriete = data.Copropriete || a.Kind == portfolio.KindApartment
			labels[a.ID] = assetLabel(a)
		}
		for _, d := range f.diags {
			cd := document.ContractDiagnostic{Asset: labels[d.AssetID], Kind: string(d.Kind), DoneOn: d.DoneOn, Result: d.Result,
				NotRequiredReason: d.NotRequiredReason}
			if exp, expires := Expiry(d, rule); expires {
				cd.ValidUntil = &exp
			}
			if d.Kind == portfolio.DiagnosticDPE && d.NotRequiredReason == "" {
				for i := range data.Assets {
					if labels[d.AssetID] == assetLabel(f.assets[i]) && data.Assets[i].DPEClass == "" {
						data.Assets[i].DPEClass = d.Result
					}
				}
			}
			data.Diagnostics = append(data.Diagnostics, cd)
		}
		for _, g := range f.guarantees {
			if g.Status != string(StatusHeld) && g.Status != string(StatusValid) {
				continue
			}
			cg := document.ContractGuarantee{Kind: g.Kind, Guarantor: guarantorOf(g.Guarantor).label(GuaranteeKind(g.Kind)), AmountCents: money.Cents(g.AmountCents)}
			if g.ValidTo != nil {
				d := period.FromTime(*g.ValidTo)
				cg.ValidTo = &d
			}
			data.Guarantees = append(data.Guarantees, cg)
		}
		lid := id
		iss, err := s.Issuer.Issue(ctx, document.Request{Class: document.ClassContract, TemplateVersion: ContractTemplate,
			AssetID: principalID(f.lease), LeaseID: &lid, Snapshot: snap,
			Data: func(number string, issued period.Date) any {
				data.Number, data.IssuedOn = number, issued
				return data
			}}, f.ps)
		if err != nil {
			return err
		}
		if out, err = s.recordLeaseDocument(ctx, sc, id, DocContract, f.st.lease.Version, nil, iss, snap, p.Sub); err != nil {
			return err
		}
		return audit.Record(ctx, "lease.contract", audit.Entity{Type: "lease_document", ID: out.ID.String()}, nil, out)
	})
	return out, err
}

// LeaseDocuments returns the generated documents of a lease.
func (s Service) LeaseDocuments(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) ([]DocumentView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []DocumentView{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if _, err := s.Repo.Get(ctx, id, false); err != nil {
			return err
		}
		rows, err := s.Repo.LeaseDocuments(ctx, id)
		for _, r := range rows {
			out = append(out, leaseDocumentView(r, ""))
		}
		return err
	})
	return out, err
}

// GuaranteeDocument generates the document of a guarantee (RENT-SPEC-008):
// the deposit receipt (DG) of a deposit held, or the acte de cautionnement
// (CA) of a surety, one per guarantor. Visale certificates and insurance
// policies are uploaded, not generated.
func (s Service) GuaranteeDocument(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (DocumentView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return DocumentView{}, err
	}
	var out DocumentView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		g, err := s.Repo.Guarantee(ctx, id)
		if err != nil {
			return err
		}
		f, err := s.facts(ctx, sc, g.LeaseID, true)
		if err != nil {
			return err
		}
		if State(f.st.lease.State) == StateClosed {
			return invariant.New(TypeGuaranteeClosed, g.LeaseID.String())
		}
		live := g.Status == string(StatusHeld) || g.Status == string(StatusValid)
		gg := guarantorOf(g.Guarantor)
		contract := ""
		if ct, ok, err := s.Repo.LatestLeaseDocument(ctx, g.LeaseID, DocContract); err != nil {
			return err
		} else if ok {
			contract = ct.Number
		}
		owner := document.Party{Name: f.owner.LegalName, Address: f.owner.Address}
		tenants, assets := tenantNames(f.tenants), assetLabels(f.assets)
		lid := g.LeaseID
		req := document.Request{AssetID: principalID(f.lease), LeaseID: &lid,
			Snapshot: map[string]any{"guarantee": guaranteeView(g, auth.RoleOwner), "lease": f.view, "owner": f.owner, "tenants": f.tenants}}
		var kind string
		switch {
		case g.Kind == string(GuaranteeDeposit) && live && g.AmountCents > 0:
			kind = DocDepositReceipt
			received := period.FromTime(g.CreatedAt.In(parisTZ()))
			if g.ValidFrom != nil {
				received = period.FromTime(*g.ValidFrom)
			}
			req.Class, req.Template, req.TemplateVersion = document.ClassDeposit, "deposit_receipt", DepositReceiptTemplate
			req.Data = func(number string, issued period.Date) any {
				return document.DepositReceiptData{Number: number, IssuedOn: issued, Owner: owner, Tenants: tenants, Assets: assets,
					ContractNumber: contract, AmountCents: money.Cents(g.AmountCents), ReceivedOn: received, RentCents: f.lease.RentCents,
					Furnished: f.lease.Kind == KindFurnished}
			}
		case g.Kind == string(GuaranteeSurety) && live:
			if gg.Name == "" || gg.Address == "" {
				return invariant.New(TypeGuarantorIncomplete, id.String())
			}
			kind = DocSuretyAct
			var validTo *period.Date
			if g.ValidTo != nil {
				d := period.FromTime(*g.ValidTo)
				validTo = &d
			}
			req.Class, req.Template, req.TemplateVersion = document.ClassSurety, "surety", SuretyTemplate
			req.Data = func(number string, issued period.Date) any {
				return document.SuretyData{Number: number, IssuedOn: issued, Owner: owner, Tenants: tenants, Assets: assets,
					ContractNumber: contract, Guarantor: document.Party{Name: gg.Name, Address: gg.Address}, LegalPerson: gg.LegalPerson,
					Solidary: gg.solidary(), MaxAmountCents: money.Cents(g.AmountCents), ValidTo: validTo, StartOn: f.lease.StartOn,
					TermMonths: f.lease.TermMonths, RentCents: f.lease.RentCents, ProvisionCents: f.lease.ProvisionCents,
					IRLRefQuarter: f.lease.IRLRefQuarter, RevisionMonth: int(f.lease.RevisionMonth)}
			}
		default:
			return invariant.New(TypeGuaranteeNoDocument, g.Kind+" "+g.Status)
		}
		iss, err := s.Issuer.Issue(ctx, req, f.ps)
		if err != nil {
			return err
		}
		gid := g.ID
		if out, err = s.recordLeaseDocument(ctx, sc, g.LeaseID, kind, f.st.lease.Version, &gid, iss, req.Snapshot, p.Sub); err != nil {
			return err
		}
		before := guaranteeView(g, auth.RoleOwner)
		if err := s.Repo.SetGuaranteeDocument(ctx, g.ID, g.Version, iss.DocumentID, s.now(), p.Sub); err != nil {
			return err
		}
		after := before
		after.DocumentID, after.Version = &iss.DocumentID, g.Version+1
		return audit.Record(ctx, "guarantee.document", audit.Entity{Type: "guarantee", ID: g.ID.String()}, before,
			map[string]any{"guarantee": after, "document": out})
	})
	return out, err
}

// InventoryInput is the body of POST /leases/{id}/inventories.
type InventoryInput struct {
	Kind   string      `json:"kind" enums:"entry,exit"`
	DoneOn period.Date `json:"done_on" swaggertype:"string" format:"date"`
	// DocumentID is the uploaded état des lieux (class inventory, this lease).
	DocumentID uuid.UUID `json:"document_id"`
	// Items are structured from phase 2; free JSON array until then.
	Items json.RawMessage `json:"items" swaggertype:"array,object"`
}

// InventoryView is a recorded inventory.
type InventoryView struct {
	ID      uuid.UUID `json:"id"`
	LeaseID uuid.UUID `json:"lease_id"`
	InventoryInput
	CreatedAt time.Time `json:"created_at"`
}

func inventoryView(i inventoryRow) InventoryView {
	return InventoryView{ID: i.ID, LeaseID: i.LeaseID, CreatedAt: i.CreatedAt,
		InventoryInput: InventoryInput{Kind: i.Kind, DoneOn: period.FromTime(i.DoneOn), DocumentID: i.DocumentID, Items: i.Items}}
}

// CreateInventory records the entry or exit inventory of a lease, from its
// uploaded document; an inventory is recorded once and never changed. The
// entry inventory is recorded on a draft or active lease, the exit one on a
// lease under notice or ended (Q-70).
func (s Service) CreateInventory(ctx context.Context, p *auth.Principal, requestID string, leaseID uuid.UUID, in InventoryInput) (InventoryView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return InventoryView{}, err
	}
	var f invariant.Fields
	if in.Kind != "entry" && in.Kind != "exit" {
		f.Add("kind", "Type attendu : entry ou exit.")
	}
	if in.DoneOn.IsZero() {
		f.Add("done_on", "La date de l’état des lieux est obligatoire.")
	} else if s.today().Before(in.DoneOn) {
		f.Add("done_on", "La date de l’état des lieux est dans le futur.")
	}
	if len(in.Items) == 0 {
		in.Items = json.RawMessage("[]")
	} else if !json.Valid(in.Items) {
		f.Add("items", "Les éléments doivent être un JSON valide.")
	}
	if err := f.Err(); err != nil {
		return InventoryView{}, err
	}
	var out InventoryView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		st, err := s.Repo.Get(ctx, leaseID, true)
		if err != nil {
			return err
		}
		state := State(st.lease.State)
		if (in.Kind == "entry" && state != StateDraft && state != StateActive) ||
			(in.Kind == "exit" && state != StateNotice && state != StateEnded) {
			return invariant.New(TypeInventoryState, in.Kind+" in "+st.lease.State)
		}
		d, ok, err := s.Repo.Document(ctx, in.DocumentID)
		if err != nil {
			return err
		}
		if !ok || d.Class != string(document.ClassInventory) || d.Source != document.SourceUpload || d.LeaseID == nil || *d.LeaseID != leaseID {
			return invariant.New(TypeInventoryDocument, in.DocumentID.String())
		}
		row := inventoryRow{ID: s.NewID(), OwnerID: sc.OwnerID, LeaseID: leaseID, Kind: in.Kind, DoneOn: in.DoneOn.Time(),
			Items: in.Items, DocumentID: in.DocumentID, CreatedAt: s.now(), CreatedBy: p.Sub}
		if err := s.Repo.InsertInventory(ctx, row); err != nil {
			if db.IsUniqueViolation(err) {
				return invariant.New(TypeInventoryExists, in.Kind)
			}
			return err
		}
		out = inventoryView(row)
		return audit.Record(ctx, "inventory.create", audit.Entity{Type: "inventory", ID: row.ID.String()}, nil, out)
	})
	return out, err
}

// Inventories returns the inventories of a lease.
func (s Service) Inventories(ctx context.Context, p *auth.Principal, requestID string, leaseID uuid.UUID) ([]InventoryView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []InventoryView{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if _, err := s.Repo.Get(ctx, leaseID, false); err != nil {
			return err
		}
		rows, err := s.Repo.Inventories(ctx, leaseID)
		for _, r := range rows {
			out = append(out, inventoryView(r))
		}
		return err
	})
	return out, err
}

// AmendmentKind is what an amendment changes (RENT-SPEC-008).
type AmendmentKind string

// Amendment kinds.
const (
	AmendRent      AmendmentKind = "rent"
	AmendTenantAdd AmendmentKind = "tenant_add"
	AmendAssetAdd  AmendmentKind = "asset_add"
)

// AmendmentInput is the body of POST /leases/{id}/amendments.
type AmendmentInput struct {
	Kind        AmendmentKind `json:"kind" enums:"rent,tenant_add,asset_add"`
	EffectiveOn period.Date   `json:"effective_on" swaggertype:"string" format:"date"`
	// RentCents is the new rent: required for rent, optional for asset_add.
	RentCents *int64      `json:"rent_cents"`
	Tenant    *TenantLink `json:"tenant"`
	// AssetID is the box added.
	AssetID *uuid.UUID `json:"asset_id"`
	Reason  string     `json:"reason"`
}

// AmendmentResult is the amended lease and its numbered amendment.
type AmendmentResult struct {
	Lease     View         `json:"lease"`
	Amendment DocumentView `json:"amendment"`
}

// Amend applies an amendment to a current lease and issues its numbered
// document AV-…, linked to the lease (RENT-SPEC-008): a rent change outside
// IRL (the deposit held never changes), a new tenant, or an added box. The
// lease rules are checked as for a renewal on the effective date; the change
// applies at once (Q-71).
func (s Service) Amend(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, in AmendmentInput) (AmendmentResult, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return AmendmentResult{}, err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	var fe invariant.Fields
	if in.EffectiveOn.IsZero() {
		fe.Add("effective_on", "La date d’effet est obligatoire.")
	}
	switch in.Kind {
	case AmendRent:
		if in.RentCents == nil || *in.RentCents < 0 {
			fe.Add("rent_cents", "Le nouveau loyer est obligatoire.")
		}
	case AmendTenantAdd:
		if in.Tenant == nil {
			fe.Add("tenant", "Le locataire ajouté est obligatoire.")
		}
	case AmendAssetAdd:
		if in.AssetID == nil {
			fe.Add("asset_id", "Le box ajouté est obligatoire.")
		}
		if in.RentCents != nil && *in.RentCents < 0 {
			fe.Add("rent_cents", "Le loyer ne peut pas être négatif.")
		}
	default:
		fe.Add("kind", "Type attendu : rent, tenant_add ou asset_add.")
	}
	if err := fe.Err(); err != nil {
		return AmendmentResult{}, err
	}
	var out AmendmentResult
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		f, err := s.facts(ctx, sc, id, true)
		if err != nil {
			return err
		}
		state := State(f.st.lease.State)
		if state != StateActive && state != StateNotice {
			return invariant.New(TypeAmendmentState, f.st.lease.State)
		}
		if in.EffectiveOn.Before(f.lease.StartOn) {
			return invariant.New(TypeAmendmentInvalid, "effective_on before the lease start")
		}
		before := f.view
		next := f.view.Input
		data := document.AmendmentData{Kind: string(in.Kind), EffectiveOn: in.EffectiveOn, OldRentCents: f.lease.RentCents,
			NewRentCents: f.lease.RentCents, Reason: in.Reason, LeaseStartOn: f.lease.StartOn}
		switch in.Kind {
		case AmendRent:
			if *in.RentCents == next.RentCents {
				return invariant.New(TypeAmendmentInvalid, "same rent")
			}
			next.RentCents = *in.RentCents
		case AmendTenantAdd:
			if slices.ContainsFunc(next.Tenants, func(t TenantLink) bool { return t.TenantID == in.Tenant.TenantID }) {
				return invariant.New(TypeAmendmentInvalid, "tenant already on the lease")
			}
			next.Tenants = append(slices.Clone(next.Tenants), *in.Tenant)
		case AmendAssetAdd:
			if slices.Contains(next.AssetIDs, *in.AssetID) {
				return invariant.New(TypeAmendmentInvalid, "asset already on the lease")
			}
			box, err := s.Portfolio.AssetsInTx(ctx, []uuid.UUID{*in.AssetID})
			if err != nil {
				return err
			}
			if len(box) == 0 || box[0].Kind != portfolio.KindBox {
				return invariant.New(TypeAmendmentInvalid, "only a box of the portfolio can be added")
			}
			if err := s.Repo.LockAssets(ctx, []uuid.UUID{*in.AssetID}); err != nil {
				return err
			}
			others, err := s.Repo.CurrentLeasesOn(ctx, []uuid.UUID{*in.AssetID}, id)
			if err != nil {
				return err
			}
			if len(others) > 0 {
				return invariant.New(TypeOneActivePerAsset, others[0].String())
			}
			next.AssetIDs = append(slices.Clone(next.AssetIDs), *in.AssetID)
			if in.RentCents != nil {
				next.RentCents = *in.RentCents
			}
			data.AssetLabel = assetLabel(box[0])
		}
		l, assets, err := s.domain(ctx, id, next, state)
		if errors.Is(err, invariant.ErrNotFound) {
			return invariant.New(TypeAmendmentInvalid, "unknown tenant or asset")
		}
		if err != nil {
			return err
		}
		errs, err := Validate(l, Check{Event: EventRenewal, AsOf: in.EffectiveOn}, f.ps)
		if err != nil {
			return err
		}
		if len(errs) > 0 {
			return errs[0]
		}
		now := s.now()
		switch in.Kind {
		case AmendTenantAdd:
			sol := true
			if in.Tenant.Solidary != nil {
				sol = *in.Tenant.Solidary
			}
			if err := s.Repo.AddTenant(ctx, leaseTenantRow{LeaseID: id, TenantID: in.Tenant.TenantID, OwnerID: sc.OwnerID, Solidary: sol}); err != nil {
				return err
			}
			ts, err := s.Portfolio.TenantsInTx(ctx, []uuid.UUID{in.Tenant.TenantID})
			if err != nil {
				return err
			}
			if len(ts) > 0 {
				data.TenantName = strings.Join(ts[0].Names, " et ")
			}
		case AmendAssetAdd:
			if err := s.Repo.AddAsset(ctx, leaseAssetRow{LeaseID: id, AssetID: *in.AssetID, OwnerID: sc.OwnerID}); err != nil {
				return err
			}
		}
		if err := s.Repo.Amend(ctx, id, f.st.lease.Version, next.RentCents, now, p.Sub); err != nil {
			return err
		}
		st, err := s.Repo.Get(ctx, id, false)
		if err != nil {
			return err
		}
		after := view(st)
		data.NewRentCents = money.Cents(after.RentCents)
		tenants, err := s.Portfolio.TenantsInTx(ctx, func() []uuid.UUID {
			ids := make([]uuid.UUID, len(after.Tenants))
			for i, t := range after.Tenants {
				ids[i] = t.TenantID
			}
			return ids
		}())
		if err != nil {
			return err
		}
		contract := ""
		if ct, ok, err := s.Repo.LatestLeaseDocument(ctx, id, DocContract); err != nil {
			return err
		} else if ok {
			contract = ct.Number
		}
		data.Owner = document.Party{Name: f.owner.LegalName, Address: f.owner.Address}
		data.Tenants, data.Assets, data.ContractNumber = tenantNames(tenants), assetLabels(assets), contract
		snap := map[string]any{"template": "amendment/" + AmendmentTemplate, "amendment": in, "before": before, "after": after}
		lid := id
		iss, err := s.Issuer.Issue(ctx, document.Request{Class: document.ClassAmendment, TemplateVersion: AmendmentTemplate,
			AssetID: principalID(l), LeaseID: &lid, Snapshot: snap,
			Data: func(number string, issued period.Date) any {
				data.Number, data.IssuedOn = number, issued
				return data
			}}, f.ps)
		if err != nil {
			return err
		}
		doc, err := s.recordLeaseDocument(ctx, sc, id, DocAmendment, after.Version, nil, iss, snap, p.Sub)
		if err != nil {
			return err
		}
		out = AmendmentResult{Lease: after, Amendment: doc}
		return audit.Record(ctx, "lease.amend", audit.Entity{Type: "lease", ID: id.String()}, before, out)
	})
	return out, err
}

package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/esign"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of the signature routes (RENT-INV-001 §7, Q-112).
const (
	TypeNotSignable       = "inventory/not-signable"
	TypeSignaturePending  = "inventory/signature-pending"
	TypeSignatureStarted  = "inventory/signature-started"
	TypeSignatureDocument = "inventory/signature-document-invalid"
	TypeESignUnavailable  = "inventory/esign-unavailable"
	TypeESignFailed       = "inventory/esign-failed"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeNotSignable:       problem.M(409, "L’état des lieux se signe une fois la saisie terminée.", "The inventory is signed once its capture is completed."),
		TypeSignaturePending:  problem.M(409, "Une demande de signature électronique est en cours : terminez-la ou annulez-la d’abord.", "An electronic signature request is pending: finish or cancel it first."),
		TypeSignatureStarted:  problem.M(409, "Des parties ont déjà signé : la saisie ne peut plus être rouverte.", "Parties have already signed: the capture can no longer be reopened."),
		TypeSignatureDocument: problem.M(422, "La copie signée doit être un document déposé, de classe inventory, de ce bail.", "The signed copy must be an uploaded document of class inventory for this lease."),
		TypeESignUnavailable:  problem.M(409, "La signature électronique n’est pas configurée : utilisez la copie signée à la main.", "Electronic signature is not configured: use the hand-signed copy."),
		TypeESignFailed:       problem.M(503, "Le prestataire de signature électronique ne répond pas : réessayez plus tard.", "The electronic signature provider does not answer: try again later."),
	})
}

// Signature methods (signature_method).
const (
	MethodESign   = "esign"
	MethodWetScan = "wet_scan"
)

// ProviderName names the provider in the requests table (OD-08).
const ProviderName = "yousign"

// maxReservations bounds a party's reservations (a product limit).
const maxReservations = 1500

// SignerInput is a party who signs: by hand on the scanned copy, or through
// the provider (then with an e-mail, which the provider requires).
type SignerInput struct {
	Party        string `json:"party" enums:"owner,tenant"`
	SignerName   string `json:"signer_name"`
	Email        string `json:"email,omitempty"`
	Reservations string `json:"reservations,omitempty"`
}

// SignInput is the body of POST /inventories/{id}/signatures: wet_scan with
// the uploaded signed copy and the parties who signed it, or esign with
// every party still to sign.
type SignInput struct {
	Method     string        `json:"method" enums:"wet_scan,esign"`
	DocumentID *uuid.UUID    `json:"document_id,omitempty"`
	Signers    []SignerInput `json:"signers"`
}

// SignatureView is a recorded signature.
type SignatureView struct {
	Party              string     `json:"party"`
	SignerName         string     `json:"signer_name"`
	Method             string     `json:"method" enums:"esign,wet_scan"`
	SignedAt           time.Time  `json:"signed_at"`
	Reservations       string     `json:"reservations"`
	EvidenceDocumentID *uuid.UUID `json:"evidence_document_id,omitempty"`
}

// RequestSignerView is a party of an electronic signature request. Link is
// where they sign, opened on the owner's tablet (INV-OD-03); it is shown to
// the owner only, while the request is pending.
type RequestSignerView struct {
	Party      string `json:"party"`
	SignerName string `json:"signer_name"`
	Link       string `json:"link,omitempty"`
	Signed     bool   `json:"signed"`
}

// RequestView is an electronic signature request.
type RequestView struct {
	ID        uuid.UUID           `json:"id"`
	Provider  string              `json:"provider"`
	State     string              `json:"state" enums:"pending,done,cancelled,refused"`
	Signers   []RequestSignerView `json:"signers"`
	CreatedAt time.Time           `json:"created_at"`
}

// SignaturesView is where an inventory stands on its signatures.
type SignaturesView struct {
	State          string          `json:"state" enums:"draft,scheduled,in_progress,completed,signed,issued"`
	ESignAvailable bool            `json:"esign_available"`
	Parties        []Party         `json:"parties"`
	Signatures     []SignatureView `json:"signatures"`
	Request        *RequestView    `json:"request,omitempty"`
	DocumentID     *uuid.UUID      `json:"document_id,omitempty"`
}

func signatureView(r signatureRow) SignatureView {
	return SignatureView{Party: r.Party, SignerName: r.SignerName, Method: r.Method, SignedAt: r.SignedAt, Reservations: r.Reservations,
		EvidenceDocumentID: r.EvidenceDocumentID}
}

func requestView(r requestRow, links bool) (*RequestView, error) {
	var signers []requestSigner
	if err := json.Unmarshal(r.Signers, &signers); err != nil {
		return nil, err
	}
	v := &RequestView{ID: r.ID, Provider: r.Provider, State: r.State, Signers: []RequestSignerView{}, CreatedAt: r.CreatedAt}
	for _, sg := range signers {
		x := RequestSignerView{Party: sg.Party, SignerName: sg.SignerName, Signed: sg.Signed}
		if links && r.State == "pending" && !sg.Signed {
			x.Link = sg.Link
		}
		v.Signers = append(v.Signers, x)
	}
	return v, nil
}

func (s Service) signaturesView(ctx context.Context, c capture, p *auth.Principal) (SignaturesView, error) {
	v := SignaturesView{State: c.head.State, ESignAvailable: s.ESign != nil, Parties: c.parties(), Signatures: []SignatureView{},
		DocumentID: c.head.DocumentID}
	for _, sg := range c.sigs {
		v.Signatures = append(v.Signatures, signatureView(sg))
	}
	r, ok, err := s.Repo.LatestRequest(ctx, c.head.ID)
	if err != nil || !ok {
		return v, err
	}
	links := p.Active != nil && p.Active.Role == auth.RoleOwner
	v.Request, err = requestView(r, links)
	return v, err
}

// Signatures returns the parties, the signatures recorded and the current
// electronic request of an inventory.
func (s Service) Signatures(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (SignaturesView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return SignaturesView{}, err
	}
	var out SignaturesView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		c, err := s.load(ctx, sc, id, false)
		if err != nil {
			return err
		}
		out, err = s.signaturesView(ctx, c, p)
		return err
	})
	return out, err
}

// Preview renders the completed inventory as the unnumbered copy to sign
// (RENT-INV-001 §5): printed for a signature by hand, or sent to the
// provider. It is neither numbered nor archived; the EE document is issued
// once every party has signed.
func (s Service) Preview(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) ([]byte, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	var out []byte
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		c, err := s.load(ctx, sc, id, false)
		if err != nil {
			return err
		}
		if err := captured(c.head); err != nil {
			return err
		}
		if c.head.State != StateCompleted {
			return invariant.New(TypeNotSignable, c.head.State)
		}
		out, err = s.renderDraft(ctx, c, nil)
		return err
	})
	return out, err
}

// renderDraft renders the copy to sign; anchors maps a party's name to its
// provider anchor ("s1"…), nil for a copy signed by hand.
func (s Service) renderDraft(ctx context.Context, c capture, anchors map[string]string) ([]byte, error) {
	data, _, err := s.documentData(ctx, c)
	if err != nil {
		return nil, err
	}
	data.Draft = true
	for _, pt := range c.parties() {
		if pt.Signed {
			continue
		}
		sg := EntrySigner{Party: partyFR[pt.Party], Name: pt.Name}
		if k, ok := anchors[pt.Party+"\x00"+pt.Name]; ok {
			sg.Anchor = "{{" + k + "|signature|180|60}}"
		}
		data.Signers = append(data.Signers, sg)
	}
	html, footer, err := s.Issuer.Renderer.Render(document.ClassInventory, EntryTemplate, data,
		document.Footer{Number: "Projet", SnapshotHash: "à signer"})
	if err != nil {
		return nil, err
	}
	return s.Issuer.PDF.Convert(ctx, html, footer)
}

// signable checks the signers of a request: each names a party of the
// inventory who has not signed, once.
func signable(c capture, in []SignerInput, needEmail bool) (map[string]Party, error) {
	open := map[string]Party{}
	for _, pt := range c.parties() {
		if !pt.Signed {
			open[pt.Party+"\x00"+pt.Name] = pt
		}
	}
	var f invariant.Fields
	if len(in) == 0 {
		f.Add("signers", "Au moins une partie signe.")
	}
	seen := map[string]bool{}
	for i, sg := range in {
		k := sg.Party + "\x00" + strings.TrimSpace(sg.SignerName)
		pre := fmt.Sprintf("signers[%d].", i)
		if _, ok := open[k]; !ok || seen[k] {
			f.Add(pre+"signer_name", "Cette personne n’est pas une partie qui reste à signer.")
		}
		seen[k] = true
		if tooLong(sg.Reservations, maxReservations) {
			f.Add(pre+"reservations", fmt.Sprintf("Au plus %d caractères.", maxReservations))
		}
		if needEmail {
			if a, err := mail.ParseAddress(sg.Email); err != nil || a.Address != strings.TrimSpace(sg.Email) {
				f.Add(pre+"email", "Adresse e-mail attendue : le prestataire de signature l’exige.")
			}
		}
	}
	return open, f.Err()
}

// Sign records the signatures of a completed inventory (RENT-INV-001 §5,
// Q-112):
//   - wet_scan: the signed copy, uploaded and scanned through POST
//     /documents (class inventory, this lease), is the evidence of the
//     parties listed, with their reservations;
//   - esign: a request to the provider (OD-08) for every party still to
//     sign, one link each, opened on the tablet in turn (INV-OD-03).
//
// When the last party has signed, the inventory is signed and its EE
// document issued in the same transaction.
func (s Service) Sign(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, in SignInput) (SignaturesView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return SignaturesView{}, err
	}
	switch in.Method {
	case MethodWetScan:
		if in.DocumentID == nil {
			return SignaturesView{}, invariant.Fields{{Field: "document_id", Message: "La copie signée est obligatoire."}}
		}
	case MethodESign:
		if s.ESign == nil {
			return SignaturesView{}, invariant.New(TypeESignUnavailable, "")
		}
	default:
		return SignaturesView{}, invariant.Fields{{Field: "method", Message: "Méthode attendue : wet_scan ou esign."}}
	}
	var out SignaturesView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		c, err := s.load(ctx, sc, id, true)
		if err != nil {
			return err
		}
		if err := captured(c.head); err != nil {
			return err
		}
		if c.head.State != StateCompleted {
			return invariant.New(TypeNotSignable, c.head.State)
		}
		if r, ok, err := s.Repo.LatestRequest(ctx, id); err != nil {
			return err
		} else if ok && r.State == "pending" {
			return invariant.New(TypeSignaturePending, r.ID.String())
		}
		open, err := signable(c, in.Signers, in.Method == MethodESign)
		if err != nil {
			return err
		}
		if in.Method == MethodWetScan {
			if err := s.signByHand(ctx, sc, p, &c, in); err != nil {
				return err
			}
		} else {
			if len(in.Signers) != len(open) {
				return invariant.Fields{{Field: "signers", Message: "Une demande de signature électronique concerne toutes les parties qui restent à signer."}}
			}
			if err := s.requestESign(ctx, sc, p, c, in); err != nil {
				return err
			}
		}
		if c, err = s.load(ctx, sc, id, false); err != nil {
			return err
		}
		out, err = s.signaturesView(ctx, c, p)
		return err
	})
	return out, err
}

func (s Service) signByHand(ctx context.Context, sc db.Scope, p *auth.Principal, c *capture, in SignInput) error {
	d, ok, err := s.Repo.DocumentInfo(ctx, *in.DocumentID)
	if err != nil {
		return err
	}
	if !ok || d.Class != string(document.ClassInventory) || d.Source != document.SourceUpload || d.LeaseID == nil || *d.LeaseID != c.head.LeaseID {
		return invariant.New(TypeSignatureDocument, in.DocumentID.String())
	}
	now := s.now()
	rows := []signatureRow{}
	for _, sg := range in.Signers {
		rows = append(rows, signatureRow{ID: s.NewID(), OwnerID: sc.OwnerID, InventoryID: c.head.ID, Party: sg.Party,
			SignerName: strings.TrimSpace(sg.SignerName), Method: MethodWetScan, SignedAt: now, Reservations: strings.TrimSpace(sg.Reservations),
			EvidenceDocumentID: in.DocumentID, CreatedAt: now, CreatedBy: p.Sub})
	}
	if err := s.Repo.InsertSignatures(ctx, rows); err != nil {
		return err
	}
	views := []SignatureView{}
	for _, r := range rows {
		views = append(views, signatureView(r))
	}
	if err := audit.Record(ctx, "inventory.signature.add", audit.Entity{Type: "inventory", ID: c.head.ID.String()}, nil,
		map[string]any{"signatures": views, "evidence_sha256": d.SHA256}); err != nil {
		return err
	}
	c.sigs = append(c.sigs, rows...)
	return s.issueIfSigned(ctx, sc, p, c)
}

// issueIfSigned moves a completed inventory whose parties have all signed to
// signed, then issues its EE document; it does nothing while one is missing.
func (s Service) issueIfSigned(ctx context.Context, sc db.Scope, p *auth.Principal, c *capture) error {
	for _, pt := range c.parties() {
		if !pt.Signed {
			return nil
		}
	}
	fresh, err := s.load(ctx, sc, c.head.ID, true)
	if err != nil {
		return err
	}
	before := header(fresh.head)
	fresh.head.State = StateSigned
	if err := s.touch(ctx, &fresh.head, p.Sub); err != nil {
		return err
	}
	if err := audit.Record(ctx, "inventory.transition", audit.Entity{Type: "inventory", ID: c.head.ID.String()}, before,
		map[string]any{"action": "sign", "state": StateSigned, "version": fresh.head.Version}); err != nil {
		return err
	}
	before = header(fresh.head)
	doc, err := s.issueEntry(ctx, fresh)
	if err != nil {
		return err
	}
	fresh.head.DocumentID, fresh.head.TemplateVersion, fresh.head.State = &doc, "inventory/"+EntryTemplate, StateIssued
	if err := s.touch(ctx, &fresh.head, p.Sub); err != nil {
		return err
	}
	after := header(fresh.head)
	after["action"] = ActionIssue
	*c = fresh
	return audit.Record(ctx, "inventory.transition", audit.Entity{Type: "inventory", ID: c.head.ID.String()}, before, after)
}

// splitName gives the provider a first and a last name: the last word is
// the last name; a single word is both.
func splitName(n string) (string, string) {
	n = strings.TrimSpace(n)
	if i := strings.LastIndex(n, " "); i > 0 {
		return strings.TrimSpace(n[:i]), n[i+1:]
	}
	return n, n
}

func (s Service) requestESign(ctx context.Context, sc db.Scope, p *auth.Principal, c capture, in SignInput) error {
	anchors := map[string]string{}
	signers := []esign.Signer{}
	kept := []requestSigner{}
	for i, sg := range in.Signers {
		key := fmt.Sprintf("s%d", i+1)
		name := strings.TrimSpace(sg.SignerName)
		anchors[sg.Party+"\x00"+name] = key
		first, last := splitName(name)
		signers = append(signers, esign.Signer{Key: key, FirstName: first, LastName: last, Email: strings.TrimSpace(sg.Email)})
		kept = append(kept, requestSigner{Key: key, Party: sg.Party, SignerName: name, Reservations: strings.TrimSpace(sg.Reservations)})
	}
	pdf, err := s.renderDraft(ctx, c, anchors)
	if err != nil {
		return err
	}
	id := s.NewID()
	started, err := s.ESign.Start(ctx, esign.Request{Name: "État des lieux d’entrée", ExternalID: id.String(),
		FileName: "etat-des-lieux-entree.pdf", PDF: pdf, Signers: signers})
	if err != nil {
		slog.ErrorContext(ctx, "esign start failed", "error", err, "inventory_id", c.head.ID)
		return invariant.New(TypeESignFailed, "")
	}
	byKey := map[string]esign.StartedSigner{}
	for _, st := range started.Signers {
		byKey[st.Key] = st
	}
	for i := range kept {
		st, ok := byKey[kept[i].Key]
		if !ok {
			_ = s.ESign.Cancel(context.WithoutCancel(ctx), started.RequestID)
			return invariant.New(TypeESignFailed, "signer missing")
		}
		kept[i].SignerID, kept[i].Link = st.SignerID, st.Link
	}
	raw, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	now := s.now()
	row := requestRow{ID: id, OwnerID: sc.OwnerID, InventoryID: c.head.ID, Provider: ProviderName, ProviderRequestID: started.RequestID,
		State: "pending", Signers: raw, Version: 1, CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub}
	if err := s.Repo.InsertRequest(ctx, row); err != nil {
		_ = s.ESign.Cancel(context.WithoutCancel(ctx), started.RequestID)
		return err
	}
	v, err := requestView(row, false)
	if err != nil {
		return err
	}
	// The audit event names the request and its parties, never the links.
	return audit.Record(ctx, "inventory.signature.request", audit.Entity{Type: "inventory", ID: c.head.ID.String()}, nil,
		map[string]any{"request": v, "provider_request_id": started.RequestID})
}

// Refresh reads the pending electronic request at the provider (pull: no
// public webhook, RENT-BE-001 §4 deny by default): each party who signed is
// recorded; once every party has, the signed file is archived as evidence
// and the inventory is signed and issued. A request the provider refused
// (declined, expired, cancelled) ends as refused. Nothing pending, nothing
// changes.
func (s Service) Refresh(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (SignaturesView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return SignaturesView{}, err
	}
	if s.ESign == nil {
		return SignaturesView{}, invariant.New(TypeESignUnavailable, "")
	}
	var out SignaturesView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		c, err := s.load(ctx, sc, id, true)
		if err != nil {
			return err
		}
		r, ok, err := s.Repo.LatestRequest(ctx, id)
		if err != nil {
			return err
		}
		if ok && r.State == "pending" {
			if err := s.refresh(ctx, sc, p, &c, r); err != nil {
				return err
			}
		}
		if c, err = s.load(ctx, sc, id, false); err != nil {
			return err
		}
		out, err = s.signaturesView(ctx, c, p)
		return err
	})
	return out, err
}

func (s Service) refresh(ctx context.Context, sc db.Scope, p *auth.Principal, c *capture, r requestRow) error {
	st, err := s.ESign.Status(ctx, r.ProviderRequestID)
	if err != nil {
		slog.ErrorContext(ctx, "esign status failed", "error", err, "inventory_id", c.head.ID)
		return invariant.New(TypeESignFailed, "")
	}
	var signers []requestSigner
	if err := json.Unmarshal(r.Signers, &signers); err != nil {
		return err
	}
	signed := map[string]bool{}
	for _, x := range st.Signers {
		signed[x.SignerID] = x.Signed
	}
	now := s.now()
	next := r
	next.UpdatedAt, next.UpdatedBy = now, p.Sub
	var fresh []requestSigner
	for i := range signers {
		if !signers[i].Signed && signed[signers[i].SignerID] {
			signers[i].Signed = true
			fresh = append(fresh, signers[i])
		}
	}
	switch st.State {
	case esign.StateRefused:
		next.State = "refused"
	case esign.StateDone:
		pdf, err := s.ESign.Download(ctx, r.ProviderRequestID)
		if err != nil {
			slog.ErrorContext(ctx, "esign download failed", "error", err, "inventory_id", c.head.ID)
			return invariant.New(TypeESignFailed, "")
		}
		lease := c.head.LeaseID
		asset := c.facts.Principal
		iss, err := s.Issuer.Attach(ctx, document.AttachRequest{Class: document.ClassInventory,
			Name: fmt.Sprintf("%s-signature-%s.pdf", c.head.ID, r.ID), Data: pdf, ContentType: "application/pdf",
			AssetID: &asset, LeaseID: &lease}, c.facts.Params)
		if err != nil {
			return err
		}
		next.State, next.EvidenceDocumentID = "done", &iss.DocumentID
		for i := range signers {
			signers[i].Signed = true
		}
	default:
		if len(fresh) == 0 {
			return nil // nothing new: no write
		}
	}
	raw, err := json.Marshal(signers)
	if err != nil {
		return err
	}
	next.Signers = raw
	if err := s.Repo.SaveRequest(ctx, next, r.Version); err != nil {
		return err
	}
	rows := []signatureRow{}
	for _, sg := range signers {
		if !sg.Signed {
			continue
		}
		already := false
		for _, x := range c.sigs {
			if x.Party == sg.Party && x.SignerName == sg.SignerName {
				already = true
			}
		}
		if already {
			continue
		}
		rows = append(rows, signatureRow{ID: s.NewID(), OwnerID: sc.OwnerID, InventoryID: c.head.ID, Party: sg.Party, SignerName: sg.SignerName,
			Method: MethodESign, SignedAt: now, Reservations: sg.Reservations, EvidenceDocumentID: next.EvidenceDocumentID,
			CreatedAt: now, CreatedBy: p.Sub})
	}
	// A signer's signature is recorded once the request is done, with the
	// provider's signed file as its evidence; until then the request keeps
	// who has signed.
	if next.State != "done" {
		rows = nil
	}
	if err := s.Repo.InsertSignatures(ctx, rows); err != nil {
		return err
	}
	v, err := requestView(next, false)
	if err != nil {
		return err
	}
	if err := audit.Record(ctx, "inventory.signature.refresh", audit.Entity{Type: "inventory", ID: c.head.ID.String()},
		map[string]any{"state": r.State}, map[string]any{"request": v, "signatures": len(rows)}); err != nil {
		return err
	}
	if next.State != "done" {
		return nil
	}
	c.sigs = append(c.sigs, rows...)
	return s.issueIfSigned(ctx, sc, p, c)
}

// CancelRequest cancels the pending electronic request at the provider, so
// the parties may sign another way.
func (s Service) CancelRequest(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (SignaturesView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return SignaturesView{}, err
	}
	if s.ESign == nil {
		return SignaturesView{}, invariant.New(TypeESignUnavailable, "")
	}
	var out SignaturesView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		c, err := s.load(ctx, sc, id, true)
		if err != nil {
			return err
		}
		r, ok, err := s.Repo.LatestRequest(ctx, id)
		if err != nil {
			return err
		}
		if !ok || r.State != "pending" {
			return invariant.ErrNotFound
		}
		if err := s.ESign.Cancel(ctx, r.ProviderRequestID); err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "esign cancel failed", "error", err, "inventory_id", id)
			return invariant.New(TypeESignFailed, "")
		}
		next := r
		next.State, next.UpdatedAt, next.UpdatedBy = "cancelled", s.now(), p.Sub
		if err := s.Repo.SaveRequest(ctx, next, r.Version); err != nil {
			return err
		}
		if err := audit.Record(ctx, "inventory.signature.cancel", audit.Entity{Type: "inventory", ID: id.String()},
			map[string]any{"request_id": r.ID, "state": r.State}, map[string]any{"request_id": r.ID, "state": next.State}); err != nil {
			return err
		}
		out, err = s.signaturesView(ctx, c, p)
		return err
	})
	return out, err
}

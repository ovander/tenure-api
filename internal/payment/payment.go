// Package payment owns payments, their matching to billing documents and the
// bank import (RENT-SPEC-002, RENT-SPEC-013). Slice 1 holds the pure domain
// type the tax calculation reads; matching and storage arrive in slice 4.
package payment

import (
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

// LineKind is the kind of billing-document line a matched amount settles.
type LineKind string

// Line kinds of a rent call.
const (
	LineRent           LineKind = "rent"
	LineProvision      LineKind = "provision"
	LineRegularisation LineKind = "regularisation"
)

// Payment is money received for a lease.
type Payment struct {
	ID      string `json:"id"`
	LeaseID string `json:"lease_id"`
	// AssetID is the lease's principal asset, set by the caller from the lease
	// (lease.Lease.PrincipalAsset); payments have no asset of their own.
	AssetID     string      `json:"asset_id"`
	PaidOn      period.Date `json:"paid_on"`
	AmountCents money.Cents `json:"amount_cents"`
	Matches     []Match     `json:"matches"`
}

// Match is the part of a payment settling one kind of line of one billing
// document. The matching service splits a match by document line before the
// tax calculation reads it.
type Match struct {
	BillingDocumentID string      `json:"billing_document_id"`
	Line              LineKind    `json:"line"`
	AmountCents       money.Cents `json:"amount_cents"`
}

// Matched returns the total matched on lines of kind k.
func (p Payment) Matched(k LineKind) money.Cents {
	var s money.Cents
	for _, m := range p.Matches {
		if m.Line == k {
			s += m.AmountCents
		}
	}
	return s
}

// Unmatched returns the part of the payment not matched to any line.
func (p Payment) Unmatched() money.Cents {
	s := p.AmountCents
	for _, m := range p.Matches {
		s -= m.AmountCents
	}
	return s
}

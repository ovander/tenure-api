// Package esign is the client of the external electronic-signature provider
// (RENT-SPEC OD-08: an eIDAS provider, Yousign, never a native signature).
// Tenure sends the document to sign and its signers, gets back one signing
// link per signer, which the owner opens on the tablet for each party in
// turn (RENT-INV-001 INV-OD-03), then reads the request's state and, once
// every signer has signed, downloads the signed file kept as evidence. The
// state is read on demand (pull), so no public webhook is exposed.
package esign

import (
	"context"
	"errors"
	"time"
)

// Signer is a person who signs. Key is the anchor of their signature field
// in the document ("s1", "s2", …, in the order of the signers).
type Signer struct {
	Key       string
	FirstName string
	LastName  string
	Email     string
}

// Request is a document to sign by its signers.
type Request struct {
	// Name names the request at the provider; ExternalID is Tenure's id.
	Name       string
	ExternalID string
	FileName   string
	PDF        []byte
	Signers    []Signer
}

// StartedSigner is a signer at the provider, with the link they sign at.
type StartedSigner struct {
	Key      string
	SignerID string
	Link     string
}

// Started is a request activated at the provider.
type Started struct {
	RequestID string
	Signers   []StartedSigner
}

// Request states, as Tenure reads them.
const (
	StateOngoing = "ongoing"
	StateDone    = "done"
	StateRefused = "refused" // declined, expired or canceled at the provider
	StateFailed  = "failed"
)

// SignerStatus is whether a signer has signed.
type SignerStatus struct {
	SignerID string
	Signed   bool
}

// Status is a request's state at the provider.
type Status struct {
	State   string
	Signers []SignerStatus
}

// Provider is the signature provider.
type Provider interface {
	Start(ctx context.Context, r Request) (Started, error)
	Status(ctx context.Context, requestID string) (Status, error)
	// Download returns the signed document once the request is done.
	Download(ctx context.Context, requestID string) ([]byte, error)
	Cancel(ctx context.Context, requestID string) error
}

// ErrProvider reports an answer of the provider Tenure cannot use.
var ErrProvider = errors.New("esign: unexpected provider answer")

// Timeout bounds one call to the provider.
const Timeout = 30 * time.Second

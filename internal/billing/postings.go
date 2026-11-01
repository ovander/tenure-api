package billing

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

// posting is a regularisation_postings row written by the charge module:
// what the next fees of a lease carry (RENT-SPEC-007 step 7).
type posting struct {
	ID                uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID           uuid.UUID  `gorm:"column:owner_id"`
	LeaseID           uuid.UUID  `gorm:"column:lease_id"`
	ReconciliationID  uuid.UUID  `gorm:"column:reconciliation_id"`
	LineID            uuid.UUID  `gorm:"column:line_id"`
	ParentID          *uuid.UUID `gorm:"column:parent_id"`
	Reference         string     `gorm:"column:reference"`
	Instalment        int        `gorm:"column:instalment"`
	Of                int        `gorm:"column:of"`
	AmountCents       int64      `gorm:"column:amount_cents"`
	DueFrom           string     `gorm:"column:due_from"`
	BillingDocumentID *uuid.UUID `gorm:"column:billing_document_id"`
	VoidedAt          *time.Time `gorm:"column:voided_at"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	CreatedBy         string     `gorm:"column:created_by"`
}

func (posting) TableName() string { return "regularisation_postings" }

// PendingPosting returns the oldest posting due on period p for the lease
// that no live rent call carries: never carried, or carried by a rent call
// since cancelled (its reissue carries it again, Q-79).
func (Repository) PendingPosting(ctx context.Context, leaseID uuid.UUID, p string) (posting, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return posting{}, false, err
	}
	var rows []posting
	err = t.Raw(`SELECT p.* FROM regularisation_postings p
		WHERE p.lease_id = ? AND p.voided_at IS NULL AND p.due_from <= ?
			AND (p.billing_document_id IS NULL OR EXISTS (
				SELECT 1 FROM billing_documents c WHERE c.cancels_id = p.billing_document_id))
		ORDER BY p.due_from, p.created_at, p.instalment LIMIT 1 FOR UPDATE OF p`, leaseID, p).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return posting{}, false, err
	}
	return rows[0], true, nil
}

// CarryPosting records the rent call that carried a posting. When the fee
// could take only part of a credit, the rest becomes a new posting due on
// the next period (Q-17); a carry-over left by an earlier, cancelled carry
// of the same posting is voided first, so it is never counted twice.
func (Repository) CarryPosting(ctx context.Context, p posting, rentCall uuid.UUID, carry money.Cents, next period.Period,
	newID uuid.UUID, at time.Time, by string) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	if err := t.Model(&posting{}).Where("parent_id = ? AND voided_at IS NULL AND (billing_document_id IS NULL OR EXISTS (SELECT 1 FROM billing_documents c WHERE c.cancels_id = regularisation_postings.billing_document_id))", p.ID).
		Update("voided_at", at).Error; err != nil {
		return err
	}
	if err := t.Model(&posting{}).Where("id = ?", p.ID).Update("billing_document_id", rentCall).Error; err != nil {
		return err
	}
	if carry == 0 {
		return nil
	}
	child := posting{ID: newID, OwnerID: p.OwnerID, LeaseID: p.LeaseID, ReconciliationID: p.ReconciliationID, LineID: p.LineID,
		ParentID: &p.ID, Reference: p.Reference, AmountCents: int64(carry), DueFrom: next.String(), CreatedAt: at, CreatedBy: by}
	return t.Create(&child).Error
}

// regularisationFor returns the regularisation line of the fee of lease id
// for period p, and the posting it comes from.
func (s Service) regularisationFor(ctx context.Context, id uuid.UUID, p period.Period) (FeeOptions, *posting, error) {
	post, ok, err := s.Repo.PendingPosting(ctx, id, p.String())
	if err != nil || !ok {
		return FeeOptions{}, nil, err
	}
	return FeeOptions{Regularisation: &Regularisation{Reference: post.Reference, AmountCents: money.Cents(post.AmountCents),
		Instalment: post.Instalment, Of: post.Of}}, &post, nil
}

// carry records that the rent call rentCall carried the posting of its fee.
func (s Service) carry(ctx context.Context, post *posting, rentCall uuid.UUID, fee Fee) error {
	if post == nil {
		return nil
	}
	return s.Repo.CarryPosting(ctx, *post, rentCall, fee.CarryOverCents, fee.Period.Next(), s.NewID(), s.now(), actorOf(ctx))
}

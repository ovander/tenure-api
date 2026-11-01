package audit

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
)

// ChainHead is an audit_chain_heads row: the head of one chain on one day,
// also written to the locked bucket (RENT-BE-001 §8b).
type ChainHead struct {
	OwnerID     *uuid.UUID `json:"owner_id" gorm:"column:owner_id"`
	AsOf        time.Time  `json:"as_of" gorm:"column:as_of" swaggertype:"string" format:"date"`
	LastEventID int64      `json:"last_event_id" gorm:"column:last_event_id"`
	HeadHash    string     `json:"head_hash" gorm:"column:head_hash"`
	ObjectKey   string     `json:"object_key" gorm:"column:object_key"`
}

// TableName maps ChainHead to audit_chain_heads.
func (ChainHead) TableName() string { return "audit_chain_heads" }

func chainFilter(ctx context.Context) (func(q *gorm.DB) *gorm.DB, error) {
	sc, ok := db.ScopeFrom(ctx)
	if !ok {
		return nil, db.ErrNoScope
	}
	return func(q *gorm.DB) *gorm.DB {
		if sc.OwnerID == uuid.Nil {
			return q.Where("owner_id IS NULL")
		}
		return q.Where("owner_id = ?", sc.OwnerID)
	}, nil
}

// Head returns the last event of the transaction's chain; ok is false for an
// empty chain.
func Head(ctx context.Context) (id int64, hash string, ok bool, err error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return 0, "", false, err
	}
	f, err := chainFilter(ctx)
	if err != nil {
		return 0, "", false, err
	}
	var e []Event
	if err := f(tx.Model(&Event{})).Order("id DESC").Limit(1).Find(&e).Error; err != nil {
		return 0, "", false, err
	}
	if len(e) == 0 {
		return 0, "", false, nil
	}
	return e[0].ID, e[0].Hash, true, nil
}

// InsertHead records a chain head.
func InsertHead(ctx context.Context, h ChainHead) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	return tx.Create(&h).Error
}

// VerifyHeads checks every stored head of the transaction's chain against the
// chain: the event it names exists and has the stored hash. A database edit
// that rewrote the chain consistently is caught here, because the heads were
// also written under object lock (RENT-SPEC-022).
func VerifyHeads(ctx context.Context) (int, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return 0, err
	}
	f, err := chainFilter(ctx)
	if err != nil {
		return 0, err
	}
	var heads []ChainHead
	if err := f(tx.Model(&ChainHead{})).Order("as_of").Find(&heads).Error; err != nil {
		return 0, err
	}
	for _, h := range heads {
		var e []Event
		if err := f(tx.Model(&Event{})).Where("id = ?", h.LastEventID).Limit(1).Find(&e).Error; err != nil {
			return 0, err
		}
		if len(e) == 0 || e[0].Hash != h.HeadHash {
			return len(heads), fmt.Errorf("audit: head of %s (event %d) does not match the chain", h.AsOf.Format(time.DateOnly), h.LastEventID)
		}
	}
	return len(heads), nil
}

// Heads returns the scope's chain heads, latest first (owner and accountant).
func (s Service) Heads(ctx context.Context, p *auth.Principal, requestID string, offset, limit int) ([]ChainHead, int, error) {
	if p.Active == nil {
		return nil, 0, invariant.ErrForbidden
	}
	var out []ChainHead
	var total int64
	err := s.DB.InScope(ctx, p.DBScope(requestID), func(ctx context.Context) error {
		tx, err := db.Tx(ctx)
		if err != nil {
			return err
		}
		q := tx.Model(&ChainHead{}).Where("owner_id = ?", p.Active.OwnerID)
		if err := q.Count(&total).Error; err != nil {
			return err
		}
		return q.Order("as_of DESC").Offset(offset).Limit(limit).Find(&out).Error
	})
	return out, int(total), err
}

// ListHeads godoc
//
//	@Summary	List the scope's daily audit chain heads
//	@Tags		audit
//	@Produce	json
//	@Param		page			query		int		false	"page, from 1"
//	@Param		size			query		int		false	"page size, 1 to 100"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	httpx.List[ChainHead]
//	@Failure	400,401,403		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/audit/heads [get]
func (h Handler) ListHeads(w http.ResponseWriter, r *http.Request) {
	pg, ok := httpx.ParsePage(w, r)
	if !ok {
		return
	}
	p, _ := auth.FromContext(r.Context())
	items, total, err := h.Service.Heads(r.Context(), p, httpx.RequestID(r), pg.Offset(), pg.Size)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, pg, total))
}

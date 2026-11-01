package billing

import (
	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

// Call is a live rent call with what was matched to it.
type Call struct {
	BillingDocumentID uuid.UUID     `json:"billing_document_id"`
	Number            string        `json:"number"`
	Period            period.Period `json:"period"`
	DueOn             period.Date   `json:"due_on"`
	TotalCents        money.Cents   `json:"total_cents"`
	PaidCents         money.Cents   `json:"paid_cents"`
	RemindedOn        *period.Date  `json:"reminded_on,omitempty"`
}

// ArrearsItem is a rent call with something left to pay.
type ArrearsItem struct {
	Call
	OutstandingCents money.Cents `json:"outstanding_cents"`
	Overdue          bool        `json:"overdue"`
	DaysLate         int         `json:"days_late"`
	// ReminderOn is the day the reminder is due (due day + the
	// receipt_due_days_before_reminder parameter).
	ReminderOn  period.Date `json:"reminder_on"`
	ReminderDue bool        `json:"reminder_due"`
}

// ArrearsView is the arrears of a lease (RENT-SPEC-003 late and unpaid rent).
type ArrearsView struct {
	LeaseID          uuid.UUID     `json:"lease_id"`
	Items            []ArrearsItem `json:"items"`
	OutstandingCents money.Cents   `json:"outstanding_cents"`
	OverdueCents     money.Cents   `json:"overdue_cents"`
}

// Arrears returns, as of today, every call with an outstanding amount
// (total − matched, never negative): overdue once its due day has passed, and
// due for a reminder from its due day plus reminderDays, unless one was sent.
// No penalty is computed (RENT-SPEC-003: none in v1).
func Arrears(leaseID uuid.UUID, calls []Call, today period.Date, reminderDays int) ArrearsView {
	v := ArrearsView{LeaseID: leaseID, Items: []ArrearsItem{}}
	for _, c := range calls {
		out := c.TotalCents - c.PaidCents
		if out <= 0 {
			continue
		}
		it := ArrearsItem{Call: c, OutstandingCents: out, ReminderOn: c.DueOn.AddDays(reminderDays)}
		if today.After(c.DueOn) {
			it.Overdue = true
			it.DaysLate = period.DaysInclusive(c.DueOn, today) - 1
			v.OverdueCents += out
		}
		it.ReminderDue = c.RemindedOn == nil && !today.Before(it.ReminderOn)
		v.OutstandingCents += out
		v.Items = append(v.Items, it)
	}
	return v
}

package overview

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/billing"
	"github.com/ovander/tenure-api/internal/calendar"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/jobs"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/payment"
	"github.com/ovander/tenure-api/internal/period"
)

// Service serves the month cockpit. It only reads: no write, no lock, no
// audit event.
type Service struct {
	DB          *db.DB // API role
	Repo        Repository
	Billing     billing.Service
	BillingRepo billing.Repository
	Payments    payment.Repository
	Calendar    calendar.Repository
	Params      params.Repository
	Now         func() time.Time
}

// View is the cockpit of a period.
type View struct {
	Period    string      `json:"period" example:"2026-11"`
	Today     period.Date `json:"today" swaggertype:"string" format:"date"`
	UpToDate  bool        `json:"up_to_date"`
	Steps     []StepView  `json:"steps"`
	Anomalies []Anomaly   `json:"anomalies"`
}

func paris() *time.Location {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return time.UTC
	}
	return loc
}

// monthlyJob is the job that issues the rent calls of a period.
const monthlyJob = "billing.monthly"

// Month returns the cockpit of period p (the current month in Europe/Paris
// when nil) for any role of the active scope (RENT-FE-001 §4b, Q-139).
func (s Service) Month(ctx context.Context, p *auth.Principal, requestID string, per *period.Period) (View, error) {
	if p.Active == nil {
		return View{}, invariant.ErrForbidden
	}
	now := s.Now()
	today := period.FromTime(now.In(paris()))
	f := Facts{Period: today.Period(), Today: today, Now: now}
	if per != nil {
		f.Period = *per
	}
	owner := p.Active.OwnerID
	err := s.DB.InScope(ctx, p.DBScope(requestID), func(ctx context.Context) error {
		return s.facts(ctx, &f, owner)
	})
	if err != nil {
		return View{}, err
	}
	steps := Steps(f)
	return View{Period: f.Period.String(), Today: today, UpToDate: UpToDate(steps), Steps: steps, Anomalies: Anomalies(f)}, nil
}

func str(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

// facts reads everything Steps and Anomalies need, inside the scope.
func (s Service) facts(ctx context.Context, f *Facts, owner uuid.UUID) error {
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return err
	}
	if f.Thresholds, err = ps.Overview(f.Today); err != nil {
		return err
	}
	bankRule, err := ps.Bank(f.Today)
	if err != nil {
		return err
	}
	assets, err := s.Repo.LeaseAssets(ctx)
	if err != nil {
		return err
	}
	// 1. The period's billable leases and their rent calls.
	ids, err := s.BillingRepo.BillableLeaseIDs(ctx, f.Period)
	if err != nil {
		return err
	}
	for _, id := range ids {
		live, _, err := s.BillingRepo.Live(ctx, id, billing.KindRentCall, f.Period.String())
		if err != nil {
			return err
		}
		f.Billable = append(f.Billable, Lease{ID: id.String(), AssetID: str(assets[id]), Called: live != nil})
	}
	if f.MonthlyRan, err = s.Repo.Ran(ctx, monthlyJob, f.Period.String()); err != nil {
		return err
	}
	// 2–3. Every live rent call, its payment and its receipt; 5. the leases
	// in arrears.
	leases, err := s.Payments.LeasesWithDues(ctx)
	if err != nil {
		return err
	}
	var callIDs []uuid.UUID
	byLease := map[uuid.UUID][]billing.Call{}
	for _, id := range leases {
		calls, err := s.Billing.CallsInTx(ctx, id)
		if err != nil {
			return err
		}
		byLease[id] = calls
		for _, c := range calls {
			callIDs = append(callIDs, c.BillingDocumentID)
		}
	}
	states, err := s.Repo.CallStates(ctx, callIDs)
	if err != nil {
		return err
	}
	for _, id := range leases {
		late := false
		for _, c := range byLease[id] {
			st := states[c.BillingDocumentID]
			call := Call{ID: c.BillingDocumentID.String(), LeaseID: id.String(), AssetID: str(assets[id]), Period: c.Period, DueOn: c.DueOn,
				TotalCents: c.TotalCents, PaidCents: c.PaidCents, Receipted: st.Receipted}
			if call.paid() {
				call.PaidAt = st.LastMatch
			}
			if !call.paid() && f.Today.After(c.DueOn) {
				late = true
			}
			f.Calls = append(f.Calls, call)
		}
		if !late {
			continue
		}
		rv, err := s.Billing.RecoveryInTx(ctx, id)
		if err != nil {
			return err
		}
		r := Recovery{LeaseID: id.String(), AssetID: str(assets[id]), Formal: len(rv.Steps) > 0}
		for _, n := range rv.Next {
			if !rv.Suspended && !n.DueOn.After(f.Today) {
				r.StepDue = true
			}
		}
		f.Recovery = append(f.Recovery, r)
	}
	if f.RemindersSent, err = s.Repo.RemindersSent(ctx, f.Period.First(), f.Period.Last()); err != nil {
		return err
	}
	// 4. The bank.
	accounts, last, lines, err := s.Repo.BankState(ctx)
	if err != nil {
		return err
	}
	f.Bank = Bank{Accounts: accounts, AlertDays: bankRule.UnmatchedAlertDays}
	if last != nil {
		d := period.FromTime(*last)
		f.Bank.LastBookedOn = &d
	}
	for _, l := range lines {
		f.Bank.Unmatched = append(f.Bank.Unmatched, BankLine{ID: l.ID.String(), BookedOn: period.FromTime(l.BookedOn), AmountCents: money.Cents(l.AmountCents)})
	}
	// Job runs: a platform run keeps only this scope's outcome (jobs.Visible).
	since := f.Today.AddDays(-f.Thresholds.JobWindowDays).Time().Add(-24 * time.Hour)
	runs, err := s.Repo.FinishedRuns(ctx, since)
	if err != nil {
		return err
	}
	for _, r := range runs {
		v := jobs.Visible(r, &owner, false)
		if v.FinishedAt != nil {
			f.Jobs = append(f.Jobs, JobRun{ID: v.ID.String(), Job: v.Job, Status: string(v.Status), FinishedAt: v.FinishedAt.In(paris())})
		}
	}
	// IRL revisions due and not applied: billing.revisions' own rule, dry.
	revisions, err := s.Billing.RevisionsDueInTx(ctx, f.Today)
	if err != nil {
		return err
	}
	for _, r := range revisions {
		f.Revisions = append(f.Revisions, Dated{LeaseID: r.LeaseID.String(), AssetID: str(assets[r.LeaseID]), On: r.Due.Anniversary})
	}
	ex, err := s.Repo.OpenExercises(ctx)
	if err != nil {
		return err
	}
	for _, e := range ex {
		f.Exercises = append(f.Exercises, Dated{AssetID: e.AssetID.String(), Ref: strconv.Itoa(e.ExerciseYear), On: period.FromTime(e.EndOn)})
	}
	missing, err := s.Repo.ActiveWithoutEntry(ctx)
	if err != nil {
		return err
	}
	for _, l := range missing {
		f.MissingInventory = append(f.MissingInventory, Dated{LeaseID: l.ID.String(), AssetID: str(assets[l.ID]), On: period.FromTime(l.StartOn)})
	}
	// Diagnostics of let assets and deposit deadlines: the calendar's rules.
	cf, err := s.Calendar.Facts(ctx)
	if err != nil {
		return err
	}
	items, err := calendar.Build(cf, f.Today, f.Today.AddDays(f.Thresholds.ExpiringDays), f.Today, ps)
	if err != nil {
		return err
	}
	let := map[string]bool{}
	for _, l := range cf.Leases {
		if l.State == "active" || l.State == "notice" {
			for _, a := range l.AssetIDs {
				let[a] = true
			}
		}
	}
	for _, it := range items {
		switch {
		case it.Kind == calendar.KindDiagnosticExpiry && let[it.AssetID]:
			f.Diagnostics = append(f.Diagnostics, Dated{AssetID: it.AssetID, Ref: it.Ref, On: it.On})
		case it.Kind == calendar.KindDepositDeadline:
			f.DepositDeadlines = append(f.DepositDeadlines, Dated{LeaseID: it.LeaseID, AssetID: it.AssetID, On: it.On})
		}
	}
	return nil
}

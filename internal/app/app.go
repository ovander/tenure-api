// Package app assembles the service from its dependencies: the router with
// every module, and the job runner with every job. cmd/tenure builds the
// dependencies from the configuration; the integration suite builds them
// from containers, so both run exactly the same wiring.
package app

import (
	"context"
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/bff"
	"github.com/ovander/tenure-api/internal/bic"
	"github.com/ovander/tenure-api/internal/billing"
	"github.com/ovander/tenure-api/internal/calendar"
	"github.com/ovander/tenure-api/internal/charge"
	"github.com/ovander/tenure-api/internal/dashboard"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/esign"
	"github.com/ovander/tenure-api/internal/httpapi"
	"github.com/ovander/tenure-api/internal/inventory"
	"github.com/ovander/tenure-api/internal/jobs"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/ledger"
	"github.com/ovander/tenure-api/internal/loan"
	"github.com/ovander/tenure-api/internal/maintenance"
	"github.com/ovander/tenure-api/internal/notify"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/payment"
	"github.com/ovander/tenure-api/internal/portfolio"
	"github.com/ovander/tenure-api/internal/sci"
	"github.com/ovander/tenure-api/internal/tax"
)

// Deps are the external dependencies of the service.
type Deps struct {
	API     *db.DB // tenure_api
	Jobs    *db.DB // tenure_jobs
	Auth    *auth.Middleware
	Store   document.Store
	PDF     document.PDFEngine
	Scanner document.Scanner
	// ESign is the inventories' signature provider (OD-08), nil when not
	// configured.
	ESign          esign.Provider
	Mailer         notify.Mailer
	INSEE          params.INSEE
	Ready          func(context.Context) error
	TrustedProxies []*net.IPNet
	// WriteBurst raises the per-subject write burst (integration suite only).
	WriteBurst int
	// FailedAuthMax raises the failed-auth lockout threshold (integration suite only).
	FailedAuthMax int
	// BFF serves browser sessions (nil: bearer clients only).
	BFF *bff.Server
	// Web is the embedded web app (nil: API only).
	Web fs.FS
	// ImageOrigins are the archive's signed-link origins the web app may
	// load images from (inventory photos, INV-OD-12).
	ImageOrigins []string
	Now          func() time.Time
}

// App is the assembled service.
type App struct {
	Handler http.Handler
	Runner  *jobs.Runner
	Billing billing.Service
}

// New assembles the service.
func New(d Deps) (*App, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	newID := func() uuid.UUID { return uuid.Must(uuid.NewV7()) }
	pf := portfolio.Service{DB: d.API, Now: d.Now, NewID: newID}
	issuer := document.Issuer{PDF: d.PDF, Store: d.Store, Now: d.Now, NewID: newID}
	ls := lease.Service{DB: d.API, Portfolio: pf, Issuer: issuer, Now: d.Now, NewID: newID,
		Drafts: inventory.Drafter{NewID: newID}}
	bs := billing.Service{DB: d.API, Lease: ls, Now: d.Now, NewID: newID, Mailer: d.Mailer, Issuer: issuer}
	docs := document.Service{DB: d.API, Store: d.Store, Scanner: d.Scanner, Now: d.Now, NewID: newID}
	ps := payment.Service{DB: d.API, Docs: docs, Params: params.Repository{}, Now: d.Now, NewID: newID}
	cs := charge.Service{DB: d.API, Lease: ls, Billing: bs, Payments: ps, Docs: docs, Issuer: issuer, Now: d.Now, NewID: newID}
	ts := tax.Service{DB: d.API, Issuer: issuer, Now: d.Now, NewID: newID}
	reg := map[string]jobs.Job{}
	for _, j := range []jobs.Job{
		jobs.AuditVerify(),
		jobs.AuditHead(d.Store, params.Repository{}),
		jobs.ArchiveRetention(d.Store, params.Repository{}),
		jobs.ArchiveVerify(d.Store, params.Repository{}),
		billing.MonthlyJob(bs, d.Jobs),
		billing.IRLFetchJob(bs, d.Jobs, d.INSEE),
		billing.ReceiptsJob(bs, d.Jobs),
		billing.RemindersJob(bs, d.Jobs),
		charge.ReconcileJob(cs),
		tax.AnnualJob(ts),
	} {
		reg[j.Name] = j
	}
	runner := &jobs.Runner{DB: d.Jobs, Jobs: reg, Now: d.Now}
	h, err := httpapi.NewRouter(httpapi.Deps{
		Auth:      d.Auth,
		Portfolio: portfolio.Handler{Service: pf},
		Lease:     lease.Handler{Service: ls},
		Inventory: inventory.Handler{Service: inventory.Service{DB: d.API, Portfolio: pf, Lease: ls, Issuer: issuer, Now: d.Now, NewID: newID,
			Photos: inventory.PhotoStore{Store: d.Store, Scanner: d.Scanner, Params: params.Repository{}}, ESign: d.ESign,
			Arrears: bs.OutstandingInTx}},
		Calendar:  calendar.Handler{Service: calendar.Service{DB: d.API, Params: params.Repository{}, Now: d.Now}},
		Params:    params.Handler{Service: params.Service{DB: d.API, Now: d.Now}},
		Audit:     audit.Handler{Service: audit.Service{DB: d.API}},
		Jobs:      jobs.Handler{DB: d.API, Runner: runner},
		Billing:   billing.Handler{Service: bs, Runner: runner},
		Documents: document.Handler{Service: docs},
		Charges:   charge.Handler{Service: cs},
		Tax:       tax.Handler{Service: ts},
		Payments:  payment.Handler{Service: ps},
		Ledger:    ledger.Handler{Service: ledger.Service{DB: d.API, Params: params.Repository{}, Now: d.Now}},
		Dashboard: dashboard.Handler{Service: dashboard.Service{DB: d.API, Ledger: ledger.Service{DB: d.API, Params: params.Repository{}, Now: d.Now},
			Lease: ls, Billing: bs, Portfolio: pf, Tax: ts, Params: params.Repository{}, Now: d.Now,
			BIC: bic.Service{DB: d.API, Portfolio: pf, Docs: docs, Params: params.Repository{}, Now: d.Now, NewID: newID}}},
		BIC:            bic.Handler{Service: bic.Service{DB: d.API, Portfolio: pf, Docs: docs, Params: params.Repository{}, Now: d.Now, NewID: newID}},
		SCI:            sci.Handler{Service: sci.Service{DB: d.API, Tax: ts, Now: d.Now, NewID: newID}},
		Maintenance:    maintenance.Handler{Service: maintenance.Service{DB: d.API, Portfolio: pf, Docs: docs, Charges: cs, Params: params.Repository{}, Now: d.Now, NewID: newID}},
		Loans:          loan.Handler{Service: loan.Service{DB: d.API, Portfolio: pf, Docs: docs, Now: d.Now, NewID: newID}},
		Ready:          d.Ready,
		TrustedProxies: d.TrustedProxies,
		WriteBurst:     d.WriteBurst,
		FailedAuthMax:  d.FailedAuthMax,
		BFF:            d.BFF,
		Web:            d.Web,
		ImageOrigins:   d.ImageOrigins,
	})
	if err != nil {
		return nil, err
	}
	return &App{Handler: h, Runner: runner, Billing: bs}, nil
}

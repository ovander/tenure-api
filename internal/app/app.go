// Package app assembles the service from its dependencies: the router with
// every module, and the job runner with every job. cmd/tenure builds the
// dependencies from the configuration; the integration suite builds them
// from containers, so both run exactly the same wiring.
package app

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/billing"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/httpapi"
	"github.com/ovander/tenure-api/internal/jobs"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/notify"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/portfolio"
)

// Deps are the external dependencies of the service.
type Deps struct {
	API            *db.DB // tenure_api
	Jobs           *db.DB // tenure_jobs
	Auth           *auth.Middleware
	Store          document.Store
	PDF            document.PDFEngine
	Mailer         notify.Mailer
	INSEE          params.INSEE
	Ready          func(context.Context) error
	TrustedProxies []*net.IPNet
	Now            func() time.Time
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
	ls := lease.Service{DB: d.API, Portfolio: pf, Now: d.Now, NewID: newID}
	bs := billing.Service{DB: d.API, Lease: ls, Now: d.Now, NewID: newID, Mailer: d.Mailer,
		Issuer: document.Issuer{PDF: d.PDF, Store: d.Store, Now: d.Now, NewID: newID}}
	reg := map[string]jobs.Job{}
	for _, j := range []jobs.Job{
		jobs.AuditVerify(),
		jobs.AuditHead(d.Store, params.Repository{}),
		billing.MonthlyJob(bs, d.Jobs),
		billing.IRLFetchJob(bs, d.Jobs, d.INSEE),
	} {
		reg[j.Name] = j
	}
	runner := &jobs.Runner{DB: d.Jobs, Jobs: reg, Now: d.Now}
	h, err := httpapi.NewRouter(httpapi.Deps{
		Auth:           d.Auth,
		Portfolio:      portfolio.Handler{Service: pf},
		Lease:          lease.Handler{Service: ls},
		Params:         params.Handler{Service: params.Service{DB: d.API, Now: d.Now}},
		Audit:          audit.Handler{Service: audit.Service{DB: d.API}},
		Jobs:           jobs.Handler{DB: d.API, Runner: runner},
		Billing:        billing.Handler{Service: bs, Runner: runner},
		Documents:      document.Handler{Service: document.Service{DB: d.API, Store: d.Store, Now: d.Now}},
		Ready:          d.Ready,
		TrustedProxies: d.TrustedProxies,
	})
	if err != nil {
		return nil, err
	}
	return &App{Handler: h, Runner: runner, Billing: bs}, nil
}

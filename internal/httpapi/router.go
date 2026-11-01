// Package httpapi wires the Chi router (RENT-BE-001 §3): middleware, the
// /api/v1 routes of every module, health endpoints and the OpenAPI document.
// Nothing imports it but cmd/tenure.
//
//	@title						Tenure API
//	@version					1
//	@description				Rental management for houses, apartments and parking boxes under French law. Every amount is in cents; every error is an RFC 9457 problem with a stable type.
//	@BasePath					/api/v1
//	@securityDefinitions.apikey	BearerAuth
//	@in							header
//	@name						Authorization
//	@description				Socrate access token: "Bearer <token>".
package httpapi

import (
	"context"
	_ "embed"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ovander/backendkit/buildinfo"
	"github.com/ovander/backendkit/httpware"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/billing"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/jobs"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/portfolio"
	"github.com/ovander/tenure-api/internal/problem"
)

//go:embed openapi/swagger.json
var openAPI []byte

// Deps are the handlers and checks the router needs.
type Deps struct {
	Auth           *auth.Middleware
	Portfolio      portfolio.Handler
	Lease          lease.Handler
	Params         params.Handler
	Audit          audit.Handler
	Jobs           jobs.Handler
	Billing        billing.Handler
	Documents      document.Handler
	Ready          func(ctx context.Context) error
	TrustedProxies []*net.IPNet
}

// Guard is the authorisation a route requires on top of a valid token.
type Guard string

// Guards.
const (
	GuardAuthenticated Guard = "authenticated"         // any valid token
	GuardScopeOwnerW   Guard = "scope, owner to write" // any scope role reads, owner writes
	GuardAuditReaders  Guard = "owner or accountant"
	GuardAdmin         Guard = "admin"
)

// Route is one /api/v1 route.
type Route struct {
	Method  string
	Pattern string
	Guard   Guard
	// StepUp marks a step_up route (RENT-BE-001 §8b).
	StepUp  bool
	Handler http.HandlerFunc
}

// Routes returns the /api/v1 routes. Every route requires a valid Socrate
// token; tests walk this table to prove it and to check the audited routes.
func Routes(d Deps) []Route {
	h := func(f http.HandlerFunc) http.HandlerFunc { return f }
	return []Route{
		{"GET", "/me", GuardAuthenticated, false, h(d.Portfolio.GetMe)},
		{"PUT", "/me", GuardAuthenticated, false, h(d.Portfolio.PutMe)},
		{"GET", "/assets", GuardScopeOwnerW, false, h(d.Portfolio.ListAssets)},
		{"POST", "/assets", GuardScopeOwnerW, false, h(d.Portfolio.CreateAsset)},
		{"GET", "/assets/{id}", GuardScopeOwnerW, false, h(d.Portfolio.GetAsset)},
		{"PUT", "/assets/{id}", GuardScopeOwnerW, false, h(d.Portfolio.UpdateAsset)},
		{"GET", "/tenants", GuardScopeOwnerW, false, h(d.Portfolio.ListTenants)},
		{"POST", "/tenants", GuardScopeOwnerW, false, h(d.Portfolio.CreateTenant)},
		{"GET", "/tenants/{id}", GuardScopeOwnerW, false, h(d.Portfolio.GetTenant)},
		{"PUT", "/tenants/{id}", GuardScopeOwnerW, false, h(d.Portfolio.UpdateTenant)},
		{"GET", "/leases", GuardScopeOwnerW, false, h(d.Lease.List)},
		{"POST", "/leases", GuardScopeOwnerW, false, h(d.Lease.Create)},
		{"GET", "/leases/{id}", GuardScopeOwnerW, false, h(d.Lease.Get)},
		{"PUT", "/leases/{id}", GuardScopeOwnerW, false, h(d.Lease.Update)},
		{"POST", "/leases/{id}/transitions", GuardScopeOwnerW, false, h(d.Lease.Transition)},
		{"GET", "/leases/{id}/guarantees", GuardScopeOwnerW, false, h(d.Lease.ListGuarantees)},
		{"POST", "/leases/{id}/guarantees", GuardScopeOwnerW, false, h(d.Lease.CreateGuarantee)},
		{"PUT", "/guarantees/{id}", GuardScopeOwnerW, false, h(d.Lease.UpdateGuarantee)},
		{"POST", "/leases/{id}/revision", GuardScopeOwnerW, false, h(d.Billing.Revise)},
		{"POST", "/billing/runs", GuardScopeOwnerW, false, h(d.Billing.CreateRun)},
		{"GET", "/billing/runs/{id}", GuardScopeOwnerW, false, h(d.Billing.GetRun)},
		{"GET", "/billing/documents", GuardScopeOwnerW, false, h(d.Billing.ListDocuments)},
		{"GET", "/billing/documents/{id}", GuardScopeOwnerW, false, h(d.Billing.GetDocument)},
		{"POST", "/billing/documents/{id}/cancel", GuardScopeOwnerW, false, h(d.Billing.Cancel)},
		{"GET", "/documents", GuardScopeOwnerW, false, h(d.Documents.List)},
		{"GET", "/documents/{id}", GuardScopeOwnerW, false, h(d.Documents.Get)},
		{"GET", "/documents/{id}/content", GuardScopeOwnerW, false, h(d.Documents.Content)},
		{"GET", "/params", GuardAuthenticated, false, h(d.Params.List)},
		{"GET", "/params/irl", GuardAuthenticated, false, h(d.Params.IRLSeries)},
		{"PUT", "/params/irl/{quarter}", GuardAdmin, true, h(d.Params.PutIRL)},
		{"PUT", "/params/{key}", GuardAdmin, true, h(d.Params.Put)},
		{"GET", "/audit/events", GuardAuditReaders, false, h(d.Audit.List)},
		{"GET", "/audit/events.csv", GuardAuditReaders, true, h(d.Audit.ExportCSV)},
		{"GET", "/audit/heads", GuardAuditReaders, false, h(d.Audit.ListHeads)},
		{"GET", "/jobs/runs", GuardAuthenticated, false, h(d.Jobs.ListRuns)},
		{"POST", "/jobs/{name}/trigger", GuardAuthenticated, false, h(d.Jobs.Trigger)},
		{"GET", "/openapi.json", GuardAuthenticated, false, serveOpenAPI},
	}
}

func serveOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(openAPI)
}

// NewRouter returns the HTTP handler of the service.
func NewRouter(d Deps) (http.Handler, error) {
	reads, err := httpware.NewKeyedRateLimiter(httpware.KeyedRateLimiterConfig{RPS: 10, Burst: 600})
	if err != nil {
		return nil, err
	}
	writes, err := httpware.NewKeyedRateLimiter(httpware.KeyedRateLimiterConfig{RPS: 1, Burst: 60})
	if err != nil {
		return nil, err
	}
	lockout := newFailedAuthLimiter(10, 15*time.Minute, d.TrustedProxies)

	r := chi.NewRouter()
	r.Use(httpware.RequestID, accessLog, recoverer, httpware.SecurityHeaders, httpware.BodyLimit(1<<20))
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { problem.Write(w, r, problem.NotFound, "", "", nil) })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		problem.Write(w, r, problem.MethodNotAllowed, "", "", nil)
	})

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","version":"` + buildinfo.Version + `"}`))
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if d.Ready != nil {
			if err := d.Ready(ctx); err != nil {
				problem.Write(w, r, problem.Unavailable, "", "", nil)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

	r.Route("/api/v1", func(api chi.Router) {
		api.Use(lockout.handler, d.Auth.Authenticate, identify, problemOn429, byMethod(reads.Handler, writes.Handler))
		api.NotFound(func(w http.ResponseWriter, r *http.Request) { problem.Write(w, r, problem.NotFound, "", "", nil) })
		for _, rt := range Routes(d) {
			var h http.Handler = rt.Handler
			switch rt.Guard {
			case GuardScopeOwnerW:
				h = auth.RequireOwnerForWrites(h)
			case GuardAuditReaders:
				h = auth.RequireRoles(auth.RoleOwner, auth.RoleAccountant)(h)
			case GuardAdmin:
				h = auth.RequireAdmin(h)
			}
			if rt.StepUp {
				h = d.Auth.StepUp(h)
			}
			api.Method(rt.Method, rt.Pattern, h)
		}
	})
	return r, nil
}

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
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ovander/backendkit/buildinfo"
	"github.com/ovander/backendkit/httpware"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/bff"
	"github.com/ovander/tenure-api/internal/billing"
	"github.com/ovander/tenure-api/internal/charge"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/inventory"
	"github.com/ovander/tenure-api/internal/jobs"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/payment"
	"github.com/ovander/tenure-api/internal/portfolio"
	"github.com/ovander/tenure-api/internal/problem"
	"github.com/ovander/tenure-api/internal/tax"
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
	Payments       payment.Handler
	Charges        charge.Handler
	Tax            tax.Handler
	Inventory      inventory.Handler
	Ready          func(ctx context.Context) error
	TrustedProxies []*net.IPNet
	// WriteBurst overrides the 60 writes a subject may make before being
	// limited to one a second (RENT-BE-001 §8b); zero keeps 60. Only the
	// integration suite raises it, to set up many records quickly.
	WriteBurst int
	// FailedAuthMax overrides the 10 failed authentications an address may
	// make in 15 minutes before it is locked out (RENT-BE-001 §8b); zero
	// keeps 10. Only the integration suite, whose every call comes from one
	// address, raises it.
	FailedAuthMax int
	// Web is the embedded tenure-web bundle served outside /api and /bff
	// (nil: none, every such path is a 404).
	Web fs.FS
	// ImageOrigins are the origins of the archive's signed links, allowed as
	// image sources in the web app's CSP (INV-OD-12).
	ImageOrigins []string
	// BFF serves /bff and turns a browser session into a bearer in front of
	// internal/auth; nil serves bearer clients only.
	BFF *bff.Server
}

// Guard is the authorisation a route requires on top of a valid token.
type Guard string

// Guards.
const (
	GuardAuthenticated Guard = "authenticated"         // any valid token
	GuardScopeOwnerW   Guard = "scope, owner to write" // any scope role reads, owner writes
	GuardScope         Guard = "scope"                 // any scope role, reads and writes (exports)
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
		{"GET", "/me/export", GuardAuthenticated, true, h(d.Portfolio.ExportMe)},
		{"GET", "/grants", GuardScopeOwnerW, false, h(d.Portfolio.ListGrants)},
		{"POST", "/grants", GuardScopeOwnerW, true, h(d.Portfolio.CreateGrant)},
		{"DELETE", "/grants/{id}", GuardScopeOwnerW, true, h(d.Portfolio.RevokeGrant)},
		{"GET", "/assets", GuardScopeOwnerW, false, h(d.Portfolio.ListAssets)},
		{"POST", "/assets", GuardScopeOwnerW, false, h(d.Portfolio.CreateAsset)},
		{"GET", "/assets/{id}", GuardScopeOwnerW, false, h(d.Portfolio.GetAsset)},
		{"PUT", "/assets/{id}", GuardScopeOwnerW, false, h(d.Portfolio.UpdateAsset)},
		{"GET", "/assets/{id}/diagnostics", GuardScopeOwnerW, false, h(d.Portfolio.ListDiagnostics)},
		{"POST", "/assets/{id}/diagnostics", GuardScopeOwnerW, false, h(d.Portfolio.CreateDiagnostic)},
		{"PUT", "/diagnostics/{id}", GuardScopeOwnerW, false, h(d.Portfolio.UpdateDiagnostic)},
		{"GET", "/assets/{id}/layout", GuardScopeOwnerW, false, h(d.Inventory.GetLayout)},
		{"PUT", "/assets/{id}/layout", GuardScopeOwnerW, false, h(d.Inventory.PutLayout)},
		{"POST", "/assets/{id}/layout/seed", GuardScopeOwnerW, false, h(d.Inventory.SeedLayout)},
		{"GET", "/inventory-templates", GuardScopeOwnerW, false, h(d.Inventory.ListTemplates)},
		{"GET", "/inventories/{id}", GuardScopeOwnerW, false, h(d.Inventory.GetCapture)},
		{"PUT", "/inventories/{id}", GuardScopeOwnerW, false, h(d.Inventory.PutCapture)},
		{"POST", "/inventories/{id}/items", GuardScopeOwnerW, false, h(d.Inventory.PostItem)},
		{"PUT", "/inventories/{id}/items/{itemId}", GuardScopeOwnerW, false, h(d.Inventory.PutItem)},
		{"PUT", "/inventories/{id}/meters", GuardScopeOwnerW, false, h(d.Inventory.PutMeters)},
		{"PUT", "/inventories/{id}/keys", GuardScopeOwnerW, false, h(d.Inventory.PutKeys)},
		{"POST", "/inventories/{id}/transitions", GuardScopeOwnerW, false, h(d.Inventory.PostTransition)},
		{"POST", "/inventories/{id}/photos", GuardScopeOwnerW, false, h(d.Inventory.UploadPhoto)},
		{"GET", "/inventories/{id}/photos/{photoId}/content", GuardScopeOwnerW, false, h(d.Inventory.PhotoContent)},
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
		{"POST", "/guarantees/{id}/document", GuardScopeOwnerW, false, h(d.Lease.GuaranteeDocument)},
		{"POST", "/leases/{id}/contract", GuardScopeOwnerW, false, h(d.Lease.GenerateContract)},
		{"GET", "/leases/{id}/blockers", GuardScopeOwnerW, false, h(d.Lease.Blockers)},
		{"GET", "/leases/{id}/documents", GuardScopeOwnerW, false, h(d.Lease.ListLeaseDocuments)},
		{"POST", "/leases/{id}/amendments", GuardScopeOwnerW, false, h(d.Lease.Amend)},
		{"GET", "/leases/{id}/inventories", GuardScopeOwnerW, false, h(d.Lease.ListInventories)},
		{"POST", "/leases/{id}/inventories", GuardScopeOwnerW, false, h(d.Lease.CreateInventory)},
		{"POST", "/leases/{id}/revision", GuardScopeOwnerW, false, h(d.Billing.Revise)},
		{"GET", "/leases/{id}/arrears", GuardScopeOwnerW, false, h(d.Billing.Arrears)},
		{"GET", "/payments", GuardScopeOwnerW, false, h(d.Payments.List)},
		{"POST", "/payments", GuardScopeOwnerW, false, h(d.Payments.Create)},
		{"GET", "/payments/{id}", GuardScopeOwnerW, false, h(d.Payments.Get)},
		{"PUT", "/payments/{id}", GuardScopeOwnerW, false, h(d.Payments.Update)},
		{"POST", "/payments/{id}/matches", GuardScopeOwnerW, false, h(d.Payments.Match)},
		{"GET", "/charges", GuardScopeOwnerW, false, h(d.Charges.ListCharges)},
		{"POST", "/charges", GuardScopeOwnerW, false, h(d.Charges.CreateCharge)},
		{"POST", "/charges/import/syndic", GuardScopeOwnerW, false, h(d.Charges.ImportSyndic)},
		{"GET", "/charges/{id}", GuardScopeOwnerW, false, h(d.Charges.GetCharge)},
		{"PUT", "/charges/{id}", GuardScopeOwnerW, false, h(d.Charges.UpdateCharge)},
		{"GET", "/reconciliations", GuardScopeOwnerW, false, h(d.Charges.ListRecons)},
		{"POST", "/reconciliations", GuardScopeOwnerW, false, h(d.Charges.CreateRecon)},
		{"GET", "/reconciliations/{id}", GuardScopeOwnerW, false, h(d.Charges.GetRecon)},
		{"POST", "/reconciliations/{id}/transitions", GuardScopeOwnerW, false, h(d.Charges.Transition)},
		{"PUT", "/reconciliations/{id}/lines/{lineId}", GuardScopeOwnerW, false, h(d.Charges.UpdateLine)},
		{"POST", "/billing/runs", GuardScopeOwnerW, false, h(d.Billing.CreateRun)},
		{"GET", "/billing/runs/{id}", GuardScopeOwnerW, false, h(d.Billing.GetRun)},
		{"GET", "/billing/documents", GuardScopeOwnerW, false, h(d.Billing.ListDocuments)},
		{"GET", "/billing/documents/{id}", GuardScopeOwnerW, false, h(d.Billing.GetDocument)},
		{"POST", "/billing/documents/{id}/cancel", GuardScopeOwnerW, false, h(d.Billing.Cancel)},
		{"GET", "/tax/summaries", GuardScopeOwnerW, false, h(d.Tax.List)},
		{"POST", "/tax/summaries", GuardScopeOwnerW, true, h(d.Tax.Create)},
		{"GET", "/tax/summaries/{id}", GuardScopeOwnerW, false, h(d.Tax.Get)},
		{"GET", "/tax/summaries/{id}/lines.csv", GuardScopeOwnerW, true, h(d.Tax.LinesCSV)},
		{"GET", "/documents", GuardScopeOwnerW, false, h(d.Documents.List)},
		{"GET", "/documents/{id}", GuardScopeOwnerW, false, h(d.Documents.Get)},
		{"GET", "/documents/{id}/content", GuardScopeOwnerW, false, h(d.Documents.Content)},
		{"POST", "/documents", GuardScopeOwnerW, false, h(d.Documents.Upload)},
		{"POST", "/documents/export", GuardScope, true, h(d.Documents.CreateExport)},
		{"GET", "/documents/exports/{id}", GuardScopeOwnerW, false, h(d.Documents.GetExport)},
		{"GET", "/templates", GuardScopeOwnerW, false, h(d.Documents.Templates)},
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

// DefaultBodyLimit caps every request body but the upload's.
const DefaultBodyLimit = 1 << 20

// bodyLimit returns the body cap of a route: the upload routes take a 25 MB
// file, every other route 1 MiB of JSON.
func bodyLimit(rt Route) int64 {
	if rt.Method == http.MethodPost && rt.Pattern == "/documents" {
		return document.UploadBodyLimit
	}
	if rt.Method == http.MethodPost && rt.Pattern == "/inventories/{id}/photos" {
		return inventory.PhotoBodyLimit
	}
	return DefaultBodyLimit
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
	burst := 60
	if d.WriteBurst > 0 {
		burst = d.WriteBurst
	}
	writes, err := httpware.NewKeyedRateLimiter(httpware.KeyedRateLimiterConfig{RPS: 1, Burst: burst})
	if err != nil {
		return nil, err
	}
	maxFails := 10
	if d.FailedAuthMax > 0 {
		maxFails = d.FailedAuthMax
	}
	lockout := newFailedAuthLimiter(maxFails, 15*time.Minute, d.TrustedProxies)

	r := chi.NewRouter()
	// The body cap is set per route (bodyLimit): 1 MiB, except uploads.
	r.Use(httpware.RequestID, accessLog, recoverer, httpware.SecurityHeaders, attribute(d.TrustedProxies))
	r.NotFound(staticApp(d.Web, d.ImageOrigins, problemNotFound))
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

	sessions := func(next http.Handler) http.Handler { return next }
	if d.BFF != nil {
		sessions = d.BFF.Authenticate
		r.Group(func(g chi.Router) {
			g.Use(httpware.BodyLimit(DefaultBodyLimit), reads.Handler)
			d.BFF.Mount(g)
		})
	}

	r.Route("/api/v1", func(api chi.Router) {
		api.Use(lockout.handler, sessions, d.Auth.Authenticate, identify, problemOn429, byMethod(reads.Handler, writes.Handler))
		api.NotFound(func(w http.ResponseWriter, r *http.Request) { problem.Write(w, r, problem.NotFound, "", "", nil) })
		for _, rt := range Routes(d) {
			var h http.Handler = rt.Handler
			switch rt.Guard {
			case GuardScopeOwnerW:
				h = auth.RequireOwnerForWrites(h)
			case GuardScope:
				h = auth.RequireScope(h)
			case GuardAuditReaders:
				h = auth.RequireRoles(auth.RoleOwner, auth.RoleAccountant)(h)
			case GuardAdmin:
				h = auth.RequireAdmin(h)
			}
			if rt.StepUp {
				h = d.Auth.StepUp(h)
			}
			api.With(httpware.BodyLimit(bodyLimit(rt))).Method(rt.Method, rt.Pattern, h)
		}
	})
	return r, nil
}

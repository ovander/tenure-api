# OWASP ASVS 4.0.3 level 2: phase 1 checklist

This checklist backs the phase 1 gate (IMPLEMENTATION-PLAN, slice 8): "ASVS L2 checklist signed
off". It maps the RENT-BE-001 §8b controls to the ASVS chapters and to the code or test that
shows each one.

- A row marked **host** depends on the production host and is checked there, not in CI.
- The sign-off at the bottom is the owner's; nothing here signs itself.

| ASVS | Control | Where it is shown | Status |
| --- | --- | --- | --- |
| V1 Architecture | Layering handler → service → repository; RLS on every owner table; threat model per phase | CLAUDE.md, RENT-BE-001 §1–§2; cross-owner zero-row checks (`rlsZeroRows` in `internal/itest`); `threat-model.md` (phase 1) | CI |
| V2 Authentication | No local credentials; Socrate tokens checked by backendkit `jwtauth` (RS256, JWKS, issuer, audience) | `internal/auth`; `TestEveryRouteRequiresAToken` | CI |
| V2.8 / step-up | `step_up` routes need `auth_time` ≤ 5 min and `mfa` in `amr` | `internal/auth` tests; 401 `auth/step-up-required` in TestTax and TestArchive | CI |
| V2.2 Anti-automation | 10 failed authentications per 15 min per address, then 429 | `TestFailedAuthLockout` | CI |
| V3 Sessions | BFF: `__Host-` HttpOnly Secure SameSite=Strict cookie, server-side encrypted sessions, CSRF token on unsafe methods, logout revokes the refresh token | `internal/bff` tests, TestBFF | CI |
| V4 Access control | Deny by default; scope from `owners`/`owner_grants`, never a claim; RLS; revoked grant ⇒ 403 | TestArchive (grants), TestScenario (isolation), `X-Owner-Scope` checks | CI |
| V5 Validation | JSON only, typed decoding, 1 MiB body cap (25 MB uploads); invariants in services; RFC 9457 problems | `internal/httpx`, `internal/invariant` | CI |
| V7 Logging | slog JSON; audit chain INSERT-only, hashed and verified weekly; heads under object lock | `internal/audit`, TestZAuditedRoutes, `audit.verify` | CI |
| V8 Data protection | `Cache-Control: no-store` on the API; signed URLs for 10 min bound to the subject; RGPD export | `internal/document`, `GET /me/export` | CI |
| V9 Communications | TLS 1.2+ at Caddy; database over TLS (`sslmode=verify-full`); S3 over TLS (insecure only on the loopback) | `deploy/Caddyfile`, `deploy/tenure.env.example`, `TestDeployTemplate` | host |
| V10 Malicious code | gitleaks, govulncheck, golangci-lint; SBOM on each release | `.github/workflows/ci.yml`, `release.yml` | CI |
| V12 Files | Quarantine, ClamAV (fail closed), sniffed type, 25 MB cap; the archive is versioned and locked | TestContracts (uploads), `internal/document` | CI |
| V13 API | Every `/api/v1` route has a swag path and a guard; audited-route list complete | `TestEveryRouteHasASwagPath`, TestZAuditedRoutes | CI |
| V14 Configuration | Settings validated at start, no default secret; hardened systemd unit; CSP per surface (API `default-src 'none'`, web app `'self'`, no inline script); HSTS | `internal/config`, `deploy/systemd/tenure.service`, `TestStaticApp` | CI + host |
| V14 Backups | Nightly encrypted backup (age, key offline), restore tested twice a year | `deploy/backup`, `deploy/runbooks/restore.md` | host |
| Incident | 72 h breach runbook; register of breaches | `deploy/runbooks/breach.md`, `docs/rgpd/registre-des-traitements.md` | host |

## Known gaps in phase 1

- **Erasure after retention:** `archive.retention` lists documents past retention and deletes
  nothing (RENT-SPEC-005).
- **Container scanning:** tenure ships no image of its own. The gotenberg and ClamAV images are
  pinned upstream images; scanning them is a follow-up.
- **Penetration test:** an external pen test is scheduled before the phase 4 portal (§8b).

## Sign-off

| Date | Release | Reviewer | Host checks done | Result |
| --- | --- | --- | --- | --- |
| | | | | |

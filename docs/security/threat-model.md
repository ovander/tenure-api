# Threat model: phase 1

RENT-SPEC-022 asks for a threat model before the phase 1 gate. This document is that model. It
covers the API, the embedded web app, the jobs, the archive and the host, as deployed by
`deploy/`. It uses STRIDE per trust boundary. Each threat names the control in place and where it
is shown, and a residual risk ends in an action (A-n) with its owner. The ASVS L2 checklist
(`asvs-l2.md`) shows the controls; this model says what they protect against and what is left.

Review it at each phase gate, after a security incident, and when a component, a sub-processor
or a trust boundary is added.

## What is protected

| Asset | Why it matters | Where it lives |
| --- | --- | --- |
| Tenants' and guarantors' personal data: identity, income documents, bank references, inventory photos | RGPD; a leak harms people who never chose Tenure | PostgreSQL (rows), archive bucket (documents) |
| The owner's finances: rents, charges, loans, tax summaries, journal and FEC | Confidential; a wrong figure becomes a wrong tax return | PostgreSQL, archive bucket |
| Issued documents (contracts, receipts, statements, summaries) | Legal evidence kept for 10 years; they must be provably unchanged | Archive bucket under object lock, hash in `documents` |
| The audit chain and its daily heads | Proves who changed what; tampering must be detectable | `audit_events` (INSERT-only), heads under object lock |
| Secrets: session key, Socrate client secret, database and S3 credentials, backup identity | Each one opens a door above | `/etc/tenure/*.env` on the host, the offline safe (`break-glass.md`) |
| Availability | Rent calls are due on the 1st; a missed run is visible to tenants | The single host |

## Actors

- **Owner, co-owner, accountant, SCI manager:** authenticated by Socrate. Their roles come from
  `owners` and `owner_grants`, never from a token claim.
- **Admin:** a Socrate role claim. It reads parameters and runs jobs; it does not bypass RLS.
- **Tenant, guarantor, candidate:** have no account in phase 1. Their data and files reach
  Tenure through the owner (uploads, typed data, bank statement lines).
- **Anonymous internet:** reaches Caddy only.
- **Attackers considered:** an outsider on the internet; another owner on the same instance; a
  grantee whose grant was revoked; a hostile file or text sent by a tenant or a syndic and
  uploaded by the owner; a compromised dependency or container image; a stolen laptop with an
  open session; an operator mistake on the host.
- **Out of scope:** a compromised Socrate (identity is delegated, so its compromise is total for
  Tenure: see A-7), a compromised hosting provider, and physical access to the data centre.

## Data flows and trust boundaries

```
 Browser ──TLS──▶ Caddy ──loopback──▶ tenure ──TLS──▶ PostgreSQL 17 (RLS, roles)
   │  cookie only     (headers, 30 MB)   │  BFF → auth → handler → service → repository
   │                                     ├──TLS──▶ Scaleway S3: archive (object lock), backups
   │                                     ├──loopback──▶ gotenberg (HTML → PDF/A)
   │                                     ├──loopback──▶ ClamAV (INSTREAM)
   │                                     ├──TLS──▶ SMTP relay (rent calls, receipts)
   │                                     ├──TLS──▶ INSEE (IRL series), Yousign (optional)
   └──redirect──▶ Socrate ◀──TLS (JWKS, token)── tenure (confidential client)
```

| Boundary | Crossing | Trust decision |
| --- | --- | --- |
| B1 internet → Caddy | Every request | TLS 1.2+, HSTS, body cap |
| B2 Caddy → tenure | Proxied request | Only from 127.0.0.1 (`TENURE_TRUSTED_PROXIES`) |
| B3 browser session → API | Cookie to bearer in `internal/bff` | Session valid, CSRF token on unsafe methods |
| B4 token → scope | `internal/auth` | Signature, issuer, audience; scope from `owners`/`owner_grants`; `step_up` freshness |
| B5 tenure → database | Each transaction | `app.owner_id` set first; RLS forced; role without BYPASSRLS or DDL |
| B6 uploaded file or imported text → archive or rows | Upload, CSV/OFX import, syndic import | Antivirus, sniffed type, size cap, typed parsing |
| B7 tenure → sidecars and providers | PDF rendering, scanning, mail, INSEE, Yousign | Loopback or TLS; failures fail closed |
| B8 operator → host | SSH, provider console | MFA, break-glass log |

## Threats and controls

| # | STRIDE | Threat | Control in place | Shown by | Residual |
| --- | --- | --- | --- | --- | --- |
| T1 | Spoofing | Forged or replayed token | RS256 against Socrate's JWKS; issuer and audience checked; unknown key or algorithm rejected | `internal/auth`, backendkit `jwtauth`; `TestEveryRouteRequiresAToken` | Low |
| T2 | Spoofing | Session theft from the browser | No token in the browser; `__Host-` HttpOnly Secure SameSite=Strict cookie; server-side encrypted session; logout revokes the refresh token | `internal/bff` tests, TestBFF | Stolen unlocked laptop: session lives until expiry (A-6) |
| T3 | Spoofing | Brute force or credential stuffing | No local password; Socrate holds MFA; 10 failed authentications per 15 min per address, then 429 | `TestFailedAuthLockout` | Low |
| T4 | Tampering | CSRF on a state-changing route | SameSite=Strict plus `X-CSRF-Token` on unsafe methods; JSON only | TestBFF | Low |
| T5 | Elevation | One owner reads or writes another's data | RLS forced on every owner table, `app.owner_id` set by the db layer; scope chosen by `X-Owner-Scope` among the caller's own grants | Cross-owner zero-row checks (`rlsZeroRows`), TestScenario | Low; a new table without a policy is caught by the check |
| T6 | Elevation | A revoked grantee keeps access | Grants are read on each request; a revoked grant is 403 at once | TestArchive | Low |
| T7 | Elevation | An accountant reads identity or guarantor data | Document classes filtered by role; RLS on guarantor tables | TestArchive | Low |
| T8 | Elevation | A sensitive action from a stale session (tax issue, grants, exports, params) | `step_up`: `auth_time` ≤ 5 min and `mfa` in `amr` | `internal/auth` tests | Low |
| T9 | Elevation | SCI manager role taken over | `manager` only through a live grant on an SCI; the creator alone during the founding transaction; the last manager cannot be revoked; self-revoke refused | TestSCI (Q-133) | Low |
| T10 | Tampering | An issued document changed in the bucket | Object lock in compliance mode; SHA-256 kept in `documents`; `archive.verify` re-hashes a weekly sample | `internal/document`, TestArchive | A mismatch is only a failed job run: A-1 |
| T11 | Tampering, repudiation | Audit rows edited or deleted to hide a change | INSERT-only grants and trigger; hash chain computed in Go; daily heads under object lock; weekly `audit.verify` | `internal/audit`, TestZAuditedRoutes | Detection depends on someone seeing the failed run: A-1 |
| T12 | Repudiation | A write with no trace | One audit event per write in the same transaction; every mutating route is listed and checked | `testdata/audited_routes.txt`, TestZAuditedRoutes | Low |
| T13 | Tampering | Malicious upload (malware, polyglot, oversized) | Quarantine prefix; ClamAV before archiving, fail closed; type sniffed; 25 MB cap; never served inline from the API; photos re-encoded without metadata | TestContracts, inventory photo tests | Signature-based scanning misses new malware |
| T14 | Tampering | Hostile text in imported or typed data ends up in a generated PDF | `html/template` escaping in every template; gotenberg on the loopback | `internal/document/render.go` | Chromium in gotenberg may still fetch an external URL if one reached a template unescaped: A-3 |
| T15 | Tampering | Hostile text in a CSV export runs as a spreadsheet formula on the accountant's machine | None: labels and names are written as typed | `internal/ledger/export.go`, `internal/tax`, `internal/audit/http.go` | A-2 |
| T16 | Information disclosure | A download link forwarded or leaked | Signed URLs valid 10 minutes, bound to the document and the caller; every issue audited | `internal/document/store.go` | Low |
| T17 | Information disclosure | Error bodies or logs leak data or stack traces | RFC 9457 problems without stack traces; slog JSON; no token logged | `internal/problem`, `internal/httpx` | Logs hold subjects and ids, kept 30 days |
| T18 | Information disclosure | Database or backup stolen | Encrypted volume; database over TLS; backups encrypted with age, identity offline; bucket SSE; data in France | `deploy/backup`, `deploy/README.md` | Backup identity custody is one person: A-5 |
| T19 | Information disclosure | Secret committed or pasted | gitleaks in CI; placeholders only in examples; secrets only in `/etc/tenure` mode 0640 | CI, `TestDeployTemplate` | Low |
| T20 | Denial of service | Request flood or large bodies | Caddy body cap; per-subject and per-address rate limits; timeouts on gotenberg | `internal/httpapi/router.go` | A single host: an outage stops the monthly run until it is back: A-4 |
| T21 | Denial of service | A sidecar or provider down (gotenberg, ClamAV, SMTP, INSEE, Socrate) | Fail closed with 503; jobs are keyed per period and retried on the next run; no login fallback | Q-101, Q-103, `job_runs` | Visible only in logs today: A-1 |
| T22 | Tampering | Compromised dependency or image | Pinned `go.sum` and `package-lock.json`; govulncheck and npm audit; Dependabot; SBOM per release; reproducible binary checked against the release | CI, release workflow | Sidecar images are not scanned (ASVS known gap) |
| T23 | Elevation | Operator or break-glass abuse | Separate roles; no RLS bypass for the API; break-glass logged in an issue; corrections go through the API | `deploy/runbooks/break-glass.md` | Relies on discipline; nothing alerts on a superuser login: A-1 |

## Actions

| # | Action | Owner | Before |
| --- | --- | --- | --- |
| A-1 | Alerting on the host. Today an "alert" is an error log line, read by no one. Route ERROR logs and failed `job_runs` (`audit.verify`, `archive.verify`, the monthly run), login-lockout bursts and superuser logins to a channel the owner reads, such as an e-mail or a Scaleway Cockpit alert. Test it once by failing a job. | Owner, host | Go-live |
| A-2 | Neutralise spreadsheet formulas in every CSV export: prefix a cell that starts with `=`, `+`, `-`, `@`, tab or CR. The FEC keeps its format, and its numeric columns are unaffected. | Code (own issue) | Go-live |
| A-3 | Restrict gotenberg's Chromium to local resources (deny every `http`/`https` URL), so a rendering can never reach the network. | Code (own issue) | Go-live |
| A-4 | Availability target and restore drill: time a full restore on a fresh host (`restore.md`) and record it; decide whether that delay is acceptable before the 1st of a month. | Owner | Phase 1 gate |
| A-5 | Backup identity custody: a second sealed copy of the age identity with a trusted person or a notary, so that losing one copy cannot lose the archive. | Owner | Go-live |
| A-6 | Session lifetime: confirm the BFF session and refresh token lifetimes in Socrate's client settings, and enable revocation of all sessions from Socrate's console. | Owner, Socrate | Go-live |
| A-7 | Socrate as a single point of trust: MFA enforced for owners and admins, admin role held by as few accounts as possible, Socrate's own runbooks reviewed. | Owner | Go-live |
| A-8 | Sign off the ASVS L2 checklist with the host checks done (`asvs-l2.md`). | Owner | Phase 1 gate |

## Accepted risks in phase 1

- **Erasure after retention** is listed, not done (`archive.retention`): object lock makes
  erasure before the end of retention impossible, which is the point of the archive.
- **No external penetration test** before the phase 4 tenant portal (RENT-SPEC-022).
- **One host**: no high availability. A restore from the nightly backup loses at most one day of
  writes.

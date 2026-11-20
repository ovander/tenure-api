# First install and go-live

The first production install, step by step, from an empty Scaleway project to the first real
monthly run. `deploy/README.md` lists what each step installs; this runbook gives the order, the
decisions to take first and the check that closes each step. Log the install in a GitHub issue
labelled `go-live`, ticking the boxes below, with no secret in it.

## 0. Decisions before starting

- [ ] Domain name of the instance, and who receives its mail (SPF and DKIM for the sender).
- [ ] Socrate production values (Q-01): issuer, JWKS URL, a confidential client for tenure,
      the admin role name. MFA is required for owners and admins (threat model A-7).
- [ ] The open questions marked **Needs answer** in `docs/OPEN-QUESTIONS.md` (Q-38, Q-72) are
      answered or knowingly accepted.
- [ ] The accountant's answers to the questionnaire (Q-132, Q-133), or the defaults knowingly
      kept for the first year.
- [ ] Who holds the offline safe, and who holds its second copy (`break-glass.md`, threat model
      A-5).
- [ ] The alert channel: the address or tool that receives the alerts of step 8.

## 1. Scaleway project (fr-par)

- [ ] A dedicated project. The console account has MFA, and an API key exists only for the
      buckets.
- [ ] Sign the provider's data processing agreement (DPA), and name the provider and the region
      in `docs/rgpd/registre-des-traitements.md`.
- [ ] One instance in fr-par, with a separate block volume for PostgreSQL. Encryption at rest:
      use the provider's volume encryption where the offer has it; otherwise use LUKS on the
      volume, with the passphrase kept in the safe.
- [ ] Security group: inbound 80 and 443 from anywhere, 22 from the admin address only, nothing
      else. PostgreSQL, gotenberg and ClamAV listen on the loopback only.
- [ ] DNS: an A (and AAAA) record of the domain points at the instance.

Check: `ss -tlnp` on the host shows only 22, 80 and 443 on a public address.

## 2. Host and database (`deploy/README.md` steps 1 and 2)

- [ ] Install the packages and create the users. Keep journald logs for 30 days.
- [ ] PostgreSQL 17 data directory on the encrypted volume. TLS on: `TENURE_DB_URL` uses
      `sslmode=verify-full`, so the server certificate must name the address tenure dials
      (`127.0.0.1`, or a host name in `/etc/hosts`), signed by a CA the tenure user trusts.
- [ ] `CREATE DATABASE tenure`, then `sql/roles.sql` and `sql/backup-role.sql`, with passwords
      generated on the host. `pg_hba.conf` allows `hostssl` and `scram-sha-256` only.

Check: `psql "postgres://tenure_api:…@127.0.0.1/tenure?sslmode=verify-full" -c 'select 1'`
connects; the same command with `sslmode=disable` is refused.

## 3. Buckets (`deploy/README.md` step 3)

- [ ] Archive bucket, created **with object lock enabled** (it cannot be turned on later):
      `aws s3api create-bucket --bucket <archive> --object-lock-enabled-for-bucket`, then
      versioning on, server-side encryption, and no default retention (tenure sets one per
      object).
- [ ] Backup bucket: server-side encryption, objects expired after 35 days, and a key that can
      only write.
- [ ] Two API keys, one per bucket, each limited to its bucket.

Check: putting an object with a compliance retention in the archive bucket, then deleting that
version, is refused.

## 4. Socrate (`deploy/README.md` step 4)

- [ ] Register tenure as a confidential client, with the redirect URI
      `https://<domain>/bff/callback`.
- [ ] Set the session and refresh token lifetimes, and check that the console can revoke every
      session of a user (threat model A-6).
- [ ] Give the admin role to as few accounts as possible.

## 5. Configuration (`deploy/README.md` step 5)

- [ ] Fill `/etc/tenure/tenure.env` and `/etc/tenure/backup.env` on the host only, owned
      `root:tenure` and `root:tenure-backup`, mode 0640.
- [ ] Generate `TENURE_SESSION_KEY` with `openssl rand -base64 32`.
- [ ] Generate the age key pair on a machine other than the host. Only the public key goes in
      `backup.env`; the identity goes into the safe and its second copy.
- [ ] Copy every secret into the safe as it is created.

Check: run `tenure serve` once by hand. It refuses to start on any missing or malformed value
and names it.

## 6. Sidecars, Caddy, first release (`deploy/README.md` steps 6 and 7)

- [ ] `systemctl enable --now tenure-sidecars tenure-backup.timer`. ClamAV takes several
      minutes to load its signatures the first time.
- [ ] Install the Caddyfile and set `TENURE_DOMAIN`. Caddy gets the certificate on its first
      request.
- [ ] Deploy the latest release with `deploy.sh` (`release.md`), after checking its
      `SHA256SUMS` and that its binary is the one CI rebuilt. Then run
      `systemctl enable tenure`.

Check: `tenure version` prints the tag, and `curl -fsS https://<domain>/readyz` succeeds.
`curl -sI https://<domain>/api/v1/me` returns 401 without a session, and the HSTS and CSP
headers are present.

## 7. First login and the owner's profile

- [ ] Log in through the browser. Socrate asks for MFA. Complete the profile (`PUT /me`), which
      creates the owner.
- [ ] Open the parameters screen: the legal parameters of the current year are present
      (`legal_params`, seeded by the migrations).
- [ ] Upload a test PDF on an asset: it is scanned and archived, and the EICAR test file is
      refused. Issue a test document, download it, and check that the link expires after
      10 minutes.

## 8. Alerts (threat model A-1)

tenure reports a failed job, a chain mismatch, a lockout burst or an unavailable sidecar as an
`ERROR` line in its JSON log; nothing forwards it yet. Before go-live:

- [ ] Forward the `tenure` and `tenure-backup` journal to the alert channel: a log alert in
      Scaleway Cockpit, or a timer that mails any new `"level":"ERROR"` line.
- [ ] Alert on a superuser login to PostgreSQL (`log_connections` with a filter on the role).
- [ ] Test the path: write a fake error into tenure's journal,
      `echo '{"level":"ERROR","msg":"alert test"}' | systemd-cat -t tenure`, and check that the
      alert arrives within the hour.

## 9. Backup and restore drill (threat model A-4)

- [ ] Wait for the first nightly backup, or start `tenure-backup.service`, and read its log.
- [ ] Restore it into a scratch database following `restore.md`, timing every step. Log the
      result in an issue labelled `restore-test`.
- [ ] Decide whether that duration is acceptable before the 1st of a month; if it is not,
      plan a warm standby.

## 10. Go-live: the phase 1 gate on the real portfolio

- [ ] Enter the real assets, leases, tenants and guarantees, and upload their documents.
- [ ] Run the first real month: rent calls on the 1st (`billing.monthly`), payments matched,
      receipts issued.
- [ ] After one real month, check that `audit.verify` passes over that month.
- [ ] Issue a real tax summary and accept it (on the last complete year, or on the first one
      once it closes).
- [ ] Sign off the ASVS checklist with the host checks done (`docs/security/asvs-l2.md`).
- [ ] Once every box above is ticked, the owner tags `v1.0.0`.

## Parameters to review each year

The legal parameters are dated; a new tax year needs its entries before its first summary.

- 2026 entries are seeded for the micro-foncier, the deficit, the deposits, the micro-BIC
  (83 600 €), the DPE floor and the lease terms.
- The 2044 line map of income year 2026 (millésime 2027) is added when the form is published, in
  spring 2027. Until then, a 2026 summary is refused with `tax/line-map-missing`.
- The IRL values are not parameters: `irl.fetch` reads them from INSEE every quarter.
- Q-136 lists the 2026 measures Tenure does not model yet.

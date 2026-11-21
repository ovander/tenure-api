# Deploying tenure

One Linux VPS in the EU (RENT-SPEC-022 residency): Caddy in front, `tenure` as a systemd
service, PostgreSQL 17 on an encrypted volume, gotenberg and ClamAV as loopback containers, the
archive and backup buckets at Scaleway fr-par (OD-04). RENT-BE-001 §8b and §9 are the
requirements; this directory is how they are met.

| Path | Holds |
| --- | --- |
| `Caddyfile` | TLS 1.2+, security headers, reverse proxy to `127.0.0.1:8080` |
| `systemd/tenure.service` | `tenure serve`, hardened, configured from `/etc/tenure/tenure.env` |
| `systemd/tenure-sidecars.service`, `sidecars.compose.yml` | gotenberg and ClamAV on the loopback |
| `systemd/tenure-backup.{service,timer}`, `backup/` | Nightly encrypted database backup and its restore |
| `tenure.env.example`, `backup/backup.env.example` | Every setting, with placeholders; real values only on the host |
| `deploy.sh` | Installs a release: checksum, migrate, swap, readiness, rollback of the binary |
| `sql/roles.sql`, `sql/backup-role.sql` | Database roles, run once per cluster |
| `runbooks/` | First install and go-live, a test install on a shared host, release and rollback, restore, breach (72 h), break-glass |
| `dev/` | Development only (docker-compose.dev.yml) |
| `test/` | A test install beside other services: capped sidecars, a local archive, Mailpit, a Caddy site to import (`runbooks/test-install.md`) |

## First install

`runbooks/first-install.md` gives the order, the decisions to take first and the check
that closes each step; the list below is what each step installs.

1. Host: an encrypted data volume for PostgreSQL; packages `postgresql-17`, `caddy`,
   `docker` with the compose plugin, `age`, `awscli`, `curl`. Users `tenure` and
   `tenure-backup` (no shell, no home). Technical logs are kept 30 days (the register):
   `MaxRetentionSec=30day` in `/etc/systemd/journald.conf`; the Caddyfile rolls its access
   log after 30 days.
2. Database: `CREATE DATABASE tenure`, then `sql/roles.sql` and `sql/backup-role.sql` with
   passwords generated on the host. `pg_hba.conf`: the tenure roles from `127.0.0.1` only,
   `hostssl`, `scram-sha-256`.
3. Buckets at fr-par:
   - the archive bucket, with versioning and object lock and no default retention;
   - the backup bucket, with SSE, a lifecycle rule that expires objects after 35 days, and a
     write-only key for `tenure-backup`.
4. Socrate: register tenure as a confidential client whose redirect URI is
   `https://<domain>/bff/callback`, with MFA required for owners and admins.
5. Configuration:
   - copy `tenure.env.example` to `/etc/tenure/tenure.env` and `backup/backup.env.example`
     to `/etc/tenure/backup.env`;
   - fill both on the host (`root:tenure` and `root:tenure-backup`, mode `0640`);
   - set `TENURE_DOMAIN` in `/etc/caddy/caddy.env`.
6. Copy this directory to `/opt/tenure/deploy` and the units to `/etc/systemd/system`, then
   run `systemctl enable --now tenure-sidecars tenure-backup.timer` and install
   `Caddyfile` as `/etc/caddy/Caddyfile`.
7. Deploy the first release with `deploy.sh` (`runbooks/release.md`), then run
   `systemctl enable tenure`.

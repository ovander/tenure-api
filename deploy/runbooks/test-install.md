# Test install on a shared host

A test install of tenure, for its owner alone and with **fictitious data only**, on a Linux host
that already serves other sites. It sits beside them without touching them:

- its own PostgreSQL 17 cluster, on port 5433;
- a site block imported into the host's Caddyfile;
- sidecars with memory caps, so that when memory runs short they are stopped, not the other
  services;
- an archive kept on the host (RustFS);
- e-mails caught by Mailpit.

This is not the production install. Before go-live with real tenants' data, follow
`first-install.md` on a dedicated host. The decisions to make first are in its step 0.

What a test host gives up, by design:

| Production (`first-install.md`) | This test install | Why |
| --- | --- | --- |
| Dedicated host | Shared with other services | No real tenant data, so isolation matters less |
| Scaleway bucket, object lock in compliance mode | RustFS on the loopback, object lock on | A document issued in a real bucket could not be deleted for 10 years |
| Encrypted database volume, nightly backup | Neither | Fictitious data; delete it all at the end |
| SMTP relay | Mailpit, read through an SSH tunnel | Rent calls never reach a real address |
| Database over TLS | Loopback without TLS (allowed by tenure only on loopback) | Same host |

Requirements: Ubuntu 24.04 or Debian 12+, root, Caddy already serving the host's sites, a
Socrate instance online, a DNS name for tenure. Plan for at least 2 GB of RAM plus 4 GB of swap.
ClamAV alone holds about 1.2 GB of signatures, and the PDF engine about 0.6 GB. On such a host,
PDFs and scans are slow but they work.

Every command runs as root. Set your own values once:

```bash
DOMAIN=tenure.example.fr          # the DNS name of the test instance
VERSION=v0.1.38                   # a tenure release that has deploy/test (v0.1.38 or later)
```

## 1. Swap, Docker and the release

```bash
fallocate -l 4G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile
echo '/swapfile none swap sw 0 0' >> /etc/fstab
apt-get update && apt-get install -y docker.io docker-compose-v2 curl
systemctl enable --now docker
ss -tlnp | grep -E ':(5433|8080|3000|3310|9000|1025|8025) ' || echo "ports free"
```

The last line must print `ports free`; if a port is taken, stop here.

The repository is private. Download the release assets on your machine, with `gh release download
$VERSION -R ovander/tenure-api` or from the release page, and copy them to `/root/tenure-$VERSION/`
on the host (`scp -P <ssh port> …`; create the folder first with `mkdir -p /root/tenure-$VERSION`). Then:

```bash
cd /root/tenure-$VERSION && sha256sum --check --ignore-missing SHA256SUMS
install -d /opt/tenure/deploy && tar -xzf "tenure-deploy-$VERSION.tar.gz" --strip-components=1 -C /opt/tenure/deploy
ls /opt/tenure/deploy/test/compose.yml
```

## 2. PostgreSQL 17 beside the existing cluster

```bash
apt-get install -y postgresql-common
/usr/share/postgresql-common/pgdg/apt.postgresql.org.sh -y
apt-get install -y postgresql-17
pg_lsclusters
```

`pg_lsclusters` shows the existing cluster unchanged and `17 main 5433 online`. If version 17 did
not get its own cluster, create it with `pg_createcluster 17 main --port 5433 --start`. Then
create the database and the roles, with passwords generated here:

```bash
install -d -m 0750 /etc/tenure
MIGRATE_PW=$(openssl rand -hex 24); API_PW=$(openssl rand -hex 24); JOBS_PW=$(openssl rand -hex 24)
runuser -u postgres -- psql -p 5433 -c 'CREATE DATABASE tenure'
runuser -u postgres -- psql -p 5433 -d tenure -v db=tenure \
  -v migrate_pw="$MIGRATE_PW" -v api_pw="$API_PW" -v jobs_pw="$JOBS_PW" -f /opt/tenure/deploy/sql/roles.sql
```

Steps 2 to 4 run in the same shell: the three passwords are only in its variables until step 4
writes them to `/etc/tenure/tenure.env`.

## 3. Socrate

In Socrate's console:
- register tenure as a **confidential client**, with redirect URI `https://$DOMAIN/bff/callback`;
- check that your account has MFA;
- keep the block of client settings the console gives (issuer, JWKS URL, client id, secret).

## 4. The configuration and the sidecars

```bash
useradd --system --no-create-home --shell /usr/sbin/nologin tenure
```

The secrets of the sidecars:

```bash
cat > /etc/tenure/test.env <<EOF
RUSTFS_ACCESS_KEY=tenure-test
RUSTFS_SECRET_KEY=$(openssl rand -hex 24)
EOF
chmod 0640 /etc/tenure/test.env
```

Fill in `/etc/tenure/tenure.env`. The values that differ from `tenure.env.example` come first:

```bash
. /etc/tenure/test.env
cat > /etc/tenure/tenure.env <<EOF
TENURE_HTTP_ADDR=127.0.0.1:8080
TENURE_PUBLIC_URL=https://$DOMAIN
TENURE_TRUSTED_PROXIES=127.0.0.1/32
TENURE_LOG_LEVEL=info
TENURE_DB_URL=postgres://tenure_api:$API_PW@127.0.0.1:5433/tenure?sslmode=disable
TENURE_JOBS_DB_URL=postgres://tenure_jobs:$JOBS_PW@127.0.0.1:5433/tenure?sslmode=disable
TENURE_MIGRATE_DB_URL=postgres://tenure_migrate:$MIGRATE_PW@127.0.0.1:5433/tenure?sslmode=disable
TENURE_SESSION_KEY=$(openssl rand -base64 32)
TENURE_STEP_UP_MAX_AGE=5m
TENURE_S3_ENDPOINT=127.0.0.1:9000
TENURE_S3_INSECURE=true
TENURE_S3_REGION=us-east-1
TENURE_S3_BUCKET=tenure-test
TENURE_S3_ACCESS_KEY=$RUSTFS_ACCESS_KEY
TENURE_S3_SECRET_KEY=$RUSTFS_SECRET_KEY
TENURE_PDF_ENGINE_URL=http://127.0.0.1:3000
TENURE_CLAMAV_ADDR=127.0.0.1:3310
TENURE_SMTP_ADDR=127.0.0.1:1025
TENURE_SMTP_FROM=no-reply@$DOMAIN
TENURE_SOCRATE_ADMIN_ROLE=tenure-admin
EOF
chown root:tenure /etc/tenure/tenure.env && chmod 0640 /etc/tenure/tenure.env
```

Then add Socrate's block to the end of `/etc/tenure/tenure.env`, as the console gives it:
`SOCRATE_ISSUER`, `SOCRATE_JWKS_URL`, `SOCRATE_CLIENT_ID`, `SOCRATE_CLIENT_SECRET`. Use an
editor, not the shell history.

Start the sidecars and create the archive bucket:

```bash
cd /opt/tenure/deploy/test
docker compose up -d
docker compose --profile init run --rm archive-init
docker compose ps
```

ClamAV takes several minutes to load its signatures. Wait until `docker compose ps` shows it
`healthy`.

## 5. tenure as a service

The sidecars unit of production starts `sidecars.compose.yml`. Install it with an override that
starts the test compose instead, then install tenure:

```bash
cp /opt/tenure/deploy/systemd/tenure-sidecars.service /opt/tenure/deploy/systemd/tenure.service /etc/systemd/system/
mkdir -p /etc/systemd/system/tenure-sidecars.service.d
cat > /etc/systemd/system/tenure-sidecars.service.d/test.conf <<'EOF'
[Service]
WorkingDirectory=/opt/tenure/deploy/test
ExecStart=
ExecStart=/usr/bin/docker compose -f compose.yml up -d --wait
ExecStop=
ExecStop=/usr/bin/docker compose -f compose.yml down
EOF
mkdir -p /etc/systemd/system/tenure.service.d
cat > /etc/systemd/system/tenure.service.d/test.conf <<'EOF'
[Unit]
After=postgresql@17-main.service
EOF
systemctl daemon-reload && systemctl enable --now tenure-sidecars
cd /root/tenure-$VERSION && /opt/tenure/deploy/deploy.sh "$VERSION" .
systemctl enable tenure
```

`deploy.sh` applies the migrations, starts tenure and waits for `/readyz`. If it fails, read
`journalctl -u tenure -n 50`: tenure names any setting it refuses.

## 6. Caddy

Add the tenure site to the existing Caddy, leaving its other sites as they are:

```bash
sed "s/TENURE_DOMAIN/$DOMAIN/" /opt/tenure/deploy/test/tenure.caddy > /etc/caddy/tenure.caddy
grep -q 'import /etc/caddy/tenure.caddy' /etc/caddy/Caddyfile || echo 'import /etc/caddy/tenure.caddy' >> /etc/caddy/Caddyfile
caddy validate --config /etc/caddy/Caddyfile && systemctl reload caddy
```

With the DNS record of `$DOMAIN` pointing at the host, Caddy gets the certificate on the first
request.

Check that the instance answers without a session:
- `curl -fsS https://$DOMAIN/readyz` succeeds;
- `curl -sI https://$DOMAIN/api/v1/me` returns 401.

## 7. First login and a test run

1. Open `https://$DOMAIN`. You are redirected to Socrate and asked for MFA. Complete your profile.
2. Enter a fictitious asset, tenant and lease, then issue a rent call.
3. Read the e-mails through a tunnel: run `ssh -p <ssh port> -L 8025:127.0.0.1:8025 <host>` from
   your machine, then open `http://127.0.0.1:8025`.
4. Upload a PDF; it is scanned and archived.

## Remove the test install

```bash
systemctl disable --now tenure tenure-sidecars
(cd /opt/tenure/deploy/test && docker compose down -v)
pg_dropcluster 17 main --stop
rm -rf /opt/tenure /etc/tenure /etc/caddy/tenure.caddy /etc/systemd/system/tenure*
sed -i '\#import /etc/caddy/tenure.caddy#d' /etc/caddy/Caddyfile && systemctl reload caddy
systemctl daemon-reload && userdel tenure
```

The swap file and Docker stay. Remove them only if nothing else uses them.

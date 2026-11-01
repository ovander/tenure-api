#!/usr/bin/env bash
# Installs a tenure release on the host (RENT-BE-001 §9), as root:
#   deploy/deploy.sh <version> [<release dir>]
# The release dir holds tenure-<version>-linux-amd64 and SHA256SUMS from the
# GitHub release (default: the current directory). Steps:
#   1. check the binary against SHA256SUMS;
#   2. install it under /opt/tenure/releases/<version>;
#   3. apply the migrations with the NEW binary while the old one still serves
#      (migrations are additive within a release, so the old binary keeps working);
#   4. swap /opt/tenure/current and restart tenure;
#   5. wait for /readyz, else roll the symlink back to the previous binary.
# A migration is never reverted automatically: see deploy/runbooks/release.md.
set -euo pipefail

version=${1:?usage: deploy.sh <version> [<release dir>]}
src=${2:-.}
bin="tenure-${version}-linux-amd64"
root=/opt/tenure
dest="$root/releases/$version"
health=${TENURE_READY_URL:-http://127.0.0.1:8080/readyz}

[ "$(id -u)" -eq 0 ] || { echo "deploy: run as root" >&2; exit 1; }
cd "$src"
grep " ${bin}\$" SHA256SUMS | sha256sum --check --strict -

install -d -m 0755 "$dest"
install -m 0755 "$bin" "$dest/tenure"
previous=$(readlink -f "$root/current" || true)

echo "deploy: migrating with $version"
set -a
# shellcheck disable=SC1091 # host file, not in the repository
. /etc/tenure/tenure.env
set +a
runuser -u tenure -- "$dest/tenure" migrate up
runuser -u tenure -- "$dest/tenure" migrate status

ln -sfn "$dest" "$root/current.new"
mv -T "$root/current.new" "$root/current"
systemctl restart tenure

for _ in $(seq 1 30); do
  if curl -fsS --max-time 3 "$health" >/dev/null; then
    echo "deploy: $version ready"
    exit 0
  fi
  sleep 2
done

echo "deploy: $version not ready after 60 s" >&2
if [ -n "$previous" ] && [ -d "$previous" ]; then
  ln -sfn "$previous" "$root/current.new"
  mv -T "$root/current.new" "$root/current"
  systemctl restart tenure
  echo "deploy: rolled back to $previous; the database keeps $version's migrations (runbooks/release.md)" >&2
fi
exit 1

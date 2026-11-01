#!/usr/bin/env bash
# Replaces web/dist with the tenure-web release pinned in web/VERSION
# (RENT-BE-001 §9) and checks its SHA-256 before unpacking. It needs the gh CLI
# with read access to ovander/tenure-web (GH_TOKEN). It refuses an unpinned
# VERSION ("none"): a release never ships the placeholder page (Q-96).
set -euo pipefail
cd "$(dirname "$0")/.."

line=$(grep -v '^[[:space:]]*#' web/VERSION | grep -v '^[[:space:]]*$')
if [ "$line" = "none" ]; then
  echo "fetch-web: web/VERSION pins no tenure-web release" >&2
  exit 1
fi
read -r tag sum extra <<<"$line"
if ! [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || ! [[ "$sum" =~ ^[0-9a-f]{64}$ ]] || [ -n "${extra:-}" ]; then
  echo "fetch-web: malformed web/VERSION: $line" >&2
  exit 1
fi

asset="tenure-web-${tag}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
gh release download "$tag" --repo ovander/tenure-web --pattern "$asset" --dir "$tmp"
echo "${sum}  ${tmp}/${asset}" | sha256sum --check --strict -

mkdir "$tmp/dist"
# The archive holds the built files at its root; no absolute path or "..".
if tar -tzf "$tmp/$asset" | grep -E '(^/|(^|/)\.\.(/|$))' >/dev/null; then
  echo "fetch-web: unsafe path in $asset" >&2
  exit 1
fi
tar -xzf "$tmp/$asset" -C "$tmp/dist" --no-same-owner --no-same-permissions
test -f "$tmp/dist/index.html" || { echo "fetch-web: $asset has no index.html" >&2; exit 1; }

rm -rf web/dist
mv "$tmp/dist" web/dist
echo "fetch-web: embedded tenure-web $tag"

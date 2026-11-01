#!/usr/bin/env bash
# Builds a stamped tenure binary (RENT-BE-001 §9) into dist/:
#   scripts/build.sh <version>
# VERSION is the release tag; the commit and the build time come from git.
# Static (CGO off) for linux/amd64 unless GOOS/GOARCH say otherwise.
set -euo pipefail
cd "$(dirname "$0")/.."

version=${1:?usage: scripts/build.sh <version>}
commit=$(git rev-parse HEAD)
date=$(git show -s --format=%cI HEAD)
pkg=github.com/ovander/backendkit/buildinfo
goos=${GOOS:-linux}
goarch=${GOARCH:-amd64}

mkdir -p dist
CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath \
  -ldflags "-s -w -X ${pkg}.Version=${version} -X ${pkg}.GitCommit=${commit} -X ${pkg}.BuildTime=${date}" \
  -o "dist/tenure-${version}-${goos}-${goarch}" ./cmd/tenure
echo "dist/tenure-${version}-${goos}-${goarch}"

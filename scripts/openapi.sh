#!/bin/sh
# Regenerates internal/httpapi/openapi/swagger.json from the swag annotations
# (swag v1.16.4). CI runs it and fails when the committed file differs.
set -eu
cd "$(dirname "$0")/.."
swag init \
  --dir internal/httpapi,internal/portfolio,internal/lease,internal/params,internal/audit,internal/jobs,internal/billing,internal/document,internal/payment,internal/charge,internal/tax,internal/inventory,internal/period,internal/money,internal/problem,internal/httpx,internal/auth \
  -g router.go --parseInternal --outputTypes json --output internal/httpapi/openapi --quiet

#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"
for tool in go npm; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "Missing local tool: $tool. Install it before running tests." >&2
    exit 1
  fi
done
go test -race ./cmd/... ./internal/...
npm ci --prefix web
npm run build --prefix web
if [[ ${RUN_BROWSER_TESTS:-0} == 1 ]]; then
  (cd web && npx playwright install chromium && npm test)
fi
if [[ ${VALIDATE_DEPLOY:-0} == 1 ]]; then
  base=(--env-file deploy/.env.example -f deploy/compose.yaml)
  common=(DATA_DIR=/tmp/swarmjet-compose-validation
    DATABASE_URL=postgres://swarmjet:validation-only@postgres:5432/swarmjet?sslmode=disable
    POSTGRES_PASSWORD=validation-only ADMIN_PASSWORD=validation-only-password
    SWARMJET_MASTER_KEY=validation-only-master)
  env "${common[@]}" docker compose "${base[@]}" config --quiet
  env "${common[@]}" docker compose "${base[@]}" -f deploy/compose.cells.yaml config --quiet
  env "${common[@]}" docker compose "${base[@]}" --profile tunnel config --quiet
  env "${common[@]}" docker compose "${base[@]}" --profile observability config --quiet
  env "${common[@]}" docker compose "${base[@]}" -f deploy/compose.build.yaml --profile tools config --quiet
fi

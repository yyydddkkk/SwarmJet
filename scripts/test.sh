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
  for mode in image build local-node; do
    manifests=(-f deploy/compose.yaml)
    if [[ $mode == build ]]; then manifests+=(-f deploy/compose.build.yaml); fi
    if [[ $mode == local-node ]]; then manifests+=(-f deploy/compose.build.yaml -f deploy/compose.local-node.yaml); fi
    env DATABASE_URL='postgres://swarmjet:validation-only@postgres:5432/swarmjet?sslmode=disable' \
      POSTGRES_PASSWORD=validation-only RABBITMQ_PASSWORD=validation-only-rabbitmq ADMIN_PASSWORD=validation-only-password \
      RUNTIME_ADMIN_SECRET=validation-only-secret SWARMJET_MASTER_KEY=validation-only-master \
      DATA_DIR=/tmp/swarmjet-compose-validation \
      docker compose "${manifests[@]}" --profile tools config --quiet
  done
fi

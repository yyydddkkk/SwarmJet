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
  for mode in image build builder; do
    manifests=(-f deploy/compose.yaml)
    if [[ $mode == build ]]; then manifests+=(-f deploy/compose.build.yaml); fi
    if [[ $mode == builder ]]; then manifests+=(-f deploy/compose.builder.yaml -f deploy/compose.registry.yaml); fi
    env DATABASE_URL='postgres://swarmjet:validation-only@postgres:5432/swarmjet?sslmode=disable' \
      POSTGRES_PASSWORD=validation-only RABBITMQ_PASSWORD=validation-only-rabbitmq ADMIN_PASSWORD=validation-only-password \
      RUNTIME_ADMIN_SECRET=validation-only-secret SWARMJET_MASTER_KEY=validation-only-master \
      REGISTRY_BIND_HOST=10.0.0.20 REGISTRY_PORT=5443 REGISTRY_TLS_DIR=/tmp/swarmjet-registry/tls \
      REGISTRY_AUTH_FILE=/tmp/swarmjet-registry/htpasswd REGISTRY_HTTP_SECRET=validation-only-secret \
      REGISTRY_BUILDKIT_CONFIG=/tmp/swarmjet-registry/buildkitd.toml REGISTRY_CA_FILE=/tmp/swarmjet-registry/tls/ca.crt \
      REGISTRY_PUSH_USER=builder REGISTRY_PUSH_PASSWORD=validation-only-push \
      REGISTRY_PULL_USER=reader REGISTRY_PULL_PASSWORD=validation-only-pull \
      DATA_DIR=/tmp/swarmjet-compose-validation \
      docker compose "${manifests[@]}" --profile tools config --quiet
  done
fi

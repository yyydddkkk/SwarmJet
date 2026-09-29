#!/usr/bin/env bash
# Usage: ./update.sh [fully-qualified-app-image [fully-qualified-agent-image]]
# Edit .env to persist image pins; arguments override them for this invocation only.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
if [[ $# -gt 2 ]]; then echo 'Usage: ./update.sh [app-image [agent-image]]' >&2; exit 2; fi
if [[ ! -f .env ]]; then echo 'Create deploy/.env from .env.example first.' >&2; exit 2; fi
if [[ ! -f .env.registry ]]; then echo 'Registry settings missing; run deploy/install.py first.' >&2; exit 2; fi
if ! grep -Eq '^UPDATER_SECRET=.+$' .env; then
  printf '\nUPDATER_SECRET=%s\n' "$(python3 -c 'import secrets; print(secrets.token_hex(32))')" >> .env
fi
if ! grep -Eq '^UPDATER_DEPLOY_DIR=.+$' .env; then
  printf '\nUPDATER_DEPLOY_DIR=%s\n' "$PWD" >> .env
fi
if ! grep -Eq '^RABBITMQ_PASSWORD=.+$' .env; then
  # Existing installations predate this dependency. Persist a new independent
  # Broker credential before Compose expands the new service definition.
  printf '\nRABBITMQ_PASSWORD=%s\n' "$(python3 -c 'import secrets; print(secrets.token_hex(24))')" >> .env
fi
if [[ $# -ge 1 ]]; then export IMAGE="$1"; fi
if [[ $# -ge 2 ]]; then export AGENT_IMAGE="$2"; fi
compose=(docker compose --env-file .env --env-file .env.registry -f compose.yaml -f compose.builder.yaml -f compose.registry.yaml)
"${compose[@]}" config --quiet
"${compose[@]}" --profile tools pull
# Schema updates and app migrations run before their dependent services start.
# Never down the stack or remove volumes; existing workflow history is retained.
"${compose[@]}" up -d postgres rabbitmq
"${compose[@]}" run --rm temporal-schema
"${compose[@]}" up -d temporal
"${compose[@]}" run --rm temporal-namespace
"${compose[@]}" run --rm migrate
"${compose[@]}" up -d --wait api worker temporal-ui builder buildkit local-node registry gateway updater
"${compose[@]}" exec -T api swarmjet doctor
"${compose[@]}" ps

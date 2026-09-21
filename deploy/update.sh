#!/usr/bin/env bash
# Usage: ./update.sh [fully-qualified-app-image [fully-qualified-agent-image]]
# Edit .env to persist image pins; arguments override them for this invocation only.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
if [[ $# -gt 2 ]]; then echo 'Usage: ./update.sh [app-image [agent-image]]' >&2; exit 2; fi
if [[ ! -f .env ]]; then echo 'Create deploy/.env from .env.example first.' >&2; exit 2; fi
if [[ $# -ge 1 ]]; then export IMAGE="$1"; fi
if [[ $# -ge 2 ]]; then export AGENT_IMAGE="$2"; fi
docker compose config --quiet
docker compose --profile tools pull
# Schema updates and app migrations run before their dependent services start.
# Never down the stack or remove volumes; existing workflow history is retained.
docker compose up -d postgres
docker compose run --rm temporal-schema
docker compose up -d temporal
docker compose run --rm temporal-namespace
docker compose run --rm migrate
docker compose up -d --wait api worker temporal-ui
docker compose exec -T api swarmjet doctor
docker compose ps

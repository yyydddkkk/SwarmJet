#!/usr/bin/env bash
# Copies the Hive console screenshots Playwright writes into test-results/ over
# the reference images under docs/claude/round1-web-console/.
#
#   cd web && npx playwright test tests/hive-console.spec.ts
#   ./scripts/copy-screenshots.sh
set -euo pipefail

here="$(cd "$(dirname "$0")/.." && pwd)"
dest="$here/../docs/claude/round1-web-console"

# The screenshots the ADR/round notes link to; the spec also shoots a couple of
# intermediate states that are not part of the published set.
shots=(
  hive-list-1440.png
  hive-list-empty-1440.png
  hive-conversation-1440.png
  forager-conversation-1440.png
  forager-handoff-1440.png
  icon-lab-1440.png
  pixel-icons-1440.png
  forager-avatars-1440.png
)

mkdir -p "$dest"
for name in "${shots[@]}"; do
  src="$(find "$here/test-results" -name "$name" -print -quit)"
  if [ -z "$src" ]; then
    echo "missing $name — run the hive-console spec first" >&2
    exit 1
  fi
  cp "$src" "$dest/$name"
  echo "updated $name"
done
echo "${#shots[@]} screenshots copied to docs/claude/round1-web-console"

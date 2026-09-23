#!/usr/bin/env bash
# Build-time installation only. Does not launch agents or invoke a model.
set -euo pipefail
IFS=',' read -r -a selected <<< "${AGENT_CLIS:-opencode}"
for agent in "${selected[@]}"; do
  case "$agent" in
    opencode) package="opencode-ai@${OPENCODE_VERSION}" ;;
    codex) package="@openai/codex@${CODEX_VERSION}" ;;
    claude) package="@anthropic-ai/claude-code@${CLAUDE_VERSION}" ;;
    dst)
      npm install --global --registry "${NPM_REGISTRY}" "@deepseek-ai/dsh@${DSH_VERSION}" "@deepseek-harness-tui/dsh-tui@${DST_VERSION}"
      continue ;;
    agy)
      # Pinned official release artifacts and SHA-512 values from the official
      # installer manifests; do not execute the auto-updating installer.
      case "${TARGETARCH:-$(dpkg --print-architecture)}" in
        amd64)
          artifact="linux-x64/cli_linux_x64.tar.gz"
          checksum="28d0f8661316850d1e1ff541dc5f7727e5df6ea9f2d28a0c9b5567aa638a778b21d9229123c65ee5b5c646d4390b589245b1fdc0004e98f0cd1774aa38147762" ;;
        arm64)
          artifact="linux-arm/cli_linux_arm64.tar.gz"
          checksum="4a26e6cb40f68995d3a556a5141eb66b1de23fa1a8a6476e0a2e6976ab19df9c0e257cb86e73c48ada63bd8d1fa1971a3e81fd233da33a953776d1744043e3ea" ;;
        *) echo "Unsupported agy architecture" >&2; exit 1 ;;
      esac
      stage=$(mktemp -d)
      curl -fsSL "https://storage.googleapis.com/antigravity-public/antigravity-cli/1.2.8-4907747922280448/${artifact}" -o "$stage/agy.tar.gz"
      printf '%s  %s\n' "$checksum" "$stage/agy.tar.gz" | sha512sum -c -
      tar -xzf "$stage/agy.tar.gz" -C "$stage" antigravity
      install -m 0755 "$stage/antigravity" /usr/local/bin/agy
      rm -rf "$stage"
      continue ;;
    '') continue ;;
    *) echo "Unsupported AGENT_CLIS entry: $agent" >&2; exit 1 ;;
  esac
  npm install --global --registry "${NPM_REGISTRY}" "$package"
done

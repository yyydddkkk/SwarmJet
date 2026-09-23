package swarmjet

import "embed"

// AgentImageAssets embeds only the reviewed build recipe, never a workspace or
// deployment environment. This also makes builds work in packaged Workers.
//
//go:embed deploy/Dockerfile.agent scripts/install-agent-clis.sh scripts/askpass.sh
var AgentImageAssets embed.FS

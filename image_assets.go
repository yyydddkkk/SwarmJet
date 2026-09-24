package swarmjet

import "embed"

// AgentImageAssets embeds only the reviewed build recipe, never a workspace or
// deployment environment. This also makes builds work in packaged Workers.
//
//go:embed deploy/Dockerfile.agent scripts/install-agent-clis.sh scripts/askpass.sh
var AgentImageAssets embed.FS

// NodeDeploymentAssets contains the reviewed, version-matched node installer.
// Deployment credentials and CA are added only when an administrator downloads
// a private bundle; they are never embedded in the application image.
//
//go:embed deploy/install_node.py deploy/compose.node.yaml
var NodeDeploymentAssets embed.FS

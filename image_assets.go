package swarmjet

import "embed"

// NodeDeploymentAssets contains the reviewed, version-matched node installer.
// The node secret is added only when an administrator downloads a private
// bundle; it is never embedded in the application image.
//
//go:embed deploy/install_node.py deploy/compose.node.yaml
var NodeDeploymentAssets embed.FS

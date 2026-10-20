// Package methodologies embeds the built-in template sources. The catalog
// package interprets them; keeping this bridge here avoids duplicate assets.
package methodologies

import "embed"

// Files contains catalog metadata and template directories. README.md files
// are human documentation and are excluded by the catalog loader.
//
//go:embed catalog.yaml */*
var Files embed.FS

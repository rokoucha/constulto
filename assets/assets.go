package assets

import "embed"

// FS contains the versioned contracts and prompt shipped with the binary.
//
//go:embed schema/*.json prompts/*.md skill/*/SKILL.md
var FS embed.FS

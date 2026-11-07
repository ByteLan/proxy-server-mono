package webui

import "embed"

// Files contains production frontend assets, embedded at build time.
//
//go:embed all:dist
var Files embed.FS

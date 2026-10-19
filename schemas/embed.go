// Package schemas embeds the Query Table schema documents, so the app and its
// tests read these files directly instead of copies that can fall behind.
package schemas

import "embed"

//go:embed *.schema.json
var Documents embed.FS

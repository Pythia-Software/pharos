// Package pricing embeds the reviewed API price history, so the app and its
// tests read this file directly instead of a copy that can fall behind.
package pricing

import _ "embed"

//go:embed cost_changes.json
var CostChanges []byte

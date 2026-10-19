// Package carbon embeds the CO2 factors, so the app and its tests read this
// file directly instead of a copy that can fall behind.
package carbon

import _ "embed"

//go:embed co2_factors.json
var Factors []byte

// Package estimation holds the versioned assumptions shipped with Pharos.
// Local calibration never modifies this globally published manifest.
package estimation

import _ "embed"

//go:embed assumptions.json
var Assumptions []byte

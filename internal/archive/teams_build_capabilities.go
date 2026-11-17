package archive

import "github.com/gbdubs/pharos/internal/teams"

// Capability IDs are announced only when the corresponding end-to-end
// implementation and its safety gates are present in the compiled binary.
// The version is the existing release ldflag, never synthesized for dev runs.
type teamsCapabilityManifest = teams.CapabilityManifest

const teamsFeatureBuildMinimum = "0.10.0"

func teamsBuildCapabilities() teamsCapabilityManifest {
	m := teamsCapabilityManifest{SchemaVersion: 1, AppVersion: pushVersion, Capabilities: []string{}}
	// Source v2 ownership, deterministic worker and scoped artifacts are not
	// announced until their complete gates are connected. A 0.10.0 version
	// string alone must never imply any of these capabilities.
	return m
}

func teamsBuildEligible(m teamsCapabilityManifest, required string) bool {
	if m.SchemaVersion != 1 || !teamsVersionAtLeast(m.AppVersion, teamsFeatureBuildMinimum) {
		return false
	}
	for _, capability := range m.Capabilities {
		if capability == required {
			return true
		}
	}
	return false
}

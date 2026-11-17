package archive

import "github.com/gbdubs/pharos/internal/teams"

// Capability IDs are announced only when the corresponding end-to-end
// implementation and its safety gates are present in the compiled binary.
// The version is the existing release ldflag, never synthesized for dev runs.
type teamsCapabilityManifest = teams.CapabilityManifest

const teamsFeatureBuildMinimum = "0.10.0"

// Keep advertisement off until the matched service release and signed
// two-contributor publication path have passed end-to-end verification.
const teamsManualReportSeriesReady = false

func teamsBuildCapabilities() teamsCapabilityManifest {
	m := teamsCapabilityManifest{SchemaVersion: 1, AppVersion: pushVersion, Capabilities: []string{"deterministic_query_v1", "export_deterministic_v1", "recurring_deterministic_v1", "report_deterministic_v1", "scoped_artifact_v1", "source_snapshot_v2"}}
	if teamsManualReportSeriesReady {
		m.V2OnlyFeatures = []string{"report_deterministic_v2"}
	}
	return m
}

func teamsBuildEligible(m teamsCapabilityManifest, required string) bool {
	if (m.SchemaVersion != 1 && m.SchemaVersion != 2) || !teamsVersionAtLeast(m.AppVersion, teamsFeatureBuildMinimum) {
		return false
	}
	capabilities := m.Capabilities
	if m.SchemaVersion == 2 {
		capabilities = m.Features
	}
	if m.SchemaVersion == 1 && required == "report_deterministic_v2" {
		capabilities = m.V2OnlyFeatures
	}
	for _, capability := range capabilities {
		if capability == required {
			return true
		}
	}
	return false
}

func teamsNumericBuildEligible(m teamsCapabilityManifest) bool {
	return teamsBuildEligible(m, "source_snapshot_v2") && teamsBuildEligible(m, "deterministic_query_v1")
}

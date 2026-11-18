package archive

import (
	"slices"
	"testing"
)

func TestTeamsFeatureBuildRequiresRealVersionAndCapability(t *testing.T) {
	for _, tc := range []struct {
		version string
		caps    []string
		want    bool
	}{
		{"", []string{"scoped_artifact_v1"}, false},
		{"0.9.9", []string{"scoped_artifact_v1"}, false},
		{"0.10.0", nil, false},
		{"0.10.0", []string{"scoped_artifact_v1"}, true},
	} {
		m := teamsCapabilityManifest{SchemaVersion: 1, AppVersion: tc.version, Capabilities: tc.caps}
		if got := teamsBuildEligible(m, "scoped_artifact_v1"); got != tc.want {
			t.Fatalf("%q %v: %v", tc.version, tc.caps, got)
		}
	}
	got := teamsBuildCapabilities()
	if len(got.Capabilities) != 6 || got.Capabilities[0] != "deterministic_query_v1" || got.Capabilities[1] != "export_deterministic_v1" || got.Capabilities[2] != "recurring_deterministic_v1" || got.Capabilities[3] != "report_deterministic_v1" || got.Capabilities[4] != "scoped_artifact_v1" || got.Capabilities[5] != "source_snapshot_v2" {
		t.Fatal("unexpected capability advertisement")
	}
}

func TestManualSeriesAdvertisementUsesReleaseVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		enabled bool
	}{{"", false}, {"dev", false}, {"0.9.9", false}, {"0.10.0", true}, {"1.0.0", true}} {
		t.Run(tc.version, func(t *testing.T) {
			override(t, &pushVersion, tc.version)
			m := teamsBuildCapabilities()
			legacy := []string{"deterministic_query_v1", "export_deterministic_v1", "recurring_deterministic_v1", "report_deterministic_v1", "scoped_artifact_v1", "source_snapshot_v2"}
			if !slices.Equal(m.Capabilities, legacy) || len(m.Profiles) != 0 {
				t.Fatal("ordinary or source-aware capability declaration changed")
			}
			want := []string{"reader_labels_v1", "report_deterministic_v2"}
			if tc.enabled && !slices.Equal(m.V2OnlyFeatures, want) {
				t.Fatalf("missing v2 presentation and report features: %v", m.V2OnlyFeatures)
			}
			if !tc.enabled && len(m.V2OnlyFeatures) != 0 {
				t.Fatalf("ineligible build advertised v2 features: %v", m.V2OnlyFeatures)
			}
			if slices.Contains(m.Capabilities, "reader_labels_v1") || slices.Contains(m.Capabilities, "report_deterministic_v2") {
				t.Fatal("v2 features leaked into v1 manifest")
			}
			if teamsBuildEligible(m, "report_deterministic_v2") != tc.enabled {
				t.Fatal("series runtime gate differs from advertisement")
			}
		})
	}
}

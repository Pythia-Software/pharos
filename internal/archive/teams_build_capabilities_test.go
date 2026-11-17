package archive

import "testing"

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
	if len(got.Capabilities) != 5 || got.Capabilities[0] != "deterministic_query_v1" || got.Capabilities[1] != "export_deterministic_v1" || got.Capabilities[2] != "recurring_deterministic_v1" || got.Capabilities[3] != "scoped_artifact_v1" || got.Capabilities[4] != "source_snapshot_v2" {
		t.Fatal("unexpected capability advertisement")
	}
}

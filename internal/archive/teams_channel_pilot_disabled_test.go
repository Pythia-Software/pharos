//go:build !pharos_channel_report_pilot

package archive

import (
	"slices"
	"testing"
)

func TestDefaultBuildDoesNotAdvertiseChannelPilot(t *testing.T) {
	override(t, &pushVersion, "0.10.0")
	if teamsChannelReportPilotEnabled || slices.Contains(teamsBuildCapabilities().V2OnlyFeatures, teamsChannelReportFeature) {
		t.Fatal("default build enabled or advertised the channel pilot")
	}
}

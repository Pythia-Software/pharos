//go:build pharos_channel_report_pilot

package archive

import (
	"slices"
	"testing"
)

func TestChannelPilotBuildAdvertisesReviewedFeature(t *testing.T) {
	override(t, &pushVersion, "0.10.0")
	m := teamsBuildCapabilities()
	if !teamsChannelReportPilotEnabled || !slices.Contains(m.V2OnlyFeatures, teamsChannelReportFeature) {
		t.Fatal("pilot build did not advertise the reviewed channel capability")
	}
}

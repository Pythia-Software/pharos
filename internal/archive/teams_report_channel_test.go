package archive

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestTeamsChannelReportPublicationClosedArm(t *testing.T) {
	target := teamsChannelTarget{WorkspaceID: "TTEST", InstallationID: "0a000000-0000-4000-8000-000000000001", ChannelID: "CTEST", Visibility: "public"}
	pub, err := newTeamsChannelReportPublication("0b000000-0000-4000-8000-000000000001", target, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(pub)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/channel_report.json")
	if err != nil || !bytes.Equal(raw, bytes.TrimSpace(fixture)) {
		t.Fatal("paired channel fixture changed", err)
	}
	if _, err := parseTeamsChannelReportPublication(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := parseTeamsReportSeries(raw); err == nil {
		t.Fatal("legacy series accepted channel arm")
	}
	if lines := teamsChannelReviewLines(pub, "Synthetic total"); len(lines) != 4 || !strings.Contains(strings.Join(lines, " "), "join can read retained reports") {
		t.Fatal("public review omitted audience disclosure")
	}
	private := target
	private.Visibility = "private"
	privatePub, err := newTeamsChannelReportPublication("0b000000-0000-4000-8000-000000000001", private, "synthetic")
	if err != nil || len(teamsChannelReviewLines(privatePub, "Synthetic total")) != 3 {
		t.Fatal("private review incorrect")
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"visibility":"public"`, `"visibility":"private"`, 1),
		strings.Replace(string(raw), `"channel_id":"CTEST"`, `"channel_id":"COTHER"`, 1),
		strings.Replace(string(raw), `"kind":"slack_report_link"`, `"kind":"web"`, 1),
		strings.Replace(string(raw), `"triggers":[]`, `"triggers":null`, 1),
		strings.Replace(string(raw), `"disclosure_revision":1`, `"disclosure_revision":2`, 1),
		strings.Replace(string(raw), `"schema_version":2`, `"schema_version":2,"extra":true`, 1),
		strings.Replace(string(raw), `"target":`, `"Target":`, 1),
		string(raw) + ` {}`,
	} {
		if _, err := parseTeamsChannelReportPublication([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

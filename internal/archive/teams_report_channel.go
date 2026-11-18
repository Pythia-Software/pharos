package archive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"

	"github.com/gbdubs/pharos/internal/teams"
)

type teamsChannelOutputs struct {
	Fields   []teamsChannelField `json:"fields"`
	Coverage []string            `json:"coverage"`
}
type teamsChannelField struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	MetricID string `json:"metric_id"`
	Unit     string `json:"unit"`
	Merge    string `json:"merge"`
}

func teamsChannelValidID(id string) bool { _, err := teams.WorkPath(id, "status"); return err == nil }
func teamsChannelDecode(raw []byte, out any) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(out) == nil
}

const teamsChannelReportFeature = "report_slack_channel_v1"

// This is only set by synthetic integration tests until the full channel
// publication and delivery path is reviewed and explicitly enabled.
var teamsChannelReportPilotEnabled = teamsChannelReportPilotBuild

// teamsChannelTarget binds a report to a single installation and visibility class.
// Names can change and are never used to decide access.
type teamsChannelTarget struct {
	WorkspaceID    string `json:"workspace_id"`
	InstallationID string `json:"installation_id"`
	ChannelID      string `json:"channel_id"`
	Visibility     string `json:"visibility"`
}

type teamsChannelReportPublication struct {
	SchemaVersion      int                            `json:"schema_version"`
	DisclosureRevision int                            `json:"disclosure_revision"`
	Descriptors        []teamsChannelReportDescriptor `json:"descriptors"`
}

type teamsChannelReportDescriptor struct {
	ID          string                     `json:"id"`
	Kind        string                     `json:"kind"`
	Readers     []teamsChannelReportReader `json:"readers"`
	Outputs     teamsChannelOutputs        `json:"outputs"`
	Disclosures []string                   `json:"disclosures"`
	Destination teamsChannelDestination    `json:"destination"`
	Triggers    []struct{}                 `json:"triggers"`
}
type teamsChannelReportReader struct {
	Kind        string             `json:"kind"`
	Target      teamsChannelTarget `json:"target"`
	DisplayName string             `json:"display_name"`
}
type teamsChannelDestination struct {
	Kind   string             `json:"kind"`
	Target teamsChannelTarget `json:"target"`
}

var (
	errTeamsChannelPublication = errTeamsQueryUnsupported
	teamsChannelWorkspaceID    = regexp.MustCompile(`^T[A-Z0-9]{1,63}$`)
	teamsChannelConversationID = regexp.MustCompile(`^[CG][A-Z0-9]{1,63}$`)
	teamsChannelDisplayName    = regexp.MustCompile(`^[a-z0-9_-]{1,80}$`)
)

func (t teamsChannelTarget) Valid() bool {
	return teamsChannelWorkspaceID.MatchString(t.WorkspaceID) && teamsChannelValidID(t.InstallationID) &&
		teamsChannelConversationID.MatchString(t.ChannelID) && (t.Visibility == "public" || t.Visibility == "private")
}

// newTeamsChannelReportPublication builds the exact one-off aggregate and neutral
// link. The requester confirms the revision and delivery together at release.
func newTeamsChannelReportPublication(id string, target teamsChannelTarget, name string) (teamsChannelReportPublication, error) {
	if !teamsChannelValidID(id) || !target.Valid() || !teamsChannelDisplayName.MatchString(name) {
		return teamsChannelReportPublication{}, errTeamsChannelPublication
	}
	d := teamsChannelReportDescriptor{
		ID: id, Kind: "aggregate",
		Readers:     []teamsChannelReportReader{{Kind: "slack_channel_members", Target: target, DisplayName: name}},
		Outputs:     teamsChannelOutputs{Fields: []teamsChannelField{{ID: "total_tokens", Type: "integer", MetricID: "captured_tokens", Unit: "tokens", Merge: "sum"}}, Coverage: []string{"complete"}},
		Disclosures: []string{"current_and_future_channel_members", "single_contributor_value_and_partial_coverage", "slack_receives_link_only", "withdrawal_does_not_remove_published_snapshots", "leaving_does_not_recall_copies"},
		Destination: teamsChannelDestination{Kind: "slack_report_link", Target: target},
		Triggers:    []struct{}{},
	}
	if target.Visibility == "public" {
		d.Disclosures = append(d.Disclosures, "public_joiners_can_read_retained_reports")
	}
	return teamsChannelReportPublication{SchemaVersion: 2, DisclosureRevision: 1, Descriptors: []teamsChannelReportDescriptor{d}}, nil
}

func parseTeamsChannelReportPublication(raw []byte) (teamsChannelReportPublication, error) {
	var pub teamsChannelReportPublication
	if len(raw) == 0 || len(raw) > 32<<10 || (!teams.ValidUniqueJSON(raw) || !teamsChannelDecode(raw, &pub)) || len(pub.Descriptors) != 1 {
		return teamsChannelReportPublication{}, errTeamsChannelPublication
	}
	d := pub.Descriptors[0]
	if len(d.Readers) != 1 {
		return teamsChannelReportPublication{}, errTeamsChannelPublication
	}
	expected, err := newTeamsChannelReportPublication(d.ID, d.Readers[0].Target, d.Readers[0].DisplayName)
	if err != nil {
		return teamsChannelReportPublication{}, err
	}
	want := expected.Descriptors[0]
	if pub.SchemaVersion != expected.SchemaVersion || pub.DisclosureRevision != expected.DisclosureRevision || d.Kind != want.Kind || d.Readers[0] != want.Readers[0] || d.Destination != want.Destination || d.Triggers == nil || len(d.Triggers) != 0 || !slices.Equal(d.Disclosures, want.Disclosures) || !slices.Equal(d.Outputs.Fields, want.Outputs.Fields) || !slices.Equal(d.Outputs.Coverage, want.Outputs.Coverage) {
		return teamsChannelReportPublication{}, errTeamsChannelPublication
	}
	// Normalize object order while requiring the exact keys and values. The
	// standard decoder otherwise accepts case variants of struct field names.
	var actual, canonical any
	expectedRaw, err := json.Marshal(expected)
	if err != nil || json.Unmarshal(raw, &actual) != nil || json.Unmarshal(expectedRaw, &canonical) != nil {
		return teamsChannelReportPublication{}, errTeamsChannelPublication
	}
	a, errA := json.Marshal(actual)
	b, errB := json.Marshal(canonical)
	if errA != nil || errB != nil || !bytes.Equal(a, b) {
		return teamsChannelReportPublication{}, errTeamsChannelPublication
	}
	return pub, nil
}

// teamsChannelReviewLines is dormant until the channel capability is supported.
func teamsChannelReviewLines(pub teamsChannelReportPublication, title string) []string {
	if len(pub.Descriptors) != 1 || len(pub.Descriptors[0].Readers) != 1 {
		return nil
	}
	r := pub.Descriptors[0].Readers[0]
	t := r.Target
	lines := []string{
		fmt.Sprintf("Report title %q and the approved aggregate are visible to current and future eligible members of #%s (%s), workspace %s, channel %s, installation %s.", title, r.DisplayName, t.Visibility, t.WorkspaceID, t.ChannelID, t.InstallationID),
		"Collection access remains limited to the named collection readers. One contributor can be identified, including with partial coverage.",
		"Slack receives only a neutral link. Withdrawal does not remove published snapshots; leaving does not recall copies.",
	}
	if t.Visibility == "public" {
		lines = append(lines, "Eligible workspace members who join can read retained reports.")
	}
	return lines
}

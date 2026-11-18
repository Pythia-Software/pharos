package archive

import (
	"bytes"
	"compress/flate"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

// These types are deliberately separate from the manual publication parser.
// They describe dormant consent only; they do not enable dispatch or submission.
type teamsScheduledReader struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type teamsScheduledField struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	MetricID string `json:"metric_id"`
	Unit     string `json:"unit"`
	Merge    string `json:"merge"`
}
type teamsScheduledColumn struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Role     string `json:"role"`
	Merge    string `json:"merge"`
	MetricID string `json:"metric_id"`
	Unit     string `json:"unit"`
}
type teamsScheduledPublication struct {
	SchemaVersion      int                        `json:"schema_version"`
	DisclosureRevision int                        `json:"disclosure_revision"`
	Descriptors        []teamsScheduledDescriptor `json:"descriptors"`
}
type teamsScheduledDescriptor struct {
	ID      string                 `json:"id"`
	Kind    string                 `json:"kind"`
	Readers []teamsScheduledReader `json:"readers"`
	Outputs struct {
		Fields   []teamsScheduledField `json:"fields"`
		Coverage []string              `json:"coverage"`
	} `json:"outputs"`
	Disclosures []string `json:"disclosures"`
	Destination struct {
		Kind string `json:"kind"`
	} `json:"destination"`
	Triggers []struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	} `json:"triggers"`
}
type teamsScheduledSchedule struct {
	Cadence               string `json:"cadence"`
	Timezone              string `json:"timezone"`
	CatchUp               string `json:"catch_up"`
	ResponseWindowSeconds int64  `json:"response_window_seconds"`
}
type teamsScheduledConfig struct {
	Question       string `json:"question"`
	QueryURL       string `json:"query_url"`
	ApprovalPolicy string `json:"approval_policy"`
	Requester      struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"requester"`
	Audiences struct {
		ResponseReaders []teamsScheduledReader `json:"response_readers"`
		Publications    []struct{}             `json:"publications"`
		Exports         []struct{}             `json:"exports"`
	} `json:"audiences"`
	ResultSchema struct {
		Columns []teamsScheduledColumn `json:"columns"`
	} `json:"result_schema"`
	MinAppVersion   string `json:"min_app_version"`
	RetentionPolicy struct {
		Mode          string `json:"mode"`
		DurationDays  *int   `json:"duration_days"`
		PolicyVersion int64  `json:"policy_version"`
	} `json:"retention_policy"`
	Schedule       teamsScheduledSchedule `json:"schedule"`
	ScheduleBounds struct {
		AnchorAt time.Time `json:"anchor_at"`
		UntilAt  time.Time `json:"until_at"`
	} `json:"schedule_bounds"`
	Publication teamsScheduledPublication `json:"publication_v2"`
}

// Shared exact zone spellings do not depend on host filesystem case rules.
//
//go:embed iana_zone_names.txt
var teamsScheduledZoneNames string

func canonicalTeamsScheduledZone(name string) bool {
	names := strings.Split(strings.TrimSuffix(teamsScheduledZoneNames, "\n"), "\n")
	i := sort.SearchStrings(names, name)
	return name != "Local" && i < len(names) && names[i] == name
}

var teamsScheduledCoverage = []string{"complete", "incomplete_inventory", "opted_out", "unavailable", "declined", "pending"}
var teamsScheduledDisclosures = []string{"attributable_collection_and_opt_out", "accepted_by_deadline", "snapshot_differences_may_reveal_contributions", "withdrawal_does_not_remove_published_snapshots"}
var teamsScheduledVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func teamsScheduledEqual(a, b []string) bool { return reflect.DeepEqual(a, b) }
func teamsScheduledReaders(r []teamsScheduledReader) bool {
	if len(r) == 0 || len(r) > 100 {
		return false
	}
	prev := ""
	for _, p := range r {
		if _, err := teams.WorkPath(p.ID, "status"); err != nil || p.Kind != "person" || p.ID <= prev {
			return false
		}
		prev = p.ID
	}
	return true
}

// Enforce exact field spelling at every level; encoding/json alone permits
// case aliases. Required values are checked by the semantic validator below.
func teamsScheduledShape(raw []byte, typ reflect.Type) bool {
	for typ.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return true
		}
		typ = typ.Elem()
	}
	if typ == reflect.TypeOf(time.Time{}) {
		return true
	}
	switch typ.Kind() {
	case reflect.Struct:
		fields := map[string]bool{}
		fieldTypes := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				fields[name] = true
				fieldTypes[name] = f.Type
			}
		}
		obj, err := teamsStrictObject(raw, fields)
		if err != nil {
			return false
		}
		for key, val := range obj {
			if !teamsScheduledShape(val, fieldTypes[key]) {
				return false
			}
		}
		return true
	case reflect.Slice:
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return false
		}
		for _, item := range items {
			if !teamsScheduledShape(item, typ.Elem()) {
				return false
			}
		}
		return true
	}
	return true
}
func parseTeamsScheduledConfig(raw []byte) (teamsScheduledConfig, error) {
	var c teamsScheduledConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if len(raw) == 0 || len(raw) > 128<<10 || !teams.ValidUniqueJSON(raw) || !teamsScheduledShape(raw, reflect.TypeOf(c)) || dec.Decode(&c) != nil {
		return c, errTeamsQueryUnsupported
	}
	var top map[string]json.RawMessage
	var retention map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil || json.Unmarshal(top["retention_policy"], &retention) != nil || retention["duration_days"] == nil {
		return c, errTeamsQueryUnsupported
	}
	if c.Publication.SchemaVersion != 2 || (c.Publication.DisclosureRevision != 1 && c.Publication.DisclosureRevision != 2) || len(c.Publication.Descriptors) != 1 {
		return c, errTeamsQueryUnsupported
	}
	d := c.Publication.Descriptors[0]
	disclosures := teamsScheduledDisclosures
	if c.Publication.DisclosureRevision == 2 {
		disclosures = append(slices.Clone(disclosures), "single_contributor_value_and_partial_coverage")
	}
	_, idErr := teams.WorkPath(d.ID, "status")
	if idErr != nil || d.Kind != "aggregate" || !teamsScheduledReaders(d.Readers) || d.Destination.Kind != "web" || len(d.Triggers) != 1 {
		return c, errTeamsQueryUnsupported
	}
	if _, err := teams.WorkPath(d.Triggers[0].ID, "status"); err != nil || d.Triggers[0].Kind != "response_deadline" || len(d.Outputs.Fields) != 1 || d.Outputs.Fields[0] != (teamsScheduledField{"total_tokens", "integer", "captured_tokens", "tokens", "sum"}) || !teamsScheduledEqual(d.Outputs.Coverage, teamsScheduledCoverage) || !teamsScheduledEqual(d.Disclosures, disclosures) {
		return c, errTeamsQueryUnsupported
	}
	if _, err := teams.WorkPath(c.Requester.ID, "status"); err != nil || c.Question == "" || len(c.Question) > 2000 || c.Requester.DisplayName == "" || len(c.Requester.DisplayName) > 256 || c.ApprovalPolicy != "metrics_or_review" || !teamsScheduledVersion.MatchString(c.MinAppVersion) || c.RetentionPolicy.Mode != "organization_controlled" || c.RetentionPolicy.PolicyVersion < 1 || (c.RetentionPolicy.DurationDays != nil && *c.RetentionPolicy.DurationDays < 1) || !teamsScheduledReaders(c.Audiences.ResponseReaders) || c.Audiences.Publications == nil || len(c.Audiences.Publications) != 0 || c.Audiences.Exports == nil || len(c.Audiences.Exports) != 0 || !canonicalTeamsScheduledZone(c.Schedule.Timezone) || !validTeamsScheduledSpec(c.Schedule) || c.ScheduleBounds.AnchorAt.IsZero() || !c.ScheduleBounds.UntilAt.After(c.ScheduleBounds.AnchorAt) {
		return c, errTeamsQueryUnsupported
	}
	calendar := teamsScheduledCalendar{Cadence: c.Schedule.Cadence, Timezone: c.Schedule.Timezone, AnchorAt: c.ScheduleBounds.AnchorAt}
	first, e := calendar.Boundary(0)
	if e != nil || !first.Equal(c.ScheduleBounds.AnchorAt) {
		return c, errTeamsQueryUnsupported
	}
	last, e := calendar.Boundary(9999)
	if e != nil || c.ScheduleBounds.UntilAt.After(last) {
		return c, errTeamsQueryUnsupported
	}
	// Existing generic work keeps its historical decoder. The new arm must
	// match the service's canonical base64 bits as well as query semantics.
	u, e := url.Parse(c.QueryURL)
	if e != nil {
		return c, errTeamsQueryUnsupported
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil || len(q["q_usage"]) != 1 {
		return c, errTeamsQueryUnsupported
	}
	token := q.Get("q_usage")
	if len(token) < 4 || len(token) > 65536 || (token[:3] != "2j." && token[:3] != "2d.") {
		return c, errTeamsQueryUnsupported
	}
	encoded := token[3:]
	decoded, e := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if e != nil || base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		return c, errTeamsQueryUnsupported
	}
	if token[:3] == "2d." {
		r := flate.NewReader(bytes.NewReader(decoded))
		decoded, e = io.ReadAll(io.LimitReader(r, 65537))
		r.Close()
		if e != nil {
			return c, errTeamsQueryUnsupported
		}
	}
	if len(decoded) > 65536 {
		return c, errTeamsQueryUnsupported
	}
	plan, err := compileTeamsQuery(c.QueryURL)
	if err != nil || len(plan.Metrics) != 1 || plan.Metrics[0].ID != "total_tokens" || plan.Metrics[0].Field != "total_tokens" || len(c.ResultSchema.Columns) != 1 || c.ResultSchema.Columns[0] != (teamsScheduledColumn{"total_tokens", "integer", "metric", "sum", "captured_tokens", "tokens"}) {
		return c, errTeamsQueryUnsupported
	}
	return c, nil
}

// teamsScheduledDisclosure is typed display content produced only from parsed
// consent, preserving distinct collection and report readers even if equal.
type teamsScheduledDisplayReader struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"`
}

type teamsScheduledDisclosure struct {
	CollectionReaders     []teamsScheduledDisplayReader `json:"collectionReaders"`
	ReportReaders         []teamsScheduledDisplayReader `json:"reportReaders"`
	Field                 string                        `json:"field"`
	Coverage              string                        `json:"coverage"`
	DefaultAndOptOut      string                        `json:"defaultAndOptOut"`
	Cutoff                string                        `json:"cutoff"`
	Inference             string                        `json:"inference"`
	Withdrawal            string                        `json:"withdrawal"`
	Cadence               string                        `json:"cadence"`
	Timezone              string                        `json:"timezone"`
	CatchUp               string                        `json:"catchUp"`
	TriggerID             string                        `json:"triggerId"`
	DisclosureRevision    int                           `json:"disclosureRevision"`
	ResponseWindowSeconds int64                         `json:"responseWindowSeconds"`
	AnchorAt              time.Time                     `json:"anchorAt"`
	UntilAt               time.Time                     `json:"untilAt"`
}

func teamsScheduledTerms(c teamsScheduledConfig) teamsScheduledDisclosure {
	terms := teamsScheduledDisclosure{Cadence: c.Schedule.Cadence, Timezone: c.Schedule.Timezone, CatchUp: c.Schedule.CatchUp, TriggerID: c.Publication.Descriptors[0].Triggers[0].ID, DisclosureRevision: c.Publication.DisclosureRevision, ResponseWindowSeconds: c.Schedule.ResponseWindowSeconds, AnchorAt: c.ScheduleBounds.AnchorAt, UntilAt: c.ScheduleBounds.UntilAt, Field: "Combined captured tokens from all available history (sum)", Coverage: "Aggregate counts: complete, incomplete inventory, opted out, unavailable, declined and pending", DefaultAndOptOut: "Collection readers can see attributable values. Report readers see only the aggregate and approved coverage. Only privacy-safe numeric sources are eligible. The organization default applies; opt out of automatic sharing or choose review.", Cutoff: "Accepted by the response deadline; an accepted result remains eligible after a later opt-out. Each input retains its observed and computed times.", Inference: "A report with one contributor reveals that person's value; comparing snapshots may reveal contributions.", Withdrawal: "Withdrawal does not remove already published snapshots."}
	if c.Publication.DisclosureRevision == 2 {
		terms.Inference = "If only your library contributes, the named report readers can see your token total. They can see the total even when your library has only partial coverage. Comparing reports can reveal changes in your contribution."
		terms.Coverage = "Each report identifies whether its token total covers all eligible data or only part of it"
	}
	return terms
}

// labels must already be scoped to this exact consent config by the caller.
// Unknown requester can use the frozen signed name; inactive identities remain
// visible as inactive, matching the existing reader-review behavior.
func renderTeamsScheduledDisclosure(c teamsScheduledConfig, labels map[string]teams.ReaderLabel) (teamsScheduledDisclosure, error) {
	out := teamsScheduledTerms(c)
	reader := func(id string) (teamsScheduledDisplayReader, error) {
		l, ok := labels[id]
		if !ok || l.PersonID != id || (l.Status != "active" && l.Status != "inactive" && l.Status != "unknown") {
			return teamsScheduledDisplayReader{}, errTeamsQueryUnsupported
		}
		name := l.DisplayName
		if name != "" && !usableTeamsReaderName(name) {
			return teamsScheduledDisplayReader{}, errTeamsQueryUnsupported
		}
		if l.Status == "unknown" && name != "" {
			return teamsScheduledDisplayReader{}, errTeamsQueryUnsupported
		}
		if l.Status == "unknown" || (l.Status == "active" && name == "") {
			if id != c.Requester.ID || !usableTeamsReaderName(c.Requester.DisplayName) {
				return teamsScheduledDisplayReader{}, errTeamsQueryUnsupported
			}
			name = c.Requester.DisplayName
		}
		if l.Status == "inactive" && name == "" {
			name = "Inactive member"
		}
		return teamsScheduledDisplayReader{ID: id, Label: name, Status: l.Status}, nil
	}
	for _, r := range c.Audiences.ResponseReaders {
		v, e := reader(r.ID)
		if e != nil {
			return teamsScheduledDisclosure{}, e
		}
		out.CollectionReaders = append(out.CollectionReaders, v)
	}
	for _, r := range c.Publication.Descriptors[0].Readers {
		v, e := reader(r.ID)
		if e != nil {
			return teamsScheduledDisclosure{}, e
		}
		out.ReportReaders = append(out.ReportReaders, v)
	}
	return out, nil
}

// The trusted value must come from the persisted server assignment. A poll
// period is a sibling of config and is not authenticated by the config hash.
// Server acceptance must independently verify its calendar derivation and
// bind these exact fields into the device-signed result before activation.
type teamsScheduledAssignment = teams.ScheduledAssignment

func matchesPersistedTeamsScheduledAssignment(c teamsScheduledConfig, persisted, submitted teamsScheduledAssignment) bool {
	return matchesTeamsScheduledPeriod(c, persisted, submitted, nil)
}

func matchesTeamsScheduledPeriod(c teamsScheduledConfig, persisted, submitted teamsScheduledAssignment, periodEnd *time.Time) bool {
	if persisted.AssignmentID != submitted.AssignmentID || persisted.PeriodKey != submitted.PeriodKey || persisted.Timezone != submitted.Timezone || !persisted.StartAt.Equal(submitted.StartAt) || !persisted.DeadlineAt.Equal(submitted.DeadlineAt) || persisted.Timezone != c.Schedule.Timezone || !teamsPeriodKey.MatchString(persisted.PeriodKey) || persisted.StartAt.Before(c.ScheduleBounds.AnchorAt) || !persisted.StartAt.Before(c.ScheduleBounds.UntilAt) {
		return false
	}
	if _, err := teams.WorkPath(persisted.AssignmentID, "status"); err != nil {
		return false
	}
	index, err := strconv.Atoi(strings.TrimPrefix(persisted.PeriodKey, "period:"))
	if err != nil || index > 9998 || strconv.Itoa(index) != strings.TrimPrefix(persisted.PeriodKey, "period:") {
		return false
	}
	start, end, err := (teamsScheduledCalendar{Cadence: c.Schedule.Cadence, Timezone: c.Schedule.Timezone, AnchorAt: c.ScheduleBounds.AnchorAt}).Window(index)
	if end.After(c.ScheduleBounds.UntilAt) {
		end = c.ScheduleBounds.UntilAt
	}
	if err != nil || !start.Equal(persisted.StartAt) || (periodEnd != nil && !end.Equal(*periodEnd)) {
		return false
	}
	deadline := persisted.StartAt.Add(time.Duration(c.Schedule.ResponseWindowSeconds) * time.Second)
	if c.ScheduleBounds.UntilAt.Before(deadline) {
		deadline = c.ScheduleBounds.UntilAt
	}
	return deadline.Equal(persisted.DeadlineAt)
}

func validTeamsScheduledSpec(s teamsScheduledSchedule) bool {
	if s.Cadence != "daily" && s.Cadence != "weekly" && s.Cadence != "monthly" {
		return false
	}
	if s.CatchUp != "process" && s.CatchUp != "skip" {
		return false
	}
	if s.ResponseWindowSeconds < 60 || s.ResponseWindowSeconds > 30*24*60*60 {
		return false
	}
	_, err := loadTeamsScheduledLocation(s.Timezone)
	return err == nil
}

package archive

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

func TestTeamsScheduledPublicationContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/teams-report-series/scheduled_series_config.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "da44ddded062fd84c3cef80a322802a8ba451a21b3d15bb94e4f649d0213edc5" {
		t.Fatal("shared vector changed")
	}
	work := teams.Work{Kind: "deterministic", Config: json.RawMessage(raw), ConfigSHA256: teamsPayloadHash(raw), PopulationKey: "current", Period: json.RawMessage(`{"key":"period:0","start_at":"2030-01-01T00:00:00Z","end_at":"2030-01-02T00:00:00Z","anchor_at":"2030-01-01T00:00:00Z"}`), ExpiresAt: time.Date(2030, 1, 1, 1, 0, 0, 0, time.UTC)}
	if _, err := teamsPlanFromWork(work); err == nil {
		t.Fatal("scheduled arm entered existing work admission")
	}
	c, err := parseTeamsScheduledConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	d, err := renderTeamsScheduledDisclosure(c, map[string]teams.ReaderLabel{"0b000000-0000-4000-8000-000000000001": {PersonID: "0b000000-0000-4000-8000-000000000001", Status: "active", DisplayName: "Report reader"}, "0c000000-0000-4000-8000-000000000001": {PersonID: "0c000000-0000-4000-8000-000000000001", Status: "active", DisplayName: "Collection reader"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.CollectionReaders) != 1 || len(d.ReportReaders) != 1 || d.CollectionReaders[0].ID == d.ReportReaders[0].ID || d.CollectionReaders[0].Label != "Collection reader" || !strings.Contains(d.DefaultAndOptOut, "attributable") || !strings.Contains(d.Cutoff, "Accepted by") {
		t.Fatal("incomplete disclosure", d)
	}
	var top map[string]json.RawMessage
	if err = json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	if _, err = parseTeamsReportSeries(top["publication_v2"]); err == nil {
		t.Fatal("manual parser accepted scheduled arm")
	}
	reordered := strings.Replace(string(raw), `/analyze?analyze_data=usage\u0026analyze_mode=query\u0026analyze_topic=tokens`, `/analyze?analyze_mode=query\u0026analyze_topic=tokens\u0026analyze_data=usage`, 1)
	if reordered == string(raw) {
		t.Fatal("query vector missing")
	}
	if _, err = parseTeamsScheduledConfig([]byte(reordered)); err != nil {
		t.Fatal("equivalent all-history query rejected", err)
	}
	sameReaders := strings.Replace(string(raw), "0b000000-0000-4000-8000-000000000001", "0c000000-0000-4000-8000-000000000001", 1)
	if _, err = parseTeamsScheduledConfig([]byte(sameReaders)); err != nil {
		t.Fatal("overlapping audiences rejected", err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"duration_days":null,`, ``, 1),
		strings.Replace(string(raw), `"unit":"tokens"`, `"nullable":true,"unit":"tokens"`, 1),
		strings.Replace(string(raw), `"approval_policy":"metrics_or_review"`, `"approval_policy":"review"`, 1),
		strings.Replace(string(raw), `"response_deadline"`, `"response_percentage"`, 1),
		strings.Replace(string(raw), `"disclosure_revision":1`, `"disclosure_revision":2`, 1),
		strings.Replace(string(raw), `"unit":"tokens"`, `"unit":"usd"`, 1),
		strings.Replace(string(raw), `"exports":[]`, `"exports":[{}]`, 1),
		strings.Replace(string(raw), `"until_at":"2030-02-01T00:00:00Z"`, `"until_at":"2029-01-01T00:00:00Z"`, 1),
		strings.Replace(string(raw), `"triggers":[{`, `"triggers":[{"id":"0d000000-0000-4000-8000-000000000002","kind":"response_deadline"},{`, 1),
		strings.Replace(string(raw), `"schema_version":2`, `"schema_version":2,"unknown":1`, 1),
	} {
		if _, err := parseTeamsScheduledConfig([]byte(bad)); err == nil {
			t.Fatal("unsupported scheduled config accepted")
		}
	}
}

func TestTeamsScheduledAssignmentSubstitution(t *testing.T) {
	raw, err := os.ReadFile("testdata/teams-report-series/scheduled_series_config.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := parseTeamsScheduledConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/teams-report-series/scheduled_assignment_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name      string                   `json:"name"`
		Persisted teamsScheduledAssignment `json:"persisted"`
		Submitted teamsScheduledAssignment `json:"submitted"`
		Valid     bool                     `json:"valid"`
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != "903fec474c9bd1d704ac31628229df97a4c3418006b84e43f74e1b2c5f73dd7a" {
		t.Fatal("assignment vectors changed")
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, v := range cases {
		if got := matchesPersistedTeamsScheduledAssignment(c, v.Persisted, v.Submitted); got != v.Valid {
			t.Fatalf("%s: %v", v.Name, got)
		}
	}
	forged := cases[0].Persisted
	forged.PeriodKey = "period:1"
	if matchesPersistedTeamsScheduledAssignment(c, forged, forged) {
		t.Fatal("persisted period inconsistent with schedule accepted")
	}
}

func TestTeamsScheduledCalendarVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/teams-report-series/scheduled_calendar_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Cadence  string    `json:"cadence"`
		Timezone string    `json:"timezone"`
		Anchor   time.Time `json:"anchor"`
		Index    int       `json:"index"`
		Boundary time.Time `json:"boundary"`
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != "f937b814e159721e49135f06fa1b905c7a04ec7aaab7680d30d1fe2a4a9562d0" {
		t.Fatal("calendar vectors changed")
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, v := range cases {
		got, err := (teamsScheduledCalendar{Cadence: v.Cadence, Timezone: v.Timezone, AnchorAt: v.Anchor}).Boundary(v.Index)
		if err != nil || !got.Equal(v.Boundary) {
			t.Fatalf("calendar mismatch: %v %v", got, err)
		}
	}
}

func TestTeamsScheduledSharedRejections(t *testing.T) {
	data, err := os.ReadFile("testdata/teams-report-series/scheduled_rejections.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != "093a602fda732f27d8e54af2c2a25cdb63a0d5d0ac3851eb14c173262b0076c1" {
		t.Fatal("rejection vectors changed")
	}
	var cases []struct {
		Name string `json:"name"`
		Raw  string `json:"raw"`
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, v := range cases {
		if _, err = parseTeamsScheduledConfig([]byte(v.Raw)); err == nil {
			t.Fatalf("accepted %s", v.Name)
		}
	}
}

func TestTeamsScheduledTimingVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/teams-report-series/scheduled_timing_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != "996048eeebb218ee9add0f217d68fda299e6f6862107367e82bca3f8f0c60101" {
		t.Fatal("timing vectors changed")
	}
	var cases []struct {
		Name            string                    `json:"name"`
		Config          json.RawMessage           `json:"config"`
		Valid           bool                      `json:"valid"`
		Assignment      *teamsScheduledAssignment `json:"assignment"`
		AssignmentValid *bool                     `json:"assignment_valid"`
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, v := range cases {
		c, e := parseTeamsScheduledConfig(v.Config)
		if (e == nil) != v.Valid {
			t.Fatalf("%s: %v", v.Name, e)
		}
		if e == nil && v.Assignment != nil {
			want := true
			if v.AssignmentValid != nil {
				want = *v.AssignmentValid
			}
			if got := matchesPersistedTeamsScheduledAssignment(c, *v.Assignment, *v.Assignment); got != want {
				t.Fatalf("%s assignment: %v", v.Name, got)
			}
		}
	}
}
func TestTeamsScheduledDisclosureIdentity(t *testing.T) {
	raw, err := os.ReadFile("testdata/teams-report-series/scheduled_series_config.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := parseTeamsScheduledConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	requester := "0c000000-0000-4000-8000-000000000001"
	other := "0b000000-0000-4000-8000-000000000001"
	labels := map[string]teams.ReaderLabel{requester: {PersonID: requester, Status: "unknown"}, other: {PersonID: other, Status: "active", DisplayName: "Report reader"}}
	d, err := renderTeamsScheduledDisclosure(c, labels)
	if err != nil || d.CollectionReaders[0].Label != "Synthetic" || !strings.Contains(d.Field, "all available history") || !strings.Contains(d.Inference, "one contributor") || d.DisclosureRevision != 1 {
		t.Fatal("incomplete disclosure", d, err)
	}
	labels[other] = teams.ReaderLabel{PersonID: other, Status: "active"}
	if _, err = renderTeamsScheduledDisclosure(c, labels); err == nil {
		t.Fatal("unidentified active reader accepted")
	}
	labels[other] = teams.ReaderLabel{PersonID: other, Status: "unknown"}
	if _, err = renderTeamsScheduledDisclosure(c, labels); err == nil {
		t.Fatal("unknown reader accepted")
	}
	labels[other] = teams.ReaderLabel{PersonID: other, Status: "active", DisplayName: "\u202eunsafe"}
	if _, err = renderTeamsScheduledDisclosure(c, labels); err == nil {
		t.Fatal("unsafe reader accepted")
	}
	labels[other] = teams.ReaderLabel{PersonID: other, Status: "inactive"}
	d, err = renderTeamsScheduledDisclosure(c, labels)
	if err != nil || d.ReportReaders[0].Status != "inactive" || d.ReportReaders[0].ID != other {
		t.Fatal("inactive reader not preserved", d, err)
	}
}

func TestTeamsScheduledAdditionalVectors(t *testing.T) {
	for _, fixture := range []struct {
		file, hash string
		zones      bool
	}{{"scheduled_equivalents.json", "51c3fbd1063828ad9f85078c22dfd8d7ca502680b8d31883f6dc534f9fbb2b0b", false}, {"scheduled_zone_vectors.json", "fb362531c6b2151758d077c681643679be23f8c2c781be82bd2e10a39b39f2c3", true}} {
		data, err := os.ReadFile("testdata/teams-report-series/" + fixture.file)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != fixture.hash {
			t.Fatal("shared fixture changed", fixture.file)
		}
		var cases []struct {
			Name  string `json:"name"`
			Raw   string `json:"raw"`
			Valid bool   `json:"valid"`
		}
		if err = json.Unmarshal(data, &cases); err != nil {
			t.Fatal(err)
		}
		for _, v := range cases {
			_, err = parseTeamsScheduledConfig([]byte(v.Raw))
			want := v.Valid
			if !fixture.zones {
				want = true
			}
			if (err == nil) != want {
				t.Fatalf("%s: %v", v.Name, err)
			}
		}
	}
	data, err := os.ReadFile("iana_zone_names.txt")
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != "8725722643bf1f4ff4fc4b22268ade98b6fae047a86897219c3a13d4c4ced93d" {
		t.Fatal("zone name inventory changed", err)
	}
}

// Whitespace preserves the valid query semantics while testing both wire bounds.
func TestTeamsScheduledQuerySizeBounds(t *testing.T) {
	raw, err := os.ReadFile("testdata/teams-report-series/scheduled_series_config.json")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	query := `{"g":[{"id":"total_tokens","op":"sum","field":"total_tokens","groupBy":[]}]}`
	for _, tc := range []struct {
		name              string
		size              int
		compressed, valid bool
	}{
		{"encoded within", 49149, false, true},
		{"encoded over", 49150, false, false},
		{"inflated within", 65536, true, true},
		{"inflated over", 65537, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(query + strings.Repeat(" ", tc.size-len(query)))
			prefix := "2j."
			if tc.compressed {
				var b bytes.Buffer
				w, err := flate.NewWriter(&b, flate.DefaultCompression)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = w.Write(data); err != nil {
					t.Fatal(err)
				}
				if err = w.Close(); err != nil {
					t.Fatal(err)
				}
				data, prefix = b.Bytes(), "2d."
			}
			token := prefix + base64.RawURLEncoding.EncodeToString(data)
			values := url.Values{"analyze_data": {"usage"}, "analyze_mode": {"query"}, "analyze_topic": {"tokens"}, "q_usage": {token}}
			config["query_url"] = "/analyze?" + values.Encode()
			candidate, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			_, err = parseTeamsScheduledConfig(candidate)
			if (err == nil) != tc.valid {
				t.Fatalf("token length %d, state length %d: got %v, valid %v", len(token), tc.size, err, tc.valid)
			}
		})
	}
}

func TestScheduledReleaseDisclosesSinglePartialContributor(t *testing.T) {
	raw, err := os.ReadFile("testdata/teams-report-series/scheduled_series_config.json")
	if err != nil {
		t.Fatal(err)
	}
	revision := strings.Replace(string(raw), `"disclosure_revision":1`, `"disclosure_revision":2`, 1)
	const end = `"withdrawal_does_not_remove_published_snapshots"`
	const release = end + `,"single_contributor_value_and_partial_coverage"`
	v2 := strings.Replace(revision, end, release, 1)
	c, err := parseTeamsScheduledConfig([]byte(v2))
	if err != nil {
		t.Fatal(err)
	}
	terms := teamsScheduledTerms(c)
	if terms.DisclosureRevision != 2 || !strings.Contains(terms.Inference, "your token total") || !strings.Contains(terms.Inference, "partial coverage") || !strings.Contains(terms.Inference, "named report readers") || !strings.Contains(terms.Inference, "web report") || !strings.Contains(terms.Inference, "separate approval") || !strings.Contains(terms.Coverage, "opted out, unavailable, declined and pending") || !strings.Contains(terms.Coverage, "With one participant") {
		t.Fatal("release disclosure omitted the scope of a single partial contribution")
	}
	for _, bad := range []string{revision, strings.Replace(string(raw), end, release, 1), strings.Replace(v2, `"disclosure_revision":2`, `"disclosure_revision":3`, 1)} {
		if _, err := parseTeamsScheduledConfig([]byte(bad)); err == nil {
			t.Fatal("mismatched release terms accepted")
		}
	}
}

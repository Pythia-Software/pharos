package archive

import (
	"bytes"
	"encoding/json"
	"github.com/gbdubs/pharos/internal/teams"
	"regexp"
	"time"
	_ "time/tzdata"
)

type teamsSchedule struct {
	Cadence               string `json:"cadence"`
	Timezone              string `json:"timezone"`
	CatchUp               string `json:"catch_up"`
	ResponseWindowSeconds int64  `json:"response_window_seconds"`
}
type teamsPeriod struct {
	Key      string     `json:"key"`
	StartAt  *time.Time `json:"start_at"`
	EndAt    *time.Time `json:"end_at"`
	AnchorAt *time.Time `json:"anchor_at"`
}

var teamsPeriodKey = regexp.MustCompile(`^period:(0|[1-9][0-9]{0,3})$`)

func parseTeamsPeriod(raw []byte, schedule *teamsSchedule) (teamsPeriod, error) {
	var p teamsPeriod
	keys, err := teamsStrictObject(raw, map[string]bool{"key": true, "start_at": true, "end_at": true, "anchor_at": true})
	if err != nil {
		return p, errTeamsQueryUnsupported
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil {
		return p, errTeamsQueryUnsupported
	}
	if schedule == nil {
		if p.Key != "once" || p.StartAt != nil || p.EndAt != nil || len(keys) > 2 {
			return p, errTeamsQueryUnsupported
		}
		return p, nil
	}
	if len(keys) != 4 || !teamsPeriodKey.MatchString(p.Key) || p.StartAt == nil || p.EndAt == nil || p.AnchorAt == nil || p.StartAt.IsZero() || !p.EndAt.After(*p.StartAt) || !p.AnchorAt.Equal(*p.StartAt) {
		return p, errTeamsQueryUnsupported
	}
	for _, k := range []string{"start_at", "end_at", "anchor_at"} {
		var v string
		if json.Unmarshal(keys[k], &v) != nil || len(v) == 0 || v[len(v)-1] != 'Z' {
			return p, errTeamsQueryUnsupported
		}
	}
	return p, nil
}
func validTeamsSchedule(s *teamsSchedule) bool {
	if s == nil {
		return true
	}
	if s.Cadence != "daily" && s.Cadence != "weekly" && s.Cadence != "monthly" {
		return false
	}
	if s.CatchUp != "process" && s.CatchUp != "skip" {
		return false
	}
	if s.Timezone == "" || s.Timezone == "Local" || s.ResponseWindowSeconds < 60 || s.ResponseWindowSeconds > 30*24*60*60 {
		return false
	}
	_, err := time.LoadLocation(s.Timezone)
	return err == nil
}

func teamsNumericWorkEligible(w teams.Work, m teamsCapabilityManifest) bool {
	if !teamsNumericBuildEligible(m) {
		return false
	}
	var p struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(w.Period, &p) != nil {
		return false
	}
	if p.Key != "once" && !teamsBuildEligible(m, "recurring_deterministic_v1") {
		return false
	}
	var c struct {
		PublicationV2 json.RawMessage `json:"publication_v2"`
		Audiences     struct {
			Exports      []json.RawMessage `json:"exports"`
			Publications []json.RawMessage `json:"publications"`
		} `json:"audiences"`
	}
	if json.Unmarshal(w.Config, &c) != nil {
		return false
	}
	if w.ScheduledAssignment != nil {
		return p.Key != "once" && len(c.PublicationV2) > 0 && teamsBuildEligible(m, "reader_labels_v1") && teamsBuildEligible(m, "report_automatic_metrics_v1")
	}
	if len(c.PublicationV2) > 0 && (!teamsBuildEligible(m, "report_deterministic_v2") || p.Key != "once") {
		return false
	}
	if len(c.Audiences.Publications) > 0 && !teamsBuildEligible(m, "report_deterministic_v1") {
		return false
	}
	if len(c.Audiences.Exports) > 0 && !teamsBuildEligible(m, "export_deterministic_v1") {
		return false
	}
	return true
}

// teamsNumericWorkEligible checks all separately advertised dispatch features.

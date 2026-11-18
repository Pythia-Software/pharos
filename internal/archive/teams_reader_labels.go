package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gbdubs/pharos/internal/teams"
)

func usableTeamsReaderName(s string) bool {
	if strings.TrimSpace(s) == "" || len(s) > 128 || !utf8.ValidString(s) || utf8.RuneCountInString(s) > 64 {
		return false
	}
	visible := false
	for _, r := range s {
		if unicode.IsControl(r) || (unicode.Is(unicode.Cf, r) && r != 0x200c && r != 0x200d) {
			return false
		}
		if unicode.IsGraphic(r) && !unicode.IsSpace(r) {
			visible = true
		}
	}
	return visible
}

// labelsForReview validates directory presentation independently of the signed config.
func validReaderLabels(w teams.Work, labels *teams.ReaderLabels, fetched time.Time, requireNames bool) bool {
	if labels == nil || time.Since(fetched) > 5*time.Minute || labels.Version != 1 || labels.ConfigSHA256 != w.ConfigSHA256 {
		return false
	}
	var config struct {
		Requester struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"requester"`
		Audiences struct {
			ResponseReaders []struct {
				ID string `json:"id"`
			} `json:"response_readers"`
		} `json:"audiences"`
		Publication json.RawMessage `json:"publication_v2"`
	}
	if json.Unmarshal(w.Config, &config) != nil || len(config.Publication) == 0 {
		return false
	}
	ids := map[string]bool{}
	for _, r := range config.Audiences.ResponseReaders {
		ids[r.ID] = true
	}
	if _, err := parseTeamsChannelReportPublication(config.Publication); err == nil && teamsChannelReportPilotEnabled {
		// Channel membership is a report audience, never a person label.
	} else {
		var series teamsReportSeries
		if json.Unmarshal(config.Publication, &series) != nil || len(series.Descriptors) != 1 {
			return false
		}
		for _, r := range series.Descriptors[0].Readers {
			ids[r.ID] = true
		}
	}
	keys := make([]string, 0, len(ids))
	for id := range ids {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	if len(keys) != len(labels.Entries) || len(keys) > 1100 {
		return false
	}
	for i, l := range labels.Entries {
		if l.PersonID != keys[i] || (l.Status != "active" && l.Status != "inactive" && l.Status != "unknown") || len(l.DisplayName) > 256 || !utf8.ValidString(l.DisplayName) {
			return false
		}
		if l.DisplayName != "" && !usableTeamsReaderName(l.DisplayName) {
			return false
		}
		if l.Status == "unknown" && l.DisplayName != "" {
			return false
		}
		if requireNames && (l.Status == "unknown" || (l.Status == "active" && strings.TrimSpace(l.DisplayName) == "")) {
			if l.PersonID != config.Requester.ID || !usableTeamsReaderName(config.Requester.DisplayName) {
				return false
			}
		}
	}
	return true
}

func labelsForReview(w teams.Work, labels *teams.ReaderLabels, fetched time.Time) bool {
	return validReaderLabels(w, labels, fetched, true)
}

func teamsCredentialScope(d LibraryDescriptor, c *teams.EnrollmentCredential) string {
	return d.ID + "\x00" + d.Instance + "\x00" + c.OrgID + "\x00" + c.PersonID + "\x00" + c.InstallationID + "\x00" + c.Origin
}
func teamsReaderLabelToken(item teamsWorkerItem, scope string, generation uint64) string {
	if item.LabelsScope != scope || !labelsForReview(item.Work, item.Labels, item.LabelsAt) {
		return ""
	}
	b, err := json.Marshal(struct {
		Scope, Work, Config, State, Binding string
		Generation                          uint64
		Labels                              *teams.ReaderLabels
	}{scope, item.Work.ID, item.Work.ConfigSHA256, item.State, teamsWorkDigest(item.Work), generation, item.Labels})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func teamsHasSeriesConfig(raw []byte) bool {
	var arms map[string]json.RawMessage
	if json.Unmarshal(raw, &arms) != nil {
		return true
	}
	for key := range arms {
		if strings.EqualFold(key, "publication_v2") {
			return true
		}
	}
	return false
}

func teamsWorkDigest(work teams.Work) string {
	b, e := json.Marshal(work)
	if e != nil {
		return ""
	}
	return teamsPayloadHash(b)
}

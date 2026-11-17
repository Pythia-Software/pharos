package archive

import (
	"regexp"
	"sort"

	"github.com/gbdubs/pharos/internal/teams"
)

type teamsFeedExport struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Destination string   `json:"destination"`
	Fields      []string `json:"fields"`
}

var teamsBigQueryDestination = regexp.MustCompile(`^bigquery:[a-z][a-z0-9-]{0,61}[a-z0-9]\.[A-Za-z_][A-Za-z0-9_]{0,510}\.[A-Za-z_][A-Za-z0-9_]{0,510}@US$`)

func validTeamsExports(exports []teamsFeedExport, approval string, plan teamsQueryPlan) bool {
	if len(exports) == 0 {
		return true
	}
	if approval != "review" || len(exports) > 8 {
		return false
	}
	metrics := map[string]bool{}
	for _, m := range plan.Metrics {
		metrics[m.ID] = true
	}
	seen := map[string]bool{}
	for _, x := range exports {
		if _, e := teams.WorkPath(x.ID, "status"); e != nil || seen[x.ID] || x.Kind != "feed" || len(x.Destination) > 512 || !teamsBigQueryDestination.MatchString(x.Destination) || x.Fields == nil || len(x.Fields) < 1 || len(x.Fields) > 8 || !sort.StringsAreSorted(x.Fields) {
			return false
		}
		seen[x.ID] = true
		for i, f := range x.Fields {
			if !metrics[f] || (i > 0 && x.Fields[i-1] == f) {
				return false
			}
		}
	}
	return true
}

type teamsReportReader struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type teamsReportPublication struct {
	ID      string              `json:"id"`
	Kind    string              `json:"kind"`
	Readers []teamsReportReader `json:"readers"`
	Fields  []string            `json:"fields"`
}

func validTeamsReports(reports []teamsReportPublication, approval string, plan teamsQueryPlan) bool {
	if len(reports) == 0 {
		return true
	}
	if approval != "review" || len(reports) > 8 {
		return false
	}
	metrics := map[string]bool{}
	for _, m := range plan.Metrics {
		metrics[m.ID] = true
	}
	seen := map[string]bool{}
	for _, r := range reports {
		if _, e := teams.WorkPath(r.ID, "status"); e != nil || seen[r.ID] || r.Kind != "report" || r.Readers == nil || len(r.Readers) < 1 || len(r.Readers) > 1000 || r.Fields == nil || len(r.Fields) < 1 || len(r.Fields) > 8 || !sort.StringsAreSorted(r.Fields) {
			return false
		}
		seen[r.ID] = true
		readers := map[string]bool{}
		for i, p := range r.Readers {
			if _, e := teams.WorkPath(p.ID, "status"); e != nil || p.Kind != "person" || readers[p.ID] || (i > 0 && r.Readers[i-1].ID >= p.ID) {
				return false
			}
			readers[p.ID] = true
		}
		for i, f := range r.Fields {
			if !metrics[f] || (i > 0 && r.Fields[i-1] == f) {
				return false
			}
		}
	}
	return true
}

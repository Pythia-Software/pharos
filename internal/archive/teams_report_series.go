package archive

import (
	"bytes"
	"encoding/json"

	"github.com/gbdubs/pharos/internal/teams"
)

// teamsReportSeries is a separate version-consented publication arm. The
// fixed disclosures are part of the exact config bytes that the signed work
// and reviewed receipt already bind.
type teamsReportSeries struct {
	SchemaVersion int                           `json:"schema_version"`
	Descriptors   []teamsReportSeriesDescriptor `json:"descriptors"`
}
type teamsReportSeriesDescriptor struct {
	ID      string              `json:"id"`
	Kind    string              `json:"kind"`
	Readers []teamsReportReader `json:"readers"`
	Outputs struct {
		Fields []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			MetricID string `json:"metric_id"`
			Unit     string `json:"unit"`
			Merge    string `json:"merge"`
		} `json:"fields"`
		Coverage []string `json:"coverage"`
	} `json:"outputs"`
	Disclosures []string `json:"disclosures"`
	Destination struct {
		Kind string `json:"kind"`
	} `json:"destination"`
	Triggers []struct{} `json:"triggers"`
}

func parseTeamsReportSeries(raw []byte) (teamsReportSeries, error) {
	var out teamsReportSeries
	object := func(b []byte, keys ...string) (map[string]json.RawMessage, bool) {
		allowed := map[string]bool{}
		for _, k := range keys {
			allowed[k] = true
		}
		m, e := teamsStrictObject(b, allowed)
		return m, e == nil && len(m) == len(keys)
	}
	top, ok := object(raw, "schema_version", "descriptors")
	if !ok {
		return out, errTeamsQueryUnsupported
	}
	var entries []json.RawMessage
	if json.Unmarshal(top["descriptors"], &entries) != nil || len(entries) != 1 {
		return out, errTeamsQueryUnsupported
	}
	d, ok := object(entries[0], "id", "kind", "readers", "outputs", "disclosures", "destination", "triggers")
	if !ok {
		return out, errTeamsQueryUnsupported
	}
	outputs, ok := object(d["outputs"], "fields", "coverage")
	if !ok {
		return out, errTeamsQueryUnsupported
	}
	var fields []json.RawMessage
	if json.Unmarshal(outputs["fields"], &fields) != nil || len(fields) != 1 {
		return out, errTeamsQueryUnsupported
	}
	if _, ok = object(fields[0], "id", "type", "metric_id", "unit", "merge"); !ok {
		return out, errTeamsQueryUnsupported
	}
	var readers []json.RawMessage
	if json.Unmarshal(d["readers"], &readers) != nil || len(readers) < 1 || len(readers) > 100 {
		return out, errTeamsQueryUnsupported
	}
	for _, r := range readers {
		if _, ok = object(r, "kind", "id"); !ok {
			return out, errTeamsQueryUnsupported
		}
	}
	if _, ok = object(d["destination"], "kind"); !ok {
		return out, errTeamsQueryUnsupported
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if !teams.ValidUniqueJSON(raw) || dec.Decode(&out) != nil || out.SchemaVersion != 2 || len(out.Descriptors) != 1 {
		return teamsReportSeries{}, errTeamsQueryUnsupported
	}
	item := out.Descriptors[0]
	if _, e := teams.WorkPath(item.ID, "status"); e != nil || item.Kind != "aggregate" || item.Destination.Kind != "web" || item.Triggers == nil || len(item.Triggers) != 0 {
		return teamsReportSeries{}, errTeamsQueryUnsupported
	}
	prev := ""
	for _, r := range item.Readers {
		if _, e := teams.WorkPath(r.ID, "status"); e != nil || r.Kind != "person" || r.ID <= prev {
			return teamsReportSeries{}, errTeamsQueryUnsupported
		}
		prev = r.ID
	}
	f := item.Outputs.Fields[0]
	if f.ID != "total_tokens" || f.Type != "integer" || f.MetricID != "captured_tokens" || f.Unit != "tokens" || f.Merge != "sum" || len(item.Outputs.Coverage) != 1 || item.Outputs.Coverage[0] != "complete" || len(item.Disclosures) != 2 || item.Disclosures[0] != "snapshot_differences_may_reveal_contributions" || item.Disclosures[1] != "withdrawal_does_not_remove_published_snapshots" {
		return teamsReportSeries{}, errTeamsQueryUnsupported
	}
	return out, nil
}

package archive

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"time"
	"unicode/utf8"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// The v2 registry is deliberately independent of the existing v1 compiler.
// It admits only the first reviewed fixed-window usage cohort; other shapes
// are unsupported rather than normalized into a broader population.
type teamsUsageQueryV2 struct {
	Start, End time.Time
	GroupBy    []string
	Metrics    []teamsMetric
}

var teamsUsageMetricID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func teamsStrictRawLinkV2(token string) ([]byte, error) {
	if len(token) < 4 || len(token) > qt.MaxQueryTokenLength || (token[:3] != "2j." && token[:3] != "2d.") {
		return nil, errTeamsQueryUnsupported
	}
	payload := token[3:]
	data, err := base64.RawURLEncoding.Strict().DecodeString(payload)
	if err != nil || base64.RawURLEncoding.EncodeToString(data) != payload {
		return nil, errTeamsQueryUnsupported
	}
	if token[:3] == "2d." {
		source := bytes.NewReader(data)
		r := flate.NewReader(source)
		decoded, e := io.ReadAll(io.LimitReader(r, qt.MaxQueryStateBytes+1))
		closeErr := r.Close()
		if e != nil || closeErr != nil || source.Len() != 0 {
			return nil, errTeamsQueryUnsupported
		}
		data = decoded
	}
	if len(data) > qt.MaxQueryStateBytes || !utf8.Valid(data) {
		return nil, errTeamsQueryUnsupported
	}
	return data, nil
}

func compileTeamsUsageQueryV2(raw string) (teamsUsageQueryV2, error) {
	var out teamsUsageQueryV2
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" || u.Path != "/analyze" || u.RawPath != "" {
		return out, errTeamsQueryUnsupported
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(values) != 4 {
		return out, errTeamsQueryUnsupported
	}
	for _, name := range []string{"analyze_mode", "analyze_topic", "analyze_data", "q_usage"} {
		if len(values[name]) != 1 {
			return out, errTeamsQueryUnsupported
		}
	}
	if values.Get("analyze_mode") != "query" || values.Get("analyze_topic") != "tokens" || values.Get("analyze_data") != "usage" {
		return out, errTeamsQueryUnsupported
	}
	data, err := teamsStrictRawLinkV2(values.Get("q_usage"))
	if err != nil {
		return out, err
	}
	top, err := teamsStrictObject(data, map[string]bool{"w": true, "g": true})
	if err != nil || len(top) != 2 {
		return out, errTeamsQueryUnsupported
	}
	var where []json.RawMessage
	if json.Unmarshal(top["w"], &where) != nil || len(where) != 2 {
		return out, errTeamsQueryUnsupported
	}
	found := map[string]bool{}
	expectedWhere := []qt.WhereTerm{}
	for _, rawTerm := range where {
		obj, e := teamsStrictObject(rawTerm, map[string]bool{"field": true, "op": true, "value": true})
		if e != nil || len(obj) != 3 {
			return out, errTeamsQueryUnsupported
		}
		var field, op, value string
		if json.Unmarshal(obj["field"], &field) != nil || json.Unmarshal(obj["op"], &op) != nil || json.Unmarshal(obj["value"], &value) != nil || field != "first_usage_at" || (op != ">=" && op != "<") || found[op] {
			return out, errTeamsQueryUnsupported
		}
		instant, e := time.Parse(time.RFC3339Nano, value)
		if e != nil || instant.UTC().Format(time.RFC3339Nano) != value {
			return out, errTeamsQueryUnsupported
		}
		found[op] = true
		expectedWhere = append(expectedWhere, qt.WhereTerm{Field: field, Op: op, Value: value})
		if op == ">=" {
			out.Start = instant
		} else {
			out.End = instant
		}
	}
	if !found[">="] || !found["<"] || !out.Start.Before(out.End) {
		return teamsUsageQueryV2{}, errTeamsQueryUnsupported
	}
	var metrics []json.RawMessage
	if json.Unmarshal(top["g"], &metrics) != nil || len(metrics) < 1 || len(metrics) > 2 {
		return teamsUsageQueryV2{}, errTeamsQueryUnsupported
	}
	ids, fields := map[string]bool{}, map[string]bool{}
	for _, m := range metrics {
		obj, e := teamsStrictObject(m, map[string]bool{"id": true, "op": true, "field": true, "groupBy": true, "scope": true})
		if e != nil {
			return teamsUsageQueryV2{}, e
		}
		var item struct {
			ID, Op, Field, Scope string
			GroupBy              []string `json:"groupBy"`
		}
		if json.Unmarshal(m, &item) != nil || !teamsUsageMetricID.MatchString(item.ID) || ids[item.ID] || item.Op != "sum" || (item.Field != "total_tokens" && item.Field != "cost_usd") || fields[item.Field] || (item.Scope != "" && item.Scope != "allMatching") {
			return teamsUsageQueryV2{}, errTeamsQueryUnsupported
		}
		if _, ok := obj["groupBy"]; !ok || len(item.GroupBy) != 3 || item.GroupBy[0] != "day" || item.GroupBy[1] != "model_family" || item.GroupBy[2] != "project_name" {
			return teamsUsageQueryV2{}, errTeamsQueryUnsupported
		}
		if item.ID == "day" || item.ID == "model_family" || item.ID == "project_name" {
			return teamsUsageQueryV2{}, errTeamsQueryUnsupported
		}
		ids[item.ID] = true
		fields[item.Field] = true
		out.Metrics = append(out.Metrics, teamsMetric{ID: item.ID, Field: item.Field})
	}
	q, err := qt.DecodeQueryLink(values.Get("q_usage"))
	if err != nil || len(q.Where) != len(where) || len(q.Aggregations) != len(metrics) || q.Grouping != nil || q.HostState != nil || q.Offset != 0 || len(q.OrderBy) != 0 || len(q.Select) != 0 || !reflect.DeepEqual(q.Where, expectedWhere) {
		return teamsUsageQueryV2{}, errTeamsQueryUnsupported
	}
	// The host normalizer must also recognize this exact page and query.
	state, err := siteLinkFromURL(raw)
	if err != nil || state.Page != "analyze/query/usage" || state.Query == nil || len(state.Query.Where) != len(where) || len(state.Query.Aggregations) != len(metrics) || !reflect.DeepEqual(state.Query.Where, expectedWhere) {
		return teamsUsageQueryV2{}, errTeamsQueryUnsupported
	}
	// The decoded and host-normalized metric fields must still describe the
	// exact raw operations and tuple; normalization cannot widen the query.
	for _, decoded := range [][]json.RawMessage{q.Aggregations, state.Query.Aggregations} {
		for i, rawMetric := range decoded {
			var v struct {
				ID, Op, Field, Scope string
				GroupBy              []string `json:"groupBy"`
			}
			if json.Unmarshal(rawMetric, &v) != nil || v.ID != out.Metrics[i].ID || v.Op != "sum" || v.Field != out.Metrics[i].Field || (v.Scope != "" && v.Scope != "allMatching") || !reflect.DeepEqual(v.GroupBy, []string{"day", "model_family", "project_name"}) {
				return teamsUsageQueryV2{}, errTeamsQueryUnsupported
			}
		}
	}
	out.GroupBy = []string{"day", "model_family", "project_name"}
	return out, nil
}

func (p teamsUsageQueryV2) valid() bool {
	if p.Start.IsZero() || p.End.IsZero() || !p.Start.Before(p.End) || p.Start.Location() != time.UTC || p.End.Location() != time.UTC || len(p.GroupBy) != 3 || p.GroupBy[0] != "day" || p.GroupBy[1] != "model_family" || p.GroupBy[2] != "project_name" || len(p.Metrics) < 1 || len(p.Metrics) > 2 {
		return false
	}
	ids, fields := map[string]bool{}, map[string]bool{}
	for _, m := range p.Metrics {
		if !teamsUsageMetricID.MatchString(m.ID) || ids[m.ID] || fields[m.Field] || (m.Field != "total_tokens" && m.Field != "cost_usd") || m.ID == "day" || m.ID == "model_family" || m.ID == "project_name" {
			return false
		}
		ids[m.ID] = true
		fields[m.Field] = true
	}
	return true
}

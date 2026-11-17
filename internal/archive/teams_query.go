package archive

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// A Teams plan is compiled only from the signed Pharos URL. Unsupported
// syntax is an explicit compatibility error, never an empty answer.
var errTeamsQueryUnsupported = errors.New("this Pharos query needs a newer Teams integration")

type teamsMetric struct {
	ID    string
	Field string
}
type teamsQueryPlan struct{ Metrics []teamsMetric }

// strictObject prevents duplicate JSON keys and rejects fields which the link
// decoder would otherwise normalize away. Every added key needs semantic review.
func teamsStrictObject(data []byte, allowed map[string]bool) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, errTeamsQueryUnsupported
	}
	out := map[string]json.RawMessage{}
	for d.More() {
		tok, err = d.Token()
		if err != nil {
			return nil, errTeamsQueryUnsupported
		}
		key, ok := tok.(string)
		if !ok || !allowed[key] {
			return nil, errTeamsQueryUnsupported
		}
		if _, exists := out[key]; exists {
			return nil, errTeamsQueryUnsupported
		}
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return nil, errTeamsQueryUnsupported
		}
		out[key] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, errTeamsQueryUnsupported
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errTeamsQueryUnsupported
	}
	return out, nil
}

func teamsRawLink(token string) ([]byte, error) {
	if len(token) > qt.MaxQueryTokenLength || len(token) < 4 {
		return nil, errTeamsQueryUnsupported
	}
	compressed := strings.HasPrefix(token, "2d.")
	if !compressed && !strings.HasPrefix(token, "2j.") {
		return nil, errTeamsQueryUnsupported
	}
	data, err := base64.RawURLEncoding.DecodeString(token[3:])
	if err != nil {
		return nil, errTeamsQueryUnsupported
	}
	if compressed {
		r := flate.NewReader(bytes.NewReader(data))
		defer r.Close()
		data, err = io.ReadAll(io.LimitReader(r, qt.MaxQueryStateBytes+1))
		if err != nil {
			return nil, errTeamsQueryUnsupported
		}
	}
	if len(data) > qt.MaxQueryStateBytes {
		return nil, errTeamsQueryUnsupported
	}
	return data, nil
}

func compileTeamsQuery(raw string) (teamsQueryPlan, error) {
	var plan teamsQueryPlan
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" || u.Path != "/analyze" || u.RawPath != "" {
		return plan, errTeamsQueryUnsupported
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(values) != 4 {
		return plan, errTeamsQueryUnsupported
	}
	for _, name := range []string{"analyze_mode", "analyze_topic", "analyze_data", "q_usage"} {
		if len(values[name]) != 1 {
			return plan, errTeamsQueryUnsupported
		}
	}
	if values.Get("analyze_mode") != "query" || values.Get("analyze_topic") != "tokens" || values.Get("analyze_data") != "usage" {
		return plan, errTeamsQueryUnsupported
	}
	token := values.Get("q_usage")
	data, err := teamsRawLink(token)
	if err != nil {
		return plan, err
	}
	top, err := teamsStrictObject(data, map[string]bool{"g": true})
	if err != nil || len(top) != 1 {
		return plan, errTeamsQueryUnsupported
	}
	var metrics []json.RawMessage
	if err = json.Unmarshal(top["g"], &metrics); err != nil || len(metrics) == 0 || len(metrics) > 32 {
		return plan, errTeamsQueryUnsupported
	}
	state, err := siteLinkFromURL(raw)
	if err != nil || state.Page != "analyze/query/usage" || state.Query == nil || len(state.Query.Aggregations) != len(metrics) {
		return plan, errTeamsQueryUnsupported
	}
	ids := map[string]bool{}
	for _, metric := range metrics {
		fields, e := teamsStrictObject(metric, map[string]bool{"id": true, "op": true, "field": true, "groupBy": true, "scope": true})
		if e != nil {
			return plan, e
		}
		var item struct {
			ID      string   `json:"id"`
			Op      string   `json:"op"`
			Field   string   `json:"field"`
			GroupBy []string `json:"groupBy"`
			Scope   string   `json:"scope"`
		}
		if json.Unmarshal(metric, &item) != nil || item.ID == "" || len(item.ID) > 128 || ids[item.ID] || item.Op != "sum" || teamsNumericCapabilities[item.Field].MetricID == "" || len(item.GroupBy) != 0 || (item.Scope != "" && item.Scope != "allMatching") {
			return plan, errTeamsQueryUnsupported
		}
		// Null groupBy is not a canonical empty group and must not widen a query.
		if group, ok := fields["groupBy"]; ok && !bytes.Equal(bytes.TrimSpace(group), []byte("[]")) {
			return plan, errTeamsQueryUnsupported
		}
		ids[item.ID] = true
		plan.Metrics = append(plan.Metrics, teamsMetric{ID: item.ID, Field: item.Field})
	}
	if len(plan.Metrics) == 0 {
		return plan, fmt.Errorf("%w: no metrics", errTeamsQueryUnsupported)
	}
	return plan, nil
}

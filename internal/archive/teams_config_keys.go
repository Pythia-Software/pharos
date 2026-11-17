package archive

import "encoding/json"

// Exact spelling matters: encoding/json otherwise accepts case-insensitive
// aliases, including two keys that address the same security-relevant field.
func teamsConfigExactKeys(raw []byte) bool {
	object := func(raw []byte, keys ...string) (map[string]json.RawMessage, bool) {
		allowed := make(map[string]bool, len(keys))
		for _, key := range keys {
			allowed[key] = true
		}
		m, err := teamsStrictObject(raw, allowed)
		return m, err == nil
	}
	top, ok := object(raw, "question", "query_url", "approval_policy", "requester", "audiences", "result_schema", "min_app_version", "retention_policy", "schedule", "publication_v2")
	if !ok {
		return false
	}
	if rawSeries, exists := top["publication_v2"]; exists {
		if _, err := parseTeamsReportSeries(rawSeries); err != nil {
			return false
		}
	}
	if schedule, exists := top["schedule"]; exists {
		fields, valid := object(schedule, "cadence", "timezone", "catch_up", "response_window_seconds")
		if !valid || len(fields) != 4 {
			return false
		}
	}
	if _, ok = object(top["requester"], "id", "display_name"); !ok {
		return false
	}
	audience, ok := object(top["audiences"], "response_readers", "publications", "exports")
	if !ok {
		return false
	}
	var publications []json.RawMessage
	if json.Unmarshal(audience["publications"], &publications) != nil {
		return false
	}
	for _, entry := range publications {
		fields, valid := object(entry, "id", "kind", "readers", "fields", "include_completeness")
		if !valid || (len(fields) != 4 && len(fields) != 5) {
			return false
		}
		if value, present := fields["include_completeness"]; present && string(value) != "true" && string(value) != "false" {
			return false
		}
		var readers []json.RawMessage
		if json.Unmarshal(fields["readers"], &readers) != nil {
			return false
		}
		for _, reader := range readers {
			v, ok := object(reader, "kind", "id")
			if !ok || len(v) != 2 {
				return false
			}
		}
	}
	var exports []json.RawMessage
	if json.Unmarshal(audience["exports"], &exports) != nil {
		return false
	}
	for _, entry := range exports {
		fields, valid := object(entry, "id", "kind", "destination", "fields")
		if !valid || len(fields) != 4 {
			return false
		}
	}
	var readers []json.RawMessage
	if json.Unmarshal(audience["response_readers"], &readers) != nil {
		return false
	}
	for _, reader := range readers {
		if _, ok = object(reader, "kind", "id"); !ok {
			return false
		}
	}
	if _, ok = object(top["retention_policy"], "mode", "duration_days", "policy_version"); !ok {
		return false
	}
	schema, ok := object(top["result_schema"], "columns")
	if !ok {
		return false
	}
	var columns []json.RawMessage
	if json.Unmarshal(schema["columns"], &columns) != nil {
		return false
	}
	for _, col := range columns {
		if _, ok = object(col, "id", "type", "role", "merge", "metric_id", "unit", "nullable"); !ok {
			return false
		}
	}
	return true
}

func teamsPayloadExactKeys(raw []byte, plan teamsQueryPlan) bool {
	object := func(raw []byte, keys ...string) (map[string]json.RawMessage, bool) {
		allowed := make(map[string]bool, len(keys))
		for _, key := range keys {
			allowed[key] = true
		}
		m, err := teamsStrictObject(raw, allowed)
		return m, err == nil
	}
	top, ok := object(raw, "outcome", "form", "observed_at", "computed_at", "coverage", "rows", "delete_keys")
	if !ok {
		return false
	}
	if _, ok = object(top["coverage"], "source_state", "measurement", "caveats"); !ok {
		return false
	}
	var rows []json.RawMessage
	if json.Unmarshal(top["rows"], &rows) != nil {
		return false
	}
	metrics := make([]string, len(plan.Metrics))
	for i, metric := range plan.Metrics {
		metrics[i] = metric.ID
	}
	for _, rawRow := range rows {
		row, valid := object(rawRow, "key", "values")
		if !valid {
			return false
		}
		if _, valid = object(row["values"], metrics...); !valid {
			return false
		}
	}
	return true
}

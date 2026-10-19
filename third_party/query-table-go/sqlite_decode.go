package querytable

import (
	"encoding/json"
	"fmt"
)

// DecodeSQLiteQuery decodes JSON in readable or compact v1 wire form. Use this
// at a SQLite HTTP boundary rather than decoding a v2 envelope into WireQuery,
// which would discard fields that do not belong to the v1 wire contract.
func DecodeSQLiteQuery(data []byte) (WireQuery, error) {
	if len(data) > maxQueryTokenBytes {
		return WireQuery{}, fmt.Errorf("SQLite query too large")
	}
	if err := sqliteEnvelope(data); err != nil {
		return WireQuery{}, err
	}
	var q WireQuery
	if err := json.Unmarshal(data, &q); err != nil {
		return q, err
	}
	for _, metric := range q.Aggregations {
		if err := validateBasicMetricShape(metric); err != nil {
			return WireQuery{}, err
		}
	}
	return q, nil
}
func sqliteEnvelope(data []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	if object == nil {
		return fmt.Errorf("SQLite request must be an object")
	}
	for _, key := range []string{"expectedRevisions", "planToken", "computedExecution"} {
		if _, ok := object[key]; ok {
			return fmt.Errorf("SQLite v1 request cannot contain %s", key)
		}
	}
	var envelope struct {
		Version  int    `json:"version"`
		Profile  string `json:"profile"`
		Snapshot string `json:"snapshot"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if envelope.Version != 0 && envelope.Version != 1 || envelope.Profile != "" || envelope.Snapshot != "" {
		return fmt.Errorf("SQLite adapter supports v1 requests; v2 profiles and snapshot tokens are unsupported")
	}
	return nil
}
func (r *SQLiteAggregationRequest) UnmarshalJSON(data []byte) error {
	if len(data) > maxQueryTokenBytes {
		return fmt.Errorf("SQLite aggregation request too large")
	}
	if err := sqliteEnvelope(data); err != nil {
		return err
	}
	type plain SQLiteAggregationRequest
	var request plain
	if err := json.Unmarshal(data, &request); err != nil {
		return err
	}
	if len(request.Diagnostics) > 0 {
		return diagnostic("residual_query", "aggregation request contains unresolved diagnostics")
	}
	if err := (WireQuery{Where: request.Where, Aggregations: request.Aggregations}).Validate(); err != nil {
		return err
	}
	for _, metric := range request.Aggregations {
		if err := validateBasicMetricShape(metric); err != nil {
			return err
		}
	}
	*r = SQLiteAggregationRequest(request)
	return nil
}

package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"reflect"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

const teamsUsageRegistryV1Digest = "623d407171680cfe90cb64420dcafe846bcd797c436958c08e21dae1bc6b7f59"

// teamsUsageCalculationBinding fixes calculation semantics and the exact work
// config. It is not a source assignment, publication authorization or receipt.
type teamsUsageCalculationBinding struct {
	ConfigSHA256   string `json:"config_sha256"`
	Registry       string `json:"registry"`
	RegistrySHA256 string `json:"registry_sha256"`
	SchemaSHA256   string `json:"schema_sha256"`
	WindowSHA256   string `json:"window_sha256"`
	PriceBook      string `json:"price_book"`
	Taxonomy       string `json:"taxonomy"`
	Buckets        string `json:"buckets"`
	Start          string `json:"start_at"`
	End            string `json:"end_at"`
	TimeZone       string `json:"time_zone"`
}

// bindTeamsUsageCalculations checks the immutable calculation arm and exact
// config bytes. It does not replace full Q2 work/audience/schema validation.
func bindTeamsUsageCalculations(plan teamsUsageQueryV2, config []byte, expectedHash string) (teamsUsageCalculationBinding, string, error) {
	var out teamsUsageCalculationBinding
	if !plan.valid() || len(config) > 128*1024 || !teams.ValidUniqueJSON(config) || teamsPayloadHash(config) != expectedHash {
		return out, "", errTeamsQueryUnsupported
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(config, &root) != nil || root == nil {
		return out, "", errTeamsQueryUnsupported
	}
	var queryURL string
	if json.Unmarshal(root["query_url"], &queryURL) != nil {
		return out, "", errTeamsQueryUnsupported
	}
	decoded, err := compileTeamsUsageQueryV2(queryURL)
	if err != nil || !reflect.DeepEqual(decoded, plan) {
		return out, "", errTeamsQueryUnsupported
	}
	arm, err := teamsStrictObject(root["query_contract"], map[string]bool{"version": true, "registry": true, "source_policy": true, "population": true, "calculations": true})
	if err != nil || len(arm) != 5 {
		return out, "", errTeamsQueryUnsupported
	}
	var version int
	var registry, policy string
	if json.Unmarshal(arm["version"], &version) != nil || json.Unmarshal(arm["registry"], &registry) != nil || json.Unmarshal(arm["source_policy"], &policy) != nil || version != 2 || registry != "usage_1" || policy != "verified_usage" {
		return out, "", errTeamsQueryUnsupported
	}
	population, err := teamsStrictObject(arm["population"], map[string]bool{"kind": true, "time_zone": true})
	if err != nil || len(population) != 2 {
		return out, "", errTeamsQueryUnsupported
	}
	var kind, zone string
	if json.Unmarshal(population["kind"], &kind) != nil || json.Unmarshal(population["time_zone"], &zone) != nil || kind != "fixed" || zone != "UTC" {
		return out, "", errTeamsQueryUnsupported
	}
	calc, err := teamsStrictObject(arm["calculations"], map[string]bool{"price_book": true, "taxonomy": true, "buckets": true})
	if err != nil || len(calc) != 3 {
		return out, "", errTeamsQueryUnsupported
	}
	var price, taxonomy, buckets string
	if json.Unmarshal(calc["price_book"], &price) != nil || json.Unmarshal(calc["taxonomy"], &taxonomy) != nil || json.Unmarshal(calc["buckets"], &buckets) != nil || taxonomy != "fixture_models_1" || buckets != "calendar_1" {
		return out, "", errTeamsQueryUnsupported
	}
	// The first registry fixture names synthetic calculations only. The real
	// default book needs a separately reviewed immutable registry identity.
	if price != "fixture_prices_1" {
		return out, "", errTeamsQueryUnsupported
	}
	schema, schemaHash, err := teamsUsageSchemaV2(plan, taxonomy)
	if err != nil {
		return out, "", err
	}
	// Enforce per-column key sets, including fields explicitly false, before
	// decoding; known keys from another column kind are still extra here.
	schemaObject, e := teamsStrictObject(root["result_schema"], map[string]bool{"schema_version": true, "columns": true})
	if e != nil || len(schemaObject) != 2 {
		return out, "", errTeamsQueryUnsupported
	}
	var columns []json.RawMessage
	if json.Unmarshal(schemaObject["columns"], &columns) != nil || len(columns) != len(schema.Columns) {
		return out, "", errTeamsQueryUnsupported
	}
	for i, column := range columns {
		expectedRaw, e := json.Marshal(schema.Columns[i])
		if e != nil {
			return out, "", e
		}
		var expectedFields map[string]json.RawMessage
		if json.Unmarshal(expectedRaw, &expectedFields) != nil {
			return out, "", errTeamsQueryUnsupported
		}
		allowed := map[string]bool{}
		for key := range expectedFields {
			allowed[key] = true
		}
		actualFields, e := teamsStrictObject(column, allowed)
		if e != nil || len(actualFields) != len(allowed) {
			return out, "", errTeamsQueryUnsupported
		}
	}
	var actual teamsUsageResultSchema
	decoder := json.NewDecoder(bytes.NewReader(root["result_schema"]))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&actual) != nil || !reflect.DeepEqual(actual, schema) {
		return out, "", errTeamsQueryUnsupported
	}
	if _, e := decoder.Token(); e != io.EOF {
		return out, "", errTeamsQueryUnsupported
	}
	window := teamsUsageWindow{Start: plan.Start.UTC().Format(time.RFC3339Nano), End: plan.End.UTC().Format(time.RFC3339Nano), TimeZone: zone}
	windowRaw, err := json.Marshal(window)
	if err != nil {
		return out, "", err
	}
	windowSum := sha256.Sum256(append([]byte("pharos-teams-usage-window-v2\n"), windowRaw...))
	out = teamsUsageCalculationBinding{ConfigSHA256: expectedHash, Registry: registry, RegistrySHA256: teamsUsageRegistryV1Digest, SchemaSHA256: schemaHash, WindowSHA256: hex.EncodeToString(windowSum[:]), PriceBook: price, Taxonomy: taxonomy, Buckets: buckets, Start: window.Start, End: window.End, TimeZone: zone}
	raw, err := json.Marshal(out)
	if err != nil {
		return teamsUsageCalculationBinding{}, "", err
	}
	digest := sha256.Sum256(append([]byte("pharos-teams-usage-calculation-v2\n"), raw...))
	return out, hex.EncodeToString(digest[:]), nil
}

// Field order is the canonical JSON order for the schema digest. Pointer
// nullable/scale fields distinguish missing from explicit false/zero.
type teamsUsageResultColumn struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Role     string `json:"role"`
	Nullable *bool  `json:"nullable"`
	Grain    string `json:"grain,omitempty"`
	TimeZone string `json:"time_zone,omitempty"`
	Taxonomy string `json:"taxonomy,omitempty"`
	Scope    string `json:"scope,omitempty"`
	Unit     string `json:"unit,omitempty"`
	Scale    *int   `json:"scale,omitempty"`
	Merge    string `json:"merge,omitempty"`
}
type teamsUsageResultSchema struct {
	Version int                      `json:"schema_version"`
	Columns []teamsUsageResultColumn `json:"columns"`
}
type teamsUsageWindow struct {
	Start    string `json:"start_at"`
	End      string `json:"end_at"`
	TimeZone string `json:"time_zone"`
}

func teamsUsageSchemaV2(plan teamsUsageQueryV2, taxonomy string) (teamsUsageResultSchema, string, error) {
	var out teamsUsageResultSchema
	if !plan.valid() || taxonomy != "fixture_models_1" {
		return out, "", errTeamsQueryUnsupported
	}
	no, yes := false, true
	scale := 6
	out = teamsUsageResultSchema{Version: 2, Columns: []teamsUsageResultColumn{
		{ID: "day", Type: "civil_bucket", Role: "dimension", Nullable: &no, Grain: "day", TimeZone: "UTC"},
		{ID: "model_family", Type: "enum", Role: "dimension", Nullable: &no, Taxonomy: taxonomy},
		{ID: "project_name", Type: "string", Role: "dimension", Nullable: &no, Scope: "library"},
	}}
	for _, metric := range plan.Metrics {
		col := teamsUsageResultColumn{ID: metric.ID, Role: "metric", Merge: "sum"}
		switch metric.Field {
		case "total_tokens":
			col.Type = "integer"
			col.Nullable = &no
			col.Unit = "tokens"
		case "cost_usd":
			col.Type = "fixed_decimal"
			col.Nullable = &yes
			col.Unit = "USD"
			col.Scale = &scale
		default:
			return teamsUsageResultSchema{}, "", errTeamsQueryUnsupported
		}
		out.Columns = append(out.Columns, col)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return teamsUsageResultSchema{}, "", err
	}
	sum := sha256.Sum256(raw)
	return out, hex.EncodeToString(sum[:]), nil
}

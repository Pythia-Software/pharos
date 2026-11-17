package archive

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"github.com/gbdubs/pharos/pricing"
	"os"
	"testing"
	"time"
)

const teamsUsageV2Raw = `{"w":[{"field":"first_usage_at","op":">=","value":"2026-11-09T00:00:00Z"},{"field":"first_usage_at","op":"<","value":"2026-11-16T00:00:00Z"}],"g":[{"id":"total_tokens","op":"sum","groupBy":["day","model_family","project_name"],"field":"total_tokens","scope":"allMatching"},{"id":"cost_usd","op":"sum","groupBy":["day","model_family","project_name"],"field":"cost_usd","scope":"allMatching"}]}`

func TestTeamsUsageV2RegistryFixture(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/teams/usage-registry-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if hashBytes(raw) != "623d407171680cfe90cb64420dcafe846bcd797c436958c08e21dae1bc6b7f59" {
		t.Fatal("synthetic registry bytes changed")
	}
}

func TestTeamsUsageV2StrictLink(t *testing.T) {
	fixtureBytes, e := os.ReadFile("../../testdata/teams/typed-usage-query-v2.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		QueryURL string `json:"query_url"`
		LinkJSON string `json:"link_json"`
	}
	if e = json.Unmarshal(fixtureBytes, &fixture); e != nil || fixture.LinkJSON != teamsUsageV2Raw {
		t.Fatal("invalid shared usage fixture")
	}
	plan, err := compileTeamsUsageQueryV2(fixture.QueryURL)
	if err != nil || len(plan.Metrics) != 2 || len(plan.GroupBy) != 3 || plan.Start.Format("2006-01-02") != "2026-11-09" {
		t.Fatalf("valid link: %+v %v", plan, err)
	}
	var top map[string]json.RawMessage
	if json.Unmarshal([]byte(teamsUsageV2Raw), &top) != nil {
		t.Fatal("invalid fixture")
	}
	for _, raw := range []string{
		`{"w":[],"g":[],"unknown":true}`,
		`{"w":[{"field":"first_usage_at","op":">=","value":"2026-11-09T00:00:00Z","extra":1},{"field":"first_usage_at","op":"<","value":"2026-11-16T00:00:00Z"}],"g":[]}`,
		`{"w":[{"field":"first_usage_at","op":">=","value":"2026-11-09T00:00:00Z"},{"field":"first_usage_at","op":"<","value":"2026-11-16T00:00:00Z"}],"g":[{"id":"t","op":"sum","field":"total_tokens","groupBy":["day","model_family","project_name"],"scope":"shownRows"}]}`,
		`{"w":[{"field":"first_usage_at","op":">=","value":"2026-11-09T00:00:00Z"},{"field":"first_usage_at","op":"<","value":"2026-11-16T00:00:00Z"}],"g":[{"id":"t","op":"sum","field":"total_tokens","groupBy":["day","model_family","project_name"],"where":[{"field":"text","op":"contains","value":"x"}]}]}`,
	} {
		if _, err = compileTeamsUsageQueryV2(teamsTestQuery(teamsRawToken(raw))); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if _, err = compileTeamsUsageQueryV2(teamsTestQuery(teamsRawToken(teamsUsageV2Raw)) + "&q_usage=duplicate"); err == nil {
		t.Fatal("accepted duplicate URL argument")
	}
}

func TestTeamsUsageV2ScopedGroupedExecution(t *testing.T) {
	c, _ := testCatalog(t)
	plan, err := compileTeamsUsageQueryV2(teamsTestQuery(teamsRawToken(teamsUsageV2Raw)))
	if err != nil {
		t.Fatal(err)
	}
	at := func(s string) time.Time {
		v, e := time.Parse(time.RFC3339, s)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	var fixture struct {
		Facts []struct {
			ID         string `json:"id"`
			At         string `json:"first_usage_at"`
			Model      string `json:"model_family"`
			Project    string `json:"project_name"`
			Protection string `json:"protection"`
			Owned      bool   `json:"owned"`
			Total      int64  `json:"total_tokens"`
			Cost       *int64 `json:"cost_micros"`
		} `json:"synthetic_facts"`
		Expected []struct {
			Key    []string          `json:"key"`
			Values map[string]*int64 `json:"values"`
		} `json:"expected_rows"`
	}
	raw, err := os.ReadFile("../../testdata/teams/typed-usage-query-v2.json")
	if err != nil || json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("invalid shared query fixture")
	}
	facts := []teamsOwnedUsageFact{}
	for _, f := range fixture.Facts {
		if !f.Owned {
			continue
		}
		facts = append(facts, teamsOwnedUsageFact{ID: f.ID, At: at(f.At), ModelFamily: f.Model, Project: f.Project, Protection: f.Protection, Total: f.Total, CostMicros: f.Cost})
	}
	tx, err := c.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := teamsExecuteUsageV2In(t.Context(), tx, plan, facts, at("2026-11-17T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 2 || !result.MissingPrice || !result.ProjectOmitted {
		t.Fatalf("unexpected grouped result: %+v", result)
	}
	byDay := map[string]map[string]any{}
	for _, r := range result.Rows {
		byDay[firstString(r["day"])] = r
	}
	for _, expected := range fixture.Expected {
		row := byDay[expected.Key[0]]
		if row == nil || row["model_family"] != expected.Key[1] || row["project_name"] != expected.Key[2] {
			t.Fatalf("missing group: %+v", result.Rows)
		}
		for id, value := range expected.Values {
			if value == nil {
				if row[id] != nil {
					t.Fatalf("unknown %s became %v", id, row[id])
				}
			} else if row[id] != *value {
				t.Fatalf("%s = %v, want %d", id, row[id], *value)
			}
		}
	}
	// A partial project-only population is not a proved empty result.
	partial, err := teamsExecuteUsageV2In(t.Context(), tx, plan, facts[2:3], at("2026-11-17T00:00:00Z"))
	if err != nil || len(partial.Rows) != 0 || !partial.ProjectOmitted {
		t.Fatalf("unwitnessed coverage: %+v %v", partial, err)
	}
	// A group budget cannot return an apparently complete prefix.
	override(t, &teamsUsageV2MaxRows, 1)
	if _, err = teamsExecuteUsageV2In(t.Context(), tx, plan, facts, at("2026-11-17T00:00:00Z")); err == nil {
		t.Fatal("group budget returned a prefix")
	}
	if _, err = c.teamsExecuteUsageV2(t.Context(), plan); !errors.Is(err, errTeamsSourceUnavailable) {
		t.Fatalf("production eligibility was not closed: %v", err)
	}
}

func TestTeamsUsageV2ExactPinnedPricing(t *testing.T) {
	c, _ := testCatalog(t)
	tx, err := c.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	book, err := c.teamsLoadExactPriceBook(t.Context(), tx, hashBytes(pricing.CostChanges))
	if err != nil || book == nil {
		t.Fatalf("embedded price book unavailable: %v", err)
	}
	fact := teamsCodexRequestFact{Timestamp: "2026-11-09T12:00:00Z", Model: "gpt-6.1-sol", Input: 8, Cached: teamsOptionalTokens{Known: true, Value: 2}, Output: 2, Total: 10}
	amount, assumed := book.priceFact(fact)
	if amount == nil || *amount != 32 || assumed {
		t.Fatalf("exact price: %v assumed=%v", amount, assumed)
	}
	native := book.native.cost(fact.Model, "2026-11-09", map[string]int64{"uncached_input_tokens": 6, "cache_read_input_tokens": 2, "output_tokens": 2})
	if native.cost == nil || math.Abs(*native.cost-float64(*amount)/1_000_000) > 0.0000005 {
		t.Fatalf("native price mismatch: %+v vs %d", native, *amount)
	}
	// Half a microdollar rounds up at the individual proved request grain.
	half := teamsCodexRequestFact{Timestamp: fact.Timestamp, Model: fact.Model, Input: 5, Cached: teamsOptionalTokens{Known: true, Value: 5}, Output: 0, Total: 5}
	if value, _ := book.priceFact(half); value == nil || *value != 1 {
		t.Fatalf("half-up rounding: %v", value)
	}
	fact.Cached = teamsOptionalTokens{}
	if value, _ := book.priceFact(fact); value != nil {
		t.Fatal("unknown cached input was priced")
	}
	if _, err = tx.Exec(`UPDATE cost_changes SET input_per_mtok=input_per_mtok+1 WHERE id=(SELECT id FROM cost_changes WHERE status='confirmed' LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	if changed, err := c.teamsLoadExactPriceBook(t.Context(), tx, hashBytes(pricing.CostChanges)); err != nil || changed != nil {
		t.Fatal("edited library price book trusted as embedded")
	}
}

func TestTeamsUsageV2CalculationBinding(t *testing.T) {
	queryURL := teamsTestQuery(teamsRawToken(teamsUsageV2Raw))
	plan, err := compileTeamsUsageQueryV2(queryURL)
	if err != nil {
		t.Fatal(err)
	}
	fixtureRaw, e := os.ReadFile("../../testdata/teams/typed-usage-contract-v2.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Schema    json.RawMessage `json:"result_schema"`
		SchemaSHA string          `json:"schema_sha256"`
		WindowSHA string          `json:"window_sha256"`
	}
	if json.Unmarshal(fixtureRaw, &fixture) != nil {
		t.Fatal("invalid contract fixture")
	}
	config, err := json.Marshal(map[string]any{"query_url": queryURL, "result_schema": fixture.Schema, "query_contract": map[string]any{"version": 2, "registry": "usage_1", "source_policy": "verified_usage", "population": map[string]any{"kind": "fixed", "time_zone": "UTC"}, "calculations": map[string]any{"price_book": "fixture_prices_1", "taxonomy": "fixture_models_1", "buckets": "calendar_1"}}})
	if err != nil {
		t.Fatal(err)
	}
	binding, hash, err := bindTeamsUsageCalculations(plan, config, teamsPayloadHash(config))
	if err != nil || len(hash) != 64 || binding.RegistrySHA256 != teamsUsageRegistryV1Digest || binding.Start != "2026-11-09T00:00:00Z" || binding.SchemaSHA256 != fixture.SchemaSHA || binding.WindowSHA256 != fixture.WindowSHA {
		t.Fatalf("calculation binding: %+v %q %v", binding, hash, err)
	}
	if _, _, err = bindTeamsUsageCalculations(plan, config, strings.Repeat("a", 64)); err == nil {
		t.Fatal("wrong immutable config hash accepted")
	}
	stale := plan
	stale.Start = stale.Start.Add(time.Second)
	if _, _, err = bindTeamsUsageCalculations(stale, config, teamsPayloadHash(config)); err == nil {
		t.Fatal("changed window accepted")
	}
	bad := bytes.Replace(config, []byte(`"time_zone":"UTC"`), []byte(`"time_zone":"UTC","hidden":true`), 1)
	if _, _, err = bindTeamsUsageCalculations(plan, bad, teamsPayloadHash(bad)); err == nil {
		t.Fatal("unknown calculation property accepted")
	}
	extraColumn := bytes.Replace(config, []byte(`"grain":"day"`), []byte(`"grain":"day","unit":""`), 1)
	if _, _, err = bindTeamsUsageCalculations(plan, extraColumn, teamsPayloadHash(extraColumn)); err == nil {
		t.Fatal("extra known-but-inapplicable schema field accepted")
	}
	duplicate := bytes.Replace(config, []byte(`"registry":"usage_1"`), []byte(`"registry":"usage_1","registry":"usage_1"`), 1)
	if _, _, err = bindTeamsUsageCalculations(plan, duplicate, teamsPayloadHash(duplicate)); err == nil {
		t.Fatal("duplicate contract property accepted")
	}
}

func TestTeamsUsageV2OverflowAndSchemaOrder(t *testing.T) {
	c, _ := testCatalog(t)
	plan, err := compileTeamsUsageQueryV2(teamsTestQuery(teamsRawToken(teamsUsageV2Raw)))
	if err != nil {
		t.Fatal(err)
	}
	schema, _, err := teamsUsageSchemaV2(plan, "fixture_models_1")
	if err != nil || len(schema.Columns) != 5 {
		t.Fatal("two-measure schema")
	}
	one := plan
	one.Metrics = append([]teamsMetric(nil), plan.Metrics[:1]...)
	single, _, err := teamsUsageSchemaV2(one, "fixture_models_1")
	if err != nil || len(single.Columns) != 4 || single.Columns[3].ID != "total_tokens" {
		t.Fatal("schema did not follow admitted measures")
	}
	instant, _ := time.Parse(time.RFC3339, "2026-11-09T12:00:00Z")
	facts := []teamsOwnedUsageFact{{ID: "a", At: instant, ModelFamily: "family_a", Project: "Build", Protection: "non_private", Total: teamsMaxExactInteger}, {ID: "b", At: instant, ModelFamily: "family_a", Project: "Build", Protection: "non_private", Total: 1}}
	tx, err := c.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = teamsExecuteUsageV2In(t.Context(), tx, plan, facts, instant); err == nil {
		t.Fatal("unsafe group integer accepted")
	}
}

func TestTeamsUsageV2CanonicalKey(t *testing.T) {
	row := map[string]any{"day": "2026-11-09T00:00:00Z", "model_family": "family_a", "project_name": `A<&"`}
	key, err := teamsUsageV2Key(row)
	if err != nil || string(key) != `["2026-11-09T00:00:00Z","family_a","A<&\""]` {
		t.Fatalf("canonical key %q %v", key, err)
	}
	var vectors struct {
		Cases []struct {
			Project   string `json:"project"`
			Canonical string `json:"canonical"`
		} `json:"canonical_key_vectors"`
	}
	rawFixture, err := os.ReadFile("../../testdata/teams/typed-usage-query-v2.json")
	if err != nil || json.Unmarshal(rawFixture, &vectors) != nil || len(vectors.Cases) == 0 {
		t.Fatal("missing canonical vectors")
	}
	for _, vector := range vectors.Cases {
		row["project_name"] = vector.Project
		actual, err := teamsUsageV2Key(row)
		if err != nil || string(actual) != vector.Canonical {
			t.Fatalf("key %q got %q want %q: %v", vector.Project, actual, vector.Canonical, err)
		}
	}
	for _, raw := range []string{`["2026-11-09T00:00:00Z","family_a","\ud800"]`, `["2026-11-09T00:00:00Z","family_a","\udc00"]`, `["2026-11-09T00:00:00Z","family_a","\u003c"]`} {
		if _, err := teamsDecodeUsageV2Key([]byte(raw)); err == nil {
			t.Fatalf("accepted noncanonical or unpaired surrogate key %q", raw)
		}
	}
	if _, err := teamsDecodeUsageV2Key([]byte(`["2026-11-09T00:00:00Z","family_a","😀"]`)); err != nil {
		t.Fatalf("valid UTF-8 key rejected: %v", err)
	}
	for _, bad := range []string{"x\x00y", "x\u2028y", "x\u2029y"} {
		row["project_name"] = bad
		if _, err = teamsUsageV2Key(row); err == nil {
			t.Fatalf("accepted invalid project %q", bad)
		}
	}
}

func TestTeamsUsageV2AliasAndFractionalWindow(t *testing.T) {
	c, _ := testCatalog(t)
	plan, err := compileTeamsUsageQueryV2(teamsTestQuery(teamsRawToken(teamsUsageV2Raw)))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := c.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	cost := int64(7)
	facts := []teamsOwnedUsageFact{
		{ID: "inside", At: plan.Start.Add(500 * time.Millisecond), ModelFamily: "family_a", Project: "Build", Protection: "non_private", Total: 11, CostMicros: &cost},
		{ID: "before_end", At: plan.End.Add(-time.Nanosecond), ModelFamily: "family_a", Project: "Build", Protection: "non_private", Total: 13, CostMicros: &cost},
		{ID: "end", At: plan.End, ModelFamily: "family_a", Project: "Build", Protection: "non_private", Total: 99, CostMicros: &cost},
	}
	for _, reverse := range []bool{false, true} {
		plan.Metrics = []teamsMetric{{ID: "q1_metric_total_tokens", Field: "cost_usd"}, {ID: "total_tokens", Field: "total_tokens"}}
		if reverse {
			plan.Metrics[0], plan.Metrics[1] = plan.Metrics[1], plan.Metrics[0]
		}
		result, err := teamsExecuteUsageV2In(t.Context(), tx, plan, facts, plan.End)
		if err != nil || len(result.Rows) != 2 || result.Rows[0]["q1_metric_total_tokens"] != int64(7) || result.Rows[0]["total_tokens"] != int64(11) || result.Rows[1]["q1_metric_total_tokens"] != int64(7) || result.Rows[1]["total_tokens"] != int64(13) {
			t.Fatalf("reverse=%v fractional-window/aliases: %+v %v", reverse, result, err)
		}
	}
}

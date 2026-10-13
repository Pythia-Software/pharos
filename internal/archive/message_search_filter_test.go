package archive

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

func TestMessageSearchFiltersExactRowsAndMetrics(t *testing.T) {
	c := textFixture(t)
	if _, err := c.DB.Exec(`UPDATE messages SET selected=1;
 INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,spans_json)
 SELECT m.id,m.conversation_id,cv.workspace_id,m.created_at,substr(m.created_at,1,10),length(m.text),'[]'
 FROM messages m JOIN conversations cv ON cv.id=m.conversation_id WHERE m.role='user' AND m.kind='message'`); err != nil {
		t.Fatal(err)
	}
	values := url.Values{"find": {"foo::bar"}, "find_mode": {"literal"}, "find_surface": {"user"}, "find_fuzzy": {"0"}}
	table, matches, err := c.messageFindDataset(t.Context(), values, writingMessageDataset)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches.search.Items) != 1 || matches.search.Items[0].MessageID != "a" {
		t.Fatalf("unexpected search: %+v", matches.search)
	}
	schema, _ := sqliteQuerySchema("writing_messages")
	d := queryV2Dataset("writing_messages", schema)
	d.SourceSQL = table.v2Source(schema)
	rows := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"id", "text"}, Limit: 50}}
	result, err := d.ExecuteV2(context.Background(), c.DB, &rows, nil, qt.PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows.Rows) != 1 || result.Rows.Rows[0]["id"] != "a" {
		t.Fatalf("filtered wrong records: %+v", result.Rows)
	}
	metrics := metricRequest(qt.AggSpec{ID: "n", Expression: "COUNT()"})
	counted, err := d.ExecuteV2(context.Background(), c.DB, nil, &metrics, qt.PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := counted.Metrics.Metrics[0].Buckets[0].Value; got != float64(1) {
		t.Fatalf("metric=%v", got)
	}
	// Scope errors are actionable, not silently reinterpreted as user-message searches.
	values.Set("find_surface", "all")
	if _, _, err = c.messageFindDataset(t.Context(), values, writingMessageDataset); err == nil {
		t.Fatal("accepted all-message scope")
	}
	values.Set("find_surface", "user")
	values.Set("find", "no-such-value")
	table, _, err = c.messageFindDataset(t.Context(), values, writingMessageDataset)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table.base, "0=1") {
		t.Fatal("empty search broadened the dataset")
	}
}

func TestMessageSearchCursorMetadataUsesSameBatch(t *testing.T) {
	c := textFixture(t)
	if _, err := c.DB.Exec(`UPDATE messages SET selected=1; INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,spans_json) SELECT m.id,m.conversation_id,cv.workspace_id,m.created_at,substr(m.created_at,1,10),length(m.text),'[]' FROM messages m JOIN conversations cv ON cv.id=m.conversation_id WHERE m.role='user' AND m.kind='message'`); err != nil {
		t.Fatal(err)
	}
	first, err := c.SearchText(t.Context(), TextSearchOptions{Query: "cache", Surface: "user", SelectedOnly: true, Limit: 1})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("cursor: %+v %v", first, err)
	}
	values := url.Values{"find": {"cache"}, "find_surface": {"user"}, "find_fuzzy": {"0"}, "find_cursor": {first.NextCursor}}
	table, batch, err := c.messageFindDataset(t.Context(), values, writingMessageDataset)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Catalog: c}
	schema, err := s.queryTableSchema("writing_messages")
	if err != nil {
		t.Fatal(err)
	}
	result, err := table.Distinct(t.Context(), c.DB, "id", "", 50, schema)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	for _, hit := range batch.search.Items {
		if !strings.Contains(string(encoded), hit.MessageID) {
			t.Fatalf("missing batch item %s in %s", hit.MessageID, encoded)
		}
	}
	if strings.Contains(string(encoded), `"`+first.Items[0].MessageID+`"`) {
		t.Fatalf("first page leaked into next batch: %s", encoded)
	}
	if _, err := table.FieldStats(t.Context(), c.DB, []string{"id", "text"}, schema); err != nil {
		t.Fatal(err)
	}
}

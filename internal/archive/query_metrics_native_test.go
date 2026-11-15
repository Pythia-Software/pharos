package archive

import (
	"encoding/json"
	"fmt"
	"testing"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// identical compares results exactly: two floats match only if they are the
// same float64.
func identical(left, right any) bool {
	a, _ := json.Marshal(left)
	b, _ := json.Marshal(right)
	return string(a) == string(b)
}

// Every request observes subsequent catalog writes and revalidates numeric bounds.
func TestAdapterMetricsFollowWritesBetweenRequests(t *testing.T) {
	catalog, config := testCatalog(t)
	// A current ledger: no request rebuilds it from the messages below.
	want, _, err := catalog.authorshipGenerations()
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RebuildAuthorship(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	write := func(id string, words int64) {
		t.Helper()
		tx := seedTx(t, catalog.DB)
		for _, statement := range []string{
			`INSERT OR IGNORE INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','local','w','Proofs','2026-09-01T00:00:00Z')`,
			`INSERT OR IGNORE INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','codex','local','c')`,
			`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES(?1,'c',?1,'user','message','words','h','2026-09-01T12:00:00.000Z')`,
			`INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,spans_json,words) VALUES(?1,'c','w','2026-09-01T12:00:00.000Z','2026-09-01',5,'[]',?2)`,
		} {
			if _, err := tx.Exec(statement, id, words); err != nil {
				t.Fatal(err)
			}
		}
		commitSeed(t, tx)
	}
	for i := range 3 {
		write(fmt.Sprint("m", i), 4)
	}
	server := testServer(t, config, catalog)

	schema, err := sqliteQuerySchema("writing_messages")
	if err != nil {
		t.Fatal(err)
	}
	q := metricRequest(qt.AggSpec{ID: "messages", Op: "count"}, qt.AggSpec{ID: "mean", Op: "avg", Field: "words"})
	measure := func() queryFixtureMetricsResult {
		t.Helper()
		result, err := server.executeQueryFixture(t.Context(), "writing_messages", nil, schema, nil, &q)
		if err != nil {
			t.Fatal(err)
		}
		return *result.Metrics
	}
	for range 2 {
		if result := measure(); result.Metrics[0].Buckets[0].Value != float64(3) || result.Metrics[1].Buckets[0].Value != float64(4) {
			t.Fatalf("before the write: %+v", result.Metrics)
		}
	}
	// A value beyond safe integers fails the average, unless a stale proof
	// let it through.
	write("late", 1<<53+1)
	result := measure()
	if messages := result.Metrics[0].Buckets[0].Value; messages != float64(4) {
		t.Fatalf("messages after a write = %v, want 4", messages)
	}
	if mean := result.Metrics[1]; mean.Error == "" && (len(mean.Buckets) == 0 || mean.Buckets[0].Error == "") {
		t.Fatalf("an unsafe total was trusted: %+v", mean)
	}
}

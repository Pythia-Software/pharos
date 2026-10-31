//go:build probe

package archive

// Read-only phase timings and CPU/heap profiles on a marked dev library:
// PHAROS_THINKING_PROBE=<dev library>/catalog/catalog.sqlite3 \
//   go test -tags probe -run '^TestThinkingPerformanceProbe$' -v \
//   -o .context/thinking-performance/archive.test \
//   -cpuprofile .context/thinking-performance/cpu.pprof \
//   -memprofile .context/thinking-performance/heap.pprof ./internal/archive
// PHAROS_THINKING_PROBE_QUERY optionally selects an API query string.
import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestThinkingPerformanceProbe(t *testing.T) {
	path := os.Getenv("PHAROS_THINKING_PROBE")
	if path == "" {
		t.Skip("set PHAROS_THINKING_PROBE to an isolated dev catalog")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(path)), ".pharos-dev")); err != nil {
		t.Fatal("requires a marked isolated dev library")
	}
	db, err := sql.Open("sqlite", readerDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	c := &Catalog{Path: path, DB: db, readOnly: true, queryOnly: true}
	defer c.closeQuery()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	raw := os.Getenv("PHAROS_THINKING_PROBE_QUERY")
	if raw == "" {
		raw = "overview=1&view=histogram&groupBy=model&timezone=America%2FDenver"
	}
	query, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	o, err := readThinkingOptions(query, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now()
	rs, err := c.thinkingRecords(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("records", len(rs), time.Since(stamp))
	stamp = time.Now()
	factors := thinkingModels(ctx, rs, o.View == "calendar", o.Overview)
	t.Log("fits", len(factors), time.Since(stamp))
	stamp = time.Now()
	v, err := aggregateThinking(ctx, rs, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("aggregate", len(v.Groups), time.Since(stamp))
	if o.Overview {
		stamp = time.Now()
		v.Compactions = thinkingCompactions(rs)
		t.Log("compactions", len(v.Compactions), time.Since(stamp))
		stamp = time.Now()
		v.Traces = thinkingTraces(rs)
		t.Log("traces", len(v.Traces), time.Since(stamp))
	}
	if o.Evidence {
		stamp = time.Now()
		v.Validation, err = c.thinkingValidation(ctx, o, rs)
		if err != nil {
			t.Fatal(err)
		}
		t.Log("validation", time.Since(stamp))
	}
	stamp = time.Now()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("json", len(b), time.Since(stamp))
}

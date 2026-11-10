package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// cachedCall is apiCall naming a result-cache policy.
func cachedCall(t *testing.T, server *Server, token, path, policy string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := queryFixtureHTTPRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+token)
	if policy != "" {
		request.Header.Set(resultCacheHeader, policy)
	}
	w := httptest.NewRecorder()
	server.ServeHTTP(w, request)
	return w
}

// The rules of docs/result-cache.md, on a domain whose data state and
// failures the test sets.
func TestResultCachePolicies(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	stamp, builds, broken := "1", 0, false
	domain := registerResultDomain(&resultDomain{name: "test", version: "1", maxAge: time.Hour,
		stamp: func(context.Context, *Server, string) (string, error) { return stamp, nil },
		build: func(ctx context.Context, _ *Server, replay []byte, _ time.Time) ([]byte, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if broken {
				return nil, errors.New("cannot compute")
			}
			builds++
			return fmt.Appendf(nil, `{"request":%q,"build":%d}`, replay, builds), nil
		},
		fail: func(w http.ResponseWriter, err error) { writeError(w, err, 500) }})
	t.Cleanup(func() { delete(resultDomains, "test") })
	ask := func(policy, replay string, clock bool) (string, string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/test", nil)
		if policy != "" {
			r.Header.Set(resultCacheHeader, policy)
		}
		w := httptest.NewRecorder()
		server.serveResult(w, r, domain, resultRequest{replay: []byte(replay), scope: "all", clock: clock})
		if w.Code != 200 {
			return "", fmt.Sprint(w.Code)
		}
		var body struct{ Build int }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err, w.Body.String())
		}
		return w.Header().Get(resultCacheHeader), fmt.Sprint(body.Build)
	}
	expect := func(what, policy, replay, state, build string) {
		t.Helper()
		if gotState, gotBuild := ask(policy, replay, false); gotState != state || gotBuild != build {
			t.Fatalf("%s: policy %q answered %s from build %s, want %s from build %s", what, policy, gotState, gotBuild, state, build)
		}
	}
	// No policy: computed, and nothing is stored.
	expect("no policy", "", "a", "live", "1")
	expect("nothing stored", resultCached, "a", "live", "2")
	// Stored and fresh: every policy but live serves it.
	expect("fresh", resultCached, "a", "fresh", "2")
	expect("fresh", resultFresh, "a", "fresh", "2")
	expect("live", resultLive, "a", "live", "3")
	expect("another request", resultFresh, "b", "live", "4")
	// The data changed: cached serves the stale entry and queues a refresh,
	// fresh computes.
	stamp = "2"
	expect("stale", resultCached, "a", "stale", "3")
	if !server.refreshResult(t.Context()) || builds != 5 {
		t.Fatalf("refresher did not rebuild the entry a request was answered with: %d builds", builds)
	}
	expect("refreshed", resultCached, "a", "fresh", "5")
	expect("stale", resultFresh, "b", "live", "6")
	// Under load, or when computing fails, fresh falls back to the stale entry.
	stamp = "3"
	held := make([]func(), 0, heavyQueries.limit)
	for range heavyQueries.limit {
		release, err := heavyQueries.acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, release)
	}
	expect("busy", resultFresh, "a", "stale", "5")
	for _, release := range held {
		release()
	}
	broken = true
	expect("failing", resultFresh, "b", "stale", "6")
	if state, code := ask(resultFresh, "never stored", false); state != "" || code != "500" {
		t.Fatalf("a failure with nothing stored answered %s %s", state, code)
	}
	broken = false
	// The sweep finds stale entries no request has queued since.
	for server.refreshResult(t.Context()) {
	}
	stamp = "4"
	server.sweepResults(t.Context())
	before := builds
	for server.refreshResult(t.Context()) {
	}
	if builds != before+2 {
		t.Fatalf("sweep refreshed %d entries, want both", builds-before)
	}
	// A request that reads the clock goes stale by age alone.
	if state, _ := ask(resultCached, "clock", true); state != "live" {
		t.Fatal(state)
	}
	override(t, &domain.maxAge, -time.Second)
	if state, _ := ask(resultCached, "clock", true); state != "stale" {
		t.Fatalf("aged clock entry is %s", state)
	}
	expect("no clock", resultCached, "a", "fresh", fmt.Sprint(builds-1))
	// The file outlives the process, and another version's entries are not served.
	_, stored := ask(resultCached, "b", false)
	catalog.closeResults()
	catalog.resultsOnce, catalog.resultStore = sync.Once{}, nil
	expect("reopened", resultCached, "b", "fresh", stored)
	override(t, &domain.version, "2")
	expect("new version", resultCached, "b", "live", fmt.Sprint(builds+1))
	// The sweep rebuilds the other version's entries, which no longer count
	// as stale, so that they are ready before they are asked for.
	server.sweepResults(t.Context())
	for server.refreshResult(t.Context()) {
	}
	expect("rebuilt for the new version", resultCached, "a", "fresh", fmt.Sprint(builds))
	// A build its request gave up on is finished when there is capacity:
	// nothing was stored to refresh, so the request itself is remembered.
	gone, cancel := context.WithCancel(t.Context())
	cancel()
	abandoned := httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(gone)
	abandoned.Header.Set(resultCacheHeader, resultCached)
	server.serveResult(httptest.NewRecorder(), abandoned, domain, resultRequest{replay: []byte("slow"), scope: "all"})
	if !server.refreshResult(t.Context()) {
		t.Fatal("an abandoned build was not queued")
	}
	expect("abandoned, then built", resultCached, "slow", "fresh", fmt.Sprint(builds))
	// A wake-up is passed on only while entries wait, so idle refreshers sleep.
	cache := server.results()
	select {
	case <-cache.wake:
	default:
	}
	if cache.woke(); len(cache.wake) != 0 {
		t.Fatal("a refresher with nothing queued woke the next one")
	}
	cache.refreshSoon(domain.name, "a", true)
	<-cache.wake
	if cache.woke(); len(cache.wake) != 1 {
		t.Fatal("a refresher did not pass on a wake-up with entries waiting")
	}
	<-cache.wake
	server.refreshResult(t.Context())
	// Refreshes do not count as work someone is waiting for.
	release, err := heavyQueries.acquire(backgroundQuery(t.Context()))
	if err != nil || heavyQueries.interactive() != 0 {
		t.Fatal(err, heavyQueries.interactive())
	}
	asked, err := heavyQueries.acquire(t.Context())
	if err != nil || heavyQueries.interactive() != 1 {
		t.Fatal(err, heavyQueries.interactive())
	}
	release()
	asked()
	if heavyQueries.interactive() != 0 {
		t.Fatal(heavyQueries.interactive())
	}
	// The file belongs to one library: each has its own, and a file found
	// holding another library's entries is emptied rather than served.
	if own, other := resultCachePath(resultCacheIdentity(config)), resultCachePath("library:other:instance"); own == other || own != server.results().path {
		t.Fatalf("cache files: %s for this catalog, %s for another library", own, other)
	}
	if _, err := server.results().db.Exec(`UPDATE library SET identity='library:other:instance'`); err != nil {
		t.Fatal(err)
	}
	catalog.closeResults()
	catalog.resultsOnce, catalog.resultStore = sync.Once{}, nil
	expect("another library's file", resultCached, "b", "live", fmt.Sprint(builds+1))
	expect("stored again", resultCached, "b", "fresh", fmt.Sprint(builds))
	// Entries are dropped least recently used first once over the size limit.
	override(t, &resultCacheBytes, 1)
	server.results().trim()
	var left int
	if err := server.results().db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d entries left over the size limit, %v", left, err)
	}
}

// A card's query is one entry however often the page loads, is served
// without touching the catalog, and is recomputed after the data changes.
func TestQueryResultStampNamesZoneAndBuild(t *testing.T) {
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	zones := map[string]string{}
	for _, name := range []string{"America/Denver", "America/Phoenix", "Europe/London", "UTC"} {
		zone, err := time.LoadLocation(name)
		if err != nil {
			t.Skip(err)
		}
		override(t, &time.Local, zone)
		id := localZoneIdentity(at)
		if id != localZoneIdentity(at.AddDate(0, 5, 0)) {
			t.Fatalf("%s is another zone in summer: %s", name, id)
		}
		if other, taken := zones[id]; taken {
			t.Fatalf("%s and %s are both %s", name, other, id)
		}
		zones[id] = name
	}
	if buildIdentity() == "" || buildIdentity() == "unknown" {
		t.Fatal("the running program has no identity")
	}
}

func TestQueryResultsAreCached(t *testing.T) {
	catalog, config := libraryFixture(t)
	server := testServer(t, config, catalog)
	request := func(now time.Time) qt.QueryRequestV3 {
		q := ungroupedRowsRequest(now)
		q.Result.Select = []string{"id", "title"}
		q.Result.OrderBy = []qt.OrderBy{{Field: "id", Dir: "asc"}}
		return q
	}
	const path = "/api/query/library/query-v3"
	first := cachedCall(t, server, config.APIToken, path, resultCached, request(time.UnixMilli(1_800_000_000_000)))
	if first.Code != 200 || first.Header().Get(resultCacheHeader) != "live" {
		t.Fatal(first.Code, first.Header(), first.Body.String())
	}
	again := cachedCall(t, server, config.APIToken, path, resultCached, request(time.UnixMilli(1_800_000_060_000)))
	if again.Header().Get(resultCacheHeader) != "fresh" || again.Body.String() != first.Body.String() || again.Header().Get(resultCacheBuiltHeader) == "" {
		t.Fatalf("second load: %v %s", again.Header(), again.Body.String())
	}
	// Without a policy the cache is neither read nor written.
	plain := cachedCall(t, server, config.APIToken, path, "", request(time.UnixMilli(1_800_000_120_000)))
	var result qt.QueryResultV3
	if err := json.Unmarshal(plain.Body.Bytes(), &result); err != nil || result.Now != 1_800_000_120_000 || len(result.Rows) == 0 {
		t.Fatalf("uncached request: %v %s", err, plain.Body.String())
	}
	if _, err := catalog.DB.Exec(`UPDATE workspaces SET title='Renamed' WHERE id='c'`); err != nil {
		t.Fatal(err)
	}
	stale := cachedCall(t, server, config.APIToken, path, resultCached, request(time.Now()))
	if stale.Header().Get(resultCacheHeader) != "stale" || stale.Body.String() != first.Body.String() {
		t.Fatalf("after a change: %v %s", stale.Header(), stale.Body.String())
	}
	if !server.refreshResult(t.Context()) {
		t.Fatal("nothing queued for refresh")
	}
	fresh := cachedCall(t, server, config.APIToken, path, resultCached, request(time.Now()))
	if fresh.Header().Get(resultCacheHeader) != "fresh" || !bytes.Contains(fresh.Body.Bytes(), []byte("Renamed")) {
		t.Fatalf("after the refresh: %v %s", fresh.Header(), titlesOf(fresh.Body.Bytes()))
	}
	// Errors are reported as before and never stored.
	bad := request(time.Now())
	bad.Result.Select = []string{"no_such_field"}
	for range 2 {
		if w := cachedCall(t, server, config.APIToken, path, resultCached, bad); w.Code == 200 || w.Header().Get(resultCacheHeader) != "" {
			t.Fatalf("invalid query: %d %v", w.Code, w.Header())
		}
	}
	// A window relative to now is the same entry at any clock, and says so.
	for i, want := range []string{"live", "fresh"} {
		rolling := request(time.Now().Add(time.Duration(i) * time.Minute))
		rolling.Source.Where = []qt.WhereTerm{{Field: "activity_at", Op: ">=", Value: "-3650d"}}
		if w := cachedCall(t, server, config.APIToken, path, resultCached, rolling); w.Header().Get(resultCacheHeader) != want {
			t.Fatalf("rolling window, load %d: %d %v %s", i, w.Code, w.Header(), w.Body.String())
		}
	}
	// A window the client computed from its page load is one entry too, and
	// is computed for the clock of whoever builds it.
	var keys [2]string
	for i := range keys {
		now := time.UnixMilli(1_800_000_000_123 + int64(i)*61_000).UTC()
		body := fmt.Sprintf(`{"now":%d,"source":{"where":[{"field":"activity_at","op":">=","value":%q},{"field":"activity_at","op":"<","value":%q},{"field":"activity_at","op":">=","value":"2026-10-04T06:00:00.000Z"}]}}`,
			now.UnixMilli(), now.Add(-37*24*time.Hour).Format(clockInstantLayout), now.Format(clockInstantLayout))
		r, err := queryResultRequest("library", nil, []byte(body))
		if err != nil || !r.clock {
			t.Fatalf("page-load window: %+v %v", r, err)
		}
		keys[i] = r.key()
		var stored queryReplay
		var fields map[string]any
		if json.Unmarshal(r.replay, &stored) != nil || json.Unmarshal(stored.Body, &fields) != nil {
			t.Fatal(string(r.replay))
		}
		clockInstants(fields, now, false)
		restored, _ := json.Marshal(fields["source"])
		var original map[string]any
		_ = json.Unmarshal([]byte(body), &original)
		if want, _ := json.Marshal(original["source"]); string(restored) != string(want) {
			t.Fatalf("restored %s, want %s", restored, want)
		}
	}
	if keys[0] != keys[1] {
		t.Fatal("two page loads of one window are two entries")
	}
	if r, err := queryResultRequest("library", nil, []byte(`{"now":5,"source":{"where":[{"field":"activity_at","op":">=","value":"-7d"}]}}`)); err != nil || !r.clock || r.now.UnixMilli() != 5 {
		t.Fatalf("relative window: %+v %v", r, err)
	}
}

func titlesOf(body []byte) []any {
	var result qt.QueryResultV3
	_ = json.Unmarshal(body, &result)
	titles := []any{}
	for _, row := range result.Rows {
		titles = append(titles, row["id"], row["title"])
	}
	return titles
}

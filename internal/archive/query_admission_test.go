package archive

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

func waitForQueryQueue(t *testing.T, gate *queryAdmission, size int) {
	t.Helper()
	waitUntil(t, 10*time.Second, "heavy query admission queue", func() bool {
		gate.mu.Lock()
		defer gate.mu.Unlock()
		return gate.wait.Len() == size
	})
}

func TestQueryAdmissionFIFOAndCancellation(t *testing.T) {
	gate := &queryAdmission{limit: 1}
	release, err := gate.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		finish, err := gate.acquire(ctx)
		if finish != nil {
			finish()
		}
		done <- err
	}()
	waitForQueryQueue(t, gate, 1)
	order := make(chan int, 3)
	for i := range 3 {
		go func() {
			finish, err := gate.acquire(t.Context())
			if err != nil {
				t.Error(err)
				return
			}
			defer finish()
			order <- i
		}()
		waitForQueryQueue(t, gate, i+2)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("a cancelled waiter stayed in the admission queue")
	}
	waitForQueryQueue(t, gate, 3)
	release()
	for want := range 3 {
		if got := <-order; got != want {
			t.Fatalf("admitted %d before %d", got, want)
		}
	}
	// A request's breakdown says how long it queued for a slot.
	held, err := gate.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	timed, timing := beginRequestTiming(t.Context(), nil, "POST /api/query/usage/query-v3", true)
	defer timing.finish()
	admitted := make(chan struct{})
	go func() {
		defer close(admitted)
		if finish, err := gate.acquire(timed); err == nil {
			finish()
		}
	}()
	waitForQueryQueue(t, gate, 1)
	held()
	<-admitted
	if got := timing.breakdown(time.Second); timing.queueWait <= 0 || !strings.Contains(got, "query_queue=") {
		t.Fatalf("queued request's breakdown: %s", got)
	}
	// A derived value's build queues within the wait its request counts as
	// derived_wait, so that queue is not counted a second time.
	held, err = gate.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	own, counted, built := timing.queueWait, timing.pause(), make(chan struct{})
	go func() {
		defer close(built)
		if finish, err := gate.acquire(timed); err == nil {
			finish()
		}
	}()
	waitForQueryQueue(t, gate, 1)
	held()
	<-built
	counted()
	if timing.queueWait != own {
		t.Fatalf("a derived build's queue was counted as the request's: %s, was %s", timing.queueWait, own)
	}
}

func TestQueryAdmissionBoundsConcurrentWork(t *testing.T) {
	gate := &queryAdmission{limit: 3}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var active, peak atomic.Int32
	started := make(chan struct{}, 20)
	proceed := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(proceed) }) }
	defer unblock()
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			finish, err := gate.acquire(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			defer finish()
			n := active.Add(1)
			defer active.Add(-1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			started <- struct{}{}
			select {
			case <-proceed:
			case <-ctx.Done():
				t.Error(ctx.Err())
			}
		})
	}
	for range gate.limit {
		<-started
	}
	waitForQueryQueue(t, gate, 17)
	unblock()
	workers.Wait()
	if got := peak.Load(); got != int32(gate.limit) {
		t.Fatalf("peak concurrency = %d, limit = %d", got, gate.limit)
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.active != 0 || gate.wait.Len() != 0 {
		t.Fatalf("slots leaked: active=%d queued=%d", gate.active, gate.wait.Len())
	}
}

func TestQueryAdmissionCancellationRacesGrant(t *testing.T) {
	gate := &queryAdmission{limit: 1}
	for range 100 {
		release, err := gate.acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			finish, _ := gate.acquire(ctx)
			if finish != nil {
				finish()
				finish() // transaction cleanup may run explicitly and as a defer.
			}
			close(done)
		}()
		go release()
		cancel()
		<-done
		// Joining this lease also joins the releasing goroutine.
		finish, err := gate.acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		finish()
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.active != 0 || gate.wait.Len() != 0 {
		t.Fatalf("cancel/grant race leaked a slot: active=%d queued=%d", gate.active, gate.wait.Len())
	}
}

func TestQueryAdmissionLeavesLightReadsAvailable(t *testing.T) {
	gate := &queryAdmission{limit: 4}
	override(t, &heavyQueries, gate)
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 3)
	publishProjections(t, catalog)
	quietDriveCache(t)
	t.Cleanup(func() { quietDriveCache(t) })
	server := testServer(t, config, catalog)
	query := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(),
		Result: qt.WireQuery{Limit: 1}, Metrics: []qt.AggSpec{{ID: "n", Op: "count"}}}
	// Warm both a SQL source and a staged source before saturating admission.
	for _, dataset := range []string{"tool_calls", "library"} {
		schema, _ := sqliteQuerySchema(dataset)
		if _, _, err := server.executeQueryV3(t.Context(), dataset, nil, schema, query); err != nil {
			t.Fatal(err)
		}
	}
	for range gate.limit {
		finish, err := gate.acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer finish()
		_, release, err := beginQueryWithTempStore(t.Context(), catalog.reader(), 1)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	for _, dataset := range []string{"tool_calls", "library"} {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			schema, _ := sqliteQuerySchema(dataset)
			_, _, err := server.executeQueryV3(ctx, dataset, nil, schema, query)
			done <- err
		}()
		waitForQueryQueue(t, gate, 1)
		if used := catalog.reader().Stats().InUse; used != gate.limit {
			t.Errorf("queued %s query leased a reader: %d in use", dataset, used)
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancelled query did not leave the queue")
		}
		waitForQueryQueue(t, gate, 0)
	}
	for _, path := range []string{"/api/preferences", "/api/findings", "/api/background", "/api/upgrade", "/api/health/freshness", "/api/status", "/api/sync/status"} {
		t.Run(path, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer "+config.APIToken)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, r)
			if response.Code != http.StatusOK || ctx.Err() != nil {
				t.Fatalf("light read blocked by heavy queries: %d %s (%v)", response.Code, response.Body.String(), ctx.Err())
			}
		})
	}
}

func TestDerivedAdmissionSharesTheQueryLimit(t *testing.T) {
	gate := &queryAdmission{limit: 1}
	override(t, &heavyQueries, gate)
	catalog, _ := testCatalog(t)
	finish, err := gate.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := cachedValue(ctx, catalog, "queued", func(context.Context) (int, error) {
			calls.Add(1)
			return 42, nil
		})
		done <- err
	}()
	waitForQueryQueue(t, gate, 1)
	if calls.Load() != 0 || catalog.reader().Stats().InUse != 0 {
		t.Fatal("a queued cache build started computing or leased a reader")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cache waiter ignored cancellation: %v", err)
	}
	// Shared builds retain their independent lifetime even before admission.
	finish()
	value, err := cachedValue(t.Context(), catalog, "queued", func(context.Context) (int, error) {
		t.Error("shared build restarted")
		return 0, nil
	})
	if err != nil || value != 42 || calls.Load() != 1 {
		t.Fatalf("shared queued build: value=%d calls=%d error=%v", value, calls.Load(), err)
	}
	finish, err = gate.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	// A warm cache hit does not need admission.
	value, err = cachedValue(t.Context(), catalog, "queued", func(context.Context) (int, error) { return 0, nil })
	if err != nil || value != 42 {
		t.Fatalf("warm hit waited for admission: %d %v", value, err)
	}
	go func() {
		_, err := cachedValue(t.Context(), catalog, "closing", func(context.Context) (int, error) {
			t.Error("a queued build ran during shutdown")
			return 0, nil
		})
		done <- err
	}()
	waitForQueryQueue(t, gate, 1)
	catalog.closeDerivedCache()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown did not cancel a queued cache build: %v", err)
	}
	waitForQueryQueue(t, gate, 0)
}

func TestDerivedAdmissionYieldsForDependencies(t *testing.T) {
	gate := &queryAdmission{limit: 1}
	override(t, &heavyQueries, gate)
	catalog, _ := testCatalog(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	for i := range 6 {
		workers.Go(func() {
			value, err := cachedValue(ctx, catalog, fmt.Sprint("outer", i), func(ctx context.Context) (int, error) {
				inner, err := cachedValue(ctx, catalog, "shared-inner", func(ctx context.Context) (int, error) {
					return cachedValue(ctx, catalog, "leaf", func(context.Context) (int, error) { return 40, nil })
				})
				return inner + 2, err
			})
			if err != nil || value != 42 {
				t.Errorf("nested cache build: %d %v", value, err)
			}
		})
	}
	workers.Wait()
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.active != 0 || gate.wait.Len() != 0 {
		t.Fatalf("nested builds leaked a slot: active=%d queued=%d", gate.active, gate.wait.Len())
	}
}

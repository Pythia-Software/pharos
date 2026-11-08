package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDerivedValuesAreComputedOncePerCommit(t *testing.T) {
	catalog, _ := libraryFixture(t)
	other := otherProcess(t, catalog)
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	compute := func(ctx context.Context) (int, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return 42, ctx.Err()
	}
	// The request that starts the computation gives up; the computation does
	// not, since other requests are waiting for it.
	override(t, &derivedIdleGrace, 0)
	abandoned, cancel := context.WithCancel(context.Background())
	abandonedDone := make(chan error, 1)
	go func() { _, err := cachedValue(abandoned, catalog, "answer", compute); abandonedDone <- err }()
	<-started
	var waiting sync.WaitGroup
	values := make([]int, 5)
	for index := range values {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			value, err := cachedValue(context.Background(), catalog, "answer", compute)
			if err != nil {
				t.Error(err)
			}
			values[index] = value
		}()
	}
	waitUntil(t, time.Second, "every request to wait for the build", func() bool { return derivedWaiters(catalog, "answer") == 1+len(values) })
	cancel()
	select {
	case err := <-abandonedDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("the initiating request waited for its abandoned cache build")
	}
	close(release)
	waiting.Wait()
	for _, value := range values {
		if value != 42 {
			t.Fatalf("values = %v", values)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("computed %d times for one commit", calls.Load())
	}
	if value, _ := cachedValue(context.Background(), catalog, "answer", compute); value != 42 || calls.Load() != 1 {
		t.Fatal("an unchanged catalog recomputed")
	}
	if _, err := other.DB.Exec(`UPDATE workspaces SET title='Renamed' WHERE id='c'`); err != nil {
		t.Fatal(err)
	}
	if value, _ := cachedValue(context.Background(), catalog, "answer", compute); value != 42 || calls.Load() != 2 {
		t.Fatalf("a commit did not recompute: %d computations", calls.Load())
	}
}

func TestDerivedAdmissionReleasesAfterFailure(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(fmt.Sprint("panic=", panics), func(t *testing.T) {
			gate := &queryAdmission{limit: 1}
			override(t, &heavyQueries, gate)
			catalog, _ := testCatalog(t)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_, err := cachedValue(ctx, catalog, "failed", func(ctx context.Context) (int, error) {
				_, err := cachedValue(ctx, catalog, "failed-dependency", func(context.Context) (int, error) {
					if panics {
						panic("cannot compute")
					}
					return 0, errors.New("cannot compute")
				})
				return 0, err
			})
			if err == nil || !strings.Contains(err.Error(), "cannot compute") {
				t.Fatalf("lost cache build error: %v", err)
			}
			value, err := cachedValue(ctx, catalog, "failed", func(context.Context) (int, error) { return 42, nil })
			if err != nil || value != 42 {
				t.Fatalf("failed cache build retained admission: %d %v", value, err)
			}
		})
	}
}

func TestDerivedBuildLifetime(t *testing.T) {
	for _, request := range []bool{true, false} {
		name := "request"
		if !request {
			name = "warm-up"
		}
		t.Run(name+" deadline and cancellation leave a reusable build", func(t *testing.T) {
			catalog, _ := libraryFixture(t)
			// A reload within the grace period reattaches; a warm-up build never
			// needs one.
			grace := time.Hour
			if !request {
				grace = 0
			}
			override(t, &derivedIdleGrace, grace)
			release := make(chan struct{})
			var releaseOnce sync.Once
			finish := func() { releaseOnce.Do(func() { close(release) }) }
			defer finish()
			started := make(chan context.Context, 1)
			compute := func(ctx context.Context) (any, error) {
				started <- ctx
				select {
				case <-release:
					return 7, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			waiting, cancel := context.WithTimeout(t.Context(), time.Hour)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := catalog.derivedValue(waiting, "slow", compute, request); done <- err }()
			var work context.Context
			select {
			case work = <-started:
			case err := <-done:
				t.Fatalf("cache read ended before starting its build: %v", err)
			}
			if _, limited := work.Deadline(); limited {
				t.Fatal("the shared cache imposed a deadline on its builder")
			}
			// Thinking's own two-minute budget must remain intact.
			thinking, stop := context.WithTimeout(work, 2*time.Minute)
			defer stop()
			if until, _ := thinking.Deadline(); time.Until(until) < time.Minute {
				t.Fatal("shared preparation shortened the Thinking budget")
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("the caller could not abandon its wait: %v", err)
			}
			if work.Err() != nil {
				t.Fatal("request cancellation ended shared work")
			}
			// A later request reuses the same in-flight build and its good value.
			if !request {
				finish()
			} else {
				reloaded := make(chan any, 1)
				go func() {
					value, _ := catalog.derivedValue(t.Context(), "slow", func(context.Context) (any, error) {
						return nil, errors.New("the reload restarted the build")
					}, true)
					reloaded <- value
				}()
				waitUntil(t, time.Second, "the reload to reattach", func() bool { return derivedWaiters(catalog, "slow") == 1 })
				finish()
				if value := <-reloaded; value != 7 {
					t.Fatalf("the reload received %v", value)
				}
			}
			value, err := catalog.derivedValue(t.Context(), "slow", func(context.Context) (any, error) {
				return nil, errors.New("the abandoned build was restarted")
			}, request)
			if err != nil || value != 7 {
				t.Fatalf("late result was discarded: %v %v", value, err)
			}
		})
	}
	t.Run("an abandoned request build stops and is not cached", func(t *testing.T) {
		catalog, _ := libraryFixture(t)
		override(t, &derivedIdleGrace, 0)
		started := make(chan struct{})
		var calls atomic.Int32
		compute := func(ctx context.Context) (any, error) {
			if calls.Add(1) > 1 {
				return 8, nil
			}
			// Hold a read snapshot the way a dataset build does.
			tx, err := catalog.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				return nil, err
			}
			defer tx.Rollback()
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM workspaces").Scan(&count); err != nil {
				return nil, err
			}
			close(started)
			<-ctx.Done()
			return count, nil // A result after cancellation must not be kept.
		}
		waiting, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { _, err := catalog.derivedValue(waiting, "abandoned", compute, true); done <- err }()
		<-started
		if catalog.reader().Stats().InUse == 0 {
			t.Fatal("the build holds no reader connection")
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		waitUntil(t, time.Second, "the abandoned build to release its reader", func() bool {
			return derivedWaiters(catalog, "abandoned") < 0 && catalog.reader().Stats().InUse == 0
		})
		if value, err := catalog.derivedValue(t.Context(), "abandoned", compute, true); err != nil || value != 8 {
			t.Fatalf("the abandoned build's result was cached: %v %v", value, err)
		}
	})
	t.Run("a superseded build nobody waits for stops", func(t *testing.T) {
		catalog, _ := libraryFixture(t)
		other := otherProcess(t, catalog)
		override(t, &derivedIdleGrace, time.Hour)
		started, stopped := make(chan struct{}), make(chan struct{})
		var calls atomic.Int32
		compute := func(ctx context.Context) (any, error) {
			if calls.Add(1) > 1 {
				return "current", nil
			}
			close(started)
			<-ctx.Done()
			close(stopped)
			return "superseded", nil
		}
		waiting, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { _, err := catalog.derivedValue(waiting, "superseded", compute, true); done <- err }()
		<-started
		cancel()
		<-done
		if _, err := other.DB.Exec(`UPDATE workspaces SET title='Renamed' WHERE id='c'`); err != nil {
			t.Fatal(err)
		}
		// The new generation cannot reattach to the old build, so it does not
		// wait out the grace period: it stops the old build, then starts its own.
		value, err := catalog.derivedValue(t.Context(), "superseded", compute, true)
		if err != nil || value != "current" {
			t.Fatalf("new generation received %v %v", value, err)
		}
		select {
		case <-stopped:
		default:
			t.Fatal("the superseded build kept running")
		}
	})
	t.Run("catalog close", func(t *testing.T) {
		catalog, _ := libraryFixture(t)
		started, finished := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, err := cachedValue(t.Context(), catalog, "closing", func(ctx context.Context) (int, error) {
				close(started)
				<-ctx.Done()
				defer close(finished)
				return 0, ctx.Err()
			})
			done <- err
		}()
		<-started
		catalog.closeDerivedCache()
		select {
		case <-finished:
		default:
			t.Fatal("catalog close did not join its cache build")
		}
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if _, err := cachedValue(t.Context(), catalog, "late", func(context.Context) (int, error) { return 1, nil }); !errors.Is(err, context.Canceled) {
			t.Fatalf("closed catalog admitted a new cache build: %v", err)
		}
	})
}

// Run the real mapped-memory fault in a subprocess: the service must exit even
// if the request that started the build has already left.
func TestDerivedCacheMappedFaultStopsService(t *testing.T) {
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestDerivedCacheFaultHelper$")
	command.Env = append(os.Environ(), "PHAROS_TEST_DERIVED_FAULT=1")
	output, err := command.CombinedOutput()
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != 1 || !strings.Contains(string(output), "library's drive disappeared") || strings.Contains(string(output), "goroutine ") {
		t.Fatalf("mapped fault did not use the clean service exit: %v\n%s", err, output)
	}
}

func TestDerivedCacheFaultHelper(t *testing.T) {
	if os.Getenv("PHAROS_TEST_DERIVED_FAULT") != "1" {
		t.Skip("helper process")
	}
	catalog, config := testCatalog(t)
	NewServer(config, catalog) // Install the service's fault handler.
	started, release := make(chan struct{}), make(chan struct{})
	waiting, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := cachedValue(waiting, catalog, "mapped-fault", func(context.Context) (int, error) {
			close(started)
			<-release
			// The independent mapping below is the fault source. Point the exit
			// diagnostic at a vanished directory without pulling a real drive.
			catalog.Path = filepath.Join(t.TempDir(), "gone", "catalog.sqlite3")
			touchVanishedMapping(t)
			return 0, nil
		})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-time.After(5 * time.Second):
		t.Fatal("a mapped fault with no waiter failed to exit the service")
	}
}

func TestDerivedCloseCancelsDatabaseWaits(t *testing.T) {
	for _, source := range []string{"TL1", "pricing", "carbon", "token usage"} {
		t.Run(source, func(t *testing.T) {
			catalog, _ := libraryFixture(t)
			catalog.reader().SetMaxOpenConns(1)
			held, err := catalog.reader().Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			before := catalog.reader().Stats().WaitCount
			done := make(chan error, 1)
			go func() {
				var err error
				switch source {
				case "TL1":
					_, err = catalog.cachedTL1AttemptRows(t.Context())
				case "pricing":
					_, err = catalog.PricingHealth(t.Context())
				case "carbon":
					_, err = catalog.CarbonHealth(t.Context())
				case "token usage":
					_, err = cachedValue(t.Context(), catalog, "health:token-usage", catalog.tokenUsageByModelContext)
				}
				done <- err
			}()
			waitUntil(t, time.Second, "shared build to wait for a connection", func() bool { return catalog.reader().Stats().WaitCount > before })
			closed := make(chan struct{})
			go func() { catalog.closeDerivedCache(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(time.Second):
				// Unblock an uncancellable regression so cleanup can still join it.
				_ = held.Close()
				<-closed
				t.Fatal("catalog close waited for a connection held by another reader")
			}
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("build did not report shutdown cancellation: %v", err)
			}
		})
	}
}

// derivedWaiters reports how many callers wait for key's running build, or -1
// when no build for key is running.
func derivedWaiters(c *Catalog, key string) int {
	c.derived.mu.Lock()
	defer c.derived.mu.Unlock()
	entry := c.derived.entries[key]
	if entry == nil || c.derived.builds[entry] == nil {
		return -1
	}
	return entry.waiters
}

func TestCachesOutliveCommitsDuringARun(t *testing.T) {
	catalog, _ := libraryFixture(t)
	other := otherProcess(t, catalog)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.Local)
	catalog.now = func() time.Time { return now }
	var calls atomic.Int32
	compute := func(context.Context) (int32, error) { return calls.Add(1), nil }
	commit := func() {
		t.Helper()
		if _, err := other.DB.Exec(`UPDATE workspaces SET title=title||'.' WHERE id='c'`); err != nil {
			t.Fatal(err)
		}
	}
	value := func() int32 {
		t.Helper()
		value, err := cachedValue(context.Background(), catalog, "answer", compute)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	if value() != 1 {
		t.Fatal("first value")
	}
	firstRows := libraryRowsByID(t, catalog)
	catalog.ingestRuns.Add(1)
	commit()
	now = now.Add(time.Minute - time.Second)
	if got := value(); got != 1 {
		t.Fatalf("a commit during a run recomputed within the hold: %d", got)
	}
	now = now.Add(2 * time.Second)
	if got := value(); got != 1 {
		t.Fatalf("a long ingest rebuilt a cached view: %d", got)
	}
	commit()
	now = now.Add(time.Hour)
	if got := value(); got != 1 {
		t.Fatalf("a later commit during a long ingest rebuilt a cached view: %d", got)
	}
	if rows := libraryRowsByID(t, catalog); !sameRow(firstRows["c"], rows["c"]) {
		t.Fatal("a long ingest rebuilt the Library's cached rows")
	}
	catalog.ingestRuns.Add(-1)
	if got := value(); got != 2 {
		t.Fatalf("the run ended without the next read seeing its commits: %d", got)
	}
	if rows := libraryRowsByID(t, catalog); rows["c"]["title"] == firstRows["c"]["title"] {
		t.Fatal("the Library did not refresh after indexing ended")
	}
	commit()
	if got := value(); got != 3 {
		t.Fatalf("a commit outside a run did not recompute: %d", got)
	}
}

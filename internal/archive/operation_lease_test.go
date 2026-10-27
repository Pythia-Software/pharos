package archive

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A lease is created and updated in place, under its lock, and never
// replaced: a probe sees it live while its process holds it, and a deleter
// can claim it only once it is not held.
func TestLeaseIsLockedInPlaceUntilReleased(t *testing.T) {
	dir := filepath.Join(t.TempDir(), leaseDirName)
	lease, err := acquireProcessLease(dir, leaseInfo{Instance: "one", PID: os.Getpid(), HostID: "host-a", Process: "cli:capture", Kinds: []string{"capture.cli"}, StartedAt: now()})
	if err != nil {
		t.Fatal(err)
	}
	path := leasePath(dir, "one")
	before, _ := os.Stat(path)
	probe := probeLease(path)
	if !probe.Live || probe.Info == nil || probe.Info.Process != "cli:capture" || probe.Err != nil {
		t.Fatalf("held lease: %+v", probe)
	}
	if err := lease.update(func(info *leaseInfo) { info.Operations = []string{"op-1"} }); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("an update replaced the lease file")
	}
	if probe := probeLease(path); probe.Info == nil || len(probe.Info.Operations) != 1 {
		t.Fatalf("updated lease: %+v", probe)
	}
	if _, _, ok := claimDeadLease(path); ok {
		t.Fatal("a held lease was claimed")
	}
	lease.release()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a released lease is still there: %v", err)
	}

	// A process whose last journal round failed leaves its outcomes in an
	// unheld lease.
	abandoned, err := acquireProcessLease(dir, leaseInfo{Instance: "two", HostID: "host-a", Process: "cli:index", StartedAt: now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := abandoned.abandon(map[string]leaseOutcome{"op-2": {State: opFailed, Error: "x", EndedAt: now()}}); err != nil {
		t.Fatal(err)
	}
	probe = probeLease(leasePath(dir, "two"))
	if probe.Live || probe.Info == nil || probe.Info.Outcomes["op-2"].State != opFailed {
		t.Fatalf("abandoned lease: %+v", probe)
	}
	info, done, ok := claimDeadLease(leasePath(dir, "two"))
	if !ok || info == nil || info.Outcomes["op-2"].Error != "x" {
		t.Fatalf("claim of a dead lease: %v %+v", ok, info)
	}
	// While the deleter holds it, a probe takes it for live (it is locked).
	if !probeLease(leasePath(dir, "two")).Live {
		t.Fatal("a claimed lease reads as unheld")
	}
	done(true)
	if probes := probeLeases([]string{dir}); len(probes) != 0 {
		t.Fatalf("after deleting: %+v", probes)
	}
}

// Failing closed (§6.5): a lease directory that exists but cannot be listed
// makes the verdict unsafe, and a writing command that cannot take its lease
// does not run.
func TestLeasesFailClosed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), leaseDirName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if _, err := os.ReadDir(dir); err == nil {
		t.Skip("running as a user who can list an unreadable directory")
	}
	if verdict := computeEjectVerdict(verdictInput{leases: probeLeases([]string{dir})}); verdict.Verdict != verdictUnsafe {
		t.Fatalf("an unlistable lease directory: %+v", verdict)
	}
	if probes := probeLeases([]string{filepath.Join(t.TempDir(), "missing")}); len(probes) != 0 {
		t.Fatalf("a missing directory: %+v", probes)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, leaseDirName), nil, 0o600); err != nil { // not a directory
		t.Fatal(err)
	}
	ran := false
	err := runCLIOperation(Config{CatalogPath: filepath.Join(root, "catalog.sqlite3"), CaptureRoot: root}, nil, "capture", OperationSpec{Kind: "capture.cli"},
		func(context.Context, *Op) error { ran = true; return nil })
	if err == nil || ran || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("a command without its lease: ran=%v err=%v", ran, err)
	}
}

// TestLeaseHelperProcess is the other Pharos process of
// TestServiceSeesAnotherProcessThroughItsLease: a Terminal index that holds
// a lease and journals into the catalog.
func TestLeaseHelperProcess(t *testing.T) {
	path := os.Getenv("PHAROS_TEST_LEASE_CATALOG")
	if path == "" {
		t.Skip("helper process")
	}
	journalRoundInterval = 20 * time.Millisecond
	catalog, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{CatalogPath: path, CaptureRoot: filepath.Join(filepath.Dir(path), "captures")}
	proceed := os.Getenv("PHAROS_TEST_LEASE_PROCEED")
	err = runCLIOperation(config, catalog, "index", OperationSpec{Kind: "index.captures"}, func(ctx context.Context, op *Op) error {
		op.Step("Index captured inputs", "conversation", 10)
		op.Advance(3)
		for {
			if _, err := os.Stat(proceed); err == nil {
				return errors.New("the helper's index failed")
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	catalog.Close()
	if err == nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// The service sees a Terminal command on this Mac through its lease: live
// while the command holds it, and its journal rows reconciled once it is
// gone, with the outcome the command recorded or as interrupted when it
// was killed (§6.5).
func TestServiceSeesAnotherProcessThroughItsLease(t *testing.T) {
	slowTest(t)
	for _, mode := range []string{"killed", "recorded"} {
		t.Run(mode, func(t *testing.T) {
			catalog, config := testCatalog(t)
			server := testServer(t, config, catalog)
			proceed := filepath.Join(t.TempDir(), "proceed")
			helper := exec.Command(os.Args[0], "-test.run=^TestLeaseHelperProcess$")
			helper.Env = append(os.Environ(), "PHAROS_TEST_LEASE_CATALOG="+catalog.Path, "PHAROS_TEST_LEASE_PROCEED="+proceed)
			var stderr strings.Builder
			helper.Stderr = &stderr
			if err := helper.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- helper.Wait() }()
			defer helper.Process.Kill()

			// Live: its lease is held and its row is open.
			var row journalRow
			for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("the helper's row did not appear: %s", stderr.String())
				}
				if rows, _ := catalog.openJournal(context.Background(), server.ops.instance); len(rows) == 1 && rows[0].StepDone == 3 {
					row = rows[0]
					break
				}
			}
			if row.Process != "cli:index" || row.Kind != "index.captures" {
				t.Fatalf("helper row: %+v", row)
			}
			document := server.operationsDocument(context.Background())
			eject := document["eject"].(ejectVerdict)
			if eject.Verdict != verdictBusy {
				t.Fatalf("verdict with a live Terminal index: %+v", eject)
			}
			listed := false
			for _, entry := range document["operations"].([]map[string]any) {
				listed = listed || entry["id"] == OpID(row.ID) && entry["process"] == "cli:index" && entry["stoppable"] == false
			}
			if !listed {
				t.Fatalf("the helper's operation is not listed: %#v", document["operations"])
			}
			// The service never ends a live owner's row.
			server.reconcileOperations(context.Background())
			if rows, _ := catalog.openJournal(context.Background(), server.ops.instance); len(rows) != 1 {
				t.Fatal("a live owner's row was reconciled")
			}

			want := opInterrupted + "/owner-exited"
			if mode == "killed" {
				if err := helper.Process.Signal(syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
				<-exited
			} else {
				// Hold SQLite's write lock so the helper's last round cannot
				// write: it records its outcome in its lease instead.
				other, err := sql.Open("sqlite", catalogDSN(catalog.Path))
				if err != nil {
					t.Fatal(err)
				}
				defer other.Close()
				conn, err := other.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(proceed, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-exited:
					if err != nil {
						t.Fatalf("helper: %v: %s", err, stderr.String())
					}
				case <-time.After(20 * time.Second):
					t.Fatal("the helper did not exit")
				}
				_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
				conn.Close()
				want = opFailed + "/the helper's index failed"
			}
			// Dead: nothing holds the lease, so it no longer blocks an eject.
			if eject := server.ejectVerdictNow(context.Background(), true); eject.Verdict == verdictBusy || eject.Verdict == verdictUnsafe {
				t.Fatalf("verdict after the helper exited: %+v", eject)
			}
			if !server.reconcileOperations(context.Background()) {
				t.Fatal("the reconcile pass did not write")
			}
			var state, detail string
			if err := catalog.DB.QueryRow("SELECT state,COALESCE(reason,'')||COALESCE(error,'') FROM operation_journal WHERE id=?", row.ID).Scan(&state, &detail); err != nil {
				t.Fatal(err)
			}
			if got := state + "/" + detail; got != want {
				t.Fatalf("reconciled row %q, want %q", got, want)
			}
			if probes := probeLeases(leaseDirs(config)); len(probes) != 0 {
				t.Fatalf("leases left: %+v", probes)
			}
		})
	}
}

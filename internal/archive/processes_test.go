package archive

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/platform/filelock"
)

// The package's tests run in several processes at once: TestMain starts this
// binary once per batch of tests (see runInProcesses), and each process runs
// its batch one test at a time, as go test runs a package. Processes rather
// than t.Parallel, because on macOS modernc.org/libc puts every SQLite
// allocation in a process behind one mutex, and opening a connection parses
// the whole schema, thousands of allocations: catalog tests running in
// parallel in one process spend their time waiting for that mutex, and run no
// faster than one at a time. Separate processes also keep a test's changes to
// package variables, the environment and time.Local to its own process.
//
// Every checkout on a machine shares one pool of slots (see testSlots), and a
// process runs only while it holds one. These tests spend much of their time
// in the filesystem, and the kernel serializes that: several runs at eight
// processes each cost twice the CPU of the same tests run a few at a time, and
// take three times as long.

// testProcessesEnv sets how many processes one run starts at once; 1 runs the
// tests all in this one. testSlotsEnv sets how many run at once on the machine,
// across every run that shares its temporary directory; 0 removes the bound.
// testProcessEnv marks a process started for a batch, which runs its tests
// itself.
const (
	testProcessesEnv = "PHAROS_TEST_PROCESSES"
	testSlotsEnv     = "PHAROS_TEST_SLOTS"
	testProcessEnv   = "PHAROS_TEST_PROCESS"
)

// minTestsPerProcess keeps a narrow -run in one process: starting another
// costs more than a few tests take.
const minTestsPerProcess = 8

// batchesPerProcess splits the tests into more batches than processes, so
// that a slot changes hands between runs every minute or so rather than once
// a run, and a process that drew slow tests does not finish long after the
// others. A batch costs a process start and one template catalog.
const batchesPerProcess = 4

// runInProcesses runs the tests -run selects in batches, one process each, and
// reports their exit code. It reports false when this process should run the
// tests itself: it is one of the batches, there are too few tests to split, or
// a flag asks for something one process must produce, such as a profile or
// benchmarks.
func runInProcesses() (int, bool) {
	if os.Getenv(testProcessEnv) != "" {
		return 0, false
	}
	processes := min(8, max(1, runtime.NumCPU()/2))
	if value, err := strconv.Atoi(os.Getenv(testProcessesEnv)); err == nil {
		processes = value
	}
	run := testFlag("test.run")
	// A pattern with a slash selects subtests; leave it whole.
	if processes < 2 || strings.Contains(run, "/") || testing.CoverMode() != "" {
		return 0, false
	}
	for _, name := range []string{"test.list", "test.bench", "test.fuzz", "test.cpuprofile", "test.memprofile",
		"test.blockprofile", "test.mutexprofile", "test.trace", "test.coverprofile", "test.gocoverdir"} {
		if testFlag(name) != "" {
			return 0, false
		}
	}
	tests, err := listTests(run)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listing the tests to run them in %d processes: %v; running them in one\n", processes, err)
		return 0, false
	}
	processes = min(processes, len(tests)/minTestsPerProcess)
	if processes < 2 {
		return 0, false
	}
	groups := make([][]string, min(processes*batchesPerProcess, len(tests)/minTestsPerProcess))
	for i, name := range tests {
		groups[i%len(groups)] = append(groups[i%len(groups)], regexp.QuoteMeta(name))
	}
	return runGroups(groups, processes), true
}

// testFlag is the value of one of the testing package's flags.
func testFlag(name string) string {
	if f := flag.Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

// listTests names the tests, examples and fuzz targets whose names run
// matches, in the order they run.
func listTests(run string) ([]string, error) {
	command := exec.Command(os.Args[0], "-test.list", cmp.Or(run, "."))
	command.Env = append(os.Environ(), testProcessEnv+"=list")
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	var tests []string
	for line := range strings.Lines(string(output)) {
		if name := strings.TrimSpace(line); name != "" && !strings.HasPrefix(name, "Benchmark") {
			tests = append(tests, name)
		}
	}
	return tests, nil
}

// runGroups runs each group of tests in its own process with this process's
// flags, at most processes at a time and each holding one of the machine's
// slots, and prints each process's output whole once it exits, so that the
// output of different processes never interleaves.
func runGroups(groups [][]string, processes int) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	testlog := testFlag("test.testlogfile")
	slots := machineTestSlots()
	// go test kills this process a minute after -timeout, however long it
	// queued. Past half of it, stop taking turns and finish.
	if timeout, err := time.ParseDuration(testFlag("test.timeout")); err == nil && timeout > 0 {
		slots.until = time.Now().Add(timeout / 2)
	}
	// Each process creates and removes its files in a directory of its own.
	// In one shared by every process on the machine, each of those waits for
	// that directory.
	scratch, err := os.MkdirTemp("", "pharos-tests-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating the test processes' temporary directory: %v\n", err)
		return 1
	}
	defer os.RemoveAll(scratch)
	var (
		mu     sync.Mutex
		failed bool
		wait   sync.WaitGroup
	)
	next := make(chan int)
	go func() {
		defer close(next)
		for i := range groups {
			select {
			case next <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	runGroup := func(i int) {
		release := slots.acquire(ctx)
		defer release()
		if ctx.Err() != nil {
			return
		}
		temporary := filepath.Join(scratch, strconv.Itoa(i))
		if err := os.Mkdir(temporary, 0o700); err != nil {
			temporary = os.TempDir()
		}
		args := slices.Concat(os.Args[1:], []string{"-test.run=^(" + strings.Join(groups[i], "|") + ")$"})
		if testlog != "" {
			args = append(args, fmt.Sprintf("-test.testlogfile=%s.%d", testlog, i))
		}
		command := exec.CommandContext(ctx, os.Args[0], args...)
		command.Env = append(os.Environ(), fmt.Sprintf("%s=%d/%d", testProcessEnv, i+1, len(groups)), "TMPDIR="+temporary)
		var output bytes.Buffer
		command.Stdout, command.Stderr = &output, &output
		err := command.Run()
		// A test's own cleanup has emptied it, unless the test failed.
		os.RemoveAll(temporary)
		mu.Lock()
		defer mu.Unlock()
		os.Stdout.Write(withoutResult(output.Bytes()))
		if err != nil {
			failed = true
			// Exit status 1 is a failed test, which the output reports.
			if exit, ok := err.(*exec.ExitError); ctx.Err() == nil && (!ok || exit.ExitCode() != 1) {
				fmt.Printf("tests in process %d of %d: %v\n", i+1, len(groups), err)
			}
			if testFlag("test.failfast") == "true" {
				cancel()
			}
		}
	}
	for range processes {
		wait.Go(func() {
			for i := range next {
				runGroup(i)
			}
		})
	}
	wait.Wait()
	// go test caches only a pass, and a process stopped by -failfast writes no log.
	if testlog != "" && !failed {
		if err := mergeTestLogs(testlog, len(groups)); err != nil {
			fmt.Printf("merging the processes' test logs: %v\n", err)
			failed = true
		}
	}
	marker := ""
	if testFlag("test.v") == "test2json" {
		marker = "\x16"
	}
	if failed {
		fmt.Println(marker + "FAIL")
		return 1
	}
	fmt.Println(marker + "PASS")
	return 0
}

// testSlots is the machine's pool of slots for test processes: count lock
// files in a directory that every checkout's test binary finds, each held by
// the run whose process is using it. The kernel releases a slot when the run
// that holds it exits, however it exits.
type testSlots struct {
	dir   string
	count int
	// until, when set, is when acquire stops waiting: a process that has
	// no slot by then runs without one.
	until time.Time
}

// machineTestSlots is the pool for this machine: a quarter of its CPUs and at
// least four, so that one run alone on a small machine starts as many
// processes as it did before there was a pool.
func machineTestSlots() testSlots {
	count := max(4, runtime.NumCPU()/4)
	if value, err := strconv.Atoi(os.Getenv(testSlotsEnv)); err == nil {
		count = value
	}
	return testSlots{dir: filepath.Join(os.TempDir(), "pharos-test-slots"), count: count}
}

// acquire waits for a free slot and returns what releases it. It gives up
// waiting when ctx ends or s.until passes. A pool that cannot be used (no
// slots, or a directory that cannot be written) bounds nothing: the tests
// still run.
//
// Every attempt, the first included, follows a short random wait. A process
// that has just released a slot then has no better chance at it than one that
// has been waiting, so a run that arrives later is not kept out until an
// earlier one has finished.
func (s testSlots) acquire(ctx context.Context) (release func()) {
	if s.count < 1 || os.MkdirAll(s.dir, 0o777) != nil {
		return func() {}
	}
	for {
		select {
		case <-ctx.Done():
			return func() {}
		case <-time.After(50*time.Millisecond + time.Duration(rand.IntN(100))*time.Millisecond):
		}
		first := rand.IntN(s.count)
		for i := range s.count {
			file, err := os.OpenFile(filepath.Join(s.dir, strconv.Itoa((first+i)%s.count)), os.O_CREATE|os.O_RDWR, 0o666)
			if err != nil {
				return func() {}
			}
			switch err := filelock.Lock(file, filelock.Exclusive, filelock.Try); {
			case err == nil:
				return func() { file.Close() }
			case !errors.Is(err, filelock.ErrContended):
				file.Close()
				return func() {}
			}
			file.Close()
		}
		if !s.until.IsZero() && time.Now().After(s.until) {
			return func() {}
		}
	}
}

// withoutResult drops the PASS or FAIL line a test binary ends with, since
// this process prints the result of them all.
func withoutResult(output []byte) []byte {
	trimmed := bytes.TrimSuffix(output, []byte("\n"))
	last := trimmed[bytes.LastIndexByte(trimmed, '\n')+1:]
	if result := strings.TrimPrefix(string(last), "\x16"); result == "PASS" || result == "FAIL" {
		return output[:len(trimmed)-len(last)]
	}
	return output
}

// mergeTestLogs writes the files and environment variables every process read
// to testlog, where go test looks for them to decide whether a cached result
// is still valid.
func mergeTestLogs(testlog string, processes int) error {
	var merged bytes.Buffer
	merged.WriteString("# test log\n")
	seen := map[string]bool{}
	for i := range processes {
		path := fmt.Sprintf("%s.%d", testlog, i)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		os.Remove(path)
		for line := range strings.Lines(string(data)) {
			if line = strings.TrimSuffix(line, "\n"); !strings.HasPrefix(line, "#") && !seen[line] {
				seen[line] = true
				merged.WriteString(line + "\n")
			}
		}
	}
	return os.WriteFile(testlog, merged.Bytes(), 0o666)
}

// TestSlotsBoundProcessesAcrossRuns: a slot held by one run is not given to
// another until it is released, and a run that has queued past its deadline
// goes ahead without one.
func TestSlotsBoundProcessesAcrossRuns(t *testing.T) {
	slots := testSlots{dir: filepath.Join(t.TempDir(), "slots"), count: 1}
	release := slots.acquire(t.Context())
	late := slots
	late.until = time.Now().Add(20 * time.Millisecond)
	late.acquire(t.Context())()
	if !time.Now().After(late.until) {
		t.Fatal("a second run took the only slot while the first held it")
	}
	taken := make(chan func())
	go func() { taken <- slots.acquire(t.Context()) }()
	select {
	case <-taken:
		t.Fatal("a second run took the only slot while the first held it")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	(<-taken)()
	if file, err := os.Open(filepath.Join(slots.dir, "0")); err != nil {
		t.Fatal(err)
	} else if err := filelock.Lock(file, filelock.Exclusive, filelock.Try); err != nil {
		t.Fatal("a released slot is still held:", err)
	} else {
		file.Close()
	}
}

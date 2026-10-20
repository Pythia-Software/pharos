package archive

import (
	"bytes"
	"cmp"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The package's tests run in several processes at once: TestMain starts this
// binary once per group of tests (see runInProcesses), and each process runs
// its group one test at a time, as go test runs a package. Processes rather
// than t.Parallel, because on macOS modernc.org/libc puts every SQLite
// allocation in a process behind one mutex, and opening a connection parses
// the whole schema, thousands of allocations: catalog tests running in
// parallel in one process spend their time waiting for that mutex, and run no
// faster than one at a time. Separate processes also keep a test's changes to
// package variables, the environment and time.Local to its own process.

// testProcessesEnv sets how many processes run the tests; 1 runs them all in
// this one. testProcessEnv marks a process started for a group, which runs its
// tests itself.
const (
	testProcessesEnv = "PHAROS_TEST_PROCESSES"
	testProcessEnv   = "PHAROS_TEST_PROCESS"
)

// minTestsPerProcess keeps a narrow -run in one process: starting another
// costs more than a few tests take.
const minTestsPerProcess = 8

// runInProcesses runs the tests -run selects in groups, one process each, and
// reports their exit code. It reports false when this process should run the
// tests itself: it is one of the groups, there are too few tests to split, or
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
	groups := make([][]string, processes)
	for i, name := range tests {
		groups[i%processes] = append(groups[i%processes], regexp.QuoteMeta(name))
	}
	return runGroups(groups), true
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
// flags, and prints each process's output whole once it exits, so that the
// output of different processes never interleaves.
func runGroups(groups [][]string) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	testlog := testFlag("test.testlogfile")
	var (
		mu     sync.Mutex
		failed bool
		wait   sync.WaitGroup
	)
	for i, group := range groups {
		wait.Go(func() {
			args := slices.Concat(os.Args[1:], []string{"-test.run=^(" + strings.Join(group, "|") + ")$"})
			if testlog != "" {
				args = append(args, fmt.Sprintf("-test.testlogfile=%s.%d", testlog, i))
			}
			command := exec.CommandContext(ctx, os.Args[0], args...)
			command.Env = append(os.Environ(), fmt.Sprintf("%s=%d/%d", testProcessEnv, i+1, len(groups)))
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
			err := command.Run()
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

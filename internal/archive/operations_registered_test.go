package archive

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// registryFiles implement the registry: their goroutines and spawns are how
// operations and daemons run.
var registryFiles = map[string]bool{"operations.go": true, "operations_service.go": true, "operation_cli.go": true, "operation_broker.go": true}

// unregisteredWorkAllowed lists the other goroutines (go statements,
// WaitGroup.Go, spawn and runBackground calls) in non-test code, as
// file:function with how many it has, each with why it is not structural
// background work (§9). A new goroutine in a listed function changes its
// count and fails the test, so it needs its own reason too; new structural
// work starts an operation instead.
var unregisteredWorkAllowed = map[string]allowedWork{
	"service.go:spawn":                            {1, "the primitive under start and daemon"},
	"service.go:stopFor":                          {1, "the stop's own wait for workers and the catalog close"},
	"service.go:serveUntilStopped":                {2, "request handling: the HTTP server and the second-signal watchdog"},
	"service.go:parentGone":                       {1, "watchdog for the parent app, not an operation"},
	"service.go:driveGone":                        {1, "watchdog for the library's drive, not an operation"},
	"application.go:runApplicationCLI":            {3, "the broker's HTTP shutdown and parent watchdog, and its maintain daemon"},
	"application.go:startChildLocked":             {1, "subprocess pipe: waits for the child service to exit"},
	"application.go:runApplicationMCPCLI":         {1, "watchdog for the app's volume in an MCP stdio process"},
	"capture.go:newHash":                          {1, "a hashing pipeline stage inside a capture, which is the operation"},
	"backup.go:boundWAL":                          {1, "a WAL poller inside a backup, which is the operation"},
	"catalog_compact.go:writeCompacted":           {1, "a size poller for progress inside a compaction, which is the operation"},
	"cli.go:Run":                                  {1, "opens the browser for serve --open"},
	"drive.go:cachedVolumeIdentity":               {1, "subprocess: a diskutil lookup for a status read; writes nothing"},
	"feedback.go:CaptureFeedbackServiceLogs":      {1, "service log capture (§3.7), not an operation"},
	"probe.go:probeSelectedSources":               {1, "subprocess: a login-shell environment probe; writes nothing"},
	"adopt.go:runAdoptCLI":                        {1, "signal handling for a Terminal command"},
	"incremental_metrics.go:beginSyncMeasurement": {1, "a timer sampling memory inside an index run, which is the operation"},
	"main.go:run":                                 {1, "cmd/feedback's signal handling, outside the service"},
	"status_api.go:statusCurrent":                 {1, "the status snapshot's shared computation: a read, bounded by statusComputeTimeout, that writes nothing"},
}

type allowedWork struct {
	count  int
	reason string
}

// Unregistered work is a test failure (§9): every goroutine, spawn and
// runBackground in non-test code runs inside the registry, is a daemon, or
// is on the allowlist above.
func TestNoUnregisteredBackgroundWork(t *testing.T) {
	files := []string{}
	for _, pattern := range []string{"*.go", "../../cmd/*/*.go"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	found := map[string]int{}
	problems := []string{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, data, 0)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(path)
		for _, decl := range file.Decls {
			function, ok := decl.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			key := name + ":" + function.Name.Name
			ast.Inspect(function.Body, func(node ast.Node) bool {
				what := ""
				switch node := node.(type) {
				case *ast.GoStmt:
					what = "go statement"
				case *ast.CallExpr:
					switch fun := node.Fun.(type) {
					case *ast.SelectorExpr:
						if fun.Sel.Name == "spawn" || fun.Sel.Name == "runBackground" || fun.Sel.Name == "Go" {
							what = fun.Sel.Name + " call"
						}
						// Every daemon, wherever it is started, is a named one.
						if fun.Sel.Name == "daemon" && len(node.Args) > 0 {
							literal, ok := node.Args[0].(*ast.BasicLit)
							if !ok || !slices.Contains(daemonNames, strings.Trim(literal.Value, `"`)) {
								problems = append(problems, fileSet.Position(node.Pos()).String()+": a daemon not named in daemonNames")
							}
						}
					case *ast.Ident:
						if fun.Name == "spawn" || fun.Name == "runBackground" {
							what = fun.Name + " call"
						}
					}
				}
				if what == "" {
					return true
				}
				if registryFiles[name] {
					return true
				}
				if _, ok := unregisteredWorkAllowed[key]; ok {
					found[key]++
					return true
				}
				problems = append(problems, fileSet.Position(node.Pos()).String()+": "+what+" in "+function.Name.Name+" is not registered as an operation")
				return true
			})
		}
	}
	sort.Strings(problems)
	for _, problem := range problems {
		t.Error(problem)
	}
	for key, allowed := range unregisteredWorkAllowed {
		if allowed.reason == "" {
			t.Errorf("%s is allowed without a reason", key)
		}
		if found[key] != allowed.count {
			t.Errorf("%s starts %d goroutines; the allowlist says %d (a new one needs its own reason)", key, found[key], allowed.count)
		}
	}
}

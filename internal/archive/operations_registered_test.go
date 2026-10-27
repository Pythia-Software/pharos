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

// unregisteredWorkAllowed lists the other goroutines (and spawn and
// runBackground calls) in non-test code, as file:function, each with why it
// is not structural background work (§9). A new entry needs a reason; new
// structural work starts an operation instead.
var unregisteredWorkAllowed = map[string]string{
	"service.go:spawn":                            "the primitive under start and daemon",
	"service.go:stop":                             "the stop's own wait for workers and the catalog close",
	"service.go:serveUntilStopped":                "request handling: the HTTP server and the second-signal watchdog",
	"service.go:parentGone":                       "watchdog for the parent app, not an operation",
	"service.go:driveGone":                        "watchdog for the library's drive, not an operation",
	"application.go:runApplicationCLI":            "the broker's HTTP shutdown and parent watchdog, and its maintain daemon",
	"application.go:startChildLocked":             "subprocess pipe: waits for the child service to exit",
	"application.go:runApplicationMCPCLI":         "watchdog for the app's volume in an MCP stdio process",
	"capture.go:newHash":                          "a hashing pipeline stage inside a capture, which is the operation",
	"backup.go:boundWAL":                          "a WAL poller inside a backup, which is the operation",
	"cli.go:Run":                                  "opens the browser for serve --open",
	"drive.go:cachedVolumeIdentity":               "subprocess: a diskutil lookup for a status read; writes nothing",
	"feedback.go:CaptureFeedbackServiceLogs":      "service log capture (§3.7), not an operation",
	"probe.go:probeSelectedSources":               "subprocess: a login-shell environment probe; writes nothing",
	"adopt.go:runAdoptCLI":                        "signal handling for a Terminal command",
	"incremental_metrics.go:beginSyncMeasurement": "a timer sampling memory inside an index run, which is the operation",
	"main.go:run":                                 "cmd/feedback's signal handling, outside the service",
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
	found := map[string]bool{}
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
						if fun.Sel.Name == "spawn" || fun.Sel.Name == "runBackground" {
							what = fun.Sel.Name + " call"
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
					found[key] = true
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
	for key, reason := range unregisteredWorkAllowed {
		if reason == "" {
			t.Errorf("%s is allowed without a reason", key)
		}
		if !found[key] {
			t.Errorf("%s is allowed but no longer starts any goroutine; remove it", key)
		}
	}
	// Every daemon the service starts is named in daemonNames.
	data, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if _, rest, ok := strings.Cut(line, "s.daemon(\""); ok {
			name, _, _ := strings.Cut(rest, "\"")
			if !slices.Contains(daemonNames, name) {
				t.Errorf("daemon %q is not in daemonNames", name)
			}
		}
	}
}

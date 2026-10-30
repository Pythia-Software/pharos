package archive

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// cliCommandCoverage classifies every command usageError lists: a writing
// command runs as a leased operation (runCLIOperation or
// runCLIOperationIn with its name), and every other one says why not.
var cliCommandCoverage = map[string]string{
	"library-transfer": "", "library-recover": "", "add-this-mac": "", "capture": "", "index": "", "history-backfill": "",
	"sqlite-records": "", "backup": "", "cloud-backup": "", "ingest": "", "repositories": "", "repair-existing": "",
	"refine-usage": "", "repair-usage-attribution": "", "upgrade": "", "build-tools": "", "findings": "",
	"app":           "the broker itself: its operations are in its registry",
	"mcp-app":       "relays MCP calls to the broker; writes nothing",
	"init":          "writes a new configuration file, not library data",
	"init-library":  "creates the library it writes, so nothing can be watching it; --adopt is a known gap (§6.5)",
	"serve":         "the library service, which runs the registry",
	"cloud-restore": "restores to a new location, out of status scope (§3.6)",
	"pricing":       "a read; leased for its open, Initialize and close (runLeasedCatalogCommand)",
	"search":        "a read; leased for its open, Initialize and close",
	"tl1":           "a read; leased for its open, Initialize and close",
	"health":        "a read; leased for its open, Initialize and close",
	"doctor":        "reads the configuration and sources; never opens the catalog",
	"dev-ui":        "talks to the running service; never opens the catalog",
	"mcp":           "an MCP stdio server: query-only opens (§3.4 metrics are in memory only); not yet leased",
	"probe":         "a read; leased for its open, Initialize and close",
	"volume-id":     "a read of the volume; never opens the catalog",
	"labels":        "labels run is the leased analysis.labels operation (cliCatalogOperation); status and explain only read",
	"labeling":      "reads only a static backup copy of a catalog (immutable) and writes only its own dataset directory; never opens a live catalog",
}

// Every writing Terminal command takes a lease and registers its
// operation; a new command must be classified here.
func TestEveryWritingCommandIsALeasedOperation(t *testing.T) {
	data, err := os.ReadFile("cli.go")
	if err != nil {
		t.Fatal(err)
	}
	usage := regexp.MustCompile(`usage: pharos \[--config PATH\] \{([^}]+)\}`).FindStringSubmatch(string(data))
	if usage == nil {
		t.Fatal("usageError's command list was not found")
	}
	sources := ""
	files, _ := filepath.Glob("*.go")
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		text, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		sources += string(text)
	}
	for _, command := range strings.Split(usage[1], ",") {
		reason, ok := cliCommandCoverage[command]
		switch {
		case !ok:
			t.Errorf("%s is not classified in cliCommandCoverage", command)
		case reason == "":
			pattern := regexp.MustCompile(`run(CLIOperation(In)?|LeasedCatalog(Command)?)\([^\n]*"` + regexp.QuoteMeta(command) + `"`)
			writing := cliCatalogOperation(command, []string{"--refresh"}).Kind != ""
			if !pattern.MatchString(sources) && !writing {
				t.Errorf("%s writes but does not run as a leased operation", command)
			}
		}
	}
}

// In a Terminal command, every catalog write, Initialize, checkpoint and
// close happens while the command holds its lease: a writing command, and a
// read command, which still opens (and may initialize) and closes the
// catalog.
func TestTerminalCommandsTouchTheCatalogOnlyUnderTheirLease(t *testing.T) {
	config, err := InitLibrary(filepath.Join(t.TempDir(), "Library"), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	violations := func() []string {
		cliLeaseViolation.Lock()
		defer cliLeaseViolation.Unlock()
		seen := cliLeaseViolation.seen
		cliLeaseViolation.seen = nil
		return seen
	}
	override(t, &strictCLILeases, true)
	violations()
	// The hook sees an unleased open.
	catalog, err := OpenCatalog(config.CatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog.Close()
	if seen := violations(); len(seen) == 0 {
		t.Fatal("an unleased open and close went unnoticed")
	}
	for _, args := range [][]string{{"ingest"}, {"upgrade", "--status"}, {"health"}, {"refine-usage"}} {
		if err := Run(append([]string{"--config", config.Path}, args...)); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if seen := violations(); len(seen) > 0 {
			t.Errorf("%v touched the catalog without its lease: %v", args, seen)
		}
		if probes := probeLeases(leaseDirs(config)); len(probes) != 0 {
			t.Errorf("%v left leases: %+v", args, probes)
		}
	}
}

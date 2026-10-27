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
	"pricing":       "reads and prints the price tables",
	"search":        "a read",
	"tl1":           "a read",
	"health":        "a read",
	"doctor":        "a read",
	"dev-ui":        "talks to the running service; never opens the catalog",
	"mcp":           "an MCP stdio server: reads (§3.4 metrics are in memory only)",
	"probe":         "a read",
	"volume-id":     "a read",
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
			pattern := regexp.MustCompile(`runCLIOperation(In)?\([^\n]*"` + regexp.QuoteMeta(command) + `"`)
			if !pattern.MatchString(sources) {
				t.Errorf("%s writes but does not run as a leased operation", command)
			}
		}
	}
}

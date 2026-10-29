package archive

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// contractKindRow is one row of the kinds tables in docs/background-work.md.
type contractKindRow struct {
	kind, label, verb, holds, affects, stop, onEject, latency, journal, priority string
}

func contractKindRows(t *testing.T) []contractKindRow {
	t.Helper()
	data, err := os.ReadFile("../../docs/background-work.md")
	if err != nil {
		t.Fatal(err)
	}
	name := regexp.MustCompile("^`([^`]+)`")
	rows := []contractKindRow{}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) != 11 {
			continue
		}
		for i := range cells {
			cells[i] = strings.TrimSpace(strings.ReplaceAll(cells[i], `\*`, "*"))
		}
		match := name.FindStringSubmatch(cells[0])
		if match == nil {
			continue
		}
		label, verb, _ := strings.Cut(cells[1], " · ")
		rows = append(rows, contractKindRow{kind: match[1], label: label, verb: verb, holds: cells[4], affects: cells[5], stop: cells[6],
			onEject: cells[7], latency: cells[8], journal: cells[9], priority: cells[10]})
	}
	return rows
}

// firstWord is a contract cell's leading token, before any note.
func firstWord(cell string) string {
	cell = strings.TrimSpace(cell)
	if fields := strings.Fields(cell); len(fields) > 0 {
		return strings.TrimSuffix(strings.TrimSuffix(fields[0], ","), ";")
	}
	return ""
}

// machineHolds is a holds cell without its note in parentheses.
func machineHolds(cell string) string {
	parts := []string{}
	for _, field := range strings.Fields(cell) {
		if strings.HasPrefix(field, "(") {
			break
		}
		parts = append(parts, field)
	}
	return strings.Join(parts, " ")
}

func plainText(cell string) string {
	return strings.NewReplacer("`", "", "**", "").Replace(cell)
}

// The Go kinds table and the contract's tables (§3) agree on every row: the
// same kinds, words, holds, freshness domains, stopper, stop text, latency,
// journal policy and priority.
func TestOperationKindsMatchTheContract(t *testing.T) {
	rows := contractKindRows(t)
	if len(rows) < 40 {
		t.Fatalf("read %d kind rows from the contract; the table format changed", len(rows))
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.kind] = true
		kind, ok := lookupOperationKind(row.kind)
		if !ok {
			t.Errorf("%s is in the contract but not in operation_kinds.go", row.kind)
			continue
		}
		check := func(column, got, want string) {
			if got != want {
				t.Errorf("%s %s: Go has %q, the contract %q", row.kind, column, got, want)
			}
		}
		check("label", kind.Label, row.label)
		check("verb", kind.Verb, row.verb)
		check("holds", kind.Holds, machineHolds(row.holds))
		check("affects", kind.Affects, row.affects)
		check("stop", defaultString(kind.Stop, "—"), firstWord(row.stop))
		check("on stop or eject", kind.OnEject, plainText(row.onEject))
		check("latency", defaultString(kind.Latency, "—"), firstWord(row.latency))
		journal := firstWord(row.journal)
		switch {
		case strings.HasPrefix(journal, ">"):
			journal = "longer"
		case journal == "step":
			journal = "stage"
		}
		check("journal", kind.Journal.Mode, journal)
		if kind.Journal.Mode == "longer" {
			after := strings.TrimPrefix(strings.Fields(row.journal)[0], ">")
			check("journal threshold", strings.TrimSuffix(kind.Journal.After.String(), "s")+"", after)
		}
		priority := map[string]string{"fg": priorityForeground, "bg": priorityBackground, "parent's": ""}[firstWord(row.priority)]
		check("priority", kind.Priority, priority)
	}
	for _, kind := range operationKinds {
		if !seen[kind.Kind] {
			t.Errorf("%s is in operation_kinds.go but not in the contract", kind.Kind)
		}
	}
}

// No kind that holds the catalog writer, or writes the drive, counts as
// read-only (§4.3): each is a drive writer for the eject verdict.
func TestNoWritingKindIsReadOnly(t *testing.T) {
	for _, kind := range operationKinds {
		holds := parseHolds(kind.Holds)
		catalog := strings.Contains(" "+kind.Holds, " W:")
		drive := strings.Contains(" "+kind.Holds, " D:w") || strings.Contains(kind.Holds, "D(source):w") || strings.Contains(kind.Holds, "D(target):w")
		if (catalog || drive) && !writesDrive(holds) {
			t.Errorf("%s holds %s but counts as read-only", kind.Kind, kind.Holds)
		}
		if strings.Contains(" "+kind.Holds, " W:r") {
			t.Errorf("%s declares a read hold on the catalog writer, which is always a write", kind.Kind)
		}
	}
	if writesDrive(parseHolds("D:r")) {
		t.Error("a drive read counts as a write")
	}
	if !writesDrive(parseHolds("D:r W:w")) {
		t.Error("a catalog write on the drive does not count as a drive write")
	}
}

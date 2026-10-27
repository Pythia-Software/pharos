package archive

import (
	"strings"
	"time"
)

// The kinds table (docs/background-work.md §3) is the one source of each
// structural operation's words, holds, freshness domains, stop wiring,
// latency, journal policy and priority. TestOperationKindsMatchTheContract
// checks it against the contract's tables, so a change to one is a change to
// both.

// operationKind is one row of the kinds table.
type operationKind struct {
	Kind string
	// Label and Verb are what surfaces show: "Index changes", "Indexing
	// changes". VariantLabels replaces Label for a variant that has its own
	// name (library.rebuild full / retained).
	Label, Verb   string
	VariantLabels map[string]string
	// Holds is the contract's machine-readable hold list ("D:w W:w I:w").
	Holds string
	// Affects lists the freshness domains (§5) the operation brings current.
	Affects string
	// Stop names the existing stopper: index, view, backup, cloud, or "".
	Stop string
	// OnEject says what a stop or a release does to it.
	OnEject string
	// Latency is how soon a stop lands: instant, record, batch, step, none,
	// or "" where the question does not apply.
	Latency string
	Journal journalPolicy
	// Priority is foreground or background; "" takes the parent's.
	Priority string
	// Hidden kinds count for the eject verdict but are never listed.
	Hidden bool
	// Broker kinds run in the broker's registry, not a library's.
	Broker bool
}

// journalPolicy is when an operation gets a journal row (§6.2).
type journalPolicy struct {
	// Mode is always, longer (if-longer-than After, or once it wrote),
	// never, or stage (only as a step of its parent: it follows its root).
	Mode  string
	After time.Duration
}

var (
	journalAlways = journalPolicy{Mode: "always"}
	journalNever  = journalPolicy{Mode: "never"}
	journalStage  = journalPolicy{Mode: "stage"}
)

func journalLonger(after time.Duration) journalPolicy {
	return journalPolicy{Mode: "longer", After: after}
}

const (
	priorityForeground = "foreground"
	priorityBackground = "background"
)

// unknownOperationKind is what an unregistered kind name records as in
// production; it makes the eject verdict unsafe.
const unknownOperationKind = "unknown"

var operationKinds = []operationKind{
	// A. Ingest: capture and index.
	{Kind: "index.changes.automatic", Label: "Index changes", Verb: "Indexing changes", Holds: "D:w W:w I:w", Affects: "conv:*, view", Stop: "index",
		OnEject: "Stops between workspaces; the next index resumes it.", Latency: "batch", Journal: journalLonger(10 * time.Second), Priority: priorityBackground},
	{Kind: "index.changes.manual", Label: "Index changes", Verb: "Indexing changes", Holds: "D:w W:w I:w", Affects: "conv:*, view, an.*, findings", Stop: "index",
		OnEject: "Stops between workspaces; the next index resumes it. The chain after the sources ignores Stop and stops only on release.", Latency: "batch", Journal: journalAlways, Priority: priorityForeground},
	{Kind: "capture.this-mac", Label: "Capture this Mac", Verb: "Capturing", Holds: "D:w I:w C(this):w", Affects: "capt:this/*",
		OnEject: "Stops between files; the next capture resumes it, and what was captured stays valid.", Latency: "record", Journal: journalAlways, Priority: priorityForeground},
	{Kind: "capture.cli", Label: "Capture (Terminal)", Verb: "Capturing", Holds: "D:w C(this):w", Affects: "capt:this/*",
		OnEject: "Pharos cannot stop it; the drive stays busy until it ends.", Latency: "none", Journal: journalNever, Priority: priorityForeground},
	{Kind: "index.captures", Label: "Index captures", Verb: "Indexing captures", Holds: "D:w W:w I:w X(c):w", Affects: "conv:*, view, an.*, findings", Stop: "index",
		OnEject: "Stops between records; the next index resumes it. Waits for a running capture (blocked(capture-running)).", Latency: "batch", Journal: journalAlways, Priority: priorityForeground},
	{Kind: "library.update", Label: "Capture & index", Verb: "Capturing and indexing", Holds: "D:w W:w I:w", Affects: "capt:this/*, conv:*, an.*, findings, upgrade", Stop: "index",
		OnEject: "As index.captures; the browser then starts library.upgrade.", Latency: "batch", Journal: journalAlways, Priority: priorityForeground},
	{Kind: "library.rebuild", Label: "Full recapture & reindex / Reindex from stored messages", Verb: "Rebuilding",
		VariantLabels: map[string]string{"full": "Full recapture & reindex", "retained": "Reindex from stored messages"},
		Holds:         "D:w W:w I:w", Affects: "conv:*, view, an.*, findings", Stop: "index",
		OnEject: "Stops between workspaces; resuming the repair continues it (sync_recovery_jobs).", Latency: "batch", Journal: journalAlways, Priority: priorityForeground},
	{Kind: "index.verify", Label: "Verify retained inputs", Verb: "Verifying", Holds: "D:w W:w I:w", Affects: "—", Stop: "index",
		OnEject: "Stops between inputs.", Latency: "record", Journal: journalAlways, Priority: priorityForeground},
	{Kind: "index.sources.legacy", Label: "Index sources (legacy API)", Verb: "Indexing", Holds: "D:w W:w I:w", Affects: "conv:*",
		OnEject: "Runs inside the request, with the service's lifetime merged into its context: a release stops it between workspaces.", Latency: "batch", Journal: journalAlways, Priority: priorityForeground},
	{Kind: "ingest.cli", Label: "Index (Terminal)", Verb: "Indexing", Holds: "D:w W:w", Affects: "conv:*",
		OnEject: "Pharos cannot stop it.", Latency: "none", Journal: journalAlways, Priority: priorityForeground},
	{Kind: "host.add", Label: "Add this Mac (Terminal)", Verb: "Adding this Mac", Holds: "D:w W:w", Affects: "conv:*",
		OnEject: "Pharos cannot stop it.", Latency: "none", Journal: journalAlways, Priority: priorityForeground},
	{Kind: "capture.history-backfill", Label: "Capture history backfill (Terminal)", Verb: "Backfilling history", Holds: "D:w C(h):w", Affects: "capt:h/*",
		OnEject: "Pharos cannot stop it; its native-snapshot pause blocks ordinary capture of the chosen sources until released.", Latency: "none", Journal: journalAlways, Priority: priorityForeground},

	// B. The analysis chain, and the reads that start it.
	{Kind: "analysis.chain", Label: "Update analyses", Verb: "Updating analyses", Holds: "D:w W:w", Affects: "an.*, findings",
		OnEject: "Stops on release only; it runs again after the next index.", Latency: "batch", Journal: journalStage},
	{Kind: "analysis.github", Label: "Check GitHub for renamed repositories", Verb: "Checking GitHub", Holds: "—", Affects: "an.identities",
		OnEject: "Stops on release only.", Latency: "record", Journal: journalStage},
	{Kind: "analysis.identities", Label: "Link identities", Verb: "Linking identities", Holds: "D:w W:w", Affects: "an.identities",
		OnEject: "Stops on release only.", Latency: "batch", Journal: journalStage},
	{Kind: "analysis.git", Label: "Look up merges in Git", Verb: "Looking up merges in Git", Holds: "D:w W:w", Affects: "an.git",
		OnEject: "Stops; it runs again after the next index. Each git call has a 5 s limit (Conductor's git has none).", Latency: "batch", Journal: journalLonger(5 * time.Second), Priority: priorityBackground},
	{Kind: "analysis.tools", Label: "Update the Tools rollup", Verb: "Updating the Tools rollup", Holds: "D:w W:w", Affects: "an.tools",
		OnEject: "Stops; it runs again on the next index or Tools request.", Latency: "batch", Journal: journalLonger(5 * time.Second), Priority: priorityBackground},
	{Kind: "analysis.authorship", Label: "Classify your writing", Verb: "Classifying your writing", Holds: "D:w W:w", Affects: "an.authorship",
		OnEject: "Stops; it runs again after the next index.", Latency: "batch", Journal: journalLonger(5 * time.Second), Priority: priorityBackground},
	{Kind: "analysis.findings", Label: "Update findings", Verb: "Updating findings", Holds: "D:w W:w", Affects: "findings",
		OnEject: "Stops; it runs again after the next index.", Latency: "batch", Journal: journalLonger(5 * time.Second), Priority: priorityBackground},
	{Kind: "analysis.refresh", Label: "Update analyses", Verb: "Updating analyses", Holds: "D:w W:w", Affects: "an.git, an.tools, findings",
		OnEject: "Stops; it runs again after the next index.", Latency: "batch", Journal: journalLonger(5 * time.Second), Priority: priorityBackground},

	// C. Continuous maintenance: each pass that does real work.
	{Kind: "maintenance.library-view", Label: "Index the Search view", Verb: "Indexing the Search view", Holds: "D:w W:w", Affects: "view", Stop: "view",
		OnEject: "Stops; the batch in progress is abandoned, and it carries on when Pharos next opens the library or on Resume.", Latency: "record", Journal: journalLonger(15 * time.Second), Priority: priorityBackground},
	{Kind: "maintenance.previews", Label: "Fill in previews", Verb: "Filling in previews", Holds: "D:w W:w", Affects: "view",
		OnEject: "Stops; it carries on later.", Latency: "record", Journal: journalNever, Priority: priorityBackground},
	{Kind: "maintenance.parallelism", Label: "Measure parallel work", Verb: "Measuring parallel work", Holds: "D:w W:w", Affects: "—",
		OnEject: "Stops; it carries on later.", Latency: "record", Journal: journalNever, Priority: priorityBackground},
	{Kind: "maintenance.cache-warm", Label: "Warm page caches", Verb: "Warming page caches", Holds: "D:r", Affects: "—",
		OnEject: "Stops.", Latency: "record", Journal: journalNever, Priority: priorityBackground, Hidden: true},
	{Kind: "maintenance.search.substring", Label: "Build substring search", Verb: "Building substring search", Holds: "D:w W:w", Affects: "search.substring",
		OnEject: "Stops between batches; it carries on when Pharos next opens the library.", Latency: "batch", Journal: journalLonger(30 * time.Second), Priority: priorityBackground},
	{Kind: "maintenance.search.text", Label: "Build text search", Verb: "Building text search", Holds: "D:w W:w", Affects: "search.text",
		OnEject: "Stops between batches; it carries on later.", Latency: "batch", Journal: journalLonger(30 * time.Second), Priority: priorityBackground},
	{Kind: "maintenance.search.tools", Label: "Build tool search", Verb: "Building tool search", Holds: "D:w W:w", Affects: "search.tools",
		OnEject: "Stops between batches; it carries on later.", Latency: "batch", Journal: journalLonger(30 * time.Second), Priority: priorityBackground},
	{Kind: "maintenance.search.query", Label: "Build Query Table text indexes", Verb: "Building table search", Holds: "D:w W:w", Affects: "search.query",
		OnEject: "Stops between batches; it carries on later.", Latency: "batch", Journal: journalLonger(30 * time.Second), Priority: priorityBackground},
	{Kind: "maintenance.checkpoint", Label: "Save the catalog", Verb: "Saving the catalog", Holds: "D:w W:w", Affects: "—",
		OnEject: "Not interruptible; short.", Latency: "record", Journal: journalLonger(time.Second), Priority: priorityBackground},
	{Kind: "maintenance.metrics", Label: "Update MCP metrics", Verb: "Updating metrics", Holds: "D:w W:w", Affects: "—",
		OnEject: "Ends with the call.", Latency: "record", Journal: journalNever, Priority: priorityForeground, Hidden: true},
	{Kind: "drive.measure", Label: "Check the drive", Verb: "Checking the drive", Holds: "D:w W:w", Affects: "—",
		OnEject: "Stops.", Latency: "record", Journal: journalNever, Priority: priorityBackground, Hidden: true},

	// E. Transitions.
	{Kind: "catalog.initialize", Label: "Prepare the catalog", Verb: "Preparing the catalog", Holds: "D:w W:w Z:w", Affects: "all derived domains",
		OnEject: "Cannot be stopped; the service has no API until it ends. A kill rolls back the step in progress, and the next open re-runs from the last committed meta key.", Latency: "none", Journal: journalLonger(2 * time.Second), Priority: priorityForeground},
	{Kind: "library.upgrade", Label: "Upgrade the library", Verb: "Upgrading the library", Holds: "D:w W:w", Affects: "upgrade, an.tools",
		OnEject: "Stops after the unit in progress; starting the upgrade again resumes it.", Latency: "record", Journal: journalAlways, Priority: priorityForeground},
	{Kind: "tools.ledger.build", Label: "Build the Tools ledger", Verb: "Building the Tools ledger", Holds: "D:w W:w", Affects: "an.ledger, an.tools",
		OnEject: "Stops between conversations; building the ledger again resumes it.", Latency: "record", Journal: journalAlways, Priority: priorityForeground},

	// F. Protect and move.
	{Kind: "backup.local", Label: "Back up", Verb: "Backing up", Holds: "D:w B(d):w C(*):r", Affects: "backup.local", Stop: "backup",
		OnEject: "Stops; the next backup carries on from what was copied. Waits for each host's capture (blocked(capture-running); today's phase \"waiting for a capture to finish\").", Latency: "record", Journal: journalAlways, Priority: priorityForeground},
	{Kind: "cloud.connect", Label: "Test a cloud backup destination", Verb: "Testing the destination", Holds: "D:w K:w", Affects: "—", Stop: "cloud",
		OnEject: "Stops the test; connect the destination again afterwards.", Latency: "instant", Journal: journalAlways, Priority: priorityForeground},
	{Kind: "cloud.upload", Label: "Upload a cloud backup", Verb: "Uploading a cloud backup", Holds: "D:w K(d):w", Affects: "backup.cloud:d", Stop: "cloud",
		OnEject: "Stops the upload and deletes its staged archive; the last completed cloud backup is unchanged. It starts again from the beginning when started again, or at the schedule's next time.", Latency: "batch", Journal: journalAlways, Priority: priorityForeground},
	{Kind: "cloud.verify", Label: "Verify a cloud backup", Verb: "Verifying a cloud backup", Holds: "D:w K(d):w W:w", Affects: "backup.cloud:d", Stop: "cloud",
		OnEject: "Stops and removes the partly recovered copy; run the verification again.", Latency: "record", Journal: journalAlways, Priority: priorityForeground},
	{Kind: "cloud.restore", Label: "Restore from the cloud (Terminal)", Verb: "Restoring", Holds: "—", Affects: "—",
		OnEject: "Out of status scope. It restores to a new location, often on a Mac with no library or Pharos configuration (cloud_cli.go), so there is no library whose status could show it. It takes no lease and writes no journal.", Journal: journalNever, Priority: priorityForeground},
	{Kind: "library.transfer", Label: "Copy library / Move library", Verb: "Copying the library", VariantLabels: map[string]string{"copy": "Copy library", "move": "Move library"},
		Holds: "O:w D(source):w D(target):w", Affects: "(target) conv:*",
		OnEject: "Not stoppable; eject of either drive must wait.", Latency: "none", Journal: journalAlways, Priority: priorityForeground, Broker: true},
	{Kind: "library.recover", Label: "Restore a backup", Verb: "Restoring a backup", Holds: "O:w D(target):w", Affects: "(target) all",
		OnEject: "Not stoppable; eject must wait.", Latency: "none", Journal: journalAlways, Priority: priorityForeground, Broker: true},
	{Kind: "library.deliver", Label: "Deliver captured history", Verb: "Delivering history", Holds: "O:w M:w D(source):w D(target):w", Affects: "(target) conv:*",
		OnEject: "Not stoppable; it stops both libraries' services first (§4.4).", Latency: "none", Journal: journalAlways, Priority: priorityBackground, Broker: true},
	{Kind: "library.deliver.capture", Label: "Capture into the pending library", Verb: "Capturing pending history", Holds: "D(source):w", Affects: "—",
		OnEject: "As its parent.", Latency: "none", Journal: journalStage, Priority: priorityBackground, Broker: true},
	{Kind: "library.adopt", Label: "Move this Mac's archive into a library", Verb: "Moving the archive", Holds: "D(source):w D(target):w", Affects: "all",
		OnEject: "Not stoppable.", Latency: "none", Journal: journalAlways, Priority: priorityForeground},
	{Kind: "library.create", Label: "Create a library", Verb: "Creating the library", Holds: "D(target):w", Affects: "—",
		OnEject: "Not stoppable; short.", Latency: "none", Journal: journalLonger(5 * time.Second), Priority: priorityForeground, Broker: true},

	// G. Process and device lifecycle.
	{Kind: "service.start", Label: "Open the library", Verb: "Opening the library", Holds: "M:w D:w", Affects: "—",
		OnEject: "Not stoppable. The broker gives up after 2 min and SIGTERMs the child (§10, G2).", Latency: "none", Journal: journalAlways, Priority: priorityForeground, Broker: true},
	{Kind: "library.release", Label: "Release the library", Verb: "Releasing the library", Holds: "D:w", Affects: "—",
		OnEject: "Is the stop. Fails after 7 s with \"Pharos is finishing a write\".", Journal: journalAlways, Priority: priorityForeground},
	{Kind: "app.update", Label: "Update Pharos", Verb: "Updating Pharos", Holds: "update-lock:w", Affects: "—",
		OnEject: "Native; bridged to the header ring as today.", Journal: journalNever, Priority: priorityBackground},
}

var operationKindIndex = func() map[string]*operationKind {
	index := make(map[string]*operationKind, len(operationKinds)+1)
	for i := range operationKinds {
		index[operationKinds[i].Kind] = &operationKinds[i]
	}
	index[unknownOperationKind] = &operationKind{Kind: unknownOperationKind, Label: "Unknown work", Verb: "Working", Holds: "D:w W:w",
		OnEject: "Pharos does not know what this work does, so ejecting is not safe until it ends.", Latency: "none", Journal: journalAlways, Priority: priorityBackground}
	return index
}()

// lookupOperationKind finds kind's row; ok is false for a kind the table
// does not have.
func lookupOperationKind(kind string) (*operationKind, bool) {
	row, ok := operationKindIndex[kind]
	return row, ok && kind != unknownOperationKind
}

// label is the kind's label for variant.
func (k *operationKind) label(variant string) string {
	if label := k.VariantLabels[variant]; label != "" {
		return label
	}
	return k.Label
}

// Hold is one resource an operation holds while it runs (§4.1).
type Hold struct {
	Resource string `json:"resource"`
	Mode     string `json:"mode"` // read | write
}

// holdResources maps the contract's hold codes to resource names (§4.1).
var holdResources = map[string]string{"D": "drive", "W": "catalog-writer", "I": "ingest", "C": "capture", "X": "index", "B": "backup-dest",
	"K": "cloud", "O": "broker-operation", "Z": "initialize", "M": "broker-mutex"}

// parseHolds reads a hold list such as "D:w W:w C(this):w". A parameter
// names the resource's instance: C(this) is capture:this, D(target) is
// drive:target. Unknown codes are kept by name.
func parseHolds(text string) []Hold {
	holds := []Hold{}
	for _, field := range strings.Fields(text) {
		if field == "—" {
			continue
		}
		code, mode, ok := strings.Cut(field, ":")
		if !ok {
			continue
		}
		mode = map[string]string{"w": "write", "r": "read"}[mode]
		if mode == "" {
			continue
		}
		name, parameter := code, ""
		if open := strings.Index(code, "("); open >= 0 && strings.HasSuffix(code, ")") {
			name, parameter = code[:open], code[open+1:len(code)-1]
		}
		resource := holdResources[name]
		if resource == "" {
			resource = name
		}
		if parameter != "" {
			resource += ":" + parameter
		}
		holds = append(holds, Hold{Resource: resource, Mode: mode})
	}
	return holds
}

// writesDrive reports whether holds make an operation a writer to the
// library's drive (§4.2): a write hold on the drive, or any hold on the
// catalog writer, since the catalog is on the drive.
func writesDrive(holds []Hold) bool {
	for _, hold := range holds {
		if hold.Resource == "catalog-writer" || hold.Mode == "write" && (hold.Resource == "drive" || strings.HasPrefix(hold.Resource, "drive:")) {
			return true
		}
	}
	return false
}

// holdsDrive reports whether holds include the library's drive in any mode.
func holdsDrive(holds []Hold) bool {
	for _, hold := range holds {
		if hold.Resource == "drive" || hold.Resource == "catalog-writer" || strings.HasPrefix(hold.Resource, "drive:") {
			return true
		}
	}
	return false
}

// parseAffects splits a kind's freshness domains.
func parseAffects(text string) []string {
	domains := []string{}
	for _, domain := range strings.Split(text, ",") {
		if domain = strings.TrimSpace(domain); domain != "" && domain != "—" {
			domains = append(domains, domain)
		}
	}
	return domains
}

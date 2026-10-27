package archive

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
)

// The eject verdict (docs/background-work.md §4.2) is computed from what
// holds the library's drive and is known to this Mac: this process's
// registry, the leases of other Pharos processes (probed at the moment of
// reading), the capture and index locks no lease or operation names, and,
// in the broker, its own operations. It errs toward not safe: the worst
// rule that applies wins, and a missing estimate never improves it.

const (
	verdictSafe   = "safe"
	verdictSafeIn = "safe-in"
	verdictBusy   = "busy"
	verdictUnsafe = "unsafe"
)

var verdictRank = map[string]int{verdictSafe: 0, verdictSafeIn: 1, verdictBusy: 2, verdictUnsafe: 3}

// ejectVerdict is the snapshot's eject document.
type ejectVerdict struct {
	Verdict    string         `json:"verdict"`
	ETASeconds *float64       `json:"eta_seconds"`
	Summary    string         `json:"summary"`
	Blockers   []ejectBlocker `json:"blockers"`
}

// ejectBlocker is one holder of the drive, with what ejecting does to it.
type ejectBlocker struct {
	Operation  string   `json:"operation,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Label      string   `json:"label"`
	OnEject    string   `json:"on_eject"`
	Latency    string   `json:"latency,omitempty"`
	Verdict    string   `json:"verdict"`
	ETASeconds *float64 `json:"eta_seconds,omitempty"`
	Process    string   `json:"process,omitempty"`
	unit       string
}

// heldLock is a capture or index lock a probe found held.
type heldLock struct {
	Kind string // capture | index
	Path string
}

// verdictInput is everything a verdict reads.
type verdictInput struct {
	ops    []opView
	leases []leaseProbe
	locks  []heldLock
	// unregistered lists writers libraryActivities reports that no
	// operation or lease accounts for (the registry's self-check).
	unregistered []string
	// unreachable is the reason the library's service cannot be asked, when
	// it cannot.
	unreachable string
}

// releaseBudget is the release's budget: a holder that cannot stop within
// it blocks the eject.
const releaseBudget = stopTimeout

func secondsPointer(value time.Duration) *float64 {
	seconds := math.Ceil(value.Seconds())
	return &seconds
}

// holderVerdict is the verdict one running operation imposes, with its
// estimate of how soon a stop would land.
func holderVerdict(view opView) (string, *float64) {
	kind := view.Kind
	switch {
	case kind == nil || kind.Kind == unknownOperationKind:
		return verdictUnsafe, nil
	case view.Foreign && holdsDrive(view.Spec.Holds):
		// Another process's work: Pharos cannot stop it (a held lease).
		return verdictBusy, nil
	case kind.Broker, kind.Kind == "catalog.initialize":
		return verdictBusy, nil
	case !writesDrive(view.Spec.Holds):
		return verdictSafe, nil
	case view.State == opQueued:
		// It holds nothing yet; a release ends its wait at once.
		return verdictSafeIn, secondsPointer(time.Second)
	case view.SafePointSet:
		if view.SafePoint > releaseBudget {
			return verdictBusy, secondsPointer(view.SafePoint)
		}
		return verdictSafeIn, secondsPointer(view.SafePoint)
	}
	switch kind.Latency {
	case "instant":
		return verdictSafeIn, secondsPointer(time.Second)
	case "record":
		return verdictSafeIn, secondsPointer(2 * time.Second)
	case "batch":
		// Assumed to stop in time; a release that times out refuses safely.
		return verdictSafeIn, nil
	}
	return verdictBusy, nil
}

// computeEjectVerdict applies §4.2 to in.
func computeEjectVerdict(in verdictInput) ejectVerdict {
	blockers := []ejectBlocker{}
	byRoot := map[OpID]int{}
	for _, view := range in.ops {
		if opTerminal(view.State) || view.Kind == nil || view.Spec.Kind == "library.release" || !holdsDrive(view.Spec.Holds) && view.Kind.Kind != unknownOperationKind {
			continue
		}
		verdict, eta := holderVerdict(view)
		label := view.Kind.label(view.Spec.Variant)
		blocker := ejectBlocker{Operation: string(view.ID), Kind: view.Spec.Kind, Label: label, OnEject: view.Kind.OnEject, Latency: view.Kind.Latency,
			Verdict: verdict, ETASeconds: eta, Process: view.Process, unit: view.Step.Unit}
		if view.Foreign {
			blocker.Label = leaseLabel(leaseInfo{Kinds: []string{view.Spec.Kind}, Process: view.Process})
			blocker.OnEject = "Pharos cannot stop it; the drive stays busy until it ends."
		}
		// One blocker per run: the worst of the run and its stages.
		if index, ok := byRoot[view.Root]; ok {
			if verdictRank[verdict] > verdictRank[blockers[index].Verdict] {
				blockers[index] = blocker
			}
			continue
		}
		byRoot[view.Root] = len(blockers)
		blockers = append(blockers, blocker)
	}
	for _, probe := range in.leases {
		if !probe.Live && probe.Err == nil {
			continue // a dead lease holds nothing
		}
		if probe.Err != nil || probe.Info == nil {
			blockers = append(blockers, ejectBlocker{Label: "A Pharos process whose lease cannot be read", Verdict: verdictUnsafe,
				OnEject: "Pharos cannot tell what it is doing, so ejecting is not safe until it ends."})
			continue
		}
		if !holdsDrive(probe.Info.Holds) {
			continue
		}
		blockers = append(blockers, ejectBlocker{Kind: firstOf(probe.Info.Kinds), Label: leaseLabel(*probe.Info), Verdict: verdictBusy, Process: probe.Info.Process,
			OnEject: "Pharos cannot stop it; the drive stays busy until it ends."})
	}
	for _, lock := range in.locks {
		what := "A capture"
		if lock.Kind == "index" {
			what = "An index of a capture"
		}
		blockers = append(blockers, ejectBlocker{Label: what + " by a process Pharos does not know", Verdict: verdictUnsafe,
			OnEject: "Another process holds " + filepath.Base(lock.Path) + " without a Pharos lease (an older Pharos, or one that just started); wait for it to finish."})
	}
	for _, label := range in.unregistered {
		blockers = append(blockers, ejectBlocker{Label: label, Verdict: verdictUnsafe, OnEject: "This work is not registered as an operation, so Pharos cannot say when it stops."})
	}
	if in.unreachable != "" {
		blockers = append(blockers, ejectBlocker{Label: "The library's service", Verdict: verdictUnsafe, OnEject: in.unreachable})
	}
	sort.SliceStable(blockers, func(i, j int) bool { return verdictRank[blockers[i].Verdict] > verdictRank[blockers[j].Verdict] })
	result := ejectVerdict{Verdict: verdictSafe, Blockers: blockers}
	unknown := false
	var eta *float64
	for _, blocker := range blockers {
		if verdictRank[blocker.Verdict] > verdictRank[result.Verdict] {
			result.Verdict = blocker.Verdict
		}
		if blocker.Verdict == verdictSafeIn {
			if blocker.ETASeconds == nil {
				unknown = true
			} else if eta == nil || *blocker.ETASeconds > *eta {
				eta = blocker.ETASeconds
			}
		}
	}
	if result.Verdict == verdictSafeIn && !unknown {
		result.ETASeconds = eta
	}
	result.Summary = verdictSummary(result)
	return result
}

// leaseLabel names a lease's work for people: "Capture (Terminal), in
// another process (pharos capture)".
func leaseLabel(info leaseInfo) string {
	label := "Pharos work"
	if kind, ok := lookupOperationKind(firstOf(info.Kinds)); ok {
		label = kind.Label
	}
	command := strings.TrimPrefix(info.Process, "cli:")
	if command == "" || command == info.Process {
		command = info.Process
	} else {
		command = "pharos " + command
	}
	return fmt.Sprintf("%s, in another process (%s)", label, command)
}

func blockerLabels(blockers []ejectBlocker, verdict string) string {
	labels := []string{}
	for _, blocker := range blockers {
		if blocker.Verdict == verdict && !slices.Contains(labels, blocker.Label) {
			labels = append(labels, blocker.Label)
		}
	}
	return joinLabels(labels)
}

func joinLabels(labels []string) string {
	switch len(labels) {
	case 0:
		return ""
	case 1:
		return labels[0]
	}
	return strings.Join(labels[:len(labels)-1], ", ") + " and " + labels[len(labels)-1]
}

// verdictSummary is the verdict's sentence (server text, §7.1).
func verdictSummary(v ejectVerdict) string {
	switch v.Verdict {
	case verdictUnsafe:
		return "Not safe to eject: " + lowerFirst(blockerLabels(v.Blockers, verdictUnsafe)) + "."
	case verdictBusy:
		return "Eject waits for " + lowerFirst(blockerLabels(v.Blockers, verdictBusy)) + ", which cannot stop in time."
	case verdictSafeIn:
		labels := lowerFirst(blockerLabels(v.Blockers, verdictSafeIn))
		if v.ETASeconds != nil {
			return fmt.Sprintf("Ejecting stops %s within about %s.", labels, secondsLabel(*v.ETASeconds))
		}
		unit := "step"
		for _, blocker := range v.Blockers {
			if blocker.Verdict == verdictSafeIn && blocker.ETASeconds == nil && blocker.unit != "" && blocker.unit != "byte" {
				unit = blocker.unit
				break
			}
		}
		return fmt.Sprintf("Ejecting stops %s after the current %s; if that takes more than %d s, Pharos refuses and you can try again.", labels, unit, int(releaseBudget.Seconds()))
	}
	if readers := blockerLabels(v.Blockers, verdictSafe); readers != "" {
		return "Safe to eject: ejecting stops " + lowerFirst(readers) + "."
	}
	return "Safe to eject: nothing is writing to the library."
}

func secondsLabel(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%d s", int(seconds))
	}
	return fmt.Sprintf("%d min", int(math.Ceil(seconds/60)))
}

// lowerFirst lowers a label's first letter for use inside a sentence, unless
// it starts a proper name (two capitals, as in "MCP").
func lowerFirst(text string) string {
	if len(text) < 2 || text[0] < 'A' || text[0] > 'Z' || text[1] >= 'A' && text[1] <= 'Z' {
		return text
	}
	return strings.ToLower(text[:1]) + text[1:]
}

// heldCaptureLocks probes the capture lock of every host under root and the
// index lock of every capture, as captureActive does (a shared lock that
// cannot be taken is held exclusively by someone).
func heldCaptureLocks(root string) []heldLock {
	held := []heldLock{}
	hosts, err := os.ReadDir(root)
	if err != nil {
		return held
	}
	probe := func(path string) bool {
		file, err := os.Open(path)
		if err != nil {
			return false
		}
		defer file.Close()
		return syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) != nil
	}
	for _, host := range hosts {
		if !host.IsDir() || !validCaptureName(host.Name()) {
			continue
		}
		dir := filepath.Join(root, host.Name())
		if path := filepath.Join(dir, captureLockName); probe(path) {
			held = append(held, heldLock{Kind: "capture", Path: path})
		}
		sources, _ := os.ReadDir(dir)
		for _, source := range sources {
			if path := filepath.Join(dir, source.Name(), indexLockName); source.IsDir() && probe(path) {
				held = append(held, heldLock{Kind: "index", Path: path})
			}
		}
	}
	return held
}

// unnamedLocks keeps the held locks that no running operation of this
// process and no live lease accounts for: capture and index locks are taken
// under the ingest coordinator in the service, and declared in a lease
// elsewhere.
func unnamedLocks(locks []heldLock, ops []opView, leases []leaseProbe) []heldLock {
	if len(locks) == 0 {
		return locks
	}
	// Only a write hold takes these locks exclusively (a backup's read of
	// the captures takes them shared, which a probe does not see).
	names := func(holds []Hold, kind string) bool {
		return slices.ContainsFunc(holds, func(hold Hold) bool {
			return hold.Mode == "write" && (hold.Resource == "ingest" || hold.Resource == kind || strings.HasPrefix(hold.Resource, kind+":"))
		})
	}
	unnamed := []heldLock{}
	for _, lock := range locks {
		named := slices.ContainsFunc(ops, func(view opView) bool { return !opTerminal(view.State) && names(view.Spec.Holds, lock.Kind) }) ||
			slices.ContainsFunc(leases, func(probe leaseProbe) bool {
				return probe.Live && probe.Info != nil && (names(probe.Info.Holds, lock.Kind) || names(probe.Info.Holds, "capture") || names(probe.Info.Holds, "index"))
			})
		if !named {
			unnamed = append(unnamed, lock)
		}
	}
	return unnamed
}

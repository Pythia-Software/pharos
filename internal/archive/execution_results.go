package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Resolved results of execution evidence components
// (docs/execution-evidence-backfill.md, "Rules versions and resolved
// results"). A derivative records the digest of the evidence its
// conversation's resolution can depend on (execution_dependencies.go). The
// digest says nothing about the rules that read that evidence, so a change of
// the resolver's, the evidence reading's or the occurrence rules' version
// leaves every digest as it was. What the rules made of a component is
// recorded beside it instead: execution_result_stamps holds, per component
// digest, a hash of everything the derivatives' builders read from the
// resolution and the occurrence index for the component's conversations
// (executionResultLines), under the rules meta names.
//
// When an executable with other rules opens the catalog, the next
// propagateExecutionChanges resolves the whole catalog once under its rules
// and compares: a derivative whose component resolves to the recorded result
// stays current, one whose result differs (or has none recorded) is marked,
// and the recorded results and rules advance in the same transaction, so the
// comparison happens once. A rules change therefore costs one resolution,
// and rebuilds only what it changed.

const (
	// executionDigestResolver and executionDigestEvidence are the first
	// lines of every component digest. They name the digest's encoding, not
	// the rules: their values are the resolver and evidence versions the
	// digest stopped following, which keeps the digests stored under those
	// versions. Change one only when the digest's rows change format: that
	// supersedes every stored digest, and so rebuilds every conversation's
	// labels and agent events.
	executionDigestResolver = "execution-v9"
	executionDigestEvidence = "execution-evidence-v5"
	// executionLegacyRules are the rules of every executable that wrote
	// digests in this encoding without recording results.
	executionLegacyRules = executionDigestResolver + "/" + executionDigestEvidence

	// executionResultEncoding names what executionResultLines writes and
	// how. Change it when a line is added, removed or reformatted. Recorded
	// results in another encoding cannot be compared, so: alone, the change
	// rewrites the recorded results and marks nothing (the rules did not
	// change); together with a rules version it marks every derivative, like
	// a catalog that never recorded results. Release the two apart.
	executionResultEncoding = "execution-result/v1"

	// executionResultStampsKey and executionResultEncodingKey (meta) are the
	// rules (executionDependentsVersions) and the encoding the recorded
	// results were written under. Only executables that record results write
	// them, so they fall behind executionDependentsVersionsKey when another
	// executable marks under other rules.
	executionResultStampsKey   = "execution_result_stamps_versions"
	executionResultEncodingKey = "execution_result_stamps_encoding"
)

// executionRulesVersion names the rules that turn evidence rows into what
// derivatives read: the resolver's and the evidence reading's. A variable so
// tests can stand in a later version.
var executionRulesVersion = executionResolverVersion + "/" + executionEvidenceVersion

// executionResultStampsDDL holds each component's resolved result by its
// digest. A side table: the ledgers' rows are written positionally by other
// executables.
const executionResultStampsDDL = `CREATE TABLE IF NOT EXISTS execution_result_stamps (
  digest TEXT PRIMARY KEY, result TEXT NOT NULL) WITHOUT ROWID`

// executionResultLines are the resolved facts about one conversation that a
// derivative of its component may rest on, one line each. They are what the
// labels and agent events builders read from the resolution and the
// occurrence index (labelingPass.inheritedHistory: each record's occurrence
// role, original and deciding rule, the binding of the stream that recorded
// it, and the transcript's partial coverage), and what the usage lineage
// projects from the resolution for a conversation and its sessions
// (usageAttributionFrom: execution, parent, root, depth, their qualities and
// conflicts, mirrors and children). The labels and agent events builders do
// not read the lineage themselves, but authorship does
// (authorshipContinuations, into stored message_authorship, which the
// labels builder reads). Recording it here marks the labels of a
// conversation whose lineage a rules change moves; it does not redo
// authorship, whose own generation follows its inputs and authorshipVersion
// and not the resolver's rules, so a rules change that flips a continuation
// needs an authorshipVersion bump with it (as before results were recorded).
//
// Left out, because no builder of a stored derivative reads them: an
// execution's label and its sibling order, its workspace and provider, and
// the claims a decision cites. A builder that starts to read one of those
// (or anything else from the resolution) must add it here and change
// executionResultEncoding; otherwise a rules change that moves it would
// leave that builder's output stale.
func executionResultLines(r *executionResolution, o *occurrenceIndex, cid string) []string {
	var lines []string
	add := func(parts ...string) { lines = append(lines, cid+"\x1f"+strings.Join(parts, "\x1f")) }
	refs := r.StreamsIn(cid)
	if len(refs) == 0 {
		add("absent")
	}
	seen := map[executionID]bool{}
	for _, ref := range refs {
		b := r.Bind(ref)
		main, attempt := "", ""
		if stream := r.Stream(ref); stream != nil {
			main = stream.MainSessionID
		}
		if a := r.Attempts[ref]; a != nil {
			attempt = string(a.Outcome) + ":" + string(a.Owner)
		}
		add("stream", ref.String(), string(b.State), string(b.ExecutionID), main, attempt, strconv.FormatBool(r.mirroredLaunches[ref]))
		x := r.Executions[b.ExecutionID]
		if x == nil || seen[x.ID] {
			continue
		}
		seen[x.ID] = true
		streams := make([]string, 0, len(x.Streams))
		for _, stream := range x.Streams {
			streams = append(streams, stream.String())
		}
		sort.Strings(streams)
		a := r.Ancestry(x.ID)
		add("execution", string(x.ID), x.Presence, x.Conversation, strings.Join(x.Segments, ","), strings.Join(streams, ","),
			string(x.Parent.State), string(x.Parent.ParentID), x.Parent.Certainty.String(), string(x.FamilyRoot), x.FamilyCertainty.String(),
			strconv.FormatBool(x.Conflicted), x.Evidence,
			string(a.State), strconv.FormatBool(a.Known), strconv.Itoa(a.Depth), string(a.Root), a.CertaintyName(), strconv.FormatBool(a.Inferred),
			strconv.FormatBool(a.Conflicted), strings.Join(executionIDStrings(a.Chain), ","), strings.Join(executionIDStrings(r.Children(x.ID)), ","))
	}
	if o == nil {
		return lines
	}
	for _, claim := range o.byConversation[cid] {
		found := o.Lookup(claim.Subject, sourceRef{})
		rules := make([]string, 0, len(found.Evidence))
		for _, evidence := range found.Evidence {
			rules = append(rules, evidence.Rule)
		}
		add("occurrence", claim.Subject.String(), string(claim.Kind), claim.Object.String(), claim.Context.String(),
			string(found.Role), found.Canonical.String(), string(found.Owner), found.Navigate.String(), strings.Join(rules, ","))
	}
	if reason := o.Partial(cid); reason != "" {
		add("partial", reason)
	}
	return lines
}

// executionComponentResults hashes, for each wanted component digest, the
// result lines of the conversations digests gives it. r and o must be the
// resolution and the catalog-wide occurrence index of the snapshot digests
// was read from.
func executionComponentResults(digests map[string]string, r *executionResolution, o *occurrenceIndex, want map[string]bool) map[string]string {
	members := map[string][]string{}
	for id, digest := range digests {
		if want[digest] {
			members[digest] = append(members[digest], id)
		}
	}
	out := make(map[string]string, len(members))
	for digest, ids := range members {
		var lines []string
		for _, id := range ids {
			lines = append(lines, executionResultLines(r, o, id)...)
		}
		sort.Strings(lines)
		sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
		out[digest] = hex.EncodeToString(sum[:12])
	}
	return out
}

// executionResultMemo holds resolved results by component digest, per
// catalog file, rules and encoding. A result is a function of the rules and
// of the evidence its digest names, so an entry holds at every source
// generation, from any reader. It carries results from where a resolution
// is at hand (a builder's snapshot, propagateExecutionChanges' committed
// read) to the write transaction that records or compares them.
var executionResultMemo struct {
	sync.Mutex
	entries map[string]map[string]string
}

// executionResultMemoLimit bounds one catalog's remembered results.
const executionResultMemoLimit = 400000

func executionResultMemoKey(file string) string {
	return file + "\x00" + executionDependentsVersions() + "\x00" + executionResultEncoding
}

// rememberedExecutionResults returns the remembered results of wanted, and
// the digests it has none for.
func rememberedExecutionResults(file string, want map[string]bool) (found map[string]string, missing map[string]bool) {
	found, missing = map[string]string{}, map[string]bool{}
	executionResultMemo.Lock()
	defer executionResultMemo.Unlock()
	entry := executionResultMemo.entries[executionResultMemoKey(file)]
	for digest := range want {
		if result, ok := entry[digest]; ok && file != "" {
			found[digest] = result
		} else {
			missing[digest] = true
		}
	}
	return found, missing
}

func rememberExecutionResults(file string, results map[string]string) {
	if file == "" || len(results) == 0 {
		return
	}
	key := executionResultMemoKey(file)
	executionResultMemo.Lock()
	defer executionResultMemo.Unlock()
	if executionResultMemo.entries == nil || len(executionResultMemo.entries) >= 2*dependencyCacheCatalogs && executionResultMemo.entries[key] == nil {
		executionResultMemo.entries = map[string]map[string]string{}
	}
	entry := executionResultMemo.entries[key]
	if entry == nil || len(entry)+len(results) > executionResultMemoLimit {
		entry = map[string]string{}
		executionResultMemo.entries[key] = entry
	}
	maps.Copy(entry, results)
}

// executionResultResolveHook, when set by a test, runs each time results are
// computed from a resolution of the whole catalog (primeExecutionResults,
// the only place: neither a request nor a write transaction does).
var executionResultResolveHook func()

// executionPrimedHook, when set by a test, runs in propagateExecutionChanges
// between its committed reads and its write transaction.
var executionPrimedHook func()

// heldExecutionDigests are the digests the ledgers' derivatives were built
// at that still hold, with the rows read: conversation and stored digest.
func heldExecutionDigests(ctx context.Context, q queryer, ledgers []string, digests map[string]string) (held map[string]bool, rows map[string][][2]string, err error) {
	held, rows = map[string]bool{}, map[string][][2]string{}
	for _, ledger := range ledgers {
		read, err := queryMapsContext(ctx, q, `SELECT conversation_id,execution_digest FROM `+ledger)
		if err != nil {
			return nil, nil, err
		}
		for _, row := range read {
			id, stored := firstString(row["conversation_id"]), firstString(row["execution_digest"])
			rows[ledger] = append(rows[ledger], [2]string{id, stored})
			if stored != "" && digests[id] == stored {
				held[stored] = true
			}
		}
	}
	return held, rows, nil
}

// storedExecutionResults reads the recorded results ({} before any were).
func storedExecutionResults(ctx context.Context, q queryer) (map[string]string, error) {
	rows, err := queryMapsContext(ctx, q, `SELECT digest,result FROM execution_result_stamps`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return map[string]string{}, nil
		}
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[firstString(row["digest"])] = firstString(row["result"])
	}
	return out, nil
}

// executionReconcilePlan is what a reconciliation at one snapshot does.
type executionReconcilePlan struct {
	// marks are, per ledger, the derivatives to mark.
	marks map[string]map[string]bool
	// results are the results to record afterwards, by digest; nil when
	// none could be compared and every derivative is marked, so that their
	// rebuilds record them.
	results map[string]string
}

// planExecutionReconcile compares every derivative in ledgers with the
// catalog at q: with its component's digest and, when the rules changed
// (state is not consistent), with the result recorded for that digest. A
// derivative is marked when its digest does not hold; when the rules changed
// and its component's result under this executable's rules differs from the
// recorded one; and when the rules changed and no comparable result is
// recorded (state is not comparable, or the digest has none), since what it
// was built on is then unknown. It resolves nothing: the current results are
// the ones primeExecutionResults remembered from a committed snapshot, and a
// component that has none (its evidence, or a publication at it, committed
// between that snapshot and q) is marked rather than resolved here, in the
// writer.
func planExecutionReconcile(ctx context.Context, q queryer, ledgers []string, state executionPropagation) (*executionReconcilePlan, error) {
	plan := &executionReconcilePlan{marks: map[string]map[string]bool{}}
	digests, err := executionDependencyDigests(ctx, q)
	if err != nil {
		return nil, err
	}
	held, rows, err := heldExecutionDigests(ctx, q, ledgers, digests)
	if err != nil {
		return nil, err
	}
	consistent, comparable := state.consistent(), state.comparable()
	recorded := map[string]string{}
	if consistent || comparable {
		// When consistent nothing rests on a comparison: a component without
		// a remembered result is left to its next publication.
		file, _, err := dependencyCacheKey(ctx, q)
		if err != nil {
			return nil, err
		}
		plan.results, _ = rememberedExecutionResults(file, held)
	}
	if !consistent && comparable {
		if recorded, err = storedExecutionResults(ctx, q); err != nil {
			return nil, err
		}
	}
	for _, ledger := range ledgers {
		plan.marks[ledger] = map[string]bool{}
		for _, row := range rows[ledger] {
			id, stored := row[0], row[1]
			switch {
			case stored == "" || digests[id] != stored:
				plan.marks[ledger][id] = true
			case consistent:
			case comparable && recorded[stored] != "" && recorded[stored] == plan.results[stored]:
				// (A missing recorded or current result compares unequal.)
			default:
				plan.marks[ledger][id] = true
			}
		}
	}
	return plan, nil
}

// reconcileExecutionResults brings the catalog to this executable's rules in
// the writing transaction: it marks what planExecutionReconcile lists,
// replaces the recorded results with the current ones and records the rules,
// the encoding and the cursor, all at once. Interrupted, nothing of it is
// stored, and the next propagateExecutionChanges starts it again.
func reconcileExecutionResults(ctx context.Context, tx *sql.Tx, ledgers [][2]string, state executionPropagation) error {
	if _, err := tx.ExecContext(ctx, executionResultStampsDDL); err != nil {
		return err
	}
	names := make([]string, 0, len(ledgers))
	for _, ledger := range ledgers {
		names = append(names, ledger[0])
	}
	plan, err := planExecutionReconcile(ctx, tx, names, state)
	if err != nil {
		return err
	}
	for _, ledger := range ledgers {
		for _, id := range slices.Sorted(maps.Keys(plan.marks[ledger[0]])) {
			if err := markExecutionDependent(ctx, tx, ledger, id); err != nil {
				return err
			}
		}
	}
	recorded, err := storedExecutionResults(ctx, tx)
	if err != nil {
		return err
	}
	for _, digest := range slices.Sorted(maps.Keys(plan.results)) {
		if recorded[digest] == plan.results[digest] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO execution_result_stamps(digest,result) VALUES(?,?) ON CONFLICT(digest) DO UPDATE SET result=excluded.result`,
			digest, plan.results[digest]); err != nil {
			return err
		}
	}
	for _, digest := range slices.Sorted(maps.Keys(recorded)) {
		if _, ok := plan.results[digest]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM execution_result_stamps WHERE digest=?`, digest); err != nil {
			return err
		}
	}
	if executionReconcileHook != nil {
		if err := executionReconcileHook(); err != nil {
			return err
		}
	}
	versions := executionDependentsVersions()
	return recordExecutionPropagation(ctx, tx, ledgers, map[string]string{executionDependentsKey: strconv.FormatInt(state.generation, 10),
		executionDependentsVersionsKey: versions, executionResultStampsKey: versions, executionResultEncodingKey: executionResultEncoding})
}

// executionReconcileHook, when set by a test, runs inside a reconciliation
// before it records where it stands; an error interrupts it.
var executionReconcileHook func() error

// stampExecutionResults records, inside a publishing transaction, the
// resolved result of each component just published at (built: its digests),
// from what the publication's builder remembered (rememberBuilderResults).
// A digest without a remembered result gets none: at the next rules change
// its derivatives are marked rather than compared.
func stampExecutionResults(ctx context.Context, tx *sql.Tx, state executionPropagation, built map[string]bool) error {
	if len(built) == 0 || !state.stamping() {
		return nil
	}
	file, _, err := dependencyCacheKey(ctx, tx)
	if err != nil {
		return err
	}
	results, _ := rememberedExecutionResults(file, built)
	for _, digest := range slices.Sorted(maps.Keys(results)) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO execution_result_stamps(digest,result) VALUES(?,?) ON CONFLICT(digest) DO UPDATE SET result=excluded.result
		 WHERE result IS NOT excluded.result`, digest, results[digest]); err != nil {
			if strings.Contains(err.Error(), "no such table") {
				return nil
			}
			return err
		}
	}
	return nil
}

// rememberBuilderResults remembers, for a builder that read digests, r and o
// from the snapshot q, the results of the components that have none
// recorded: the ones a publication of this builder's output may be the first
// to rest on. It reads the recorded digests and hashes only the rest, so a
// pass after the first costs what changed.
func rememberBuilderResults(ctx context.Context, q queryer, digests map[string]string, r *executionResolution, o *occurrenceIndex) error {
	state, err := executionPropagationState(ctx, q)
	if err != nil || !state.stamping() || r == nil {
		return err
	}
	file, _, err := dependencyCacheKey(ctx, q)
	if err != nil || file == "" {
		return err
	}
	recorded, err := storedExecutionResults(ctx, q)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, digest := range digests {
		if _, ok := recorded[digest]; !ok {
			want[digest] = true
		}
	}
	_, missing := rememberedExecutionResults(file, want)
	rememberExecutionResults(file, executionComponentResults(digests, r, o, missing))
	return nil
}

// primeExecutionResults remembers, from a committed snapshot and the
// catalog's cached resolution of it, the results a reconciliation at state
// will read: those of every component a derivative rests on. The writer then
// finds them by digest, and marks a component it finds none for. This is the
// one place a rules change resolves the whole catalog, and only background
// work reaches it (propagateExecutionChanges). Nothing is read when the
// reconciliation compares nothing (state is neither consistent nor
// comparable: every derivative is marked).
func (c *Catalog) primeExecutionResults(ctx context.Context, state executionPropagation) error {
	if !state.consistent() && !state.comparable() {
		return nil
	}
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ledgers, err := executionLedgersPresent(ctx, tx)
	if err != nil || len(ledgers) == 0 {
		return err
	}
	names := make([]string, 0, len(ledgers))
	for _, ledger := range ledgers {
		names = append(names, ledger[0])
	}
	digests, err := executionDependencyDigests(ctx, committedReads{tx})
	if err != nil {
		return err
	}
	held, _, err := heldExecutionDigests(ctx, tx, names, digests)
	if err != nil {
		return err
	}
	file, _, err := dependencyCacheKey(ctx, tx)
	if err != nil || file == "" {
		return err
	}
	_, missing := rememberedExecutionResults(file, held)
	if len(missing) == 0 {
		return nil
	}
	if executionResultResolveHook != nil {
		executionResultResolveHook()
	}
	snapshot, err := c.executionSnapshotAt(ctx, tx)
	if err != nil {
		return err
	}
	occurrences, err := snapshot.catalogOccurrences(ctx, tx)
	if err != nil {
		return err
	}
	rememberExecutionResults(file, executionComponentResults(digests, snapshot.resolution, occurrences, missing))
	return nil
}

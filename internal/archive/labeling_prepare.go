package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// labelingTaxonomy is the frozen candidate-0.3 taxonomy, byte for byte as the
// prompt contract was measured with it. Preparation
// records its version and hash; prompts use it.
//
//go:embed labeling/taxonomy-candidate-0.3.json
var labelingTaxonomy []byte

const labelingTaxonomyVersion = "candidate-0.3"

// Files of a dataset directory. Only a complete preparation publishes the
// manifest; until then the marker says preparation is incomplete.
const (
	labelingDatasetFile    = "dataset.sqlite3"
	labelingManifestFile   = "manifest.json"
	labelingIncompleteFile = "prepare.incomplete.json"
	labelingLockFile       = ".lock"
)

type labelingPrepareOptions struct {
	Dir, CatalogPath string
	FreshDays, Limit int
	Conversations    []string
	DryRun           bool
	// SnapshotCopy reads a static copy (a backup) immutably; AllowStaleShm
	// accepts a non-empty -shm beside it. Live reads a live catalog in one
	// read transaction: tests only, the command never does.
	SnapshotCopy, AllowStaleShm, Live bool
	// SplitsFrom is an earlier dataset whose split assignments are kept;
	// splits holds them, loaded before a replacement removes that dataset.
	SplitsFrom string
	splits     map[string]string
	Progress   io.Writer
	// Now is the snapshot time; tests fix it.
	Now func() time.Time
}

// labelingRecord is one message as preparation reads it. Text is read only
// for kinds that can become units, and metadata that may record unavailable
// reasoning.
type labelingRecord struct {
	id, role, kind, createdAt, sender, format, contentHash, session, callID string
	selected                                                                bool
	text                                                                    sql.NullString
}

// labelingUnit is one prepared unit (labeling-units-v1: one per message).
type labelingUnit struct {
	id, messageID, conversation, workspace, cohort, actor, provider, role, kind string
	sender, channel, format, createdAt, contentHash, authorship                 string
	session, sessionRole, canonical, root, run, depthStatus, length             string
	depth                                                                       int64
	text, visible, context, contextPrompt                                       string
	contextExcerpt                                                              bool
	contextRefs                                                                 map[string]any
	pieces                                                                      []labelingPiece
	chunks, markers                                                             []labelingChunk
	// attachedChunks counts chunks holding only markers, attached to an item.
	attachedChunks int
	// hashes are the items' normalized target-text hashes.
	hashes []string
}

// labelingConversation is what preparation keeps of each conversation.
type labelingConversation struct {
	id, workspace, provider, started, parent string
	depth                                    int64
}

// labelingUnitStat is what the manifest's split coverage needs of a unit.
type labelingUnitStat struct {
	conversation, cohort, provider, depth, length string
	items                                         int
	// hashes are the items' normalized target-text hashes.
	hashes []string
}

// labelingPass holds the snapshot's shared inputs and the counts as
// conversations are read.
type labelingPass struct {
	tx             labelingReader
	writer         *labelingWriter
	reasoningStore bool
	// itemsOnly builds units and nothing else: no duplicate checks, actions,
	// statistics or exclusion rows. The labels ledger reads a live catalog
	// this way (labels_items.go); prepare keeps every record.
	itemsOnly bool
	// emit receives each unit once its context is set; excluded hears why a
	// record is not one (pharos labels explain).
	emit     func(*labelingUnit) error
	excluded func(messageID, reason, detail string)
	// loadAuthorship, when set, supplies classifications not in authorship:
	// the labels ledger reads them from the stored ledger on demand, where
	// prepare classifies the whole snapshot in memory first.
	loadAuthorship func(id string) (*authorResult, error)
	authorshipErr  error
	// targetCache holds resolved target bytes of re-sent sources.
	targetCache map[string][]bool
	// automated marks conversations with a user message from automation.
	automated map[string]bool
	// laterCopies maps fork records after the in-order copy that repeat a
	// parent message to it.
	laterCopies map[string]string
	// userTimeline holds each conversation's user messages in send order;
	// flattenedKeys, the current conversation's flattened question answers.
	userTimeline map[string][]labelingUserMessage
	// shortHashes are item hashes of texts under labelingShortChars.
	shortHashes   map[string]bool
	flattenedKeys map[string]bool
	// pending holds a conversation's actions until it is read; unparsedActor
	// is who would have answered each unparsed question-tool result.
	pending        []labelingPendingAction
	unparsedActor  map[string]string
	conversations  map[string]*labelingConversation
	representative map[string]string
	lineage        *usageConversationAttribution
	authorship     map[string]*authorResult
	mainSession    map[string]string
	sessionKind    map[string]string
	childOwners    map[string]bool
	current        map[string]bool
	// children holds a delegated child's messages (conversation "c:" or
	// embedded session "s:"), to find delegation duplicates.
	children map[string]*labelingChild
	checks   []labelingDuplicateCheck
	// parents caches a forked conversation's parent messages by role and text.
	parents map[string][]labelingParentMessage
	// units counts cohort, provider, depth, length: units, items, characters.
	units      map[[4]string]*[3]int
	exclusions map[[2]string]int
	counters   map[string]int
	stats      []labelingUnitStat
	firstAt    map[string]string
}

// labelingReader is a read connection or transaction the pass reads with.
type labelingReader interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// author is the classification of a user message: from the in-memory map,
// else from loadAuthorship (cached, including a miss). A load error is kept
// in authorshipErr and ends the conversation's pass.
func (p *labelingPass) author(id string) *authorResult {
	if result, ok := p.authorship[id]; ok || p.loadAuthorship == nil {
		return result
	}
	result, err := p.loadAuthorship(id)
	if err != nil {
		if p.authorshipErr == nil {
			p.authorshipErr = err
		}
		return nil
	}
	p.authorship[id] = result
	return result
}

// labelingDuplicateCheck compares a delegation, or its result, with the
// messages of the child its delegation message started. Texts are
// whitespace-normalized.
type labelingDuplicateCheck struct{ messageID, delegation, kind, payload, whole string }

// labelingChild is what duplicate detection keeps of a delegated child: its
// messages by normalized text, its first request and its last reply.
type labelingChild struct {
	texts           map[string]string
	first, last     string
	firstID, lastID string
}

type labelingParentMessage struct{ id, key, norm, createdAt string }

// itemTargetHash hashes an item's target text, without markers, lowercased
// with whitespace collapsed.
func itemTargetHash(visible string, chunk labelingChunk, markers []labelingChunk) (string, bool) {
	var target strings.Builder
	at := chunk.Start
	for _, marker := range markers {
		if marker.End <= at || marker.Start >= chunk.End {
			continue
		}
		target.WriteString(visible[at:max(at, marker.Start)])
		target.WriteByte(' ')
		at = min(chunk.End, marker.End)
	}
	target.WriteString(visible[at:chunk.End])
	normalized := strings.ToLower(normalizeSpace(target.String()))
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:16]), utf8.RuneCountInString(normalized) < labelingShortChars
}

// labelingShortChars is the length below which identical texts ("continue",
// "yes", "do it") recur by chance: their repeats are a stratum to report
// (common_short_text), not near-duplicates to drop from evaluation.
const labelingShortChars = 40

// normalizeSpace collapses whitespace runs to one space.
func normalizeSpace(text string) string { return strings.Join(strings.Fields(text), " ") }

// labelingDepthBucket groups resolved delegation depths.
func labelingDepthBucket(depth int64, status string) string {
	switch {
	case strings.HasPrefix(status, "unresolved"):
		return "unresolved"
	case depth >= 2:
		return "2+"
	}
	return strconv.FormatInt(depth, 10)
}

func labelingProgress(w io.Writer, started time.Time) func(string, ...any) {
	return func(format string, args ...any) {
		if w != nil {
			fmt.Fprintf(w, "[%7.1fs] %s\n", time.Since(started).Seconds(), fmt.Sprintf(format, args...))
		}
	}
}

func labelingMeta(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var value string
	err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

// labelingBuild identifies the build that prepared a dataset.
func labelingBuild() map[string]any {
	build := map[string]any{}
	if info, ok := debug.ReadBuildInfo(); ok {
		build["go"] = info.GoVersion
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" || setting.Key == "vcs.modified" || setting.Key == "vcs.time" {
				build[setting.Key] = setting.Value
			}
		}
	}
	if path, err := os.Executable(); err == nil {
		if data, err := os.ReadFile(path); err == nil {
			build["executable_sha256"] = hashBytes(data)
		}
	}
	return build
}

func labelingID(prefix string, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return prefix + hex.EncodeToString(sum[:10])
}

func textKey(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:])
}

// delegationPayload is the prompt a delegation sent or the content its
// result returned, from the stored {"tool","input"|"content"} envelope. It
// is compared with the child's own messages to record duplication.
func delegationPayload(text string) string {
	var envelope map[string]any
	if json.Unmarshal([]byte(text), &envelope) != nil {
		return text
	}
	if input := mapValue(envelope["input"]); input != nil {
		return firstString(input["prompt"], input["message"], input["task"], input["instructions"], input["description"])
	}
	switch content := envelope["content"].(type) {
	case string:
		return content
	case []any:
		parts := []string{}
		for _, part := range content {
			if text := firstString(mapValue(part)["text"]); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return text
}

// delegationString is the prompt a delegation sent, or the content its
// result returned, when it is one JSON string in the stored envelope.
func delegationString(text string) (string, bool) {
	var envelope map[string]any
	if json.Unmarshal([]byte(text), &envelope) != nil {
		return "", false
	}
	if input := mapValue(envelope["input"]); input != nil {
		for _, key := range []string{"prompt", "message", "task", "instructions"} {
			if value, ok := input[key].(string); ok && strings.TrimSpace(value) != "" {
				return value, true
			}
		}
		return "", false
	}
	switch content := envelope["content"].(type) {
	case string:
		return content, strings.TrimSpace(content) != ""
	case []any:
		if len(content) == 1 {
			value, ok := mapValue(content[0])["text"].(string)
			return value, ok && strings.TrimSpace(value) != ""
		}
	}
	return "", false
}

// delegationPieces makes a delegation's prompt (or a result's content) the
// target, as a json-string-v1 piece over its encoded bytes in the stored
// envelope, shown decoded; the envelope around it is dropped silently. It
// reports false when the payload is not one string found in the text.
func delegationPieces(text, status string) ([]labelingPiece, bool) {
	payload, ok := delegationString(text)
	if !ok {
		return nil, false
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, false
	}
	at := strings.Index(text, string(encoded))
	if at < 0 {
		return nil, false
	}
	start, end := at+1, at+len(encoded)-1
	return []labelingPiece{
		{Role: pieceDropped, Status: "envelope", Category: "envelope", Start: 0, End: start},
		{Role: pieceTarget, Status: status, Start: start, End: end, Transform: feedbackJSONTransform},
		{Role: pieceDropped, Status: "envelope", Category: "envelope", Start: end, End: len(text)},
	}, true
}

// labelingCandidate is the cohort family a record could join, or "" for a
// kind or role that never becomes a unit.
func labelingCandidate(record labelingRecord) string {
	switch {
	case record.kind == "message" && record.role == "user":
		return "user"
	case record.kind == "message" && (record.role == "assistant" || record.role == "agent"):
		return "agent_reply"
	case record.kind == "delegation" || record.kind == "delegation_result":
		return "delegation"
	case record.kind == "reasoning" || record.kind == "thinking" || record.kind == "analysis":
		return "thinking"
	}
	return ""
}

// readLabelingRecords reads one conversation's messages in transcript order,
// with the embedded agent session each belongs to.
func readLabelingRecords(ctx context.Context, tx labelingReader, conversation string) ([]labelingRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT m.id,m.role,m.kind,m.selected,COALESCE(m.created_at,''),COALESCE(m.sender,''),COALESCE(m.reasoning_format,''),m.content_hash,COALESCE(m.call_id,''),
		CASE WHEN m.kind IN ('message','delegation','delegation_result','reasoning','thinking','analysis')
			OR (m.kind='metadata' AND substr(m.text,1,10)='{"reason":') THEN m.text END,
		COALESCE((SELECT MIN(agent_session_id) FROM agent_session_messages WHERE message_id=m.id),'')
		FROM messages m WHERE m.conversation_id=? ORDER BY m.source_order IS NULL,m.source_order,m.created_at,m.id`, conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []labelingRecord{}
	for rows.Next() {
		var record labelingRecord
		if err := rows.Scan(&record.id, &record.role, &record.kind, &record.selected, &record.createdAt, &record.sender, &record.format, &record.contentHash,
			&record.callID, &record.text, &record.session); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// prepareLabeling builds a dataset from one snapshot of the catalog: units
// for the five cohorts with their pieces, items, context, actions,
// exclusions, families and splits. A dry run only counts. It returns the
// manifest; the caller publishes it.
func prepareLabeling(ctx context.Context, opts labelingPrepareOptions) (map[string]any, error) {
	started := time.Now()
	if opts.Now != nil {
		started = opts.Now()
	}
	progress := labelingProgress(opts.Progress, time.Now())
	if opts.SnapshotCopy == opts.Live {
		return nil, errors.New("prepare reads either a snapshot copy or, explicitly, a live catalog")
	}
	db, tx, access, err := openLabelingSnapshot(ctx, opts.CatalogPath, opts.SnapshotCopy, opts.AllowStaleShm)
	if err != nil {
		return nil, fmt.Errorf("open catalog read-only: %w", err)
	}
	defer db.Close()
	defer tx.Rollback()
	snapshot := map[string]any{"started_at": formatTime(started), "catalog": opts.CatalogPath, "access": access}
	version, err := labelingMeta(ctx, tx, "schema_version")
	if err != nil {
		return nil, err
	}
	schema, err := strconv.Atoi(strings.TrimSpace(version))
	if err != nil || schema > catalogSchemaVersion || schema < labelingMinSchema {
		return nil, fmt.Errorf("catalog %s has schema version %q; labeling reads schemas %d to %d (the catalog was not modified)",
			opts.CatalogPath, version, labelingMinSchema, catalogSchemaVersion)
	}
	snapshot["schema_version"] = schema
	coverage, present, err := labelingCompatibility(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("catalog %s: %w", opts.CatalogPath, err)
	}
	if !present["reasoning_extraction_state"] {
		coverage = append(coverage, fmt.Sprintf("thinking: unavailable from this snapshot; catalog schema %d has no normalized reasoning store (reasoning_extraction_state), so retained thinking is excluded as reasoning_store_absent", schema))
	}
	var dataVersion int64
	if err := tx.QueryRowContext(ctx, "PRAGMA data_version").Scan(&dataVersion); err != nil {
		return nil, err
	}
	snapshot["data_version"] = dataVersion
	generations := map[string]string{}
	for _, key := range []string{"tool_ledger_generation", "authorship_inputs_generation", "authorship_generation", "reasoning_extraction_generation"} {
		if generations[key], err = labelingMeta(ctx, tx, key); err != nil {
			return nil, err
		}
		snapshot[key] = generations[key]
	}
	snapshot["build"] = labelingBuild()

	progress("reading works, identity links and lineage")
	p := newLabelingPass(tx, present["reasoning_extraction_state"])
	ids, kept, sessions, unavailableLedger, err := loadLabelingPass(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	delegated := map[string][]string{} // delegation message: its child's owner keys
	for _, row := range sessions {
		if message := firstString(row["delegation"]); message != "" {
			owner := "s:" + firstString(row["id"])
			if child := firstString(row["child"]); child != "" {
				owner = "c:" + child
			}
			delegated[message] = append(delegated[message], owner)
		}
	}

	scope, selection, err := labelingScope(ids, p, opts)
	if err != nil {
		return nil, err
	}

	// Human contribution: the existing classifier, in memory, on this snapshot.
	progress("reading authorship inputs")
	reported := time.Time{}
	workCtx := withWorkProgress(ctx, func(work activityWork) {
		if time.Since(reported) > 15*time.Second || work.TotalKnown && work.Done == work.Total && work.Total > 0 {
			reported = time.Now()
			progress("%s: %.0f/%.0f", work.Phase, work.Done, work.Total)
		}
	})
	inputs, launches, err := (&Catalog{Path: opts.CatalogPath, DB: db}).authorshipInputsFrom(workCtx, tx)
	if err != nil {
		return nil, fmt.Errorf("authorship inputs: %w", err)
	}
	// Lineage uses the launches derived on this snapshot, not the stored
	// ledger, which may predate the newest headless runs.
	derived := make([]map[string]any, 0, len(launches))
	for _, launch := range launches {
		derived = append(derived, map[string]any{"child_id": launch.child, "parent_id": launch.parent})
	}
	if p.lineage, err = usageConversationRootsWith(tx, kept, derived); err != nil {
		return nil, err
	}
	// A conversation with a parent is a sub-agent to authorship, but a
	// continuation (a Codex filename fork) is the root's own execution: its
	// new messages are the person's. Here only, in memory.
	for index := range inputs {
		id := inputs[index].ConversationID
		if inputs[index].SubAgent && p.lineage.roots[id] != nil && p.lineage.runs[id] == "" {
			inputs[index].SubAgent = false
			p.counters["continuation_messages"]++
		}
	}
	results, err := classifyInputsFrom(workCtx, tx, inputs)
	if err != nil {
		return nil, fmt.Errorf("authorship classification: %w", err)
	}
	p.authorship = make(map[string]*authorResult, len(results))
	for index := range results {
		p.authorship[results[index].Input.ID] = &results[index]
		automated := len(results[index].Spans) > 0 && results[index].Spans[0].Category == spanAutomated
		if automated {
			p.automated[results[index].Input.ConversationID] = true
		}
		// Results are in send order, so each timeline is sorted.
		conversation := results[index].Input.ConversationID
		p.userTimeline[conversation] = append(p.userTimeline[conversation], labelingUserMessage{formatTime(results[index].Input.SentAt), automated})
	}
	want := generations["tool_ledger_generation"] + ":" + generations["authorship_inputs_generation"] + "/" + authorshipVersion

	if !opts.DryRun {
		if p.writer, err = createLabelingDataset(filepath.Join(opts.Dir, labelingDatasetFile)); err != nil {
			return nil, err
		}
		defer p.writer.close()
	}
	progress("reading %d conversations", len(scope))
	for index, id := range scope {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := p.conversation(ctx, id); err != nil {
			return nil, err
		}
		if p.writer != nil && (index+1)%500 == 0 {
			if err := p.writer.commit(); err != nil {
				return nil, err
			}
		}
		if (index+1)%2000 == 0 || index+1 == len(scope) {
			progress("conversations %d/%d, units %d", index+1, len(scope), len(p.stats))
		}
	}
	// A restricted scope may hold a delegation without the child it started:
	// read that child's messages too, so duplication is found either way.
	read := map[string]bool{}
	for _, id := range scope {
		read[id] = true
	}
	for _, check := range p.checks {
		for _, owner := range delegated[check.delegation] {
			child, ok := strings.CutPrefix(owner, "c:")
			if !ok || read[child] || p.conversations[child] == nil {
				continue
			}
			read[child] = true
			records, err := readLabelingRecords(ctx, tx, child)
			if err != nil {
				return nil, err
			}
			for _, record := range records {
				p.rememberChildText(child, record)
			}
		}
	}
	// An immutable read is only sound if the copy did not change under it.
	if opts.SnapshotCopy {
		if err := labelingCopyStable(opts.CatalogPath, access); err != nil {
			return nil, err
		}
	}
	snapshot["ended_at"] = now()
	tx.Rollback()
	db.Close()

	// A delegation or its result that repeats the child's own message is
	// kept, with the duplication recorded rather than counted twice.
	duplicates := map[string]map[string]int{"delegation": {}, "delegation_result": {}}
	for _, check := range p.checks {
		duplicates[check.kind]["units"]++
		if len(delegated[check.delegation]) > 0 {
			duplicates[check.kind]["linked"]++
		}
		for _, owner := range delegated[check.delegation] {
			child := p.children[owner]
			if child == nil {
				continue
			}
			match, how := matchDelegation(check, child)
			if match == "" {
				continue
			}
			duplicates[check.kind][how]++
			if p.writer != nil {
				if err := p.writer.duplicate(check.messageID, match, check.kind+"_"+how+"_child_message"); err != nil {
					return nil, err
				}
			}
			break
		}
	}

	progress("assigning families and splits")
	// A copy's data ends when it was taken, so its fresh window does too.
	asOf := started
	if copied, ok := parseTime(firstString(access["modified_at"])); ok && opts.Now == nil {
		asOf = copied
	}
	freshSince := formatTime(asOf.Add(-time.Duration(opts.FreshDays) * 24 * time.Hour))
	if opts.SplitsFrom != "" && opts.splits == nil {
		if opts.splits, err = loadLabelingSplits(opts.SplitsFrom); err != nil {
			return nil, fmt.Errorf("--splits-from: %w", err)
		}
	}
	families, inheritance := labelingFamilies(ids, p, sessions, launches, results, freshSince, opts.splits)
	unavailable := int64(0)
	for _, id := range scope {
		unavailable += unavailableLedger[id]
	}
	manifest := p.manifest(opts, snapshot, selection, scope, families, duplicates)
	splits := manifest["splits"].(map[string]any)
	splits["inherited_from"], splits["assignment"] = nilIfEmpty(opts.SplitsFrom), inheritance
	// Identical target texts never link families (a stock reply would chain
	// unrelated work). An item whose text recurs in another split is marked;
	// a held-out item whose text also occurs in train is seen_in_train, which
	// evaluation excludes (near-duplicate filtering).
	splitsOf := map[string]map[string]bool{}
	for _, stat := range p.stats {
		for _, hash := range stat.hashes {
			if splitsOf[hash] == nil {
				splitsOf[hash] = map[string]bool{}
			}
			splitsOf[hash][families[stat.conversation].split] = true
		}
	}
	crossSplit, seenInTrain := map[string]bool{}, map[string]bool{}
	duplicateCounts := map[string]int{}
	for _, stat := range p.stats {
		split := families[stat.conversation].split
		for _, hash := range stat.hashes {
			if len(splitsOf[hash]) < 2 {
				continue
			}
			crossSplit[hash] = true
			duplicateCounts["items"]++
			switch {
			case p.shortHashes[hash]:
				duplicateCounts["common_short_text"]++
			case split != "train" && splitsOf[hash]["train"]:
				seenInTrain[hash] = true
				duplicateCounts["seen_in_train_"+split]++
			}
		}
	}
	splits["cross_split_duplicates"] = map[string]any{"texts": len(crossSplit), "items": duplicateCounts["items"],
		"seen_in_train": map[string]int{"dev": duplicateCounts["seen_in_train_dev"], "test": duplicateCounts["seen_in_train_test"],
			"fresh": duplicateCounts["seen_in_train_fresh"], "quarantine": duplicateCounts["seen_in_train_quarantine"]},
		"common_short_text": duplicateCounts["common_short_text"], "short_chars": labelingShortChars,
		"note": "items whose normalized target text occurs in more than one split are cross_split_duplicate. Held-out items whose text (at least short_chars characters) also occurs in train are seen_in_train, which evaluation should exclude; shorter repeated texts are common_short_text, a stratum to report separately, not to drop"}
	manifest["coverage"] = coverage
	manifest["reasoning"] = map[string]any{
		"extraction_version": reasoningExtractionVersion, "pending_conversations": p.counters["reasoning_pending_conversations"],
		"unavailable_records_ledger": unavailable, "unavailable_records_seen": p.exclusions[[2]string{"reasoning_unavailable", ""}],
		"note": "Thinking in conversations whose extraction is pending is excluded; unavailable (redacted or encrypted) reasoning is never inferred.",
	}
	manifest["authorship"] = map[string]any{
		"version": authorshipVersion, "computed": "in memory on this snapshot (authorshipInputs + classifyInputs); the stored ledger was not read or written",
		"classified_messages": len(results), "missing": p.exclusionTotal("authorship_missing"),
		"stored_generation": generations["authorship_generation"], "snapshot_generation": want, "stored_ledger_current": want == generations["authorship_generation"],
		"question_form_actions": p.counters["actions_flattened_message"],
	}
	if p.writer != nil {
		if err := p.writer.finish(families, crossSplit, seenInTrain, p.shortHashes, manifest); err != nil {
			return nil, err
		}
		p.writer = nil
	}
	progress("prepared %d units (%d items)", len(p.stats), manifest["items"])
	return manifest, nil
}

// newLabelingPass is an empty pass reading through tx.
func newLabelingPass(tx labelingReader, reasoningStore bool) *labelingPass {
	return &labelingPass{tx: tx, reasoningStore: reasoningStore, targetCache: map[string][]bool{}, automated: map[string]bool{}, unparsedActor: map[string]string{},
		laterCopies: map[string]string{}, userTimeline: map[string][]labelingUserMessage{}, shortHashes: map[string]bool{}, conversations: map[string]*labelingConversation{},
		mainSession: map[string]string{}, sessionKind: map[string]string{}, childOwners: map[string]bool{}, current: map[string]bool{}, children: map[string]*labelingChild{},
		parents: map[string][]labelingParentMessage{}, units: map[[4]string]*[3]int{}, exclusions: map[[2]string]int{}, counters: map[string]int{}, firstAt: map[string]string{},
		authorship: map[string]*authorResult{}, lineage: &usageConversationAttribution{}}
}

// loadLabelingPass reads what every conversation's pass needs from the whole
// catalog: which works are mirrors (counted once, through their
// representative), the conversations, the agent sessions (their kinds, each
// conversation's main session, the children delegations started) and which
// conversations' reasoning extraction is current. Prepare and the labels
// ledger both start here, so they decide units alike.
func loadLabelingPass(ctx context.Context, q labelingReader, p *labelingPass) (ids []string, kept, sessions []map[string]any, unavailable map[string]int64, err error) {
	workspaces, err := queryMapsContext(ctx, q, `SELECT w.id,w.source_kind,w.activity_at FROM workspaces w`)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	links, err := queryMapsContext(ctx, q, identityLinkWorkspacesSQL)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	p.representative = mirrorRepresentatives(workspaces, links)
	for _, row := range workspaces {
		if p.representative[firstString(row["id"])] == "" {
			kept = append(kept, map[string]any{"workspace_id": row["id"]})
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT id,workspace_id,provider,COALESCE(started_at,''),COALESCE(parent_id,''),agent_depth FROM conversations ORDER BY id`)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for rows.Next() {
		info := &labelingConversation{}
		if err := rows.Scan(&info.id, &info.workspace, &info.provider, &info.started, &info.parent, &info.depth); err != nil {
			rows.Close()
			return nil, nil, nil, nil, err
		}
		p.conversations[info.id] = info
		ids = append(ids, info.id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, nil, nil, err
	}
	sessions, err = queryMapsContext(ctx, q, `SELECT id,conversation_id,native_id,kind,COALESCE(child_conversation_id,'') child,COALESCE(delegation_message_id,'') delegation FROM agent_sessions`)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for _, row := range sessions {
		id := firstString(row["id"])
		p.sessionKind[id] = firstString(row["kind"])
		if row["native_id"] == "main" {
			p.mainSession[firstString(row["conversation_id"])] = id
		}
		if message := firstString(row["delegation"]); message != "" {
			owner := "s:" + id
			if child := firstString(row["child"]); child != "" {
				owner = "c:" + child
			}
			p.childOwners[owner] = true
		}
	}
	unavailable = map[string]int64{}
	if p.reasoningStore {
		reasoning, err := queryMapsContext(ctx, q, `SELECT conversation_id,unavailable_count FROM reasoning_extraction_state WHERE version=?`, reasoningExtractionVersion)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		for _, row := range reasoning {
			p.current[firstString(row["conversation_id"])] = true
			unavailable[firstString(row["conversation_id"])] = integer(row["unavailable_count"])
		}
	}
	return ids, kept, sessions, unavailable, nil
}

// labelingScope chooses the conversations to prepare: all of them, the named
// ones, or a deterministic sample of those whose work is not a mirror.
func labelingScope(ids []string, p *labelingPass, opts labelingPrepareOptions) ([]string, map[string]any, error) {
	switch {
	case len(opts.Conversations) > 0:
		scope := []string{}
		for _, id := range opts.Conversations {
			if p.conversations[id] == nil {
				return nil, nil, fmt.Errorf("unknown conversation %s", id)
			}
			scope = append(scope, id)
		}
		sort.Strings(scope)
		scope = slices.Compact(scope)
		return scope, map[string]any{"restricted": true, "method": "conversations named with --conversation", "conversations": scope}, nil
	case opts.Limit > 0:
		candidates := []string{}
		for _, id := range ids {
			if p.representative[p.conversations[id].workspace] == "" {
				candidates = append(candidates, id)
			}
		}
		sort.Slice(candidates, func(i, j int) bool { return textKey(candidates[i]) < textKey(candidates[j]) })
		scope := candidates[:min(opts.Limit, len(candidates))]
		sort.Strings(scope)
		return scope, map[string]any{"restricted": true, "limit": opts.Limit,
			"method": "the first N conversations, in SHA-256 order of their IDs, of works that are not mirrors"}, nil
	}
	return ids, map[string]any{"restricted": false, "method": "every conversation in the catalog"}, nil
}

func (p *labelingPass) exclude(reason, cohort string, count int) {
	p.exclusions[[2]string{reason, cohort}] += count
}

// prefixed returns the counters whose names start with prefix, without it.
func (p *labelingPass) prefixed(prefix string) map[string]int {
	found := map[string]int{}
	for key, count := range p.counters {
		if name, ok := strings.CutPrefix(key, prefix); ok {
			found[name] = count
		}
	}
	return found
}

func (p *labelingPass) exclusionTotal(reason string) int {
	total := 0
	for key, count := range p.exclusions {
		if key[0] == reason {
			total += count
		}
	}
	return total
}

// conversation prepares one conversation's records in transcript order.
func (p *labelingPass) conversation(ctx context.Context, id string) error {
	info := p.conversations[id]
	if p.representative[info.workspace] != "" {
		var count int
		if err := p.tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE conversation_id=?", id).Scan(&count); err != nil {
			return err
		}
		p.exclude("mirrored_work", "", count)
		p.counters["mirrored_conversations"]++
		return nil
	}
	if !p.current[id] {
		p.counters["reasoning_pending_conversations"]++
	}
	answers, err := p.toolResultAnswers(ctx, id)
	if err != nil {
		return err
	}
	records, err := readLabelingRecords(ctx, p.tx, id)
	if err != nil {
		return err
	}
	p.flattenedKeys = map[string]bool{}
	for _, record := range records {
		// Only user messages are classified.
		if record.role != "user" || !record.selected {
			continue
		}
		if result := p.author(record.id); result != nil {
			for _, answer := range result.Answers {
				p.flattenedKeys[answer.Origin.message+"\x00"+answer.Title] = true
			}
		}
	}
	inherited, copied, err := p.inheritedHistory(ctx, info, records)
	if err != nil {
		return err
	}
	// context-v2, per agent session (the main session, or each embedded
	// sub-agent session): the previous request unit's visible text (a
	// person's or an agent's) and the previous selected agent reply, even an
	// excluded one.
	type prior struct {
		user, assistant       string
		userRef, assistantRef map[string]any
	}
	priors := map[string]*prior{}
	calls := map[string]string{} // call ID: the delegation message that made it
	for _, record := range records {
		if record.kind == "delegation" && record.callID != "" {
			calls[record.callID] = record.id
		}
		if record.createdAt != "" && (p.firstAt[id] == "" || record.createdAt < p.firstAt[id]) {
			p.firstAt[id] = record.createdAt
		}
		session := ""
		if p.sessionKind[record.session] == "subagent" {
			session = record.session
		}
		state := priors[session]
		if state == nil {
			state = &prior{}
			priors[session] = state
		}
		var unit *labelingUnit
		reason, detail, duplicate := "", "", ""
		if copied[record.id] {
			reason, detail, duplicate = "inherited_history", "copied from the parent conversation before the fork", inherited[record.id]
		} else {
			if !p.itemsOnly {
				p.rememberChildText(id, record)
			}
			if unit, reason, detail, err = p.record(id, record, answers, calls); err != nil {
				return err
			}
		}
		reply := record.selected && record.kind == "message" && (record.role == "assistant" || record.role == "agent")
		if unit == nil {
			// Per-record exclusions are kept for kinds that could have been
			// units; the rest (tool calls and results, metadata) are counted.
			candidate := labelingCandidate(record)
			p.exclude(reason, candidate, 1)
			if p.excluded != nil {
				p.excluded(record.id, reason, detail)
			}
			if p.writer != nil && candidate != "" {
				if err := p.writer.exclusion(record.id, id, candidate, reason, detail, duplicate); err != nil {
					return err
				}
			}
			if reply {
				state.assistant, state.assistantRef = record.text.String, map[string]any{"message": record.id}
				if !utf8.ValidString(state.assistant) {
					state.assistant, state.assistantRef = "", nil
				}
			}
			// A copied request is context for what follows it, though not a unit.
			if copied[record.id] && record.kind == "message" && record.role == "user" {
				if visible := p.requestVisible(record); visible != "" {
					state.user, state.userRef = visible, map[string]any{"message": record.id, "inherited_from": inherited[record.id]}
				}
			}
			continue
		}
		unit.context, unit.contextPrompt, unit.contextExcerpt = labelingContext(state.user, state.assistant)
		unit.contextRefs = map[string]any{}
		if state.user != "" {
			unit.contextRefs["prior_user"] = state.userRef
		}
		if state.assistant != "" {
			unit.contextRefs["prior_assistant"] = state.assistantRef
		}
		if !p.itemsOnly {
			p.addUnit(unit)
		}
		if p.emit != nil {
			if err := p.emit(unit); err != nil {
				return err
			}
		}
		if p.writer != nil {
			if err := p.writer.unit(unit); err != nil {
				return err
			}
			if parent := p.laterCopies[record.id]; parent != "" {
				if err := p.writer.duplicate(record.id, parent, "fork_copy_after_difference"); err != nil {
					return err
				}
			}
		}
		if unit.cohort == "human" || unit.cohort == "agent_in_user_role" {
			state.user, state.userRef = unit.visible, map[string]any{"unit": unit.id, "message": record.id}
		}
		if reply {
			state.assistant, state.assistantRef = record.text.String, map[string]any{"message": record.id}
		}
	}
	if p.authorshipErr != nil {
		return fmt.Errorf("authorship of conversation %s: %w", id, p.authorshipErr)
	}
	return p.flushActions()
}

// attachMarkerChunks folds each chunk that holds only markers into the item
// before it, or, at the start, into the one after it: every item holds target
// text, the teacher still sees the marker (a quote the person reacts to), and
// items concatenate to the visible text exactly. An attached marker can take
// an item past the chunk limit by its own length. It returns how many chunks
// were attached.
func attachMarkerChunks(chunks []labelingChunk, pieces []labelingPiece) ([]labelingChunk, int) {
	result, attached, leading := []labelingChunk{}, 0, -1
	for _, chunk := range chunks {
		if len(chunkSourceRanges(pieces, chunk)) == 0 {
			attached++
			if len(result) > 0 {
				result[len(result)-1].End = chunk.End
			} else if leading < 0 {
				leading = chunk.Start
			}
			continue
		}
		if leading >= 0 {
			chunk.Start, leading = leading, -1
		}
		result = append(result, chunk)
	}
	return result, attached
}

// requestVisible is the visible text a user-role record would have as a
// unit, without recording anything: context for the records after a copy.
func (p *labelingPass) requestVisible(record labelingRecord) string {
	result := p.author(record.id)
	if result == nil || len(result.Spans) == 0 || !utf8.ValidString(record.text.String) {
		return ""
	}
	var unit humanUnit
	if result.Spans[0].Category == spanAutomated || p.sessionKind[record.session] == "subagent" {
		unit = agentUserPieces(record.text.String)
	} else {
		unit = humanPieces(record.text.String, result.Spans, p.reusePieces)
	}
	if unit.Exclude != "" {
		return ""
	}
	return unit.Visible
}

// inheritedHistory finds the records a forked conversation copied from its
// parent. Copies keep their original send times, so when the fork's start
// and the records' times are known, every selected record sent before the
// start is a copy and none sent at or after it is; each copied text record
// is matched to the parent record it repeats (same role, same text), in
// order where possible. Without times it walks the child's selected text
// records alongside the parent's, in order, and stops at the first
// difference. It returns the parent message each copy repeats and the
// records that belong to the copy.
func (p *labelingPass) inheritedHistory(ctx context.Context, info *labelingConversation, records []labelingRecord) (map[string]string, map[string]bool, error) {
	inherited, copied := map[string]string{}, map[string]bool{}
	if info.parent == "" || p.conversations[info.parent] == nil {
		return inherited, copied, nil
	}
	history, cached := p.parents[info.parent]
	if !cached {
		parentRecords, err := readLabelingRecords(ctx, p.tx, info.parent)
		if err != nil {
			return nil, nil, err
		}
		for _, record := range parentRecords {
			if record.selected && record.text.Valid && strings.TrimSpace(record.text.String) != "" {
				history = append(history, labelingParentMessage{record.id, record.role + "\x00" + textKey(record.text.String),
					record.role + "\x00" + textKey(normalizeSpace(record.text.String)), record.createdAt})
			}
		}
		p.parents[info.parent] = history
	}
	text := func(record labelingRecord) bool {
		return record.selected && record.text.Valid && strings.TrimSpace(record.text.String) != ""
	}
	p.counters["child_conversations"]++
	// Evidence for the rule below: how the times of an in-order copy relate
	// to the parent's and to the child's start.
	for next, index := 0, 0; index < len(records); index++ {
		record := records[index]
		if !text(record) {
			continue
		}
		if next >= len(history) || history[next].key != record.role+"\x00"+textKey(record.text.String) {
			break
		}
		switch {
		case record.createdAt == history[next].createdAt:
			p.counters["fork_copy_time:same_as_parent"]++
		case record.createdAt < history[next].createdAt:
			p.counters["fork_copy_time:before_parent"]++
		default:
			p.counters["fork_copy_time:after_parent"]++
		}
		if info.started != "" && record.createdAt < info.started {
			p.counters["fork_copy_time:before_child_start"]++
		} else {
			p.counters["fork_copy_time:at_or_after_child_start"]++
		}
		next++
	}
	inOrder := func() {
		next, last := 0, -1
		for index, record := range records {
			if !text(record) {
				continue
			}
			if next >= len(history) || history[next].key != record.role+"\x00"+textKey(record.text.String) {
				break
			}
			inherited[record.id] = history[next].id
			next++
			last = index
		}
		// The copy is every selected record up to its last matched text.
		for index, record := range records[:last+1] {
			if record.selected {
				copied[records[index].id] = true
			}
		}
		// A copy can differ from the parent early on (a regenerated
		// instruction message), ending the walk before the copy ends. Later
		// records that repeat a parent message from before the fork, not yet
		// matched, are kept as units but marked duplicate_of it, so plan
		// skips them and training does not count them twice.
		used := map[int]bool{}
		for index := 0; index < next; index++ {
			used[index] = true
		}
		// Short texts ("Done.", "continue") repeat by chance, in sub-agents
		// that copied nothing; only longer ones are taken for copies.
		for _, record := range records[last+1:] {
			if !text(record) || utf8.RuneCountInString(normalizeSpace(record.text.String)) < labelingShortChars {
				continue
			}
			norm := record.role + "\x00" + textKey(normalizeSpace(record.text.String))
			for index, entry := range history {
				if used[index] || entry.norm != norm || info.started != "" && entry.createdAt != "" && entry.createdAt >= info.started {
					continue
				}
				used[index] = true
				p.laterCopies[record.id] = entry.id
				p.counters["fork_copies_after_difference"]++
				p.counters["copies_after_difference:"+info.provider]++
				break
			}
		}
	}
	timed := info.started != ""
	for _, record := range records {
		timed = timed && (!record.selected || record.createdAt != "")
	}
	if !timed {
		p.counters["forks_without_times"]++
		inOrder()
		return inherited, copied, nil
	}
	next, unmatched := 0, 0
	for _, record := range records {
		if !record.selected || record.createdAt >= info.started {
			continue
		}
		copied[record.id] = true
		p.counters["records_before_child_start:"+info.provider]++
		if !text(record) {
			continue
		}
		key := record.role + "\x00" + textKey(record.text.String)
		match := -1
		for index := next; index < len(history) && match < 0; index++ {
			if history[index].key == key {
				match = index
			}
		}
		for index := 0; index < len(history) && match < 0; index++ {
			if history[index].key == key {
				match = index
			}
		}
		if match < 0 {
			unmatched++
			continue
		}
		inherited[record.id] = history[match].id
		next = match + 1
	}
	// Records before the start that repeat none of the parent's are clock
	// skew, not a copy: such a catalog stamps copies when it forks, so only
	// the in-order walk can find them.
	if len(inherited) == 0 {
		inherited, copied = map[string]string{}, map[string]bool{}
		p.counters["forks_restamped"]++
		inOrder()
		return inherited, copied, nil
	}
	p.counters["forks_with_original_times"]++
	if unmatched > 0 {
		p.counters["forks_with_unmatched_copies"]++
		p.counters["fork_unmatched_copies"] += unmatched
	}
	return inherited, copied, nil
}

// labelingAnswer is a structured answer to a question an agent asked: a
// prewritten option submitted, or a free-text answer.
type labelingAnswer struct {
	kind, status, source, question, option, text, actor string
	titleStart, titleEnd, start, end                    int
	questionConversation, questionMessage, questionCall string
	// evidence names what the actor rests on.
	evidence string
}

// toolResultAnswers reads the answers to question tools whose results this
// conversation recorded, by result message. A result that cannot be parsed
// maps to nil: a counted coverage gap.
func (p *labelingPass) toolResultAnswers(ctx context.Context, conversation string) (map[string][]labelingAnswer, error) {
	answers := map[string][]labelingAnswer{}
	calls, err := queryMapsContext(ctx, p.tx, `SELECT t.tool_name,t.result_message_id,COALESCE(t.call_message_id,'') call_message_id,COALESCE(t.call_id,t.id) call_id,
		COALESCE(c.text,'') call_text,COALESCE(r.text,'') result_text,COALESCE(r.created_at,'') result_at FROM tool_calls t
		LEFT JOIN messages c ON c.id=t.call_message_id LEFT JOIN messages r ON r.id=t.result_message_id
		WHERE t.conversation_id=? AND t.result_message_id IS NOT NULL AND (t.tool_name LIKE '%question%' OR t.tool_name LIKE '%user_input%')`, conversation)
	if err != nil {
		return nil, err
	}
	for _, call := range calls {
		switch authorshipToolName(firstString(call["tool_name"])) {
		case "request_user_input", "request_user_input_async", "askuserquestion", "ask_question":
			result := firstString(call["result_message_id"])
			parsed := parseQuestionAnswers(asString(call["call_text"]), asString(call["result_text"]))
			// Only an interactive session's answer to a question asked in it
			// is credited to a person; automated, headless and delegated runs
			// have no person to answer. The session's state is judged at the
			// answer's time, and the evidence is recorded for consumers.
			interactive, evidence := p.interactiveAt(conversation, firstString(call["result_at"]))
			for index := range parsed {
				parsed[index].questionCall, parsed[index].evidence = firstString(call["call_id"]), evidence
				if !interactive {
					parsed[index].actor, parsed[index].status = "unknown", "tool_result_answer_unverified"
				}
			}
			if len(parsed) == 0 {
				p.counters["unparsed_answer_shape:"+questionAnswerShape(asString(call["call_text"]), asString(call["result_text"]))]++
				actor := "unknown"
				if interactive {
					actor = "human"
				}
				p.unparsedActor[firstString(call["result_message_id"])] = actor
			}
			for index := range parsed {
				parsed[index].questionConversation, parsed[index].questionMessage = conversation, firstString(call["call_message_id"])
			}
			answers[result] = parsed
		}
	}
	return answers, nil
}

// questionAnswerShape describes, without its text, a question tool result
// that could not be parsed, so the manifest shows which forms to support.
func questionAnswerShape(callText, resultText string) string {
	var call, result map[string]any
	shape := []string{}
	if json.Unmarshal([]byte(callText), &call) != nil {
		shape = append(shape, "call:unparsed")
	} else {
		input := mapValue(call["input"])
		questions, _ := input["questions"].([]any)
		keys := []string{}
		if len(questions) > 0 {
			for key := range mapValue(questions[0]) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		shape = append(shape, fmt.Sprintf("call:%d questions %s", len(questions), strings.Join(keys, "+")))
	}
	if json.Unmarshal([]byte(resultText), &result) != nil {
		return strings.Join(append(shape, "result:unparsed"), "; ")
	}
	keys := []string{}
	for key := range result {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	content := delegationPayload(resultText)
	if text, ok := result["content"].(string); ok {
		content = text
	}
	form := "other"
	var structured map[string]any
	switch {
	case strings.HasPrefix(content, "User has answered"):
		form = "user_has_answered"
	case json.Unmarshal([]byte(strings.TrimSpace(content)), &structured) == nil:
		form = "json " + jsonShape(structured, 3)
	case content == "":
		form = "empty"
	default:
		// Letters and digits masked: the form of the text, none of its words.
		masked := []rune{}
		for _, r := range firstRunes(content, 32) {
			switch {
			case unicode.IsUpper(r):
				r = 'X'
			case unicode.IsLetter(r):
				r = 'x'
			case unicode.IsDigit(r):
				r = '9'
			}
			masked = append(masked, r)
		}
		form = fmt.Sprintf("text %q contains_pair=%t", string(masked), strings.Contains(content, `"="`))
	}
	return strings.Join(append(shape, fmt.Sprintf("result:%s content:%T %s", strings.Join(keys, "+"), result["content"], form)), "; ")
}

// jsonShape lists a JSON value's keys to a depth, with object keys that look
// like IDs collapsed, and no values.
func jsonShape(value any, depth int) string {
	switch typed := value.(type) {
	case map[string]any:
		if depth == 0 {
			return "{…}"
		}
		keys := []string{}
		for key, item := range typed {
			keys = append(keys, key+":"+jsonShape(item, depth-1))
		}
		sort.Strings(keys)
		return "{" + strings.Join(keys, ",") + "}"
	case []any:
		if len(typed) == 0 {
			return "[]"
		}
		return "[" + jsonShape(typed[0], depth-1) + "]"
	}
	return fmt.Sprintf("%T", value)
}

var numberedAnswer = regexp.MustCompile(`(?m)^([0-9]+)\. (.*)$`)

// interactiveAt reports whether a conversation was an interactive session
// at a time: a root's own execution (not delegated or launched) whose latest
// user message by then came from a person, not automation. It returns the
// evidence either way.
func (p *labelingPass) interactiveAt(conversation, at string) (bool, string) {
	if p.lineage.roots[conversation] == nil || p.lineage.runs[conversation] != "" {
		return false, "delegated_or_unresolved_run"
	}
	timeline := p.userTimeline[conversation]
	index := sort.Search(len(timeline), func(i int) bool { return timeline[i].at > at }) - 1
	switch {
	case at == "" || index < 0:
		return false, "no_user_message_before_answer"
	case timeline[index].automated:
		return false, "automated_session"
	}
	return true, "interactive_session_question_tool"
}

// labelingUserMessage is one user message's send time and automation.
type labelingUserMessage struct {
	at        string
	automated bool
}

// labelingQuestion is one question a question tool asked.
type labelingQuestion struct {
	id, title string
	options   []string
}

// parseQuestionAnswers reads the answers in a question tool's result, given
// the call that asked: Claude's `User has answered your questions: "Q"="A",
// ...` text, or a JSON {"answers": {id: {"answers": [...]}}} object. An answer
// that is one of the offered options (or several, comma-separated) is a
// submitted option; any other answer is free text.
func parseQuestionAnswers(callText, resultText string) []labelingAnswer {
	var call map[string]any
	if json.Unmarshal([]byte(callText), &call) != nil {
		return nil
	}
	input := mapValue(call["input"])
	if text, ok := call["input"].(string); ok {
		var decoded map[string]any
		if json.Unmarshal([]byte(text), &decoded) == nil {
			input = decoded
		}
	}
	questions := []labelingQuestion{}
	list, _ := input["questions"].([]any)
	for _, raw := range list {
		q := mapValue(raw)
		question := labelingQuestion{id: firstString(q["id"]), title: firstString(q["question"], q["title"])}
		options, _ := q["options"].([]any)
		for _, option := range options {
			label := firstString(option)
			if f := mapValue(option); f != nil {
				label = firstString(f["label"])
			}
			if label != "" {
				question.options = append(question.options, label)
			}
		}
		if question.title != "" {
			questions = append(questions, question)
		}
	}
	content := resultText
	var envelope map[string]any
	if json.Unmarshal([]byte(resultText), &envelope) == nil {
		content = delegationPayload(resultText)
		if text, ok := envelope["content"].(string); ok {
			content = text
		}
	}
	answered := map[int][]string{}
	// A structured record of the answers (Conductor keeps the tool's own),
	// by question title.
	if details := mapValue(mapValue(envelope["details"])["answers"]); len(details) > 0 {
		for index, q := range questions {
			if value := firstString(details[q.title]); value != "" {
				answered[index] = append(answered[index], value)
			}
		}
		content = ""
	}
	var structured map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &structured) == nil && structured["answers"] != nil {
		for id, raw := range mapValue(structured["answers"]) {
			for index, q := range questions {
				if q.id == id || q.title == id {
					values, _ := mapValue(raw)["answers"].([]any)
					for _, value := range values {
						if text := firstString(value); text != "" {
							answered[index] = append(answered[index], text)
						}
					}
				}
			}
		}
	} else if first, rest, ok := strings.Cut(strings.TrimSpace(content), "\n"); ok && strings.HasSuffix(first, "responses:") {
		// Conductor's form: "User responses:" then one numbered line per question.
		for _, match := range numberedAnswer.FindAllStringSubmatch(rest, -1) {
			if index, err := strconv.Atoi(match[1]); err == nil && index >= 1 && index <= len(questions) {
				answered[index-1] = append(answered[index-1], strings.TrimSpace(match[2]))
			}
		}
	} else {
		type found struct{ index, at int }
		positions := []found{}
		for index, q := range questions {
			if at := strings.Index(content, `"`+q.title+`"="`); at >= 0 {
				positions = append(positions, found{index, at})
			}
		}
		sort.Slice(positions, func(i, j int) bool { return positions[i].at < positions[j].at })
		for i, position := range positions {
			start := position.at + len(questions[position.index].title) + 4
			end := len(content)
			if i+1 < len(positions) {
				end = positions[i+1].at
			}
			value := strings.TrimSpace(content[start:end])
			if at := strings.LastIndex(value, `". You can now continue`); at >= 0 {
				value = value[:at]
			}
			value = strings.TrimSuffix(strings.TrimSuffix(value, ","), `"`)
			answered[position.index] = append(answered[position.index], value)
		}
	}
	answers := []labelingAnswer{}
	for index, q := range questions {
		for _, value := range answered[index] {
			// One offered option, or several joined by a multi-select.
			options := []string{}
			if slices.Contains(q.options, value) {
				options = []string{value}
			} else if parts := strings.Split(value, ", "); len(parts) > 1 {
				options = parts
				for _, part := range parts {
					if !slices.Contains(q.options, part) {
						options = nil
						break
					}
				}
			}
			answer := labelingAnswer{status: "tool_result_answer", source: "tool_result", question: q.title, actor: "human", titleStart: -1, titleEnd: -1, start: -1, end: -1}
			if len(options) == 0 {
				answer.kind, answer.text = "free_text_answer", value
				answers = append(answers, answer)
			}
			for _, option := range options {
				answer.kind, answer.option = "submitted_option", option
				answers = append(answers, answer)
			}
		}
	}
	return answers
}

// rememberChildText indexes a delegated child's messages by normalized text,
// and keeps its first request and last reply.
func (p *labelingPass) rememberChildText(conversation string, record labelingRecord) {
	if !record.selected || record.kind != "message" || !record.text.Valid {
		return
	}
	for _, owner := range []string{"c:" + conversation, "s:" + record.session} {
		if !p.childOwners[owner] {
			continue
		}
		child := p.children[owner]
		if child == nil {
			child = &labelingChild{texts: map[string]string{}}
			p.children[owner] = child
		}
		text := normalizeSpace(record.text.String)
		if child.texts[text] == "" {
			child.texts[text] = record.id
		}
		if record.role == "user" && child.firstID == "" {
			child.first, child.firstID = text, record.id
		}
		if record.role == "assistant" || record.role == "agent" {
			child.last, child.lastID = text, record.id
		}
	}
}

// labelingContainMin is the shortest text a containment match may rest on.
const labelingContainMin = 20

// matchDelegation finds the child message a delegation or its result
// repeats: the same normalized text, a first request containing the
// delegation's prompt, or a result containing the child's last reply.
func matchDelegation(check labelingDuplicateCheck, child *labelingChild) (string, string) {
	for _, text := range []string{check.payload, check.whole} {
		if id := child.texts[text]; id != "" && text != "" {
			return id, "equal"
		}
	}
	if check.kind == "delegation" && len(check.payload) >= labelingContainMin && strings.Contains(child.first, check.payload) {
		return child.firstID, "contains"
	}
	if check.kind == "delegation_result" && len(child.last) >= labelingContainMin && strings.Contains(check.payload, child.last) {
		return child.lastID, "contains"
	}
	return "", ""
}

// record decides one message: a unit, or the reason it is excluded.
func (p *labelingPass) record(conversation string, record labelingRecord, answers map[string][]labelingAnswer, calls map[string]string) (*labelingUnit, string, string, error) {
	text := record.text.String
	candidate := labelingCandidate(record)
	switch parsed, asked := answers[record.id]; {
	case !record.selected:
		return nil, "unselected_branch", "", nil
	case asked && len(parsed) == 0:
		p.counters["unparsed_answer_actor:"+p.unparsedActor[record.id]]++
		return nil, "question_tool_result_unparsed", "a question tool's result whose answer could not be read (often none: accepted-only or an error)", nil
	case asked:
		// An answer also submitted as a flattened question-form message is
		// recorded there, with byte ranges; this copy adds nothing.
		fresh := []labelingAnswer{}
		for _, answer := range parsed {
			if !p.flattenedKeys[answer.questionMessage+"\x00"+answer.question] {
				fresh = append(fresh, answer)
			}
		}
		if len(fresh) == 0 {
			return nil, "tool_result_answer_duplicate", "the same answers were submitted as a question-form message", nil
		}
		if err := p.actions(record.id, conversation, fresh); err != nil {
			return nil, "", "", err
		}
		return nil, fresh[0].status, "answers recorded as actions", nil
	case record.kind == "metadata" && strings.HasPrefix(text, `{"reason":`):
		if value, ok := parseReaderJSON(text); ok && firstString(mapValue(value)["type"]) == "reasoning_unavailable" {
			return nil, "reasoning_unavailable", "", nil
		}
		return nil, "kind_metadata", "", nil
	case record.kind == "result":
		return nil, "result_record", "a possible duplicate of the final reply", nil
	case candidate == "" && record.kind == "message":
		return nil, "role_" + record.role, "", nil
	case candidate == "":
		return nil, "kind_" + record.kind, "", nil
	case strings.TrimSpace(text) == "":
		return nil, "empty_text", "", nil
	case !utf8.ValidString(text):
		return nil, "invalid_utf8", "", nil
	case candidate == "thinking" && !p.reasoningStore:
		return nil, "reasoning_store_absent", "the catalog's schema has no normalized reasoning store", nil
	case candidate == "thinking" && !p.current[conversation]:
		return nil, "reasoning_extraction_pending", "", nil
	}
	info := p.conversations[conversation]
	unit := &labelingUnit{id: labelingID("u_", labelingUnitsVersion, record.id), messageID: record.id, conversation: conversation, workspace: info.workspace,
		provider: info.provider, role: record.role, kind: record.kind, sender: record.sender, channel: "unspecified", format: record.format,
		createdAt: record.createdAt, contentHash: record.contentHash, session: record.session, text: text,
		canonical: p.lineage.canonical[conversation], run: p.lineage.runs[conversation]}
	if root := p.lineage.roots[conversation]; root != nil {
		unit.root = firstString(root["id"])
	}
	p.resolveDepth(unit, info)
	var rendered humanUnit
	switch candidate {
	case "user":
		result := p.author(record.id)
		if result == nil || len(result.Spans) == 0 {
			if record.createdAt == "" {
				return nil, "authorship_missing", "the message has no send time, which classification needs", nil
			}
			return nil, "authorship_missing", "the classifier returned no result for this message", nil
		}
		automated := result.Spans[0].Category == spanAutomated
		if automated || p.sessionKind[record.session] == "subagent" {
			unit.cohort, unit.actor, unit.authorship = "agent_in_user_role", "agent", "Embedded sub-agent session"
			if automated {
				unit.authorship = result.Spans[0].Reason
			}
			rendered = agentUserPieces(text)
		} else {
			unit.cohort, unit.actor = "human", "human"
			rendered = humanPieces(text, result.Spans, p.reusePieces)
		}
		flattened := []labelingAnswer{}
		for _, answer := range result.Answers {
			flattened = append(flattened, labelingAnswer{kind: "submitted_option", status: "recognized_question_form", source: "flattened_message",
				question: answer.Title, option: answer.Option, actor: unit.actor, evidence: "flattened_question_form_message", titleStart: answer.TitleStart, titleEnd: answer.TitleEnd,
				start: answer.Start, end: answer.End, questionConversation: answer.Origin.conversation, questionMessage: answer.Origin.message})
		}
		if err := p.actions(record.id, conversation, flattened); err != nil {
			return nil, "", "", err
		}
		if rendered.Exclude == "no_target_text" && len(flattened) > 0 {
			return nil, "action_only", "the message only submitted a question-form option, recorded as an action", nil
		}
	default:
		unit.cohort, unit.actor = candidate, "agent"
		status := map[string]string{"agent_reply": "agent_text", "delegation": "delegation_text", "thinking": "thinking_text"}[candidate]
		start := len(text) - len(strings.TrimLeft(text, " \t\r\n"))
		end := len(strings.TrimRight(text, " \t\r\n"))
		raw := []labelingPiece{{Role: pieceTarget, Status: status, Start: start, End: end}}
		if candidate == "delegation" {
			if payload, ok := delegationPieces(text, status); ok {
				raw = payload
				p.counters["delegation_payload_decoded"]++
			} else {
				p.counters["delegation_payload_raw"]++
			}
		}
		pieces, _, secrets := cleanPieces(text, raw, false)
		rendered = renderPieces(text, pieces)
		rendered.Secrets = secrets
		if candidate == "thinking" {
			unit.channel = "analysis"
		}
		if candidate == "delegation" && !p.itemsOnly {
			delegation := record.id
			if record.kind == "delegation_result" {
				delegation = calls[record.callID]
			}
			p.checks = append(p.checks, labelingDuplicateCheck{record.id, delegation, record.kind, normalizeSpace(delegationPayload(text)), normalizeSpace(text)})
		}
	}
	p.counters["redacted_secrets"] += rendered.Secrets
	p.counters["unrecognized_harness_tags"] += rendered.Unrecognized
	if rendered.Exclude != "" {
		return nil, rendered.Exclude, "", nil
	}
	unit.pieces, unit.visible, unit.markers = rendered.Pieces, rendered.Visible, rendered.Markers
	unit.chunks, unit.attachedChunks = attachMarkerChunks(chunkLabelingText(unit.visible, labelingChunkChars, unit.markers), unit.pieces)
	p.counters["marker_chunks_attached"] += unit.attachedChunks
	unit.length = "short"
	if len(unit.chunks) > 1 {
		unit.length = "long"
	}
	for _, chunk := range unit.chunks {
		hash, short := itemTargetHash(unit.visible, chunk, unit.markers)
		unit.hashes = append(unit.hashes, hash)
		if short && !p.itemsOnly {
			p.shortHashes[hash] = true
		}
	}
	return unit, "", "", nil
}

// actions records answers as actions of the message that submitted them.
// actions queues answers as actions of the message that submitted them;
// flushActions records a conversation's queue.
func (p *labelingPass) actions(message, conversation string, answers []labelingAnswer) error {
	for _, answer := range answers {
		p.pending = append(p.pending, labelingPendingAction{message, conversation, answer})
	}
	return nil
}

type labelingPendingAction struct {
	message, conversation string
	answer                labelingAnswer
}

// flushActions records the queued actions once each: an answer recorded both
// as a flattened message and as the question tool's result (same asking
// call, same question) keeps the flattened form, which has byte ranges.
func (p *labelingPass) flushActions() error {
	key := func(action labelingPendingAction) string {
		return action.answer.questionMessage + "\x00" + action.answer.question
	}
	flattened := map[string]bool{}
	for _, action := range p.pending {
		if action.answer.source == "flattened_message" && action.answer.questionMessage != "" {
			flattened[key(action)] = true
		}
	}
	positions := map[string]int{}
	for _, action := range p.pending {
		if action.answer.source != "flattened_message" && flattened[key(action)] {
			p.counters["duplicate_actions"]++
			continue
		}
		p.counters["actions_"+action.answer.source]++
		p.counters["action_status:"+action.answer.status]++
		if p.writer != nil {
			if err := p.writer.action(action.message, action.conversation, positions[action.message], action.answer); err != nil {
				return err
			}
		}
		positions[action.message]++
	}
	p.pending = p.pending[:0]
	return nil
}

// agentUserPieces makes an agent's user-role message a unit: the whole
// message after its leading harness blocks (those authorship knows and the
// unrecognized tags), which are dropped, with secrets redacted.
func agentUserPieces(text string) humanUnit {
	pieces := []labelingPiece{}
	start := 0
	blocks, unrecognized := labelingHarnessBlocks(text)
	tags := 0
	for index, block := range blocks {
		reason := reasonHarness.text()
		if unrecognized[index] {
			reason = unrecognizedHarnessReason
			tags++
		}
		pieces = append(pieces, labelingPiece{Role: pieceDropped, Status: spanHarness, Category: spanHarness, Reason: reason, Start: block.Start, End: block.End})
		start = block.End
	}
	start += len(text[start:]) - len(strings.TrimLeft(text[start:], " \t\r\n"))
	end := max(start, len(strings.TrimRight(text, " \t\r\n")))
	pieces = append(pieces, labelingPiece{Role: pieceTarget, Status: "agent_text", Start: start, End: end})
	pieces, _, secrets := cleanPieces(text, pieces, true)
	unit := renderPieces(text, pieces)
	unit.Secrets, unit.Unrecognized = secrets, tags
	return unit
}

// resolveDepth records the delegation depth the usage lineage resolves for
// the unit's agent session (its embedded session, else the conversation's
// main one), falling back to the stored depth, or why it is unresolved.
func (p *labelingPass) resolveDepth(unit *labelingUnit, info *labelingConversation) {
	session := unit.session
	if session == "" {
		session = p.mainSession[info.id]
	} else {
		unit.sessionRole = p.sessionKind[session]
	}
	if depth, ok := p.lineage.delegatedDepth[session]; ok && session != "" {
		unit.depth, unit.depthStatus = depth, "resolved"
		return
	}
	if reason := p.lineage.unresolved[info.id]; reason != "" {
		unit.depth, unit.depthStatus = -1, "unresolved:"+reason
		return
	}
	unit.depth, unit.depthStatus = info.depth, "stored"
}

func (p *labelingPass) addUnit(unit *labelingUnit) {
	depth := labelingDepthBucket(unit.depth, unit.depthStatus)
	key := [4]string{unit.cohort, unit.provider, depth, unit.length}
	if p.units[key] == nil {
		p.units[key] = &[3]int{}
	}
	p.units[key][0]++
	p.units[key][1] += len(unit.chunks)
	p.units[key][2] += utf8.RuneCountInString(unit.visible)
	p.stats = append(p.stats, labelingUnitStat{unit.conversation, unit.cohort, unit.provider, depth, unit.length, len(unit.chunks), unit.hashes})
}

// labelingFamily is the family (splits-v1) a conversation belongs to.
type labelingFamily struct{ id, split, first string }

// labelingCopyLinkBytes is the shortest re-sent or quoted span, with a
// recorded source, that links two conversations into one family. Repeated
// lines and templates never link.
var labelingCopyLinkBytes = 0

// labelingFamilies groups conversations that share text or execution, so a
// split never separates them: mirrors and aliases, roots and runs, parents,
// delegated children, launched runs, and re-sent or quoted passages of at
// least labelingCopyLinkBytes found by authorship (not repeated lines or
// templates). A family keeps the split an earlier dataset gave any of its
// conversations (inherited, by conversation); otherwise it is fresh when its
// first activity falls within the fresh window, and train, dev or test by
// the hash of its ID (80/10/10). Every conversation gets a family.
func labelingFamilies(ids []string, p *labelingPass, sessions []map[string]any, launches []conversationLaunch, results []authorResult, freshSince string,
	inherited map[string]string) (map[string]labelingFamily, map[string]int) {
	parent := map[string]string{}
	for _, id := range ids {
		parent[id] = id
	}
	var find func(string) string
	find = func(id string) string {
		if parent[id] != id {
			parent[id] = find(parent[id])
		}
		return parent[id]
	}
	union := func(left, right string) {
		if parent[left] == "" || parent[right] == "" {
			return
		}
		if l, r := find(left), find(right); l != r {
			parent[r] = l
		}
	}
	for _, id := range ids {
		union(id, p.lineage.canonical[id])
		if root := p.lineage.roots[id]; root != nil {
			union(id, firstString(root["id"]))
		}
		union(id, p.conversations[id].parent)
	}
	for _, row := range sessions {
		union(firstString(row["conversation_id"]), firstString(row["child"]))
	}
	for _, launch := range launches {
		union(launch.child, launch.parent)
	}
	for _, result := range results {
		for _, span := range result.Spans {
			copied := span.Category == spanResent && span.Reason != reasonLineSent.text() || span.Category == spanQuoted
			if span.Source != "" && copied && span.End-span.Start >= labelingCopyLinkBytes {
				union(result.Input.ConversationID, span.Source)
			}
		}
	}
	stats := map[string]int{}
	smallest, first, old := map[string]string{}, map[string]string{}, map[string]map[string]bool{}
	oldBy := map[string]string{} // family root: split of its smallest previously assigned member
	sorted := slices.Clone(ids)
	sort.Strings(sorted)
	for _, id := range sorted {
		root := find(id)
		if smallest[root] == "" {
			smallest[root] = id
		}
		// The family's first message, not when a session was opened.
		at := p.firstAt[id]
		if at == "" {
			at = p.conversations[id].started
		}
		if at != "" && (first[root] == "" || at < first[root]) {
			first[root] = at
		}
		if split := inherited[id]; split != "" {
			if old[root] == nil {
				old[root] = map[string]bool{}
				oldBy[root] = split
			}
			old[root][split] = true
		}
	}
	families := map[string]labelingFamily{}
	for _, id := range ids {
		root := find(id)
		family := labelingFamily{id: labelingID("f_", labelingSplitsVersion, smallest[root]), first: first[root]}
		family.split = labelingSplit(family.id, family.first, freshSince)
		if split := oldBy[root]; split != "" {
			family.split = split
			stats["inherited_conversations"]++
			if inherited[id] == "" {
				stats["joined_old_family"]++
			}
			if len(old[root]) > 1 {
				// Linked work an earlier dataset split apart can belong to no
				// split without leaking: it is kept out of training and evaluation.
				family.split = "quarantine"
				stats["quarantined_conversations"]++
			}
		}
		families[id] = family
	}
	return families, stats
}

// loadLabelingSplits reads the split each conversation had in an earlier
// dataset.
func loadLabelingSplits(dir string) (map[string]string, error) {
	db, err := openLabelingDataset(dir)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := queryMaps(db, `SELECT conversation_id,split FROM families`)
	if err != nil {
		return nil, err
	}
	splits := map[string]string{}
	for _, row := range rows {
		splits[firstString(row["conversation_id"])] = firstString(row["split"])
	}
	return splits, nil
}

// labelingSplit assigns a family to fresh by time, or by its ID's hash.
func labelingSplit(family, first, freshSince string) string {
	if first != "" && first >= freshSince {
		return "fresh"
	}
	sum := sha256.Sum256([]byte(labelingSplitsVersion + "\x00" + family))
	switch bucket := binary.BigEndian.Uint64(sum[:8]) % 100; {
	case bucket < 80:
		return "train"
	case bucket < 90:
		return "dev"
	}
	return "test"
}

// manifest describes the prepared population: scope, snapshot, contracts,
// counts, exclusions, duplicates and split coverage.
func (p *labelingPass) manifest(opts labelingPrepareOptions, snapshot, selection map[string]any, scope []string, families map[string]labelingFamily, duplicates map[string]map[string]int) map[string]any {
	counts := []map[string]any{}
	units, items := 0, 0
	for key, value := range p.units {
		counts = append(counts, map[string]any{"cohort": key[0], "provider": key[1], "depth": key[2], "length": key[3], "units": value[0], "items": value[1], "visible_chars": value[2]})
		units += value[0]
		items += value[1]
	}
	sort.Slice(counts, func(i, j int) bool {
		a, b := counts[i], counts[j]
		return fmt.Sprint(a["cohort"], a["provider"], a["depth"], a["length"]) < fmt.Sprint(b["cohort"], b["provider"], b["depth"], b["length"])
	})
	cohorts := map[string]int{}
	long := map[string]int{}
	for _, row := range counts {
		cohorts[row["cohort"].(string)] += row["units"].(int)
		if row["length"] == "long" {
			long["units"] += row["units"].(int)
			long["items"] += row["items"].(int)
		}
	}
	exclusions := []map[string]any{}
	excluded := 0
	for key, count := range p.exclusions {
		exclusions = append(exclusions, map[string]any{"reason": key[0], "cohort": key[1], "records": count})
		excluded += count
	}
	sort.Slice(exclusions, func(i, j int) bool {
		return fmt.Sprint(exclusions[i]["reason"], exclusions[i]["cohort"]) < fmt.Sprint(exclusions[j]["reason"], exclusions[j]["cohort"])
	})
	coverage := map[string]map[string]map[string]int{}
	for _, stat := range p.stats {
		split := families[stat.conversation].split
		for dimension, value := range map[string]string{"cohort": stat.cohort, "provider": stat.provider, "depth": stat.depth, "length": stat.length} {
			if coverage[dimension] == nil {
				coverage[dimension] = map[string]map[string]int{}
			}
			if coverage[dimension][value] == nil {
				coverage[dimension][value] = map[string]int{}
			}
			coverage[dimension][value][split]++
		}
	}
	// Family sizes, in units: one giant family would hold a split hostage.
	familyUnits, splitUnits := map[string]int{}, map[string]int{}
	for _, stat := range p.stats {
		familyUnits[families[stat.conversation].id]++
		splitUnits[families[stat.conversation].split]++
	}
	sizes := []int{}
	for _, size := range familyUnits {
		sizes = append(sizes, size)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sizes)))
	share := func(n int) float64 {
		if units == 0 {
			return 0
		}
		return float64(n) / float64(units)
	}
	top := []float64{}
	for _, size := range sizes[:min(10, len(sizes))] {
		top = append(top, share(size))
	}
	splitShare := map[string]float64{}
	for split, n := range splitUnits {
		splitShare[split] = share(n)
	}
	familyStats := map[string]any{"families_with_units": len(sizes), "top10_unit_shares": top, "split_unit_shares": splitShare,
		"copy_link_min_bytes": labelingCopyLinkBytes}
	if len(sizes) > 0 {
		familyStats["largest_units"], familyStats["largest_share"] = sizes[0], share(sizes[0])
	}
	familyIDs := map[string]bool{}
	for _, family := range families {
		familyIDs[family.id] = true
	}
	sum := sha256.Sum256(labelingTaxonomy)
	statement := fmt.Sprintf("%d units (%d items) prepared from the selected records of %d conversations in this catalog snapshot. Excluded records are counted by reason; this is not every record in the library.",
		units, items, len(scope))
	if selection["restricted"] == true {
		statement = fmt.Sprintf("A restricted sample, not the library: %d units (%d items) from %d conversations chosen as %s. Counts describe only this sample.",
			units, items, len(scope), selection["method"])
	}
	return map[string]any{
		"population": statement,
		"scope": map[string]any{"selection": selection, "conversations": len(scope), "mirrored_conversations": p.counters["mirrored_conversations"],
			"fresh_days": opts.FreshDays, "dry_run": opts.DryRun},
		"snapshot": snapshot,
		"contracts": map[string]any{"units": labelingUnitsVersion, "human_pieces": humanPiecesVersion, "marker": labelingMarker,
			"context_marker": labelingContextMarker, "excerpt_chars": labelingExcerptChars, "excerpt_budget": labelingExcerptBudget,
			"sentences": labelingSentenceVersion, "context": labelingContextVersion,
			"chunking": labelingChunkVersion, "chunk_chars": labelingChunkChars, "splits": labelingSplitsVersion,
			"taxonomy": map[string]any{"version": labelingTaxonomyVersion, "sha256": hex.EncodeToString(sum[:])}, "prompt": nil,
			"channel": "analysis for thinking, unspecified otherwise: the catalog keeps no final/commentary channel"},
		"units": units, "items": items, "cohorts": cohorts, "counts": counts,
		"long":       map[string]any{"units": long["units"], "items": long["items"], "merge": "unvalidated"},
		"exclusions": exclusions, "excluded_records": excluded,
		"duplicates": map[string]any{"delegation": duplicates["delegation"], "delegation_result": duplicates["delegation_result"],
			"inherited_history": p.exclusionTotal("inherited_history"), "result_records": p.exclusionTotal("result_record"),
			"forks": map[string]any{"child_conversations": p.counters["child_conversations"], "without_times": p.counters["forks_without_times"],
				"records_before_start":        p.prefixed("records_before_child_start:"),
				"forks_with_unmatched_copies": p.counters["forks_with_unmatched_copies"], "unmatched_copies": p.counters["fork_unmatched_copies"],
				"with_original_times": p.counters["forks_with_original_times"], "restamped_or_no_copy": p.counters["forks_restamped"],
				"copies_after_difference": p.counters["fork_copies_after_difference"], "copies_after_difference_by_provider": p.prefixed("copies_after_difference:"),
				"in_order_copy_times": p.prefixed("fork_copy_time:")},
			"note": "delegation units that repeat a child's message (equal or containing, whitespace-normalized) are kept with duplicate_of set; inherited history is excluded"},
		"invalid_utf8":       p.exclusionTotal("invalid_utf8"),
		"delegation_payload": map[string]any{"transform": feedbackJSONTransform, "decoded": p.counters["delegation_payload_decoded"], "raw": p.counters["delegation_payload_raw"]},
		"actions": map[string]any{"question_forms": p.counters["actions_flattened_message"], "tool_results": p.counters["actions_tool_result"],
			"by_status":             p.prefixed("action_status:"),
			"unparsed_tool_results": p.exclusionTotal("question_tool_result_unparsed"), "unparsed_by_actor": p.prefixed("unparsed_answer_actor:"),
			"duplicates_dropped": p.counters["duplicate_actions"], "unparsed_shapes": p.prefixed("unparsed_answer_shape:")},
		"redacted_secrets":          p.counters["redacted_secrets"],
		"unrecognized_harness_tags": p.counters["unrecognized_harness_tags"],
		"marker_chunks_attached":    p.counters["marker_chunks_attached"],
		"splits":                    map[string]any{"families": len(familyIDs), "family_sizes": familyStats, "coverage": coverage},
	}
}

// reusePieces resolves a re-sent piece against its source message (see
// labelingReuse): the matched text is found in the source, and only the
// bytes that were target there, resolved the same way, stay target.
func (p *labelingPass) reusePieces(text string, piece labelingPiece) []labelingPiece {
	return p.reuseFrom(text, piece, map[string]bool{})
}

func (p *labelingPass) reuseFrom(text string, piece labelingPiece, visiting map[string]bool) []labelingPiece {
	source := p.author(piece.SourceMessage)
	if source == nil {
		return nil
	}
	value := text[piece.Start:piece.End]
	core := strings.TrimSpace(value)
	targets := p.sourceTargets(piece.SourceMessage, visiting)
	if core == "" || targets == nil {
		return nil
	}
	// The copied text may occur in the source more than once; when the
	// occurrences were attributed differently, which was copied is unknown.
	at := -1
	for offset := 0; ; {
		found := strings.Index(source.Input.Text[offset:], core)
		if found < 0 {
			break
		}
		found += offset
		if at < 0 {
			at = found
		} else if !slices.Equal(targets[at:at+len(core)], targets[found:found+len(core)]) {
			return nil
		}
		offset = found + 1
	}
	if at < 0 {
		return nil
	}
	offset := piece.Start + strings.Index(value, core)
	parts := []labelingPiece{}
	// Each part records the bytes it matched in the source; whitespace
	// around the match matched nothing there.
	add := func(start, end int, target bool) {
		part := piece
		part.Start, part.End = start, end
		part.SourceStart, part.SourceEnd = 0, 0
		if from, to := max(start, offset), min(end, offset+len(core)); from < to {
			part.SourceStart, part.SourceEnd = at+from-offset, at+to-offset
		}
		if !target {
			part.Role, part.Status = pieceContext, "reused_unverified"
		}
		if last := len(parts) - 1; last >= 0 && parts[last].Status == part.Status {
			parts[last].End = end
			if part.SourceEnd > 0 {
				if parts[last].SourceEnd == 0 {
					parts[last].SourceStart = part.SourceStart
				}
				parts[last].SourceEnd = part.SourceEnd
			}
			return
		}
		parts = append(parts, part)
	}
	if offset > piece.Start {
		add(piece.Start, offset, true) // leading whitespace
	}
	for k := 0; k < len(core); {
		end := k
		for end < len(core) && targets[at+end] == targets[at+k] {
			end++
		}
		add(offset+k, offset+end, targets[at+k])
		k = end
	}
	if tail := offset + len(core); tail < piece.End {
		add(tail, piece.End, parts[len(parts)-1].Role == pieceTarget)
	}
	return parts
}

// messageSessionKind is the kind of the embedded agent session a message
// belongs to, if any: an embedded sub-agent's prompt is never the person's.
func (p *labelingPass) messageSessionKind(id string) string {
	if p.tx == nil {
		return ""
	}
	var session sql.NullString
	if err := p.tx.QueryRowContext(context.Background(), `SELECT MIN(agent_session_id) FROM agent_session_messages WHERE message_id=?`, id).Scan(&session); err != nil {
		return ""
	}
	return p.sessionKind[session.String]
}

// sourceTargets marks, byte by byte, which text of a user message is target
// under human-pieces-v2, resolving its own re-sent pieces in turn. It returns
// nil for a message without classification, an automated one, or a cycle.
func (p *labelingPass) sourceTargets(id string, visiting map[string]bool) []bool {
	if mask, ok := p.targetCache[id]; ok {
		return mask
	}
	result := p.author(id)
	if result == nil || visiting[id] || len(result.Spans) == 0 || result.Spans[0].Category == spanAutomated || p.messageSessionKind(id) == "subagent" {
		return nil
	}
	visiting[id] = true
	defer delete(visiting, id)
	text := result.Input.Text
	pieces, _, _ := cleanPieces(text, resolveReuse(text, mergeFeedbackJSON(text, result.Spans), func(text string, piece labelingPiece) []labelingPiece {
		return p.reuseFrom(text, piece, visiting)
	}), true)
	mask := make([]bool, len(text))
	for _, piece := range pieces {
		if piece.Role == pieceTarget {
			for index := piece.Start; index < piece.End; index++ {
				mask[index] = true
			}
		}
	}
	p.targetCache[id] = mask
	return mask
}

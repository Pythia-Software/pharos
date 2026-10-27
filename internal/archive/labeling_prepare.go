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

// labelingTaxonomy is the frozen candidate-0.3 taxonomy, byte for byte as it
// was measured (evidence/label-frontier/20261027/taxonomy.json). Preparation
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
	// SnapshotCopy reads a static copy (a backup) immutably.
	SnapshotCopy bool
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
	// droppedChunks counts chunks holding only markers, which become no item.
	droppedChunks int
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
}

// labelingPass holds the snapshot's shared inputs and the counts as
// conversations are read.
type labelingPass struct {
	tx             *sql.Tx
	writer         *labelingWriter
	reasoningStore bool
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
	parents map[string]map[string]labelingParentMessage
	// units counts cohort, provider, depth, length: units, items, characters.
	units      map[[4]string]*[3]int
	exclusions map[[2]string]int
	counters   map[string]int
	stats      []labelingUnitStat
	firstAt    map[string]string
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

type labelingParentMessage struct{ id, createdAt string }

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
func readLabelingRecords(ctx context.Context, tx *sql.Tx, conversation string) ([]labelingRecord, error) {
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
	db, tx, access, err := openLabelingSnapshot(ctx, opts.CatalogPath, opts.SnapshotCopy)
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
	p := &labelingPass{tx: tx, reasoningStore: present["reasoning_extraction_state"], conversations: map[string]*labelingConversation{}, mainSession: map[string]string{}, sessionKind: map[string]string{},
		childOwners: map[string]bool{}, current: map[string]bool{}, children: map[string]*labelingChild{}, parents: map[string]map[string]labelingParentMessage{},
		units:      map[[4]string]*[3]int{},
		exclusions: map[[2]string]int{}, counters: map[string]int{}, firstAt: map[string]string{}}
	workspaces, err := queryMapsContext(ctx, tx, `SELECT w.id,w.source_kind,w.activity_at FROM workspaces w`)
	if err != nil {
		return nil, err
	}
	links, err := queryMapsContext(ctx, tx, identityLinkWorkspacesSQL)
	if err != nil {
		return nil, err
	}
	p.representative = mirrorRepresentatives(workspaces, links)
	kept := []map[string]any{}
	for _, row := range workspaces {
		if p.representative[firstString(row["id"])] == "" {
			kept = append(kept, map[string]any{"workspace_id": row["id"]})
		}
	}
	ids := []string{}
	rows, err := tx.QueryContext(ctx, `SELECT id,workspace_id,provider,COALESCE(started_at,''),COALESCE(parent_id,''),agent_depth FROM conversations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		info := &labelingConversation{}
		if err := rows.Scan(&info.id, &info.workspace, &info.provider, &info.started, &info.parent, &info.depth); err != nil {
			rows.Close()
			return nil, err
		}
		p.conversations[info.id] = info
		ids = append(ids, info.id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sessions, err := queryMapsContext(ctx, tx, `SELECT id,conversation_id,native_id,kind,COALESCE(child_conversation_id,'') child,COALESCE(delegation_message_id,'') delegation FROM agent_sessions`)
	if err != nil {
		return nil, err
	}
	delegated := map[string][]string{} // delegation message: its child's owner keys
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
			delegated[message] = append(delegated[message], owner)
			p.childOwners[owner] = true
		}
	}
	reasoning, err := queryMapsContext(ctx, tx, `SELECT conversation_id,unavailable_count FROM reasoning_extraction_state WHERE version=?`, reasoningExtractionVersion)
	if err != nil {
		return nil, err
	}
	unavailableLedger := map[string]int64{}
	for _, row := range reasoning {
		p.current[firstString(row["conversation_id"])] = true
		unavailableLedger[firstString(row["conversation_id"])] = integer(row["unavailable_count"])
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
	results, err := classifyInputsFrom(workCtx, tx, inputs)
	if err != nil {
		return nil, fmt.Errorf("authorship classification: %w", err)
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
	p.authorship = make(map[string]*authorResult, len(results))
	for index := range results {
		p.authorship[results[index].Input.ID] = &results[index]
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
	splits["inherited_from"], splits["inheritance"] = nilIfEmpty(opts.SplitsFrom), inheritance
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
		if err := p.writer.finish(families, manifest); err != nil {
			return nil, err
		}
		p.writer = nil
	}
	progress("prepared %d units (%d items)", len(p.stats), manifest["items"])
	return manifest, nil
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
	inherited, err := p.inheritedHistory(ctx, info, records)
	if err != nil {
		return err
	}
	// context-v1: the previous request unit's visible text (a person's or an
	// agent's) and the previous selected agent reply, even an excluded one.
	var priorUser, priorAssistant string
	var priorUserRef, priorAssistantRef map[string]any
	calls := map[string]string{} // call ID: the delegation message that made it
	for _, record := range records {
		if record.kind == "delegation" && record.callID != "" {
			calls[record.callID] = record.id
		}
		if record.createdAt != "" && (p.firstAt[id] == "" || record.createdAt < p.firstAt[id]) {
			p.firstAt[id] = record.createdAt
		}
		var unit *labelingUnit
		reason, detail, duplicate := "", "", ""
		if parent := inherited[record.id]; parent != "" {
			reason, detail, duplicate = "inherited_history", "copied from the parent conversation before the fork", parent
		} else {
			p.rememberChildText(id, record)
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
			if p.writer != nil && candidate != "" {
				if err := p.writer.exclusion(record.id, id, candidate, reason, detail, duplicate); err != nil {
					return err
				}
			}
			if reply {
				priorAssistant, priorAssistantRef = record.text.String, map[string]any{"message": record.id}
				if !utf8.ValidString(priorAssistant) {
					priorAssistant, priorAssistantRef = "", nil
				}
			}
			continue
		}
		unit.context, unit.contextPrompt, unit.contextExcerpt = labelingContext(priorUser, priorAssistant)
		unit.contextRefs = map[string]any{}
		if priorUser != "" {
			unit.contextRefs["prior_user"] = priorUserRef
		}
		if priorAssistant != "" {
			unit.contextRefs["prior_assistant"] = priorAssistantRef
		}
		p.addUnit(unit)
		if p.writer != nil {
			if err := p.writer.unit(unit); err != nil {
				return err
			}
		}
		if unit.cohort == "human" || unit.cohort == "agent_in_user_role" {
			priorUser, priorUserRef = unit.visible, map[string]any{"unit": unit.id, "message": record.id}
		}
		if reply {
			priorAssistant, priorAssistantRef = record.text.String, map[string]any{"message": record.id}
		}
	}
	return nil
}

// inheritedHistory finds the records a forked conversation copied from its
// parent: the leading run of text records that repeat (same role, same text)
// a parent message from before the fork. It maps them to the parent message.
// The first text record that is not a copy ends the run.
func (p *labelingPass) inheritedHistory(ctx context.Context, info *labelingConversation, records []labelingRecord) (map[string]string, error) {
	inherited := map[string]string{}
	if info.parent == "" || p.conversations[info.parent] == nil {
		return inherited, nil
	}
	index := p.parents[info.parent]
	if index == nil {
		parentRecords, err := readLabelingRecords(ctx, p.tx, info.parent)
		if err != nil {
			return nil, err
		}
		index = map[string]labelingParentMessage{}
		for _, record := range parentRecords {
			if !record.selected || !record.text.Valid || strings.TrimSpace(record.text.String) == "" {
				continue
			}
			key := record.role + "\x00" + textKey(record.text.String)
			if _, seen := index[key]; !seen {
				index[key] = labelingParentMessage{record.id, record.createdAt}
			}
		}
		p.parents[info.parent] = index
	}
	for _, record := range records {
		if !record.text.Valid || strings.TrimSpace(record.text.String) == "" {
			continue
		}
		match, ok := index[record.role+"\x00"+textKey(record.text.String)]
		if !ok || info.started != "" && match.createdAt != "" && match.createdAt > info.started {
			break
		}
		inherited[record.id] = match.id
	}
	return inherited, nil
}

// labelingAnswer is a structured answer to a question an agent asked: a
// prewritten option submitted, or a free-text answer.
type labelingAnswer struct {
	kind, status, source, question, option, text, actor string
	titleStart, titleEnd, start, end                    int
	questionConversation, questionMessage               string
}

// toolResultAnswers reads the answers to question tools whose results this
// conversation recorded, by result message. A result that cannot be parsed
// maps to nil: a counted coverage gap.
func (p *labelingPass) toolResultAnswers(ctx context.Context, conversation string) (map[string][]labelingAnswer, error) {
	answers := map[string][]labelingAnswer{}
	calls, err := queryMapsContext(ctx, p.tx, `SELECT t.tool_name,t.result_message_id,COALESCE(t.call_message_id,'') call_message_id,
		COALESCE(c.text,'') call_text,COALESCE(r.text,'') result_text FROM tool_calls t
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
			if len(parsed) == 0 {
				p.counters["unparsed_answer_shape:"+questionAnswerShape(asString(call["call_text"]), asString(call["result_text"]))]++
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
	if parsed, asked := answers[record.id]; asked {
		if len(parsed) == 0 {
			return nil, "human_answer_in_tool_result", "a person's answer that reached the agent only as a tool result, not parsed", nil
		}
		if err := p.actions(record.id, conversation, parsed); err != nil {
			return nil, "", "", err
		}
		return nil, "tool_result_answer", "answers recorded as actions", nil
	}
	switch {
	case !record.selected:
		return nil, "unselected_branch", "", nil
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
		result := p.authorship[record.id]
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
			rendered = humanPieces(text, result.Spans)
		}
		flattened := []labelingAnswer{}
		for _, answer := range result.Answers {
			flattened = append(flattened, labelingAnswer{kind: "submitted_option", status: "recognized_question_form", source: "flattened_message",
				question: answer.Title, option: answer.Option, actor: unit.actor, titleStart: answer.TitleStart, titleEnd: answer.TitleEnd,
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
		pieces, _, secrets := cleanPieces(text, []labelingPiece{{Role: pieceTarget, Status: status, Start: start, End: end}})
		rendered = renderPieces(text, pieces)
		rendered.Secrets = secrets
		if candidate == "thinking" {
			unit.channel = "analysis"
		}
		if candidate == "delegation" {
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
	// Chunks holding only markers carry nothing to label: merge each into the
	// chunk before it when that fits, otherwise drop it.
	for _, chunk := range chunkLabelingText(unit.visible, labelingChunkChars, unit.markers) {
		if len(chunkSourceRanges(unit.pieces, chunk)) > 0 {
			unit.chunks = append(unit.chunks, chunk)
			continue
		}
		if last := len(unit.chunks) - 1; last >= 0 && utf8.RuneCountInString(unit.visible[unit.chunks[last].Start:chunk.End]) <= labelingChunkChars {
			unit.chunks[last].End = chunk.End
			continue
		}
		unit.droppedChunks++
		p.counters["marker_only_chunks"]++
	}
	unit.length = "short"
	if len(unit.chunks) > 1 {
		unit.length = "long"
	}
	return unit, "", "", nil
}

// actions records answers as actions of the message that submitted them.
func (p *labelingPass) actions(message, conversation string, answers []labelingAnswer) error {
	for position, answer := range answers {
		p.counters["actions_"+answer.source]++
		if p.writer != nil {
			if err := p.writer.action(message, conversation, position, answer); err != nil {
				return err
			}
		}
	}
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
	pieces, _, secrets := cleanPieces(text, pieces)
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
	p.stats = append(p.stats, labelingUnitStat{unit.conversation, unit.cohort, unit.provider, depth, unit.length, len(unit.chunks)})
}

// labelingFamily is the family (splits-v1) a conversation belongs to.
type labelingFamily struct{ id, split, first string }

// labelingCopyLinkBytes is the shortest copied span that links two
// conversations into one family: shorter reuse (a repeated instruction) would
// chain unrelated work together.
var labelingCopyLinkBytes = 200

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
	smallest, first, old := map[string]string{}, map[string]string{}, map[string]map[string]bool{}
	oldBy := map[string]string{} // family root: split of its smallest previously assigned member
	sorted := slices.Clone(ids)
	sort.Strings(sorted)
	for _, id := range sorted {
		root := find(id)
		if smallest[root] == "" {
			smallest[root] = id
		}
		at := p.conversations[id].started
		if at == "" {
			at = p.firstAt[id]
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
	stats := map[string]int{}
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
				stats["conflicting_conversations"]++
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
			"note": "delegation units that repeat a child's message (equal or containing, whitespace-normalized) are kept with duplicate_of set; inherited history is excluded"},
		"invalid_utf8": p.exclusionTotal("invalid_utf8"),
		"actions": map[string]any{"question_forms": p.counters["actions_flattened_message"], "tool_results": p.counters["actions_tool_result"],
			"unparsed_tool_results": p.exclusionTotal("human_answer_in_tool_result"), "unparsed_shapes": p.prefixed("unparsed_answer_shape:")},
		"redacted_secrets":          p.counters["redacted_secrets"],
		"unrecognized_harness_tags": p.counters["unrecognized_harness_tags"],
		"marker_only_chunks":        p.counters["marker_only_chunks"],
		"splits":                    map[string]any{"families": len(familyIDs), "family_sizes": familyStats, "coverage": coverage},
	}
}

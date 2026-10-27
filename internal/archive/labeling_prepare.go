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
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"
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
	Progress         io.Writer
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
	chunks                                                                      []labelingChunk
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
	conversations  map[string]*labelingConversation
	representative map[string]string
	lineage        *usageConversationAttribution
	authorship     map[string]*authorResult
	mainSession    map[string]string
	sessionKind    map[string]string
	childOwners    map[string]bool
	current        map[string]bool
	// owned maps a delegated child (conversation "c:" or embedded session
	// "s:") to its messages by text, to find delegation duplicates.
	owned  map[string]map[string]string
	checks []labelingDuplicateCheck
	// units counts cohort, provider, depth, length: units, items, characters.
	units      map[[4]string]*[3]int
	exclusions map[[2]string]int
	counters   map[string]int
	stats      []labelingUnitStat
	firstAt    map[string]string
}

// labelingDuplicateCheck compares a delegation, or its result, with the
// messages of the child its delegation message started.
type labelingDuplicateCheck struct{ messageID, delegation, kind, payload, whole string }

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

// openLabelingSnapshot opens the catalog strictly read-only on one
// connection and begins the one read transaction every preparation read
// runs in, so they all see one snapshot, including the WAL. It never
// initializes or migrates the catalog, and takes no lock beyond a reader's.
func openLabelingSnapshot(ctx context.Context, path string) (*sql.DB, *sql.Tx, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, nil, err
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?mode=ro&_pragma=busy_timeout(10000)&_pragma=query_only(1)")
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, tx, nil
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
	db, tx, err := openLabelingSnapshot(ctx, opts.CatalogPath)
	if err != nil {
		return nil, fmt.Errorf("open catalog read-only: %w", err)
	}
	defer db.Close()
	defer tx.Rollback()
	snapshot := map[string]any{"started_at": formatTime(started), "catalog": opts.CatalogPath, "access": "mode=ro, query_only, one read transaction on one connection"}
	version, err := labelingMeta(ctx, tx, "schema_version")
	if err != nil {
		return nil, err
	}
	if version != strconv.Itoa(catalogSchemaVersion) {
		return nil, fmt.Errorf("catalog %s has schema version %q; labeling reads only schema %d, the version this build writes, so use a matching build (the catalog was not modified)",
			opts.CatalogPath, version, catalogSchemaVersion)
	}
	snapshot["schema_version"] = catalogSchemaVersion
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
	p := &labelingPass{tx: tx, conversations: map[string]*labelingConversation{}, mainSession: map[string]string{}, sessionKind: map[string]string{},
		childOwners: map[string]bool{}, current: map[string]bool{}, owned: map[string]map[string]string{}, units: map[[4]string]*[3]int{},
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
	if p.lineage, err = usageConversationRootsFrom(tx, kept); err != nil {
		return nil, err
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
	duplicates := map[string]int{}
	for _, check := range p.checks {
		for _, owner := range delegated[check.delegation] {
			match := p.owned[owner][check.payload]
			if match == "" {
				match = p.owned[owner][check.whole]
			}
			if match == "" {
				continue
			}
			duplicates[check.kind]++
			if p.writer != nil {
				if err := p.writer.duplicate(check.messageID, match, check.kind+"_repeats_child_message"); err != nil {
					return nil, err
				}
			}
			break
		}
	}

	progress("assigning families and splits")
	freshSince := formatTime(started.Add(-time.Duration(opts.FreshDays) * 24 * time.Hour))
	families := labelingFamilies(ids, p, sessions, launches, results, freshSince)
	unavailable := int64(0)
	for _, id := range scope {
		unavailable += unavailableLedger[id]
	}
	manifest := p.manifest(opts, snapshot, selection, scope, families, duplicates)
	manifest["reasoning"] = map[string]any{
		"extraction_version": reasoningExtractionVersion, "pending_conversations": p.counters["reasoning_pending_conversations"],
		"unavailable_records_ledger": unavailable, "unavailable_records_seen": p.exclusions[[2]string{"reasoning_unavailable", ""}],
		"note": "Thinking in conversations whose extraction is pending is excluded; unavailable (redacted or encrypted) reasoning is never inferred.",
	}
	manifest["authorship"] = map[string]any{
		"version": authorshipVersion, "computed": "in memory on this snapshot (authorshipInputs + classifyInputs); the stored ledger was not read or written",
		"classified_messages": len(results), "missing": p.exclusionTotal("authorship_missing"),
		"stored_generation": generations["authorship_generation"], "snapshot_generation": want, "stored_ledger_current": want == generations["authorship_generation"],
		"question_form_actions": p.counters["actions"],
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
	// Answers that reached the agent only as a tool result are a coverage gap.
	answers := map[string]string{}
	questions, err := queryMapsContext(ctx, p.tx, `SELECT tool_name,result_message_id FROM tool_calls WHERE conversation_id=? AND result_message_id IS NOT NULL
		AND (tool_name LIKE '%question%' OR tool_name LIKE '%user_input%')`, id)
	if err != nil {
		return err
	}
	for _, call := range questions {
		switch authorshipToolName(firstString(call["tool_name"])) {
		case "request_user_input", "request_user_input_async", "askuserquestion", "ask_question":
			answers[firstString(call["result_message_id"])] = firstString(call["tool_name"])
		}
	}
	records, err := readLabelingRecords(ctx, p.tx, id)
	if err != nil {
		return err
	}
	// context-v1: the previous human unit's visible text and the previous
	// selected agent reply, even an empty or excluded one.
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
		p.rememberChildText(id, record)
		unit, reason, detail, err := p.record(id, record, answers, calls)
		if err != nil {
			return err
		}
		reply := record.selected && record.kind == "message" && (record.role == "assistant" || record.role == "agent")
		if unit == nil {
			// Per-record exclusions are kept for kinds that could have been
			// units; the rest (tool calls and results, metadata) are counted.
			candidate := labelingCandidate(record)
			p.exclude(reason, candidate, 1)
			if p.writer != nil && candidate != "" {
				if err := p.writer.exclusion(record.id, id, candidate, reason, detail); err != nil {
					return err
				}
			}
			if reply {
				priorAssistant, priorAssistantRef = record.text.String, map[string]any{"message": record.id}
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
		if unit.cohort == "human" {
			priorUser, priorUserRef = unit.visible, map[string]any{"unit": unit.id, "message": record.id}
		}
		if reply {
			priorAssistant, priorAssistantRef = record.text.String, map[string]any{"message": record.id}
		}
	}
	return nil
}

// rememberChildText indexes a delegated child's messages by text.
func (p *labelingPass) rememberChildText(conversation string, record labelingRecord) {
	if !record.selected || record.kind != "message" || !record.text.Valid {
		return
	}
	for _, owner := range []string{"c:" + conversation, "s:" + record.session} {
		if !p.childOwners[owner] {
			continue
		}
		if p.owned[owner] == nil {
			p.owned[owner] = map[string]string{}
		}
		if key := textKey(record.text.String); p.owned[owner][key] == "" {
			p.owned[owner][key] = record.id
		}
	}
}

// record decides one message: a unit, or the reason it is excluded.
func (p *labelingPass) record(conversation string, record labelingRecord, answers, calls map[string]string) (*labelingUnit, string, string, error) {
	text := record.text.String
	candidate := labelingCandidate(record)
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
	case answers[record.id] != "":
		return nil, "human_answer_in_tool_result", "a person's answer that reached the agent only as a tool result", nil
	case candidate == "" && record.kind == "message":
		return nil, "role_" + record.role, "", nil
	case candidate == "":
		return nil, "kind_" + record.kind, "", nil
	case strings.TrimSpace(text) == "":
		return nil, "empty_text", "", nil
	case !utf8.ValidString(text):
		return nil, "invalid_utf8", "", nil
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
	switch candidate {
	case "user":
		result := p.authorship[record.id]
		if result == nil || len(result.Spans) == 0 {
			if record.createdAt == "" {
				return nil, "authorship_missing", "the message has no send time, which classification needs", nil
			}
			return nil, "authorship_missing", "the classifier returned no result for this message", nil
		}
		for position, answer := range result.Answers {
			p.counters["actions"]++
			if p.writer != nil {
				if err := p.writer.action(record.id, conversation, position, answer); err != nil {
					return nil, "", "", err
				}
			}
		}
		automated := result.Spans[0].Category == spanAutomated
		if automated || p.sessionKind[record.session] == "subagent" {
			unit.cohort, unit.actor, unit.authorship = "agent_in_user_role", "agent", "Embedded sub-agent session"
			if automated {
				unit.authorship = result.Spans[0].Reason
			}
			agentUserPieces(unit)
			break
		}
		human := humanPieces(text, result.Spans)
		if human.Exclude != "" {
			if human.Exclude == "no_target_text" && len(result.Answers) > 0 {
				return nil, "action_only", "the message only submitted a question-form option, recorded as an action", nil
			}
			return nil, human.Exclude, "", nil
		}
		unit.cohort, unit.actor, unit.pieces, unit.visible = "human", "human", human.Pieces, human.Visible
	default:
		unit.cohort, unit.actor = candidate, "agent"
		status := map[string]string{"agent_reply": "agent_text", "delegation": "delegation_text", "thinking": "thinking_text"}[candidate]
		start := len(text) - len(strings.TrimLeft(text, " \t\r\n"))
		end := len(strings.TrimRight(text, " \t\r\n"))
		unit.pieces = []labelingPiece{{Role: pieceTarget, Status: status, Start: start, End: end, VisibleStart: 0, VisibleEnd: end - start}}
		unit.visible = text[start:end]
		if candidate == "thinking" {
			unit.channel = "analysis"
		}
		if candidate == "delegation" {
			delegation := record.id
			if record.kind == "delegation_result" {
				delegation = calls[record.callID]
			}
			p.checks = append(p.checks, labelingDuplicateCheck{record.id, delegation, record.kind, textKey(delegationPayload(text)), textKey(text)})
		}
	}
	unit.chunks = chunkLabelingText(unit.visible, labelingChunkChars)
	unit.length = "short"
	if len(unit.chunks) > 1 {
		unit.length = "long"
	}
	return unit, "", "", nil
}

// agentUserPieces makes an agent's user-role message a unit: the whole
// message after its leading harness blocks, which are recorded as dropped.
func agentUserPieces(unit *labelingUnit) {
	text := unit.text
	start := 0
	for _, block := range leadingHarnessBlocks(text) {
		unit.pieces = append(unit.pieces, labelingPiece{Role: pieceDropped, Status: spanHarness, Category: spanHarness, Start: block.Start, End: block.End,
			VisibleStart: -1, VisibleEnd: -1})
		start = block.End
	}
	start += len(text[start:]) - len(strings.TrimLeft(text[start:], " \t\r\n"))
	end := max(start, len(strings.TrimRight(text, " \t\r\n")))
	unit.pieces = append(unit.pieces, labelingPiece{Role: pieceTarget, Status: "agent_text", Start: start, End: end, VisibleStart: 0, VisibleEnd: end - start})
	unit.visible = text[start:end]
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

// labelingFamilies groups conversations that share text or execution, so a
// split never separates them: mirrors and aliases, roots and runs, parents,
// delegated children, launched runs, and copies found by authorship. Each
// family is fresh when its first activity falls within the fresh window, and
// otherwise train, dev or test by the hash of its ID (80/10/10).
func labelingFamilies(ids []string, p *labelingPass, sessions []map[string]any, launches []conversationLaunch, results []authorResult, freshSince string) map[string]labelingFamily {
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
			if span.Source != "" {
				union(result.Input.ConversationID, span.Source)
			}
		}
	}
	smallest, first := map[string]string{}, map[string]string{}
	for _, id := range ids {
		root := find(id)
		if smallest[root] == "" || id < smallest[root] {
			smallest[root] = id
		}
		at := p.conversations[id].started
		if at == "" {
			at = p.firstAt[id]
		}
		if at != "" && (first[root] == "" || at < first[root]) {
			first[root] = at
		}
	}
	families := map[string]labelingFamily{}
	for _, stat := range p.stats {
		if _, done := families[stat.conversation]; done {
			continue
		}
		root := find(stat.conversation)
		family := labelingFamily{id: labelingID("f_", labelingSplitsVersion, smallest[root]), first: first[root]}
		family.split = labelingSplit(family.id, family.first, freshSince)
		families[stat.conversation] = family
	}
	return families
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
func (p *labelingPass) manifest(opts labelingPrepareOptions, snapshot, selection map[string]any, scope []string, families map[string]labelingFamily, duplicates map[string]int) map[string]any {
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
		"contracts": map[string]any{"units": labelingUnitsVersion, "human_pieces": humanPiecesVersion, "marker": labelingMarker, "context": labelingContextVersion,
			"chunking": labelingChunkVersion, "chunk_chars": labelingChunkChars, "splits": labelingSplitsVersion,
			"taxonomy": map[string]any{"version": labelingTaxonomyVersion, "sha256": hex.EncodeToString(sum[:])}, "prompt": nil,
			"channel": "analysis for thinking, unspecified otherwise: the catalog keeps no final/commentary channel"},
		"units": units, "items": items, "cohorts": cohorts, "counts": counts,
		"long":       map[string]any{"units": long["units"], "items": long["items"], "merge": "unvalidated"},
		"exclusions": exclusions, "excluded_records": excluded,
		"duplicates": map[string]any{"delegation": duplicates["delegation"], "delegation_result": duplicates["delegation_result"],
			"result_records": p.exclusionTotal("result_record"), "note": "delegation units that repeat a child's message are kept with duplicate_of set"},
		"invalid_utf8": p.exclusionTotal("invalid_utf8"),
		"actions":      p.counters["actions"],
		"splits":       map[string]any{"families": len(familyIDs), "coverage": coverage},
	}
}

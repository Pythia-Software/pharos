package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	labelrt "github.com/gbdubs/pharos/internal/labeler"
)

// The agent events pass runs the JAYHAWK verifier on this Mac
// (docs/agent-events.md): for each agent reply and thinking block, as
// labeling prepare builds them, it finds keyword candidates, frames them
// (agent_event_candidates.go), scores them on the local labeler runtime and
// replaces the message's rows in agent_event_messages and agent_events. A
// message is scored again only when its text or source, or the verifier,
// changed. Conversations to look at come from a dirty table fed by the same
// triggers as the labels ledger's, and from a structure fingerprint.

// agentEventsLedgerDDL is created by the first run: a library that never runs
// the verifier has none of it.
const agentEventsLedgerDDL = `
CREATE TABLE IF NOT EXISTS agent_events_conversations (
  conversation_id TEXT PRIMARY KEY,
  structure TEXT NOT NULL,
  built_at TEXT NOT NULL,
  since TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS agent_events_dirty (
  conversation_id TEXT PRIMARY KEY,
  token TEXT NOT NULL
);
`

// agentEventsLedgerVersion 2 records each conversation's horizon (since:
// the earliest time it was read from, ” for all of it).
const agentEventsLedgerVersion = "2"

// agentEventStoreFloor is the lowest probability stored: rows between it and
// agentEventThreshold are not events, but let a later threshold be studied
// without scoring again. Imports use the same floor.
const agentEventStoreFloor = 0.3

// agentEventRunOutputs are the verifier's outputs the pass reads.
var agentEventRunOutputs = []string{"verdict", "sentences"}

func (c *Catalog) ensureAgentEventsLedger(ctx context.Context) error {
	if version, err := c.metaValue(ctx, "agent_events_ledger_version"); err == nil && version == agentEventsLedgerVersion {
		return nil
	}
	return c.writeTransaction(ctx, "create the agent events ledger", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, agentEventsLedgerDDL); err != nil {
			return err
		}
		var since int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('agent_events_conversations') WHERE name='since'`).Scan(&since); err != nil {
			return err
		}
		if since == 0 {
			// Version 1 did not record it; '' would claim a whole conversation
			// was read, so its rows are rebuilt by the next run.
			if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_events_conversations ADD COLUMN since TEXT NOT NULL DEFAULT '~'`); err != nil {
				return err
			}
		}
		for _, statement := range itemDirtyTriggers("agent_events_dirty", "agent_events_dirty_") {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("%s: %w", statement[:60], err)
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('agent_events_ledger_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, agentEventsLedgerVersion)
		return err
	})
}

// AgentEventSettings locate the verifier. The worker, its runtime and the
// budget are the labeler's (LabelerSettings): both models share one runtime.
type AgentEventSettings struct {
	Enabled bool
	// ModelDir holds the verifier's manifest.json, ONNX files and tokenizer.
	ModelDir, Variant string
	// Days is how far back the pass reads (0: everything). Scoring is the
	// cost, and the dashboard's windows are weeks and months.
	Days int
	// Threads per worker (agent_events_threads); 0 uses labels_threads.
	Threads int
}

func agentEventSettingsFrom(root map[string]any, base string) AgentEventSettings {
	dir := expandPath(os.Getenv("PHAROS_AGENT_EVENTS_MODEL_DIR"))
	if dir == "" {
		dir = resolvePath(base, stringValue(root, "agent_events_model_dir", ""))
	}
	return AgentEventSettings{Enabled: boolValue(root, "agent_events_enabled", false), ModelDir: dir, Variant: stringValue(root, "agent_events_variant", "int8"),
		Days: intValue(root, "agent_events_days", 90), Threads: intValue(root, "agent_events_threads", 0)}
}

// labeler is the labeler settings the pass runs with: the labeler's worker,
// runtime and budget, with its own threads per worker.
func (settings AgentEventSettings) labeler(labels LabelerSettings) LabelerSettings {
	events := labels
	events.Threads = settings.threads(labels)
	events.PeerThreads = max(1, labels.Workers) * max(1, labels.Threads)
	return events
}

// threads is the pass's threads per worker.
func (settings AgentEventSettings) threads(labels LabelerSettings) int {
	if settings.Threads > 0 {
		return settings.Threads
	}
	return labels.Threads
}

// withPeer is labels with the pass's workers counted beside them.
func (settings AgentEventSettings) withPeer(labels LabelerSettings) LabelerSettings {
	labels.PeerThreads = max(1, labels.Workers) * max(1, settings.threads(labels))
	return labels
}

// since is the horizon Days sets (zero: none).
func (settings AgentEventSettings) since() time.Time {
	if settings.Days <= 0 {
		return time.Time{}
	}
	return time.Now().AddDate(0, 0, -settings.Days)
}

// withAssets fills in the verifier's directory from this Mac's download
// when the configuration names none.
func (settings AgentEventSettings) withAssets() AgentEventSettings {
	if settings.ModelDir == "" && labelsAssets.AgentEventsModel.present() {
		settings.ModelDir = labelsAssets.AgentEventsModel.target()
	}
	return settings
}

// missingAssets is what the pass still needs downloaded: the runtime the
// labeler's worker loads, and the verifier.
func (settings AgentEventSettings) missingAssets(labels LabelerSettings) []labelsAsset {
	missing := []labelsAsset{}
	if labels.withAssets().needsRuntime() {
		missing = append(missing, labelsAssets.Runtime)
	}
	if settings.withAssets().ModelDir == "" {
		missing = append(missing, labelsAssets.AgentEventsModel)
	}
	return missing
}

// waiting is why the pass waits for a download ("" when it does not).
func (settings AgentEventSettings) waiting(labels LabelerSettings) string {
	parts := []string{}
	for _, asset := range settings.missingAssets(labels) {
		what := "the agent events model"
		if asset.Name == labelsAssets.Runtime.Name {
			what = "ONNX Runtime"
		}
		parts = append(parts, what+": "+labelsAssetStatus(asset))
	}
	if len(parts) == 0 {
		return ""
	}
	return "waiting for the download (" + strings.Join(parts, "; ") + ")"
}

// agentEventVerifier is a loaded verifier and what its manifest says beyond
// what the runtime reads.
type agentEventVerifier struct {
	model *labelrt.Model
	// identity names what scored a row: the model's identity and the
	// contract its inputs were made under.
	identity    string
	definitions map[string]string
}

// agentEventsContract is what a message's candidates and inputs depend on
// besides the model: the framing, the lexicon, the units and the sentences.
func agentEventsContract() string {
	lexicon, _ := agentEventLexicon()
	return strings.Join([]string{agentEventFraming.Contract, lexicon, labelingUnitsVersion, labelingSentenceVersion}, "+")
}

// loadAgentEventVerifier loads and checks a verifier directory: its framing
// and input contract (LoadFramedModel), and that it was trained on this
// build's lexicon, decides at the datasets' threshold and defines every kind.
func loadAgentEventVerifier(dir, variant string) (*agentEventVerifier, error) {
	model, err := labelrt.LoadFramedModel(dir, variant, agentEventFraming)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", labelrt.ErrModelUnavailable, err)
	}
	var manifest struct {
		Lexicon     string            `json:"lexicon"`
		Definitions map[string]string `json:"definitions"`
		Decision    struct {
			Threshold float64 `json:"threshold"`
		} `json:"decision"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("%w: manifest.json: %v", labelrt.ErrModelUnavailable, err)
	}
	lexicon, kinds := agentEventLexicon()
	switch {
	case manifest.Lexicon != lexicon:
		return nil, fmt.Errorf("%w: the verifier was trained on candidates from lexicon %q; this build finds them with %q", labelrt.ErrModelUnavailable, manifest.Lexicon, lexicon)
	case manifest.Decision.Threshold != agentEventThreshold:
		return nil, fmt.Errorf("%w: the verifier decides at %g; the datasets count events at %g", labelrt.ErrModelUnavailable, manifest.Decision.Threshold, agentEventThreshold)
	}
	for _, kind := range kinds {
		if manifest.Definitions[kind.Kind] == "" {
			return nil, fmt.Errorf("%w: the manifest does not define %s", labelrt.ErrModelUnavailable, kind.Kind)
		}
	}
	if err := model.HasOutputs(agentEventRunOutputs); err != nil {
		return nil, fmt.Errorf("%w: %v", labelrt.ErrModelUnavailable, err)
	}
	return &agentEventVerifier{model: model, identity: labelsModelIdentity(model) + "|" + agentEventsContract(), definitions: manifest.Definitions}, nil
}

// errAgentEventsRunning reports an events run already in progress.
var errAgentEventsRunning = errors.New("the agent events pass is already running")

// AgentEventsRunOptions bound one run.
type AgentEventsRunOptions struct {
	// Limit stops before a conversation once this many messages were scored
	// (0: no limit); the rest stay pending.
	Limit int
	// Full looks at every conversation; only messages whose inputs changed
	// are scored again either way.
	Full bool
	// Conversations restricts the run (tests and diagnostics).
	Conversations []string
	// DryRun builds and frames candidates and counts them, but starts no
	// worker and stores nothing.
	DryRun bool
	// Since, when set, leaves older messages unread: their stored rows stay,
	// and conversations that ended before it are not looked at.
	Since    time.Time
	Progress io.Writer
}

// AgentEventsRunStats describe a run.
type AgentEventsRunStats struct {
	Conversations, ConversationsBuilt, ConversationsRemoved, ConversationsFailed int
	MessagesScored, MessagesUnchanged, MessagesRemoved, MessagesBefore           int
	Candidates, Stored, Events                                                   int
	Cut, Complete                                                                bool
	Infer, Total                                                                 time.Duration
}

// agentEventsConversation is one conversation's pending work.
type agentEventsConversation struct {
	id, token, structure string
	// since is the horizon the conversation is read from ('' for all of it).
	since    string
	messages []*agentEventsMessage
	stale    []string
	// retimed are messages whose rows stand but whose time changed.
	retimed []*agentEventsMessage
	// unchanged counts messages whose stored rows are current; before
	// messages older than the run's horizon.
	unchanged, before int
}

// agentEventsMessage is one message to store: its candidates and their rows.
type agentEventsMessage struct {
	id, conversation, source, text, createdAt, hash string
	sentences                                       [][2]int
	candidates                                      []agentEventCandidate
	rows                                            []labelrt.Framed
	// windows are each candidate's first window sentence.
	windows []int
	outputs []labelrt.RowOutputs
}

// agentEventsChunkRows is how many candidates the pass gathers before it
// scores them and stores their conversations.
var agentEventsChunkRows = 512

// RunAgentEvents brings agent events up to date: it rebuilds the units of
// conversations whose inputs may have changed (newest first), scores the
// candidates of messages whose inputs differ from what their stored rows
// were made from, and stores each conversation's results with its ledger
// row in one transaction. It stops between chunks when ctx ends, keeping
// what was stored; the next run resumes.
func (c *Catalog) RunAgentEvents(ctx context.Context, labels LabelerSettings, settings AgentEventSettings, opts AgentEventsRunOptions) (*AgentEventsRunStats, error) {
	started := time.Now()
	stats := &AgentEventsRunStats{}
	defer func() { stats.Total = time.Since(started) }()
	progress := labelingProgress(opts.Progress, started)
	labels, settings = labels.withAssets(), settings.withAssets()
	if settings.ModelDir == "" {
		return stats, fmt.Errorf("%w: the verifier is %s; run pharos agent-events fetch, or set agent_events_model_dir (PHAROS_AGENT_EVENTS_MODEL_DIR)",
			labelrt.ErrModelUnavailable, labelsAssetStatus(labelsAssets.AgentEventsModel))
	}
	if labels.Worker == "" && !opts.DryRun {
		return stats, fmt.Errorf("%w: set labels_worker (or PHAROS_LABELS_WORKER)", labelrt.ErrModelUnavailable)
	}
	if !c.agentEventsRun.TryLock() {
		return stats, errAgentEventsRunning
	}
	defer c.agentEventsRun.Unlock()
	if c.Path != "" {
		lease, err := fileLease(canonical(c.Path)+".agent-events.lock", true)
		if err != nil {
			return stats, fmt.Errorf("%w: %v", errAgentEventsRunning, err)
		}
		defer lease.Close()
	}
	verifier, err := loadAgentEventVerifier(settings.ModelDir, settings.Variant)
	if err != nil {
		return stats, err
	}
	if err := c.ensureAgentEventsLedger(ctx); err != nil {
		return stats, err
	}
	builder, err := newLabelsItemBuilder(ctx, c.DB)
	if err != nil {
		return stats, err
	}
	jobs, err := c.agentEventsWork(ctx, builder, verifier.identity, opts, stats)
	if err != nil {
		return stats, err
	}
	progress("agent events: %d conversations to look at of %d", len(jobs), stats.Conversations)
	reportWork(ctx, "finding agent events", "conversation", 0, len(jobs))

	var runner *labelrt.Runner
	release := func() {}
	defer func() {
		if runner != nil && ctx.Err() != nil {
			// A stop: batches already queued at the workers must not run on,
			// and the workers' memory goes back to the budget now.
			c.labelerManager(labels).Kill(runner)
		}
		release()
	}()
	pending, rows := []*agentEventsConversation{}, 0
	flush := func() error {
		defer func() { pending, rows = pending[:0], 0 }()
		if opts.DryRun || len(pending) == 0 {
			return nil
		}
		framed := make([]labelrt.Framed, 0, rows)
		for _, conversation := range pending {
			for _, message := range conversation.messages {
				framed = append(framed, message.rows...)
			}
		}
		if len(framed) > 0 {
			if runner == nil {
				cfg := labelrt.RunnerConfig{Worker: labels.Worker, Args: runtimeArgs(labels), Workers: max(1, labels.Workers), Threads: labels.Threads,
					MaxBatch: max(1, labels.MaxBatch), Backoff: labelsWorkerBackoff,
					OnRestart: func(slot int, cause error) { progress("agent events: restarting worker %d after: %v", slot, cause) }}
				var err error
				if runner, release, err = c.labelerManager(labels).Runner(ctx, verifier.model, cfg); err != nil {
					runner, release = nil, func() {}
					return err
				}
			}
			inferStarted := time.Now()
			outputs, err := runner.Run(ctx, framed, agentEventRunOutputs, labels.BatchTokens)
			stats.Infer += time.Since(inferStarted)
			if err != nil {
				return err
			}
			for _, conversation := range pending {
				for _, message := range conversation.messages {
					message.outputs, outputs = outputs[:len(message.rows)], outputs[len(message.rows):]
				}
			}
		}
		return c.storeAgentEvents(ctx, verifier.identity, pending, stats)
	}
	done := 0
	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		if opts.Limit > 0 && stats.MessagesScored >= opts.Limit {
			stats.Cut = true
			break
		}
		conversation, err := c.agentEventsConversation(ctx, builder, verifier, job, opts.Since)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			stats.ConversationsFailed++
			progress("agent events: conversation %s: %v", job.id, err)
			continue
		}
		stats.ConversationsBuilt++
		stats.MessagesUnchanged += conversation.unchanged
		stats.MessagesBefore += conversation.before
		for _, message := range conversation.messages {
			stats.MessagesScored++
			stats.Candidates += len(message.rows)
			rows += len(message.rows)
		}
		pending = append(pending, conversation)
		if rows >= agentEventsChunkRows || len(pending) >= agentEventsChunkRows {
			if err := flush(); err != nil {
				return stats, err
			}
			progress("agent events: %d of %d conversations, %d messages scored, %d candidates, %d events", done+1, len(jobs), stats.MessagesScored, stats.Candidates, stats.Events)
		}
		done++
		reportWork(ctx, "finding agent events", "conversation", done, len(jobs))
	}
	if err := flush(); err != nil {
		return stats, err
	}
	if ctx.Err() != nil {
		return stats, ctx.Err()
	}
	if !stats.Cut && stats.ConversationsFailed == 0 && len(opts.Conversations) == 0 && !opts.DryRun {
		var dirty int
		if err := c.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_events_dirty`).Scan(&dirty); err != nil {
			return stats, err
		}
		stats.Complete = dirty == 0
	}
	progress("agent events: %d messages scored (%d unchanged), %d candidates, %d events", stats.MessagesScored, stats.MessagesUnchanged, stats.Candidates, stats.Events)
	return stats, nil
}

// agentEventsWork chooses the conversations to look at, newest first: dirty
// ones, ones never built or whose structure changed, and ones with a row
// another verifier (or an import) made; every one for Full. Conversations
// no longer in the catalog lose their rows.
func (c *Catalog) agentEventsWork(ctx context.Context, builder *labelsItemBuilder, identity string, opts AgentEventsRunOptions, stats *AgentEventsRunStats) ([]*agentEventsConversation, error) {
	p := builder.p
	read := func(query string, args ...any) (map[string]string, error) {
		out := map[string]string{}
		rows, err := queryMapsContext(ctx, c.DB, query, args...)
		for _, row := range rows {
			out[firstString(row["k"])] = firstString(row["v"])
		}
		return out, err
	}
	stored, err := read(`SELECT conversation_id k,structure v FROM agent_events_conversations`)
	if err != nil {
		return nil, err
	}
	horizons, err := read(`SELECT conversation_id k,since v FROM agent_events_conversations`)
	if err != nil {
		return nil, err
	}
	tokens, err := read(`SELECT conversation_id k,token v FROM agent_events_dirty`)
	if err != nil {
		return nil, err
	}
	since := ""
	if !opts.Since.IsZero() {
		since = formatTime(opts.Since)
	}
	other, err := read(`SELECT DISTINCT conversation_id k,'' v FROM agent_event_messages WHERE (model<>? OR input_hash IS NULL) AND (?='' OR occurred_at IS NULL OR occurred_at>=?)`, identity, since, since)
	if err != nil {
		return nil, err
	}
	// An unknown end is not the start: such a conversation may run into the
	// horizon, so it stays eligible.
	ended, err := read(`SELECT id k,COALESCE(ended_at,'') v FROM conversations`)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for id := range p.conversations {
		ids = append(ids, id)
	}
	stats.Conversations = len(ids)
	all := opts.Full
	if len(opts.Conversations) > 0 {
		ids = slices.DeleteFunc(slices.Clone(opts.Conversations), func(id string) bool { return p.conversations[id] == nil })
		all = true
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := p.conversations[ids[i]], p.conversations[ids[j]]
		if a.started != b.started {
			return a.started > b.started
		}
		return ids[i] < ids[j]
	})
	structures := map[string]string{}
	for _, id := range ids {
		structures[id] = labelsStructure(p, builder.sessions, id)
	}
	// A fork's units depend on its parent's messages (inherited history), so
	// a conversation whose inputs changed takes its forks along, as labelsWork
	// does. A follower gets a dirty mark of its own first, which only its own
	// build clears, so a limited run that stops before it keeps it pending.
	children := map[string][]string{}
	for id, info := range p.conversations {
		if info.parent != "" {
			children[info.parent] = append(children[info.parent], id)
		}
	}
	queue, followed := []string{}, []string{}
	for _, id := range ids {
		if previous, built := stored[id]; tokens[id] != "" || built && previous != structures[id] {
			queue = append(queue, id)
		}
	}
	for seen := map[string]bool{}; len(queue) > 0; queue = queue[1:] {
		for _, child := range children[queue[0]] {
			if !seen[child] && tokens[child] == "" {
				seen[child] = true
				followed = append(followed, child)
				queue = append(queue, child)
			}
		}
	}
	if opts.DryRun {
		// Counted as work, marked by nothing.
		for _, id := range followed {
			tokens[id] = "dry-run"
		}
	} else if len(followed) > 0 && len(opts.Conversations) == 0 {
		if err := c.writeTransaction(ctx, "mark forks of changed agent event conversations", func(tx *sql.Tx) error {
			for _, id := range followed {
				var token string
				if err := tx.QueryRowContext(ctx, `INSERT INTO agent_events_dirty(conversation_id,token) VALUES(?,hex(randomblob(8)))
					ON CONFLICT(conversation_id) DO UPDATE SET token=agent_events_dirty.token RETURNING token`, id).Scan(&token); err != nil {
					return err
				}
				tokens[id] = token
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	jobs := []*agentEventsConversation{}
	for _, id := range ids {
		structure := structures[id]
		previous, built := stored[id]
		_, otherModel := other[id]
		if !all && tokens[id] == "" && since != "" && ended[id] != "" && ended[id] < since {
			// It ended before the horizon and nothing in it changed.
			continue
		}
		// Built from a later horizon than this run's: its earlier messages
		// were left unread ('~' is a version 1 row, horizon unknown).
		widened := built && horizons[id] != since && (horizons[id] == "~" || since == "" || horizons[id] != "" && since < horizons[id])
		if all || !built || previous != structure || tokens[id] != "" || otherModel || widened {
			jobs = append(jobs, &agentEventsConversation{id: id, token: tokens[id], structure: structure, since: since})
		}
	}
	gone := []string{}
	for _, table := range []map[string]string{stored, tokens, other} {
		for id := range table {
			if p.conversations[id] == nil && !slices.Contains(gone, id) {
				gone = append(gone, id)
			}
		}
	}
	if len(gone) > 0 && len(opts.Conversations) == 0 && !opts.DryRun {
		stats.ConversationsRemoved = len(gone)
		if err := c.writeTransaction(ctx, "remove agent events of deleted conversations", func(tx *sql.Tx) error {
			for _, id := range gone {
				for _, statement := range []string{`DELETE FROM agent_events WHERE conversation_id=?`, `DELETE FROM agent_event_messages WHERE conversation_id=?`,
					`DELETE FROM agent_events_conversations WHERE conversation_id=?`} {
					if _, err := tx.ExecContext(ctx, statement, id); err != nil {
						return err
					}
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM agent_events_dirty WHERE conversation_id=? AND token=?`, id, tokens[id]); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return jobs, nil
}

// agentEventInputHash is what a message's candidates are made from.
func agentEventInputHash(source, text string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + text))
	return hex.EncodeToString(sum[:16])
}

// agentEventsConversation builds one conversation's units and frames the
// candidates of its agent replies and thinking whose inputs changed.
func (c *Catalog) agentEventsConversation(ctx context.Context, builder *labelsItemBuilder, verifier *agentEventVerifier, job *agentEventsConversation, since time.Time) (conversation *agentEventsConversation, err error) {
	defer func() {
		if value := recover(); value != nil {
			conversation, err = nil, fmt.Errorf("building its units panicked: %v", value)
		}
	}()
	units, err := builder.conversation(ctx, job.id)
	if err != nil {
		return nil, err
	}
	stored, times := map[string]string{}, map[string]string{}
	rows, err := queryMapsContext(ctx, c.DB, `SELECT message_id,COALESCE(input_hash,'') h,model,COALESCE(occurred_at,'') t FROM agent_event_messages WHERE conversation_id=?`, job.id)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		hash := firstString(row["h"])
		if firstString(row["model"]) != verifier.identity {
			hash = ""
		}
		stored[firstString(row["message_id"])] = hash
		times[firstString(row["message_id"])] = firstString(row["t"])
	}
	seen := map[string]bool{}
	for _, unit := range units {
		if unit.cohort != "agent_reply" && unit.cohort != "thinking" {
			continue
		}
		seen[unit.messageID] = true
		if at, ok := parseTime(unit.createdAt); ok && at.Before(since) {
			job.before++
			continue
		}
		source := "reply"
		if unit.cohort == "thinking" {
			source = "thinking"
		}
		hash := agentEventInputHash(source, unit.text)
		if stored[unit.messageID] == hash {
			job.unchanged++
			// Its events stand, but a corrected time moves them to its day.
			if times[unit.messageID] != unit.createdAt {
				job.retimed = append(job.retimed, &agentEventsMessage{id: unit.messageID, createdAt: unit.createdAt})
			}
			continue
		}
		message := &agentEventsMessage{id: unit.messageID, conversation: job.id, source: source, text: unit.text, createdAt: unit.createdAt, hash: hash,
			sentences: agentEventSentences(unit.text)}
		texts := make([]string, len(message.sentences))
		for i, r := range message.sentences {
			texts[i] = unit.text[r[0]:r[1]]
		}
		message.candidates = agentEventCandidates(texts)
		for _, candidate := range message.candidates {
			lo, _ := agentEventWindow(len(texts), candidate.Sentence)
			message.rows = append(message.rows, agentEventFrame(verifier.model.Tokenizer, verifier.definitions, candidate.Kind, source, texts, candidate.Sentence, verifier.model.MaxSeq))
			message.windows = append(message.windows, lo)
		}
		job.messages = append(job.messages, message)
	}
	for id := range stored {
		if !seen[id] {
			job.stale = append(job.stale, id)
		}
	}
	sort.Strings(job.stale)
	return job, nil
}

// storeAgentEvents writes scored conversations in one transaction: each
// message's row and its candidates at or above agentEventStoreFloor, rows of
// messages that are no longer agent replies or thinking removed, the
// conversation's ledger row, and the dirty mark this run read.
func (c *Catalog) storeAgentEvents(ctx context.Context, identity string, conversations []*agentEventsConversation, stats *AgentEventsRunStats) error {
	lexicon, _ := agentEventLexicon()
	at := now()
	return c.writeTransaction(ctx, "store agent events", func(tx *sql.Tx) error {
		for _, conversation := range conversations {
			for _, id := range conversation.stale {
				for _, statement := range []string{`DELETE FROM agent_events WHERE message_id=?`, `DELETE FROM agent_event_messages WHERE message_id=?`} {
					if _, err := tx.ExecContext(ctx, statement, id); err != nil {
						return err
					}
				}
				stats.MessagesRemoved++
			}
			for _, message := range conversation.retimed {
				for _, table := range []string{"agent_event_messages", "agent_events"} {
					if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET occurred_at=?, day=? WHERE message_id=?`, nilIfEmpty(message.createdAt), agentEventDay(message.createdAt), message.id); err != nil {
						return err
					}
				}
			}
			for _, message := range conversation.messages {
				day := agentEventDay(message.createdAt)
				if _, err := tx.ExecContext(ctx, `DELETE FROM agent_events WHERE message_id=?`, message.id); err != nil {
					return err
				}
				events := 0
				for k, candidate := range message.candidates {
					out := message.outputs[k]
					probability := float64(out["verdict"][0])
					if probability < agentEventStoreFloor {
						continue
					}
					sentence := message.sentences[candidate.Sentence]
					subject, response := [][2]int{}, [][2]int{}
					for slot, span := range message.rows[k].Extra[labelrt.SegmentSpans] {
						if span.Len() == 0 {
							continue
						}
						if out["sentences"][2*slot] >= agentEventThreshold {
							subject = append(subject, message.sentences[message.windows[k]+slot])
						}
						if out["sentences"][2*slot+1] >= agentEventThreshold {
							response = append(response, message.sentences[message.windows[k]+slot])
						}
					}
					if len(subject) == 0 {
						subject = [][2]int{sentence}
					}
					sentenceText, _ := agentEventText(message.text, [][2]int{sentence})
					subjectText, _ := agentEventText(message.text, subject)
					responseText, _ := agentEventText(message.text, response)
					subjectRanges, _ := json.Marshal(subject)
					responseRanges, _ := json.Marshal(response)
					if _, err := tx.ExecContext(ctx, `INSERT INTO agent_events(message_id,conversation_id,source,occurred_at,day,family,probability,
						sentence_start,sentence_end,sentence,subject,response,subject_ranges,response_ranges,model) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
						message.id, message.conversation, message.source, nilIfEmpty(message.createdAt), day, candidate.Kind, probability,
						sentence[0], sentence[1], sentenceText, subjectText, responseText, string(subjectRanges), string(responseRanges), identity); err != nil {
						return err
					}
					stats.Stored++
					if probability >= agentEventThreshold {
						events++
					}
				}
				stats.Events += events
				if _, err := tx.ExecContext(ctx, `INSERT INTO agent_event_messages(message_id,conversation_id,source,occurred_at,day,candidates,events,model,lexicon,imported_at,input_hash)
					VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(message_id) DO UPDATE SET conversation_id=excluded.conversation_id, source=excluded.source,
					occurred_at=excluded.occurred_at, day=excluded.day, candidates=excluded.candidates, events=excluded.events, model=excluded.model,
					lexicon=excluded.lexicon, imported_at=excluded.imported_at, input_hash=excluded.input_hash`,
					message.id, message.conversation, message.source, nilIfEmpty(message.createdAt), day, len(message.candidates), events, identity, lexicon, at, message.hash); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO agent_events_conversations(conversation_id,structure,built_at,since) VALUES(?,?,?,?)
				ON CONFLICT(conversation_id) DO UPDATE SET structure=excluded.structure, built_at=excluded.built_at, since=excluded.since`, conversation.id, conversation.structure, at, conversation.since); err != nil {
				return err
			}
			// Only the mark this run read: one written since stays for the next run.
			if _, err := tx.ExecContext(ctx, `DELETE FROM agent_events_dirty WHERE conversation_id=? AND token=?`, conversation.id, conversation.token); err != nil {
				return err
			}
		}
		return nil
	})
}

// agentEventsFlag is the Settings switch for the Agent Experience preview
// (Background Processing, beside message labels): it lists the dashboard and turns the pass on, as
// agent_events_enabled does in the configuration.
const agentEventsFlag = "pharos-agent-events-v1"

// agentEventsEnabled is whether this library finds agent events.
func (s *Server) agentEventsEnabled(ctx context.Context) bool {
	return s.Config().AgentEvents.Enabled || s.Catalog.featureFlag(ctx, agentEventsFlag)
}

// agentEventsChainLimit is the chain stage's share: about this many messages
// scored per pass; a backlog continues as its own run when plugged in.
var agentEventsChainLimit = 1000

// chainAgentEvents is the analysis chain's agent events stage, after labels:
// the same power rules (labelsForPower) and the same backlog handling.
func (s *Server) chainAgentEvents(ctx context.Context, labels LabelerSettings, settings AgentEventSettings) error {
	if labels.Worker == "" {
		// No worker is installed beside Pharos or configured.
		return errStageContinues
	}
	if missing := settings.missingAssets(labels); len(missing) > 0 {
		// The broker downloads for a library whose configuration turns the
		// pass on; the Settings switch lives in the catalog, which the broker
		// does not read, so the stage downloads what is missing itself, under
		// the same per-Mac lock.
		if slices.ContainsFunc(missing, func(asset labelsAsset) bool { return !asset.pinned() }) {
			return errStageContinues
		}
		switch err := fetchLabelsAssets(ctx, missing, nil, nil); {
		case errors.Is(err, errLabelsFetching):
			return errStageContinues
		case err != nil:
			return err
		}
	}
	labels, bulk := labelsForPower(settings.labeler(labels), s.cachedPower(ctx))
	stats, err := s.Catalog.RunAgentEvents(ctx, labels, settings, AgentEventsRunOptions{Limit: agentEventsChainLimit, Since: settings.since()})
	switch {
	case errors.Is(err, errAgentEventsRunning):
		return errStageContinues
	case err != nil:
		return err
	case !stats.Cut && !stats.Complete:
		return errStageContinues
	case !stats.Cut:
		return nil
	case !bulk:
		return errStageContinues
	}
	s.Catalog.startBackground(OperationSpec{Kind: "analysis.agent_events", Variant: "backlog", Trigger: triggerChained, Priority: priorityBackground}, func(ctx context.Context, op *Op) error {
		ctx = withWorkProgress(ctx, op.mirrorWork)
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		// Stop ends it between batches; the next run resumes it.
		op.setStopper(cancel)
		var unplugged atomic.Bool
		go func() {
			ticker := time.NewTicker(labelsPowerCheck)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, bulk := labelsForPower(labels, s.cachedPower(ctx)); !bulk {
						unplugged.Store(true)
						cancel()
						return
					}
				}
			}
		}()
		_, err := s.Catalog.RunAgentEvents(ctx, labels, settings, AgentEventsRunOptions{Since: settings.since()})
		switch {
		case errors.Is(err, labelrt.ErrWorkersUnavailable), unplugged.Load():
			op.legacyOutcome("interrupted", nil)
			return nil
		case errors.Is(err, errAgentEventsRunning):
			return nil
		}
		return err
	})
	return errStageContinues
}

// agentEventsStatus is pharos agent-events status: whether the pass is on,
// what it would use or waits for, and what the tables hold.
func (c *Catalog) agentEventsStatus(ctx context.Context, config Config) (map[string]any, error) {
	labels, settings := config.Labels.withAssets(), config.AgentEvents.withAssets()
	// On by the configuration or by the Settings switch, as agentEventsEnabled.
	status := map[string]any{"enabled": settings.Enabled || c.featureFlag(ctx, agentEventsFlag), "days": settings.Days, "threshold": agentEventThreshold,
		"model_dir": settings.ModelDir, "model": labelsAssetStatus(labelsAssets.AgentEventsModel), "worker": labels.Worker,
		"runtime": defaultString(labels.Runtime, labelsAssetStatus(labelsAssets.Runtime))}
	if waiting := settings.waiting(config.Labels); waiting != "" {
		status["waiting"] = waiting
	}
	if settings.ModelDir != "" {
		if verifier, err := loadAgentEventVerifier(settings.ModelDir, settings.Variant); err != nil {
			status["model_error"] = err.Error()
		} else {
			status["verifier"] = verifier.identity
		}
	}
	var messages, events, scored int
	var first, last, models sql.NullString
	if err := c.DB.QueryRowContext(ctx, `SELECT count(*), COALESCE(sum(events),0), min(day), max(day), group_concat(DISTINCT model) FROM agent_event_messages`).Scan(&messages, &events, &first, &last, &models); err != nil {
		return nil, err
	}
	if err := c.DB.QueryRowContext(ctx, `SELECT count(*) FROM agent_events`).Scan(&scored); err != nil {
		return nil, err
	}
	status["messages"], status["events"], status["candidates_stored"] = messages, events, scored
	status["first_day"], status["last_day"], status["models"] = first.String, last.String, models.String
	var tables int
	if err := c.DB.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('agent_events_conversations','agent_events_dirty')`).Scan(&tables); err == nil && tables == 2 {
		var built, dirty int
		if err := c.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM agent_events_conversations), (SELECT count(*) FROM agent_events_dirty)`).Scan(&built, &dirty); err != nil {
			return nil, err
		}
		status["conversations_built"], status["conversations_changed"] = built, dirty
	}
	return status, nil
}

// agentEventDay is a message time's local day, or NULL without a time.
func agentEventDay(createdAt string) any {
	if when, ok := parseTime(createdAt); ok {
		return when.Local().Format("2006-01-02")
	}
	return nil
}

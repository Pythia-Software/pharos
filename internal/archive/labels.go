package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	labelrt "github.com/gbdubs/pharos/internal/labeler"
)

// The labels ledger (docs/local-labelrt.md): message-function labels from
// the local labeler, derived like the authorship ledger and rebuilt after it,
// never by a request. Inference runs in a separate worker process
// (internal/labeler); this file chooses what to label, batches it, and
// stores the results.

// labelsContract names every input contract a stored label depends on: the
// framing, the item contracts of labeling prepare, the segment rule, and how
// a long message's chunks are merged. A change to any of them relabels.
var labelsContract = strings.Join([]string{labelrt.FramingContract, labelingUnitsVersion, humanPiecesVersion, labelingContextVersion,
	labelingChunkVersion, labelingSegmentsVersion, labelsMergeVersion}, "+")

// labelsMergeVersion: a message split into chunks is labeled per chunk; its
// labels are the union of the chunks' closed sets and its probabilities the
// per-label maximum. Not validated against teachers (library labeling
// labels chunks separately too).
const labelsMergeVersion = "chunk-union-v1"

const labelsSchemaVersion = "3"

// labelsSchemaDDL is created by the first labels run, not when a catalog
// opens: a library that never enables labeling has none of it. Older builds
// ignore these tables; their writes still mark conversations dirty through
// the triggers, and every label records the inputs it was made from.
const labelsSchemaDDL = `
CREATE TABLE IF NOT EXISTS message_labels (
  message_id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL,
  segment TEXT NOT NULL,
  labels INTEGER NOT NULL,
  probs BLOB NOT NULL,
  chunks INTEGER NOT NULL,
  tokens INTEGER NOT NULL,
  input_hash TEXT NOT NULL,
  model TEXT NOT NULL,
  contract TEXT NOT NULL,
  labeled_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS message_labels_conversation_idx ON message_labels(conversation_id);
CREATE INDEX IF NOT EXISTS message_labels_segment_idx ON message_labels(segment);
CREATE TABLE IF NOT EXISTS message_labels_conversations (
  conversation_id TEXT PRIMARY KEY,
  structure TEXT NOT NULL,
  units INTEGER NOT NULL,
  built_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS message_labels_dirty (
  conversation_id TEXT PRIMARY KEY,
  token TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS message_labels_models (
  model TEXT PRIMARY KEY,
  labels_json TEXT NOT NULL
);
`

// labelsDirtyTriggers mark a conversation dirty when anything its items are
// built from changes: its messages, their classification, reasoning
// extraction, agent-session membership and kinds, and the conversation row.
// Each mark carries a fresh token, so a run clears only marks it has read.
func labelsDirtyTriggers() []string {
	return itemDirtyTriggers("message_labels_dirty", "labels_dirty_")
}

// itemDirtyTriggers are labelsDirtyTriggers for any ledger built from
// labeling items: they mark dirty, a (conversation_id, token) table, and are
// named with prefix.
func itemDirtyTriggers(dirty, prefix string) []string {
	mark := func(conversation string) string {
		return `INSERT INTO ` + dirty + `(conversation_id,token) SELECT ` + conversation + `,hex(randomblob(8)) WHERE ` + conversation +
			` IS NOT NULL ON CONFLICT(conversation_id) DO UPDATE SET token=excluded.token;`
	}
	statements := []string{}
	add := func(table, event, columns, conversation string) {
		name := prefix + table + "_" + strings.ToLower(event)
		of := ""
		if columns != "" {
			of = " OF " + columns
		}
		refs := map[string][]string{"INSERT": {"NEW"}, "DELETE": {"OLD"}, "UPDATE": {"OLD", "NEW"}}[event]
		body := ""
		for _, ref := range refs {
			body += mark(strings.ReplaceAll(conversation, "$", ref))
		}
		// An upsert names every column in its SET list, so UPDATE OF fires
		// for unchanged values too: mark only a real change.
		when := ""
		if event == "UPDATE" && columns != "" {
			changed := []string{}
			for _, column := range strings.Split(columns, ",") {
				changed = append(changed, "OLD."+column+" IS NOT NEW."+column)
			}
			when = " WHEN " + strings.Join(changed, " OR ")
		}
		statements = append(statements, "DROP TRIGGER IF EXISTS "+name, "CREATE TRIGGER "+name+" AFTER "+event+of+" ON "+table+when+" BEGIN "+body+" END")
	}
	for _, event := range []string{"INSERT", "DELETE", "UPDATE"} {
		columns := map[string]string{
			"messages":                   "conversation_id,role,kind,text,created_at,selected,sender,reasoning_format,source_order,call_id,content_hash",
			"message_authorship":         "spans_json,conversation_id",
			"reasoning_extraction_state": "",
			"agent_sessions":             "kind,conversation_id,native_id,child_conversation_id,delegation_message_id",
		}
		if event != "UPDATE" {
			for table := range columns {
				columns[table] = ""
			}
		}
		add("messages", event, columns["messages"], "$.conversation_id")
		add("message_authorship", event, columns["message_authorship"], "$.conversation_id")
		add("reasoning_extraction_state", event, columns["reasoning_extraction_state"], "$.conversation_id")
		add("agent_sessions", event, columns["agent_sessions"], "$.conversation_id")
	}
	for _, event := range []string{"INSERT", "DELETE"} {
		add("agent_session_messages", event, "", "(SELECT conversation_id FROM messages WHERE id=$.message_id)")
	}
	add("conversations", "UPDATE", "workspace_id,provider,parent_id,started_at", "$.id")
	add("conversations", "DELETE", "", "$.id")
	return statements
}

// ensureLabelsSchema creates the ledger's tables and triggers once, and
// replaces the triggers when labelsSchemaVersion changes (version 2 marks
// only real changes; version 3 adds each model's label order).
func (c *Catalog) ensureLabelsSchema(ctx context.Context) error {
	if version, err := c.metaValue(ctx, "labels_schema_version"); err == nil && version == labelsSchemaVersion {
		return nil
	}
	return c.writeTransaction(ctx, "create the labels ledger", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, labelsSchemaDDL); err != nil {
			return err
		}
		for _, statement := range labelsDirtyTriggers() {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("%s: %w", statement[:60], err)
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('labels_schema_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, labelsSchemaVersion)
		return err
	})
}

func (c *Catalog) hasLabelsLedger() bool {
	var n int
	return c.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='message_labels'`).Scan(&n) == nil && n > 0
}

// LabelerSettings locate the model and the worker. For the prototype they
// come from the configuration (labels_* keys) or the environment; a bundled
// app would supply them from Contents/Resources (docs/local-labelrt.md).
type LabelerSettings struct {
	Enabled bool
	// ModelDir holds manifest.json, the ONNX files and tokenizer.json.
	ModelDir, Variant string
	// Worker is the worker executable; Runtime the ONNX Runtime library it
	// loads (passed as --runtime).
	Worker, Runtime string
	// Threads per worker, workers, and the padded-token budget of a batch.
	Threads, Workers, BatchTokens, MaxBatch int
	// ThreadBudget and MemoryBudgetMB bound every model's workers together
	// (labeler.Budget); IdleSeconds is how long finished workers stay warm.
	ThreadBudget, MemoryBudgetMB, IdleSeconds int
}

func labelerSettingsFrom(root map[string]any, base string) LabelerSettings {
	env := func(key, value string) string {
		if value != "" {
			return value
		}
		return expandPath(os.Getenv(key))
	}
	settings := LabelerSettings{
		Enabled:  boolValue(root, "labels_enabled", false),
		ModelDir: env("PHAROS_LABELS_MODEL_DIR", resolvePath(base, stringValue(root, "labels_model_dir", ""))),
		Variant:  stringValue(root, "labels_variant", "int8"),
		Worker:   env("PHAROS_LABELS_WORKER", resolvePath(base, stringValue(root, "labels_worker", ""))),
		Runtime:  env("PHAROS_ORT_LIBRARY", resolvePath(base, stringValue(root, "labels_runtime", ""))),
		Threads:  intValue(root, "labels_threads", 4), Workers: intValue(root, "labels_workers", 1),
		BatchTokens: intValue(root, "labels_batch_tokens", 16384), MaxBatch: intValue(root, "labels_max_batch", 64),
		ThreadBudget: intValue(root, "labels_thread_budget", 0), MemoryBudgetMB: intValue(root, "labels_memory_budget_mb", 8192),
		IdleSeconds: intValue(root, "labels_idle_seconds", 120),
	}
	if exe := runningExecutable(); exe != "" {
		// Beside the pharos executable, as an app bundle would place them:
		// Contents/MacOS/pharos-labeler-ort and Contents/Resources/labeler/<model>.
		if candidate := filepath.Join(filepath.Dir(exe), "pharos-labeler-ort"); settings.Worker == "" && fileExists(candidate) {
			settings.Worker = candidate
		}
		if candidate := filepath.Join(filepath.Dir(exe), "..", "Resources", "labeler", labelsDefaultModel); settings.ModelDir == "" && fileExists(filepath.Join(candidate, "manifest.json")) {
			settings.ModelDir = candidate
		}
	}
	return settings
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// labelerManager is the process's labeler runtime: one runner per model,
// within one thread and memory budget, workers kept warm between runs. It
// is created from the first settings that ask for it; each later call sets
// the budget those settings give (labelsBudget), so a run on battery starts
// its workers within the battery budget.
func (c *Catalog) labelerManager(settings LabelerSettings) *labelrt.Manager {
	c.labelersOnce.Do(func() {
		c.labelers = labelrt.NewManager(labelsBudget(settings), time.Duration(settings.IdleSeconds)*time.Second)
	})
	c.labelers.SetBudget(labelsBudget(settings))
	return c.labelers
}

// labelsBudget is what settings allow every model's workers together:
// labels_thread_budget, else the run's own workers and threads or half the
// cores, whichever is more; and labels_memory_budget_mb.
func labelsBudget(settings LabelerSettings) labelrt.Budget {
	threads := settings.ThreadBudget
	if threads <= 0 {
		threads = max(max(1, settings.Workers)*max(1, settings.Threads), runtime.NumCPU()/2)
	}
	return labelrt.Budget{Threads: threads, MemoryMB: settings.MemoryBudgetMB}
}

// The battery budget (docs/local-labeler.md, "Scheduling and power"): one
// worker of at most labelsBatteryThreads threads, batches of at most
// labelsBatteryBatchTokens padded tokens, and no backlog run.
const (
	labelsBatteryThreads     = 2
	labelsBatteryBatchTokens = 4096
)

// labelsForPower is settings as this power allows. Plugged in (or on a Mac
// without a battery) they are unchanged: the full budget. On battery a run
// uses one small worker and small batches, unless labels_thread_budget is
// set, which then holds on battery too. bulk is whether a backlog may
// continue as its own run after the chain's bounded share; on battery it
// waits for power.
func labelsForPower(settings LabelerSettings, power powerState) (adjusted LabelerSettings, bulk bool) {
	if !power.OnBattery {
		return settings, true
	}
	if settings.ThreadBudget <= 0 {
		settings.Workers = 1
		settings.Threads = min(max(1, settings.Threads), labelsBatteryThreads)
		settings.ThreadBudget = settings.Threads
		settings.BatchTokens = min(settings.BatchTokens, labelsBatteryBatchTokens)
	}
	return settings, false
}

// labelsDefaultModel is the model the app ships: LAPWING's target-pooling
// kestrel-68m (input contract ids-attention-pool-v1). Any model directory
// can still be configured, kestrel-68m (ids-attention-v1) included.
const labelsDefaultModel = "kestrel-68m-tpool"

// labelsModelIdentity names what made a label: the model's name, its input
// contract, its file's SHA-256 and its manifest's (which holds the
// thresholds and tokenizer hash). Every label row records it, so a model
// switch, in either direction, relabels.
func labelsModelIdentity(model *labelrt.Model) string {
	manifest, _ := os.ReadFile(filepath.Join(model.Dir, "manifest.json"))
	sum := sha256.Sum256(manifest)
	return fmt.Sprintf("%s/%s/%s/%s", model.Manifest.Name, model.InputContract, model.SHA256[:16], hex.EncodeToString(sum[:8]))
}

// labelsFrame frames one item for model. It reports false for an item a
// pooling model cannot take: a text with no tokens leaves nothing to pool,
// so the item is never sent (status empty_target).
func labelsFrame(model *labelrt.Model, item labelingItem) ([]int32, labelrt.FrameSpans, bool) {
	seq, spans := model.Tokenizer.FrameWithSpans(labelrt.FrameInput{Segment: item.Segment, Cohort: item.Cohort, Sender: item.SenderRole, Context: item.Context, Text: item.Text})
	return seq, spans, !model.Pools() || spans.Text.Len() > 0
}

// labelsGeneration is what a complete ledger was built for.
func labelsGeneration(authorship, model string) string {
	return authorship + "|" + model + "|" + labelsContract
}

// errLabelsRunning reports a labels run already in progress.
var errLabelsRunning = errors.New("labeling is already running")

// LabelsRunOptions bound one run.
type LabelsRunOptions struct {
	// Limit stops after this many messages are labeled (0: no limit). A
	// conversation cut short stays pending.
	Limit int
	// Full rebuilds every conversation's items, not only changed ones; only
	// messages whose inputs changed are labeled again either way.
	Full bool
	// Conversations restricts the run (tests and diagnostics).
	Conversations []string
	// OneAtATime sends one item per batch, one batch at a time: the
	// baseline the batching is measured against.
	OneAtATime bool
	// DryRun builds and frames items and counts what would be labeled, but
	// starts no worker and stores nothing.
	DryRun bool
	// Export, when set, receives one JSON line per item built (dry run) or
	// labeled: its identity, segment, cohort, sender role, hashes of its
	// context and text, token count and, once labeled, its probabilities and
	// closed labels. ExportText adds the context, text and token ids. A
	// diagnostic for parity checks; it holds conversation text.
	Export     io.Writer
	ExportText bool
	Progress   io.Writer
}

// LabelsRunStats describe a run.
type LabelsRunStats struct {
	Conversations, ConversationsBuilt, ConversationsRemoved int
	// ConversationsFailed could not be built and stay pending.
	ConversationsFailed int
	// MessagesEmptyTarget have no text tokens for a pooling model to pool.
	MessagesEmptyTarget int
	// Cut is set when the limit left messages for a later run.
	Cut                                                bool
	Units, Items, ItemsLabeled, MessagesLabeled        int
	MessagesUnchanged, MessagesRemoved, MessagesFailed int
	Batches, Tokens, PaddedTokens, Restarts            int
	// PeakPending is the most items built and not yet labeled at once: how
	// far the builder ran ahead of the workers.
	PeakPending                             int
	Complete                                bool
	WorkerStart, Build, Infer, Write, Total time.Duration
	Worker                                  string
	WorkerPIDs                              []int
}

// labelsMessage is one message's pending work: its items' sequences, and the
// merged result as chunks come back.
type labelsMessage struct {
	id, conversation, hash, segment string
	seqs                            [][]int32
	spans                           []labelrt.FrameSpans
	segments                        []string
	tokens, done                    int
	probs                           []float32
	mask                            uint64
	failed                          bool
	job                             *labelsJob
	// items are kept only for an export.
	items []labelingItem
}

// labelsJob is one conversation: messages to label, stored labels to
// delete, and whether the run covers the whole conversation.
type labelsJob struct {
	conversation, token, structure string
	units                          int
	messages                       []*labelsMessage
	stale                          []string
	remaining                      int
	complete                       bool
}

type labelsSeq struct {
	message *labelsMessage
	chunk   int
}

// labelsStructure fingerprints what a conversation's items depend on beyond
// its own rows: its work's mirror status, its conversation row, whether its
// reasoning extraction is current, and its agent sessions' kinds. A
// conversation whose fingerprint changed is rebuilt even without a dirty mark.
func labelsStructure(p *labelingPass, sessions map[string][]string, id string) string {
	info := p.conversations[id]
	parts := []string{info.workspace, p.representative[info.workspace], info.provider, info.parent, info.started, fmt.Sprint(p.current[id])}
	parts = append(parts, sessions[id]...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:12])
}

// labelsInputHash hashes a message's framing inputs, chunk by chunk.
func labelsInputHash(items []labelingItem) string {
	hash := sha256.New()
	for _, item := range items {
		for _, field := range []string{item.Segment, item.Cohort, item.SenderRole, item.Context, item.Text} {
			var size [8]byte
			binary.LittleEndian.PutUint64(size[:], uint64(len(field)))
			hash.Write(size[:])
			hash.Write([]byte(field))
		}
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

// RunLabels brings the labels ledger up to date: it rebuilds the items of
// conversations whose inputs may have changed, labels the messages whose
// items differ from what their stored labels were made from, and stores the
// results in batched write transactions. It stops between batches when ctx
// ends, keeping what was stored; the next run resumes.
func (c *Catalog) RunLabels(ctx context.Context, settings LabelerSettings, opts LabelsRunOptions) (*LabelsRunStats, error) {
	started := time.Now()
	stats := &LabelsRunStats{}
	defer func() { stats.Total = time.Since(started) }()
	progress := labelingProgress(opts.Progress, started)
	settings = settings.withAssets()
	if settings.ModelDir == "" {
		return stats, fmt.Errorf("%w: the model is %s; run pharos labels fetch, or set labels_model_dir (PHAROS_LABELS_MODEL_DIR)", labelrt.ErrModelUnavailable, labelsAssetStatus(labelsAssets.Model))
	}
	if settings.Worker == "" && !opts.DryRun {
		return stats, fmt.Errorf("%w: set labels_worker (or PHAROS_LABELS_WORKER)", labelrt.ErrModelUnavailable)
	}
	// One run at a time: in this process, and across processes (the
	// service and pharos labels run) through a lease the OS releases if the
	// holder exits.
	if !c.labels.TryLock() {
		return stats, errLabelsRunning
	}
	defer c.labels.Unlock()
	if c.Path != "" {
		lease, err := fileLease(canonical(c.Path)+".labels.lock", true)
		if err != nil {
			return stats, fmt.Errorf("%w: %v", errLabelsRunning, err)
		}
		defer lease.Close()
	}
	model, err := labelrt.LoadModel(settings.ModelDir, settings.Variant)
	if err != nil {
		return stats, err
	}
	identity := labelsModelIdentity(model)
	if err := c.ensureLabelsSchema(ctx); err != nil {
		return stats, err
	}
	_, authorship, err := c.authorshipGenerations()
	if err != nil {
		return stats, err
	}
	if !opts.DryRun {
		// A row's labels are bits in its model's label order; readers decode
		// them with the order recorded here (messageLabelsAttacher).
		order, _ := json.Marshal(model.Manifest.Labels.Order)
		if err := c.writeTransaction(ctx, "record the labels model", func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO message_labels_models(model,labels_json) VALUES(?,?) ON CONFLICT(model) DO NOTHING`, identity, string(order))
			return err
		}); err != nil {
			return stats, err
		}
	}
	builder, err := newLabelsItemBuilder(ctx, c.DB)
	if err != nil {
		return stats, err
	}
	todo, err := c.labelsWork(ctx, builder, identity, opts, stats)
	if err != nil {
		return stats, err
	}
	progress("labels: %d conversations to rebuild of %d", len(todo), stats.Conversations)
	reportWork(ctx, "labeling messages", "conversation", 0, len(todo))

	run := &labelsRun{c: c, settings: settings, opts: opts, model: model, identity: identity, stats: stats, progress: progress}
	if err := run.execute(ctx, builder, todo); err != nil {
		return stats, err
	}
	// The ledger is current when every conversation was covered and no input
	// changed meanwhile.
	if stats.Complete {
		var dirty int
		if err := c.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_labels_dirty`).Scan(&dirty); err != nil {
			return stats, err
		}
		_, current, err := c.authorshipGenerations()
		if err != nil {
			return stats, err
		}
		stats.Complete = dirty == 0 && current == authorship
		if stats.Complete {
			err = c.writeTransaction(ctx, "complete labels", func(tx *sql.Tx) error {
				for key, value := range map[string]string{"labels_generation": labelsGeneration(authorship, identity), "labels_built_at": now()} {
					if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
						return err
					}
				}
				return nil
			})
		}
	}
	return stats, err
}

// labelsWork chooses the conversations to rebuild: dirty ones, ones whose
// structure changed or that were never built, and forks of any of those
// (they copy their parent's history); everything for Full, a new model or a
// new contract. Conversations no longer in the catalog lose their labels.
func (c *Catalog) labelsWork(ctx context.Context, builder *labelsItemBuilder, identity string, opts LabelsRunOptions, stats *LabelsRunStats) ([]*labelsJob, error) {
	p := builder.p
	stored := map[string]string{}
	rows, err := queryMapsContext(ctx, c.DB, `SELECT conversation_id,structure FROM message_labels_conversations`)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		stored[firstString(row["conversation_id"])] = firstString(row["structure"])
	}
	tokens := map[string]string{}
	if rows, err = queryMapsContext(ctx, c.DB, `SELECT conversation_id,token FROM message_labels_dirty`); err != nil {
		return nil, err
	}
	for _, row := range rows {
		tokens[firstString(row["conversation_id"])] = firstString(row["token"])
	}
	// A conversation whose stored labels no longer number its units lost
	// some (or gained stray ones): rebuild it.
	counts := map[string]int64{}
	if rows, err = queryMapsContext(ctx, c.DB, `SELECT conversation_id,COUNT(*) n FROM message_labels GROUP BY conversation_id`); err != nil {
		return nil, err
	}
	for _, row := range rows {
		counts[firstString(row["conversation_id"])] = integer(row["n"])
	}
	units := map[string]int64{}
	if rows, err = queryMapsContext(ctx, c.DB, `SELECT conversation_id,units FROM message_labels_conversations`); err != nil {
		return nil, err
	}
	for _, row := range rows {
		units[firstString(row["conversation_id"])] = integer(row["units"])
	}
	// A model or contract that differs from any stored label's relabels all.
	var other int
	if err := c.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM message_labels WHERE model<>? OR contract<>? LIMIT 1)`, identity, labelsContract).Scan(&other); err != nil {
		return nil, err
	}
	all := opts.Full || other > 0
	ids := []string{}
	for id := range p.conversations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	stats.Conversations = len(ids)
	if len(opts.Conversations) > 0 {
		ids = slices.DeleteFunc(slices.Clone(opts.Conversations), func(id string) bool { return p.conversations[id] == nil })
		all = true
	}
	want, changed := map[string]bool{}, map[string]bool{}
	structures := map[string]string{}
	for _, id := range ids {
		structures[id] = labelsStructure(p, builder.sessions, id)
		previous, built := stored[id]
		// Its inputs changed: its forks, which copy its history, follow.
		changed[id] = all || built && previous != structures[id] || tokens[id] != ""
		if changed[id] || !built || counts[id] != units[id] {
			want[id] = true
		}
	}
	// Forks copy their parent's history, and a re-sent message's target
	// text is resolved against its source's classification, so both follow
	// a conversation whose inputs changed (not one only rebuilt to restore
	// lost labels).
	children := map[string][]string{}
	for id, info := range p.conversations {
		if info.parent != "" {
			children[info.parent] = append(children[info.parent], id)
		}
	}
	queue := []string{}
	for id := range changed {
		if changed[id] {
			queue = append(queue, id)
		}
	}
	if len(queue) > 0 && !all {
		reusers, err := c.labelsReusers(ctx)
		if err != nil {
			return nil, err
		}
		for source, users := range reusers {
			for _, user := range users {
				if p.conversations[user] != nil {
					children[source] = append(children[source], user)
				}
			}
		}
	}
	followed := []string{}
	for ; len(queue) > 0; queue = queue[1:] {
		for _, child := range children[queue[0]] {
			if !want[child] && structures[child] != "" {
				want[child] = true
				queue = append(queue, child)
				followed = append(followed, child)
			}
		}
	}
	// A conversation that follows another is wanted only because of this
	// run's view of its source, whose dirty mark the source's own write
	// clears. A run cut short (--limit, a stop) before reaching it would lose
	// it for good, so it gets a dirty mark of its own first, which this run
	// clears only once it is built.
	if len(followed) > 0 && !opts.DryRun && len(opts.Conversations) == 0 {
		if err := c.writeTransaction(ctx, "mark conversations that follow a changed one", func(tx *sql.Tx) error {
			for _, id := range followed {
				if tokens[id] != "" {
					continue
				}
				var token string
				if err := tx.QueryRowContext(ctx, `INSERT INTO message_labels_dirty(conversation_id,token) VALUES(?,hex(randomblob(8)))
					ON CONFLICT(conversation_id) DO UPDATE SET token=message_labels_dirty.token RETURNING token`, id).Scan(&token); err != nil {
					return err
				}
				tokens[id] = token
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	jobs := []*labelsJob{}
	for _, id := range ids {
		if want[id] {
			jobs = append(jobs, &labelsJob{conversation: id, token: tokens[id], structure: structures[id]})
		}
	}
	// Conversations that left the catalog (or a dirty mark for one).
	gone := []string{}
	for id := range stored {
		if p.conversations[id] == nil {
			gone = append(gone, id)
		}
	}
	for id := range tokens {
		if p.conversations[id] == nil && stored[id] == "" {
			gone = append(gone, id)
		}
	}
	if len(gone) > 0 && len(opts.Conversations) == 0 && !opts.DryRun {
		stats.ConversationsRemoved = len(gone)
		if err := c.writeTransaction(ctx, "remove labels of deleted conversations", func(tx *sql.Tx) error {
			for _, id := range gone {
				for _, statement := range []string{`DELETE FROM message_labels WHERE conversation_id=?`, `DELETE FROM message_labels_conversations WHERE conversation_id=?`} {
					if _, err := tx.ExecContext(ctx, statement, id); err != nil {
						return err
					}
				}
				// Only the mark this run read: one written since (a conversation
				// created after the builder read the catalog) stays.
				if _, err := tx.ExecContext(ctx, `DELETE FROM message_labels_dirty WHERE conversation_id=? AND token=?`, id, tokens[id]); err != nil {
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

// labelsRun is one run's pipeline: a producer builds conversations' items;
// the run packs their sequences into batches, keeps a bounded number in
// flight at the workers, merges results per message and hands finished
// conversations to a writer that stores them in batched transactions.
type labelsRun struct {
	c        *Catalog
	settings LabelerSettings
	opts     LabelsRunOptions
	model    *labelrt.Model
	identity string
	stats    *LabelsRunStats
	progress func(string, ...any)
	runner   *labelrt.Runner
	release  func()
	// restartsBefore is the warm runner's restarts when this run took it.
	restartsBefore int
	// cut is set when the limit left work for a later run.
	cut bool
	// cancel stops the run (the writer's failure does).
	cancel   context.CancelFunc
	exportMu sync.Mutex
}

// export writes one item's line (see LabelsRunOptions.Export).
func (r *labelsRun) export(item labelingItem, seq []int32, spans labelrt.FrameSpans, probs []float32, mask uint64) {
	if r.opts.Export == nil {
		return
	}
	digest := func(text string) string { sum := sha256.Sum256([]byte(text)); return hex.EncodeToString(sum[:12]) }
	line := map[string]any{"item_id": item.ID, "message_id": item.MessageID, "conversation_id": item.ConversationID, "chunk": item.ChunkIndex,
		"chunks": item.ChunkCount, "segment": item.Segment, "cohort": item.Cohort, "sender": item.SenderRole, "context_sha": digest(item.Context),
		"text_sha": digest(item.Text), "chars": item.Chars(), "tokens": len(seq)}
	if r.opts.ExportText {
		line["context"], line["text"], line["input_ids"], line["text_span"] = item.Context, item.Text, seq, [2]int{spans.Text.Start, spans.Text.End}
	}
	if probs != nil {
		line["probs"], line["labels"] = probs, r.model.LabelNames(mask)
	}
	data, _ := json.Marshal(line)
	r.exportMu.Lock()
	defer r.exportMu.Unlock()
	r.opts.Export.Write(append(data, '\n'))
}

type labelsBatch struct {
	seqs []labelsSeq
	// isolated batches (a crashed batch's items, one each) run with nothing
	// else in flight, so an item that crashes the worker takes no other
	// item down with it.
	isolated bool
}

type labelsBatchResult struct {
	batch labelsBatch
	probs []float32
	err   error
	took  time.Duration
}

func (r *labelsRun) startPool(ctx context.Context) error {
	if r.runner != nil {
		return nil
	}
	started := time.Now()
	cfg := labelrt.RunnerConfig{Worker: r.settings.Worker, Args: runtimeArgs(r.settings), Workers: max(1, r.settings.Workers), Threads: r.settings.Threads,
		MaxBatch: max(1, r.settings.MaxBatch), Backoff: labelsWorkerBackoff,
		OnRestart: func(slot int, cause error) { r.progress("labels: restarting worker %d after: %v", slot, cause) }}
	if r.opts.OneAtATime {
		cfg.Workers, cfg.InFlight, cfg.MaxBatch = 1, 1, 1
	}
	runner, release, err := r.c.labelerManager(r.settings).Runner(ctx, r.model, cfg)
	r.stats.WorkerStart += time.Since(started)
	if err != nil {
		return err
	}
	r.runner, r.release = runner, release
	r.restartsBefore = runner.Pool().Restarts()
	ready := runner.Pool().Ready()
	r.stats.Worker = fmt.Sprintf("%s %s (%s)", ready.Worker.Name, ready.Worker.Version, ready.Worker.Runtime)
	r.stats.WorkerPIDs = runner.Pool().PIDs()
	return nil
}

func (r *labelsRun) maxBatch() int {
	switch {
	case r.opts.OneAtATime:
		return 1
	case r.runner != nil:
		// The settings' limit, lowered to what the worker announced.
		return r.runner.MaxBatch()
	}
	return max(1, r.settings.MaxBatch)
}

func (r *labelsRun) capacity() int {
	if r.opts.OneAtATime {
		return 1
	}
	return max(1, r.settings.Workers) * 2
}

// execute runs the pipeline over jobs.
func (r *labelsRun) execute(parent context.Context, builder *labelsItemBuilder, jobs []*labelsJob) (err error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer func() {
		if r.runner != nil {
			r.stats.Restarts = r.runner.Pool().Restarts() - r.restartsBefore
			if parent.Err() != nil {
				// A stop: do not wait for batches in flight.
				r.c.labelerManager(r.settings).Kill(r.runner)
			}
			// Otherwise the runner stays warm for the next run.
			r.release()
		}
	}()
	built := make(chan *labelsJob, 2)
	produced := make(chan error, 1)
	go func() { produced <- r.produce(ctx, builder, jobs, built) }()
	writes := make(chan *labelsJob, 64)
	written := make(chan error, 1)
	r.cancel = cancel
	go func() { written <- r.write(ctx, writes) }()
	batches := make(chan labelsBatch, r.capacity())
	results := make(chan labelsBatchResult, r.capacity())
	for range r.capacity() {
		go func() {
			for batch := range batches {
				rows := make([]labelrt.Framed, len(batch.seqs))
				for i, seq := range batch.seqs {
					rows[i] = labelrt.Framed{IDs: seq.message.seqs[seq.chunk], Spans: seq.message.spans[seq.chunk]}
				}
				started := time.Now()
				result, err := r.runner.RunBatch(ctx, rows, []string{"probs"})
				out := labelsBatchResult{batch: batch, err: err, took: time.Since(started)}
				if err == nil {
					if probs := result.Tensor("probs"); probs == nil || len(probs.Float32) != len(rows)*r.model.Labels() {
						out.err = fmt.Errorf("labeler worker returned no probs of shape [%d,%d]", len(rows), r.model.Labels())
					} else {
						out.probs = probs.Float32
					}
				}
				results <- out
			}
		}()
	}
	defer close(batches)

	var window []labelsSeq
	var ready []labelsBatch
	inflight, producing, pending, exclusive := 0, true, 0, false
	finish := func(job *labelsJob) { writes <- job }
	pack := func() {
		if len(window) == 0 {
			return
		}
		lengths := make([]int, len(window))
		for i, seq := range window {
			lengths[i] = len(seq.message.seqs[seq.chunk])
		}
		for _, indices := range labelrt.PackBatches(lengths, r.settings.BatchTokens, r.maxBatch(), r.model.PadMultiple) {
			batch := labelsBatch{}
			for _, index := range indices {
				batch.seqs = append(batch.seqs, window[index])
			}
			ready = append(ready, batch)
		}
		window = nil
	}
	accept := func(job *labelsJob) {
		if r.opts.DryRun {
			for _, message := range job.messages {
				r.stats.ItemsLabeled += len(message.seqs)
				r.stats.Tokens += message.tokens
			}
			r.stats.MessagesLabeled += len(job.messages)
			return
		}
		if len(job.messages) == 0 {
			finish(job)
			return
		}
		for _, message := range job.messages {
			for chunk := range message.seqs {
				window = append(window, labelsSeq{message, chunk})
				pending++
			}
		}
		r.stats.PeakPending = max(r.stats.PeakPending, pending)
	}
	// A window of a few batches' worth sorts well enough by length; a short
	// run fills no window and is packed as soon as the producer pauses. New
	// jobs are taken only while the window has room and few packed batches
	// wait, so the builder stays a bounded distance ahead of the workers
	// (memory holds a few windows, not the library).
	windowSize := 4 * r.maxBatch() * r.capacity()
	room := func() bool { return len(window) < windowSize && len(ready) <= r.capacity() }
	var failure error
	for producing || inflight > 0 || len(window) > 0 || len(ready) > 0 {
		if failure == nil {
			// Take what the producer has ready without waiting.
			for producing && room() {
				select {
				case job, ok := <-built:
					if !ok {
						producing = false
						continue
					}
					accept(job)
					continue
				default:
				}
				break
			}
			if len(window) >= windowSize || !producing || len(ready) == 0 && inflight < r.capacity() {
				// The worker's limits, which may lower the batch size, are
				// known before the first batch is packed.
				if len(window) > 0 && r.runner == nil {
					if err := r.startPool(ctx); err != nil {
						failure = err
						cancel()
						continue
					}
				}
				pack()
			}
			for len(ready) > 0 && inflight < r.capacity() && !exclusive && (!ready[0].isolated || inflight == 0) {
				if err := r.startPool(ctx); err != nil {
					failure = err
					cancel()
					break
				}
				exclusive = ready[0].isolated
				batches <- ready[0]
				ready = ready[1:]
				inflight++
			}
		} else {
			// Stopping: drop what was not sent, wait for what was.
			window, ready = nil, nil
			if producing {
				for range built {
				}
				producing = false
			}
		}
		if inflight == 0 && (!producing || failure != nil) && len(window) == 0 && len(ready) == 0 {
			break
		}
		var jobs <-chan *labelsJob
		done := ctx.Done()
		if producing && failure == nil && room() {
			jobs = built
		}
		if failure != nil {
			done = nil
		}
		select {
		case job, ok := <-jobs:
			if !ok {
				producing = false
			} else {
				accept(job)
			}
		case result := <-results:
			inflight--
			if result.batch.isolated {
				exclusive = false
			}
			before := len(ready)
			err := r.apply(result, &ready, finish)
			pending -= len(result.batch.seqs) - (len(ready) - before)
			if err != nil && failure == nil {
				failure = err
				cancel()
			}
		case <-done:
			failure = ctx.Err()
		}
	}
	if err := <-produced; err != nil && failure == nil && !errors.Is(err, context.Canceled) {
		failure = err
	}
	close(writes)
	if err := <-written; err != nil && (failure == nil || errors.Is(failure, context.Canceled) && parent.Err() == nil) {
		failure = err
	}
	r.stats.Cut = r.cut
	if r.opts.DryRun {
		r.stats.Complete = false
		return failure
	}
	r.stats.Complete = failure == nil && !r.cut && r.stats.MessagesFailed == 0 && r.stats.ConversationsFailed == 0 && len(r.opts.Conversations) == 0
	return failure
}

// apply merges a batch's results into its messages. A batch the worker
// rejected, or that kept crashing workers, is split and sent again one item
// at a time, so one bad item fails only its own message (which stays
// pending for a later run) and the rest are labeled.
func (r *labelsRun) apply(result labelsBatchResult, ready *[]labelsBatch, finish func(*labelsJob)) error {
	r.stats.Infer += result.took
	if result.err != nil {
		if errors.Is(result.err, labelrt.ErrWorkersUnavailable) || errors.Is(result.err, context.Canceled) {
			return result.err
		}
		var reply *labelrt.ErrorReply
		if !errors.As(result.err, &reply) && !errors.Is(result.err, labelrt.ErrWorkerExited) {
			return result.err
		}
		if len(result.batch.seqs) > 1 {
			// A crash's items go first and alone; a rejection's are only split.
			crashed := errors.Is(result.err, labelrt.ErrWorkerExited)
			singles := []labelsBatch{}
			for _, seq := range result.batch.seqs {
				singles = append(singles, labelsBatch{seqs: []labelsSeq{seq}, isolated: crashed})
			}
			if crashed {
				*ready = append(singles, *ready...)
			} else {
				*ready = append(*ready, singles...)
			}
			return nil
		}
		message := result.batch.seqs[0].message
		r.progress("labels: message %s failed: %v", message.id, result.err)
		message.failed = true
		message.done++
		r.messageDone(message, finish)
		return nil
	}
	r.stats.Batches++
	labels := r.model.Labels()
	longest := 0
	for _, seq := range result.batch.seqs {
		longest = max(longest, len(seq.message.seqs[seq.chunk]))
	}
	r.stats.PaddedTokens += len(result.batch.seqs) * labelrt.PadLength(longest, r.model.PadMultiple)
	for row, seq := range result.batch.seqs {
		message := seq.message
		probs := result.probs[row*labels : (row+1)*labels]
		r.stats.Tokens += len(message.seqs[seq.chunk])
		r.stats.ItemsLabeled++
		chunkMask := r.model.Decide(probs, message.segments[seq.chunk])
		message.mask |= chunkMask
		if message.items != nil {
			r.export(message.items[seq.chunk], message.seqs[seq.chunk], message.spans[seq.chunk], probs, chunkMask)
		}
		if message.probs == nil {
			message.probs = slices.Clone(probs)
		} else {
			for j, p := range probs {
				message.probs[j] = max(message.probs[j], p)
			}
		}
		message.done++
		r.messageDone(message, finish)
	}
	return nil
}

// messageDone counts a message whose chunks are all back; the last one of
// a conversation finishes its job.
func (r *labelsRun) messageDone(message *labelsMessage, finish func(*labelsJob)) {
	if message.done < len(message.seqs) {
		return
	}
	message.job.remaining--
	if message.job.remaining == 0 {
		finish(message.job)
	}
}

// produce builds each job's items, frames them and compares them with the
// stored labels, handing jobs over in order.
func (r *labelsRun) produce(ctx context.Context, builder *labelsItemBuilder, jobs []*labelsJob, out chan<- *labelsJob) error {
	defer close(out)
	limited := 0
	for index, job := range jobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.opts.Limit > 0 && limited >= r.opts.Limit {
			r.cut = true
			break
		}
		building := time.Now()
		units, err := r.buildConversation(ctx, builder, job.conversation)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// One conversation that cannot be built (a damaged row) keeps its
			// dirty mark and is retried by the next run; the rest go on.
			r.progress("labels: conversation %s skipped: %v", job.conversation, err)
			r.stats.ConversationsFailed++
			continue
		}
		stored := map[string][3]string{}
		rows, err := queryMapsContext(ctx, r.c.DB, `SELECT message_id,input_hash,model,contract FROM message_labels WHERE conversation_id=?`, job.conversation)
		if err != nil {
			return err
		}
		for _, row := range rows {
			stored[firstString(row["message_id"])] = [3]string{firstString(row["input_hash"]), firstString(row["model"]), firstString(row["contract"])}
		}
		job.units, job.complete = len(units), true
		r.stats.ConversationsBuilt++
		r.stats.Units += len(units)
		for _, unit := range units {
			items := unit.items()
			r.stats.Items += len(items)
			hash := labelsInputHash(items)
			if r.opts.DryRun && r.opts.Export != nil {
				// A dry run's export lists every item it built.
				for _, item := range items {
					seq, spans := r.model.Tokenizer.FrameWithSpans(labelrt.FrameInput{Segment: item.Segment, Cohort: item.Cohort, Sender: item.SenderRole, Context: item.Context, Text: item.Text})
					r.export(item, seq, spans, nil, 0)
				}
			}
			previous, had := stored[unit.messageID]
			delete(stored, unit.messageID)
			if had && previous == [3]string{hash, r.identity, labelsContract} {
				r.stats.MessagesUnchanged++
				continue
			}
			if r.opts.Limit > 0 && limited >= r.opts.Limit {
				job.complete, r.cut = false, true
				continue
			}
			limited++
			message := &labelsMessage{id: unit.messageID, conversation: job.conversation, hash: hash, segment: items[0].Segment}
			if r.opts.Export != nil {
				message.items = items
			}
			empty := false
			for _, item := range items {
				seq, spans, ok := labelsFrame(r.model, item)
				empty = empty || !ok
				message.spans = append(message.spans, spans)
				message.seqs = append(message.seqs, seq)
				message.segments = append(message.segments, item.Segment)
				message.tokens += len(seq)
			}
			if empty {
				// A pooling model has nothing to pool for a text with no
				// tokens: the message cannot be labeled (status empty_target)
				// and does not count among the conversation's labeled units.
				r.stats.MessagesEmptyTarget++
				job.units--
				continue
			}
			job.messages = append(job.messages, message)
		}
		for id := range stored {
			job.stale = append(job.stale, id)
		}
		sort.Strings(job.stale)
		job.remaining = len(job.messages)
		for _, message := range job.messages {
			message.job = job
		}
		r.stats.Build += time.Since(building)
		select {
		case out <- job:
		case <-ctx.Done():
			return ctx.Err()
		}
		reportWork(ctx, "labeling messages", "conversation", index+1, len(jobs))
	}
	return nil
}

// buildConversation is builder.conversation with a panic turned into an
// error for that conversation alone.
func (r *labelsRun) buildConversation(ctx context.Context, builder *labelsItemBuilder, id string) (units []*labelingUnit, err error) {
	defer func() {
		if value := recover(); value != nil {
			units, err = nil, fmt.Errorf("building its items panicked: %v", value)
		}
	}()
	return builder.conversation(ctx, id)
}

// write stores finished jobs in batched transactions: at most about 500
// rows or two seconds' worth each.
func (r *labelsRun) write(ctx context.Context, in <-chan *labelsJob) error {
	var pending []*labelsJob
	rows := 0
	var failure error
	flush := func() {
		if len(pending) == 0 || failure != nil {
			pending, rows = nil, 0
			return
		}
		started := time.Now()
		// What is finished is stored even when the run is stopping.
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		labeledAt := now()
		err := r.c.writeTransaction(writeCtx, "store message labels", func(tx *sql.Tx) error {
			for _, job := range pending {
				for _, id := range job.stale {
					if _, err := tx.ExecContext(writeCtx, `DELETE FROM message_labels WHERE message_id=? AND conversation_id=?`, id, job.conversation); err != nil {
						return err
					}
				}
				complete := job.complete
				for _, message := range job.messages {
					if message.failed {
						complete = false
						continue
					}
					if _, err := tx.ExecContext(writeCtx, `INSERT INTO message_labels(message_id,conversation_id,segment,labels,probs,chunks,tokens,input_hash,model,contract,labeled_at)
						VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(message_id) DO UPDATE SET conversation_id=excluded.conversation_id,segment=excluded.segment,labels=excluded.labels,
						probs=excluded.probs,chunks=excluded.chunks,tokens=excluded.tokens,input_hash=excluded.input_hash,model=excluded.model,contract=excluded.contract,labeled_at=excluded.labeled_at`,
						message.id, message.conversation, message.segment, int64(message.mask), encodeLabelProbs(message.probs), len(message.seqs), message.tokens,
						message.hash, r.identity, labelsContract, labeledAt); err != nil {
						return err
					}
				}
				if !complete {
					continue
				}
				if _, err := tx.ExecContext(writeCtx, `INSERT INTO message_labels_conversations(conversation_id,structure,units,built_at) VALUES(?,?,?,?)
					ON CONFLICT(conversation_id) DO UPDATE SET structure=excluded.structure,units=excluded.units,built_at=excluded.built_at`,
					job.conversation, job.structure, job.units, labeledAt); err != nil {
					return err
				}
				// A mark written after this run read it carries another token
				// and stays.
				if _, err := tx.ExecContext(writeCtx, `DELETE FROM message_labels_dirty WHERE conversation_id=? AND token=?`, job.conversation, job.token); err != nil {
					return err
				}
			}
			return nil
		})
		r.stats.Write += time.Since(started)
		if err != nil {
			// Nothing more can be stored: stop labeling now. The writer
			// keeps draining (and dropping) so the run is never blocked on it.
			failure = err
			r.cancel()
			return
		}
		for _, job := range pending {
			r.stats.MessagesRemoved += len(job.stale)
			for _, message := range job.messages {
				if message.failed {
					r.stats.MessagesFailed++
				} else {
					r.stats.MessagesLabeled++
				}
			}
		}
		pending, rows = nil, 0
	}
	ticker := time.NewTicker(labelsFlushEvery)
	defer ticker.Stop()
	for {
		select {
		case job, ok := <-in:
			if !ok {
				flush()
				return failure
			}
			pending = append(pending, job)
			rows += 1 + len(job.messages) + len(job.stale)
			if rows >= 500 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// labelsWorkerBackoff is the wait before a crashed worker's nth restart
// (nil: the pool's default, 0.5 s doubling to 30 s).
var labelsWorkerBackoff func(n int) time.Duration

// labelsFlushEvery is the longest finished labels wait to be stored.
var labelsFlushEvery = 2 * time.Second

// encodeLabelProbs stores probabilities as little-endian float32.
func encodeLabelProbs(probs []float32) []byte {
	out := make([]byte, 4*len(probs))
	for j, p := range probs {
		binary.LittleEndian.PutUint32(out[4*j:], math.Float32bits(p))
	}
	return out
}

func decodeLabelProbs(raw []byte) []float32 {
	probs := make([]float32, len(raw)/4)
	for j := range probs {
		probs[j] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*j:]))
	}
	return probs
}

// labelsChainLimit bounds the messages the analysis chain labels inline, so
// an index run ends in bounded time; a larger backlog continues as its own
// background run when power allows (a few new messages after an index never
// get there).
var labelsChainLimit = 2000

// errStageContinues is a stage that did its share but leaves work waiting:
// its deferred generation stays pending, so the schedule runs it again.
var errStageContinues = errors.New("stage continues later")

// labelsPowerCheck is how often a backlog run checks the power source.
var labelsPowerCheck = time.Minute

// chainLabels is the analysis chain's labels stage (background item
// "labels"). It waits while the model or runtime is still downloading. It
// runs within the budget this power allows (labelsForPower): plugged in, a
// backlog past the chain's share continues as its own analysis.labels run,
// which stops if this Mac goes onto battery; on battery the backlog waits,
// and the stage stays pending for the next pass.
func (s *Server) chainLabels(ctx context.Context, settings LabelerSettings) error {
	settings = settings.withAssets()
	if len(settings.missingAssets()) > 0 {
		return errStageContinues
	}
	settings, bulk := labelsForPower(settings, s.cachedPower(ctx))
	stats, err := s.Catalog.RunLabels(ctx, settings, LabelsRunOptions{Limit: labelsChainLimit})
	switch {
	case errors.Is(err, errLabelsRunning):
		// A backlog run (or pharos labels run) is labeling; this pass
		// checks again later.
		return errStageContinues
	case err != nil:
		return err
	case !stats.Cut && !stats.Complete:
		// A failed message or an unbuildable conversation is retained for
		// the next pass: completing the stage would stop its schedule.
		return errStageContinues
	case !stats.Cut:
		return nil
	case !bulk:
		return errStageContinues
	}
	s.Catalog.startBackground(OperationSpec{Kind: "analysis.labels", Variant: "backlog", Trigger: triggerChained, Priority: priorityBackground}, func(ctx context.Context, op *Op) error {
		ctx = withWorkProgress(ctx, op.mirrorWork)
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		var unplugged atomic.Bool
		go func() {
			ticker := time.NewTicker(labelsPowerCheck)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, bulk := labelsForPower(settings, s.cachedPower(ctx)); !bulk {
						unplugged.Store(true)
						cancel()
						return
					}
				}
			}
		}()
		_, err := s.Catalog.RunLabels(ctx, settings, LabelsRunOptions{})
		switch {
		case errors.Is(err, labelrt.ErrWorkersUnavailable), unplugged.Load():
			// Workers failing, or this Mac went onto battery: stored labels
			// are kept and the next pass resumes.
			op.legacyOutcome("interrupted", nil)
			return nil
		case errors.Is(err, errLabelsRunning):
			return nil
		}
		return err
	})
	return errStageContinues
}

// labelsReusers maps a conversation to the conversations that re-send one of
// its messages. A re-sent message's target text is resolved against its
// source's classification (labelingPass.reuseFrom), so when the source's
// classification changes, the copy's items change although its own spans do
// not. The source's conversation is taken from its message, so a mirror's
// recorded source conversation cannot hide the dependency.
func (c *Catalog) labelsReusers(ctx context.Context) (map[string][]string, error) {
	rows, err := queryMapsContext(ctx, c.DB, `SELECT DISTINCT src.conversation_id source,m.conversation_id user
		FROM message_authorship_spans s
		JOIN messages m ON m.id=s.message_id
		JOIN messages src ON src.id=s.source_message_id
		WHERE s.category=? AND src.conversation_id<>m.conversation_id`, spanResent)
	if err != nil {
		return nil, err
	}
	reusers := map[string][]string{}
	for _, row := range rows {
		source := firstString(row["source"])
		reusers[source] = append(reusers[source], firstString(row["user"]))
	}
	return reusers, nil
}

// messageLabelsAttacher adds a labeled message's labels to its reader row:
// its segment, its closed label set in the model's order, and each label's
// probability. summary describes the conversation: whether labeling still has
// work there, and the taxonomy rows its labels name, so the reader can show
// names and definitions. Without a ledger it attaches nothing and summary is
// nil; it only reads, so a page never starts labeling.
func (c *Catalog) messageLabelsAttacher(conversationID any) (attach func(map[string]any), summary func() (map[string]any, error), err error) {
	attach, summary = func(map[string]any) {}, func() (map[string]any, error) { return nil, nil }
	var tables int
	if err := c.reader().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='message_labels_models'`).Scan(&tables); err != nil || tables == 0 {
		return attach, summary, err
	}
	models, err := queryMaps(c.reader(), `SELECT model,labels_json FROM message_labels_models`)
	if err != nil {
		return attach, summary, err
	}
	orders := map[string][]string{}
	for _, row := range models {
		var order []string
		if json.Unmarshal([]byte(firstString(row["labels_json"])), &order) == nil {
			orders[firstString(row["model"])] = order
		}
	}
	rows, err := queryMaps(c.reader(), `SELECT message_id,segment,labels,probs,model FROM message_labels WHERE conversation_id=?`, conversationID)
	if err != nil {
		return attach, summary, err
	}
	byID := map[string]map[string]any{}
	named := map[string]bool{}
	for _, row := range rows {
		order := orders[firstString(row["model"])]
		// queryMaps reads a BLOB as a string.
		raw, _ := row["probs"].([]byte)
		if text, ok := row["probs"].(string); ok {
			raw = []byte(text)
		}
		probs, mask := decodeLabelProbs(raw), uint64(integer(row["labels"]))
		ids, chances := []string{}, map[string]float64{}
		for j, id := range order {
			if j < 64 && mask&(1<<j) != 0 {
				ids = append(ids, id)
				named[id] = true
				if j < len(probs) {
					chances[id] = math.Round(float64(probs[j])*1000) / 1000
				}
			}
		}
		if len(order) > 0 {
			byID[firstString(row["message_id"])] = map[string]any{"segment": row["segment"], "ids": ids, "probs": chances}
		}
	}
	attach = func(message map[string]any) {
		if labels := byID[firstString(message["id"])]; labels != nil {
			message["labels"] = labels
		}
	}
	summary = func() (map[string]any, error) {
		var pending bool
		if err := c.reader().QueryRow(`SELECT EXISTS(SELECT 1 FROM message_labels_dirty WHERE conversation_id=?)
			OR NOT EXISTS(SELECT 1 FROM message_labels_conversations WHERE conversation_id=?)`, conversationID, conversationID).Scan(&pending); err != nil {
			return nil, err
		}
		if len(byID) == 0 && !pending {
			return nil, nil
		}
		taxonomy, err := labelingTaxonomyRows()
		if err != nil {
			return nil, err
		}
		shown := []map[string]any{}
		for _, row := range taxonomy {
			if named[row.ID] {
				shown = append(shown, map[string]any{"id": row.ID, "name": row.Name, "definition": row.Definition})
			}
		}
		return map[string]any{"labeled": len(byID), "pending": pending, "taxonomy": shown}, nil
	}
	return attach, summary, nil
}

package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// labelingJobSQL is the run ledger, kept in the dataset beside what it
// labels. It is not part of labelingDatasetSQL, so prepare --replace refuses
// a dataset that holds it.
const labelingJobSQL = `
CREATE TABLE IF NOT EXISTS jobs(
  name TEXT PRIMARY KEY, created_at TEXT NOT NULL, route TEXT NOT NULL, model TEXT NOT NULL, effort TEXT NOT NULL,
  prompt_version TEXT NOT NULL, prompt_template_sha256 TEXT NOT NULL, schema_version TEXT NOT NULL, schema_sha256 TEXT NOT NULL, taxonomy_version TEXT NOT NULL, taxonomy_sha256 TEXT NOT NULL,
  dataset_contracts TEXT NOT NULL, selection TEXT NOT NULL, packing TEXT NOT NULL, imported_from TEXT,
  served_model TEXT NOT NULL, pricing_rule TEXT, cli_path TEXT, cli_version TEXT, stop_requested INTEGER NOT NULL DEFAULT 0, run_started_at TEXT, run_settings TEXT, halted TEXT);
CREATE TABLE IF NOT EXISTS requests(
  id TEXT PRIMARY KEY, job TEXT NOT NULL, position INTEGER NOT NULL, state TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
  retry_of TEXT, items INTEGER NOT NULL, chars INTEGER NOT NULL, est_input_tokens REAL NOT NULL, est_output_tokens REAL NOT NULL,
  updated_at TEXT NOT NULL, note TEXT);
CREATE INDEX IF NOT EXISTS requests_job_state_idx ON requests(job, state, position);
CREATE TABLE IF NOT EXISTS request_items(
  request_id TEXT NOT NULL, position INTEGER NOT NULL, item_id TEXT NOT NULL, contract_hash TEXT NOT NULL, tokens REAL NOT NULL,
  chars INTEGER NOT NULL, PRIMARY KEY(request_id, position)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS request_items_item_idx ON request_items(item_id);
CREATE TABLE IF NOT EXISTS attempts(
  id TEXT PRIMARY KEY, request_id TEXT NOT NULL, number INTEGER NOT NULL, job TEXT NOT NULL, state TEXT NOT NULL, outcome TEXT,
  submitted_at TEXT NOT NULL, finished_at TEXT, day TEXT NOT NULL, dir TEXT NOT NULL, prompt_sha256 TEXT NOT NULL, session_id TEXT,
  reported_models TEXT, pid INTEGER, exit_code INTEGER, terminal_event TEXT, cleanup TEXT, tool_events TEXT, refusal TEXT, error TEXT,
  cli_cost_usd REAL, duration_ms INTEGER, problems TEXT, recovered INTEGER NOT NULL DEFAULT 0,
  not_sent INTEGER NOT NULL DEFAULT 0, served_model TEXT, cli_version TEXT, process_start TEXT, receipt_error TEXT,
  usage_missing INTEGER NOT NULL DEFAULT 0, cli_warnings TEXT);
CREATE INDEX IF NOT EXISTS attempts_request_idx ON attempts(request_id, number);
CREATE TABLE IF NOT EXISTS item_results(
  attempt_id TEXT NOT NULL, item_id TEXT NOT NULL, job TEXT NOT NULL, request_id TEXT, outcome TEXT NOT NULL,
  labels TEXT NOT NULL, uncertain_labels TEXT NOT NULL, evidence TEXT NOT NULL, need_context INTEGER, issues TEXT NOT NULL,
  annotation TEXT, contract_hash TEXT NOT NULL, imported_from TEXT, served_model TEXT, labels_evidence TEXT NOT NULL DEFAULT '{}', held_outcome TEXT,
  created_at TEXT NOT NULL, PRIMARY KEY(attempt_id, item_id));
CREATE TABLE IF NOT EXISTS decisions(job TEXT NOT NULL, kind TEXT NOT NULL, value TEXT NOT NULL, decided_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS accepted_models(job TEXT NOT NULL, model TEXT NOT NULL, accepted_at TEXT NOT NULL, PRIMARY KEY(job, model));
CREATE INDEX IF NOT EXISTS item_results_job_item_idx ON item_results(job, item_id);
CREATE INDEX IF NOT EXISTS item_results_outcome_idx ON item_results(outcome);
CREATE INDEX IF NOT EXISTS item_results_item_idx ON item_results(item_id);
CREATE TABLE IF NOT EXISTS usage(
  attempt_id TEXT NOT NULL, model TEXT NOT NULL, model_source TEXT NOT NULL, tokens TEXT NOT NULL, cost_usd REAL, cost_status TEXT NOT NULL,
  priced_model TEXT, day TEXT NOT NULL, role TEXT, PRIMARY KEY(attempt_id, model));
`

// Request states. Every transition is one transaction; submitted is
// written before the CLI starts, so a request is never sent unrecorded.
const (
	requestPending   = "pending"
	requestLeased    = "leased"
	requestSubmitted = "submitted"
	requestCompleted = "completed"
	requestInvalid   = "invalid"
	requestRefused   = "refused"
	requestFailed    = "failed"
	requestUnknown   = "unknown"
	requestStopped   = "stopped"
	// held: a model other than the requested one answered; its results are
	// kept, not published, and the run halts for a decision.
	requestHeld = "held"
	// refusal_suspected: refusal words in a reply that is not a clear
	// refusal; re-sent only by reconcile --retry-suspected-refusals.
	requestRefusalSuspected = "refusal_suspected"
	// refused_elsewhere: every item of the request was blocked, before it
	// was sent, by a refusal recorded elsewhere.
	requestRefusedElsewhere = "refused_elsewhere"
	// waiting_suspicion: held, unsent, while one of its items is a
	// suspected refusal in another job; back to pending at the next run.
	requestWaitingSuspicion = "waiting_suspicion"
)

// Served-model verification, per job, attempt and item result.
const (
	servedModelVerified   = "verified"
	servedModelUnverified = "served_model_unverified"
	servedModelDiffers    = "served_model_differs"
	// A served model that differs, accepted by the operator.
	servedModelAccepted = "served_model_accepted"
)

var labelingRequestStates = []string{requestPending, requestLeased, requestSubmitted, requestCompleted, requestInvalid, requestRefused,
	requestFailed, requestUnknown, requestHeld, requestRefusalSuspected, requestRefusedElsewhere, requestWaitingSuspicion, requestStopped}

var labelingJobName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// labelingJobSpec is what a job labels and how; it is frozen at creation.
type labelingJobSpec struct {
	Route, Model, Effort, Prompt string
	Cohorts, Splits              []string
	Items                        []string
	LimitItems                   int
	IncludeDuplicates            bool
	MaxPerText                   int
	ItemsPerRequest              int
	MaxRequestTokens             float64
	MaxRequestChars              int
	// SkipOversize creates the job without items too large for one
	// request: they are recorded as blocked_oversize and the job is
	// incomplete. Without it, creation fails.
	SkipOversize bool
}

type labelingJob struct {
	Name, Route, Model, Effort, Prompt string
	CLIVersion, CLIPath                string
}

// openLabelingLedger opens a prepared dataset for writing the run ledger.
func openLabelingLedger(dir string) (*sql.DB, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, labelingManifestFile)); err != nil {
		return nil, fmt.Errorf("%s has no complete prepared dataset (no %s); run labeling prepare", dir, labelingManifestFile)
	}
	path := filepath.Join(dir, labelingDatasetFile)
	// Several runners (one per job) can share a dataset. WAL lets their
	// readers proceed beside a writer, and immediate transactions take the
	// write lock up front: a deferred transaction that read before another
	// runner committed could not upgrade (SQLITE_BUSY_SNAPSHOT), whatever the
	// busy timeout. A finished dataset leaves prepare in rollback-journal mode;
	// the first ledger writer switches it.
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+
		"?_pragma=busy_timeout(60000)&_pragma=synchronous(FULL)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := labelingUseWAL(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := upgradeLabelingLedger(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// labelingWALWait bounds how long an opener retries switching a dataset to
// WAL while other openers hold it.
var labelingWALWait = 60 * time.Second

// labelingUseWAL switches the dataset to WAL (it stays so). While runners
// open a dataset in rollback-journal mode at once, SQLite refuses the
// switch at once with SQLITE_BUSY rather than wait (one holding a shared
// lock cannot wait for another's exclusive one), so it is retried.
func labelingUseWAL(db *sql.DB) error {
	deadline := time.Now().Add(labelingWALWait)
	for {
		var mode string
		err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode)
		if err == nil && mode != "wal" {
			err = fmt.Errorf("journal_mode is %s, not wal", mode)
		}
		if err == nil || !strings.Contains(err.Error(), "SQLITE_BUSY") && !strings.Contains(err.Error(), "database is locked") || time.Now().After(deadline) {
			return err
		}
		time.Sleep(time.Duration(5+rand.IntN(20)) * time.Millisecond)
	}
}

// labelingLedgerColumns are columns added after a ledger may already have
// been created by an earlier build; the ledger gains them in place.
var labelingLedgerColumns = []struct{ table, column, definition string }{
	{"jobs", "pricing_rule", "TEXT"},
	{"attempts", "cli_warnings", "TEXT"},
	{"usage", "role", "TEXT"},
	{"jobs", "stop_requested_at", "TEXT"},
	{"jobs", "refusal_registry", "TEXT"},
	{"usage", "estimate_usd", "REAL"},
	{"usage", "estimate_basis", "TEXT"},
	{"jobs", "registry_entries", "INTEGER"},
	{"request_items", "blocked", "TEXT"},
}

// labelingHasColumn lets read-only status read a ledger an older build
// created, before a writing command adds the column.
func labelingHasColumn(db queryer, table, column string) bool {
	rows, err := queryMaps(db, `SELECT COUNT(*) n FROM pragma_table_info(?) WHERE name=?`, table, column)
	return err == nil && len(rows) == 1 && integer(rows[0]["n"]) > 0
}

// upgradeLabelingLedger creates the ledger's tables and adds missing
// columns in one immediate transaction: runners that open a fresh dataset
// at once serialize on the write lock, and each sees the tables and columns
// the one before it committed.
func upgradeLabelingLedger(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(labelingJobSQL); err != nil {
		return err
	}
	for _, add := range labelingLedgerColumns {
		if err := labelingAddColumn(tx, add.table, add.column, add.definition); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// labelingAddColumn adds a column unless the table has it, tolerating one
// another opener added meanwhile.
func labelingAddColumn(db interface {
	queryer
	Exec(string, ...any) (sql.Result, error)
}, table, column, definition string) error {
	if labelingHasColumn(db, table, column) {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	return nil
}

// labelingHasLedger reports whether a dataset holds run tables, so status
// can read them without creating them.
func labelingHasLedger(db *sql.DB) bool {
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='jobs'`).Scan(&n)
	return n > 0
}

func loadLabelingJob(db *sql.DB, name string) (labelingJob, error) {
	job := labelingJob{Name: name}
	var version, path sql.NullString
	err := db.QueryRow(`SELECT route,model,effort,prompt_version,cli_version,cli_path FROM jobs WHERE name=?`, name).
		Scan(&job.Route, &job.Model, &job.Effort, &job.Prompt, &version, &path)
	if errors.Is(err, sql.ErrNoRows) {
		return job, fmt.Errorf("no job %q in this dataset; create it with labeling run --create", name)
	}
	job.CLIVersion, job.CLIPath = version.String, path.String
	return job, err
}

// labelingPromptTemplateHash identifies a prompt version's fixed text (the
// prompt with no items), so a resumed job notices a changed build.
func labelingPromptTemplateHash(version string) string {
	if hash, ok := labelingTemplateHashes.Load(version); ok {
		return hash.(string)
	}
	prompt, err := buildLabelingPrompt(version, nil)
	if err != nil {
		return ""
	}
	hash := hashBytes([]byte(prompt))
	labelingTemplateHashes.Store(version, hash)
	return hash
}

var labelingTemplateHashes sync.Map

// labelingSchemaHash is the schema text's hash, computed once.
var labelingSchemaHash = sync.OnceValue(func() string {
	schema, _ := labelingSchemaJSON()
	return hashBytes([]byte(schema))
})

// labelingContractHash identifies what the model saw for an item: prompt
// version, taxonomy, schema, sender, channel, context and CURRENT_TEXT.
// An item whose hash is unchanged in a later snapshot can keep its result.
func labelingContractHash(promptVersion string, row labelingPromptRow) string {
	return hashBytes([]byte(strings.Join([]string{promptVersion, labelingPromptTemplateHash(promptVersion), labelingTaxonomyVersion, hashBytes(labelingTaxonomy), labelingSchemaHash(),
		row.Sender, row.Channel, row.Context, fmt.Sprint(row.Excerpt), row.Text}, "\x00")))
}

// labelingSelectedItem is one selected item with its prompt row.
type labelingSelectedItem struct {
	row                               labelingPromptRow
	cohort, split, family, refusalKey string
	chars                             int
	tokens                            float64
	duplicate                         bool
}

// selectLabelingItems applies a selection in item-id order, as planLabeling
// does: cohorts, splits, an explicit item list, duplicate skipping and a
// limit.
func selectLabelingItems(ctx context.Context, db *sql.DB, spec labelingJobSpec) ([]labelingSelectedItem, int, int, error) {
	rows, err := db.QueryContext(ctx, `SELECT i.id,u.cohort,COALESCE(u.split,''),i.chars,i.text,u.context_prompt,u.context_excerpt,u.sender_role,u.channel,
		u.duplicate_of IS NOT NULL,COALESCE(u.family_id,u.conversation_id),i.target_hash,u.message_sha256 FROM items i JOIN units u ON u.id=i.unit_id ORDER BY i.id`)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	wanted := map[string]bool{}
	for _, id := range spec.Items {
		wanted[id] = true
	}
	items := []labelingSelectedItem{}
	duplicates, repeated := 0, 0
	perText := map[[2]string]int{}
	for rows.Next() {
		var item labelingSelectedItem
		var hash, message string
		if err := rows.Scan(&item.row.ID, &item.cohort, &item.split, &item.chars, &item.row.Text, &item.row.Context, &item.row.Excerpt,
			&item.row.Sender, &item.row.Channel, &item.duplicate, &item.family, &hash, &message); err != nil {
			return nil, 0, 0, err
		}
		item.refusalKey = message + ":" + hash
		if len(wanted) > 0 && !wanted[item.row.ID] {
			continue
		}
		if len(spec.Cohorts) > 0 && !slices.Contains(spec.Cohorts, item.cohort) || len(spec.Splits) > 0 && !slices.Contains(spec.Splits, item.split) {
			continue
		}
		if item.duplicate && !spec.IncludeDuplicates && len(wanted) == 0 {
			duplicates++
			continue
		}
		if !labelingTextAllowed(perText, item.cohort, hash, spec.MaxPerText) {
			repeated++
			continue
		}
		item.chars += utf8.RuneCountInString(item.row.Context)
		item.tokens = labelingItemTokens(item.row.Text, labelingCharsPerToken) + labelingItemTokens(item.row.Context, labelingCharsPerToken)
		items = append(items, item)
		if spec.LimitItems > 0 && len(items) == spec.LimitItems {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	if len(wanted) > 0 && len(items) < len(wanted) {
		found := map[string]bool{}
		for _, item := range items {
			found[item.row.ID] = true
		}
		missing := []string{}
		for _, id := range spec.Items {
			if !found[id] {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 && spec.LimitItems == 0 {
			return nil, 0, 0, fmt.Errorf("items not in this dataset or outside the selection: %s", strings.Join(missing, ", "))
		}
	}
	return items, duplicates, repeated, nil
}

// labelingImported is a result copied from an earlier dataset's job.
type labelingImported struct {
	result             labelingItemResult
	hash, from, served string
}

// labelingImportHook runs inside the creation transaction between the
// requests and the imported results; tests make it fail.
var labelingImportHook = func() error { return nil }

// createLabelingJob freezes a job's selection and packs it into pending
// requests with phase 1's packer. Imported results (from an earlier
// dataset) are written in the same transaction; their items are selected
// but not sent. Every selected item ends up requested, imported, skipped
// for a refusal, or (with SkipOversize) blocked as oversize, or nothing is
// written.
func createLabelingJob(ctx context.Context, db *sql.DB, name string, spec labelingJobSpec, imports []labelingImported, importedFrom, registry string, importRefused map[string]bool) (map[string]any, error) {
	skip := map[string]bool{}
	for _, imported := range imports {
		skip[imported.result.ItemID] = true
	}
	if !labelingJobName.MatchString(name) {
		return nil, fmt.Errorf("job name %q: use letters, digits, '.', '_' and '-'", name)
	}
	if spec.Route != labelingRouteClaude && spec.Route != labelingRouteCodex && spec.Route != labelingRouteAgy {
		return nil, fmt.Errorf("--route %q: use claude, codex or agy", spec.Route)
	}
	if spec.Model == "" || spec.Effort == "" {
		return nil, errors.New("a new job needs --route, --model and --effort")
	}
	if spec.Prompt == "" {
		spec.Prompt = labelingPromptPieces
	}
	if !slices.Contains(labelingPromptVersions, spec.Prompt) {
		return nil, fmt.Errorf("--prompt %q: use one of %s", spec.Prompt, strings.Join(labelingPromptVersions, ", "))
	}
	if spec.ItemsPerRequest == 0 {
		spec.ItemsPerRequest = 4
	}
	if spec.MaxRequestTokens == 0 {
		spec.MaxRequestTokens = 24000
	}
	if spec.ItemsPerRequest < 1 || spec.MaxRequestTokens < 0 || spec.MaxRequestChars < 0 {
		return nil, errors.New("--items-per-request and --max-request-tokens must be positive, --max-request-chars not negative")
	}
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE name=?`, name).Scan(&exists); err != nil {
		return nil, err
	}
	if exists > 0 {
		return nil, fmt.Errorf("job %q already exists; its selection is frozen (run it without --create, or choose another name)", name)
	}
	items, duplicates, repeated, err := selectLabelingItems(ctx, db, spec)
	if err != nil {
		return nil, err
	}
	// Refusals (labeling_refusals.go): pinned refusals block by content
	// (the registry, and this dataset's own), co-packed ones by item ID (this
	// dataset's, and the dataset imported from). Skipped items get a
	// refused_elsewhere result, so they stay visible.
	check, err := loadLabelingRefusalCheck(registry, nil)
	if err != nil {
		return nil, err
	}
	refusedItems, pinned, err := labelingLedgerRefusals(db)
	if err != nil {
		return nil, err
	}
	send, refusedSkipped := []labelingSelectedItem{}, []labelingSelectedItem{}
	for _, item := range items {
		switch {
		case skip[item.row.ID]:
		case check.registry[item.refusalKey] || pinned[item.refusalKey] || refusedItems[item.row.ID] || importRefused[item.row.ID]:
			refusedSkipped = append(refusedSkipped, item)
		default:
			send = append(send, item)
		}
	}
	calibration := labelingCalibrations[spec.Route]
	sizes, lengths, families := make([]float64, len(send)), make([]int, len(send)), make([]string, len(send))
	for index, item := range send {
		sizes[index], lengths[index], families[index] = item.tokens, item.chars, item.family
	}
	packed, oversize := packLabelingFamilies(sizes, lengths, families, spec.ItemsPerRequest, calibration.overheadTokens, spec.MaxRequestTokens, spec.MaxRequestChars)
	oversized := []string{}
	for _, index := range oversize {
		oversized = append(oversized, send[index].row.ID)
	}
	// A selected item that fits no request would never get an outcome.
	if len(oversized) > 0 && !spec.SkipOversize {
		return nil, fmt.Errorf("%d selected items do not fit one request within --max-request-tokens/--max-request-chars: %s; raise the limits, leave them out (--item, --cohort), or create the job without them (--skip-oversize); nothing was created",
			len(oversized), strings.Join(oversized, ", "))
	}
	var manifestText string
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='manifest'`).Scan(&manifestText); err != nil {
		return nil, fmt.Errorf("dataset manifest: %w", err)
	}
	var manifest map[string]any
	json.Unmarshal([]byte(manifestText), &manifest)
	schema, err := labelingSchemaJSON()
	if err != nil {
		return nil, err
	}
	selection := map[string]any{"cohorts": spec.Cohorts, "splits": spec.Splits, "items": spec.Items, "limit_items": spec.LimitItems,
		"include_duplicates": spec.IncludeDuplicates, "duplicate_items_skipped": duplicates, "selected_items": len(items),
		"imported_items": len(skip), "refused_skipped": len(refusedSkipped), "refusal_registry": registry,
		"max_per_text": spec.MaxPerText, "blocked_oversize": len(oversized), "incomplete": len(oversized) > 0, "repeated_text_items_skipped": repeated, "one_item_per_family": true, "oversize_items": oversized, "requests": len(packed)}
	packing := map[string]any{"items_per_request": spec.ItemsPerRequest, "max_request_tokens": spec.MaxRequestTokens,
		"max_request_chars": spec.MaxRequestChars, "overhead_tokens": calibration.overheadTokens, "output_tokens_per_item": calibration.outputItem,
		"chars_per_token": labelingCharsPerToken, "calibration": calibration.source}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	served := servedModelVerified
	if spec.Route == labelingRouteCodex {
		served = servedModelUnverified
	}
	if _, err := tx.Exec(`INSERT INTO jobs(name,created_at,route,model,effort,prompt_version,schema_version,schema_sha256,taxonomy_version,taxonomy_sha256,
		dataset_contracts,selection,packing,imported_from,served_model,prompt_template_sha256,pricing_rule,refusal_registry) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		name, now(), spec.Route, spec.Model, spec.Effort, spec.Prompt, labelingSchemaVersion, hashBytes([]byte(schema)), labelingTaxonomyVersion,
		hashBytes(labelingTaxonomy), compactJSON(manifest["contracts"]), compactJSON(selection), compactJSON(packing), nilIfEmpty(importedFrom), served, labelingPromptTemplateHash(spec.Prompt), labelingPricingRule, registry); err != nil {
		return nil, err
	}
	for position, request := range packed {
		id := fmt.Sprintf("%s:%06d", name, position+1)
		input, chars := calibration.overheadTokens, 0
		for _, index := range request {
			input += send[index].tokens
			chars += send[index].chars
		}
		if _, err := tx.Exec(`INSERT INTO requests(id,job,position,state,items,chars,est_input_tokens,est_output_tokens,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			id, name, position+1, requestPending, len(request), chars, input, calibration.outputItem*float64(len(request)), now()); err != nil {
			return nil, err
		}
		for itemPosition, index := range request {
			item := send[index]
			if _, err := tx.Exec(`INSERT INTO request_items(request_id,position,item_id,contract_hash,tokens,chars) VALUES(?,?,?,?,?,?)`,
				id, itemPosition, item.row.ID, labelingContractHash(spec.Prompt, item.row), item.tokens, item.chars); err != nil {
				return nil, err
			}
		}
	}
	for _, item := range refusedSkipped {
		if err := insertLabelingRefusedElsewhere(tx, "refused-elsewhere:create:"+name, name, "", item.row.ID, labelingContractHash(spec.Prompt, item.row), "refused_before_selection"); err != nil {
			return nil, err
		}
	}
	// Oversize items skipped on request keep a result saying so; the job is
	// incomplete.
	for _, item := range oversized {
		result := labelingItemResult{ItemID: item, Outcome: itemBlockedOversize, Labels: []string{}, Uncertain: []string{}, Evidence: []labelingEvidence{},
			Issues: []string{"blocked_oversize"}}
		if err := insertLabelingItemResult(tx, "blocked-oversize:"+name, name, "", result, "", "", ""); err != nil {
			return nil, err
		}
	}
	if err := labelingImportHook(); err != nil {
		return nil, err
	}
	for _, imported := range imports {
		if err := insertLabelingItemResult(tx, "imported:"+hashBytes([]byte(imported.from))[:16], name, "", imported.result, imported.hash, imported.from, imported.served); err != nil {
			return nil, err
		}
	}
	var requested, copied int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM request_items i JOIN requests r ON r.id=i.request_id WHERE r.job=?`, name).Scan(&requested); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(`SELECT COUNT(DISTINCT item_id) FROM item_results WHERE job=? AND imported_from IS NOT NULL`, name).Scan(&copied); err != nil {
		return nil, err
	}
	if requested+copied+len(oversized)+len(refusedSkipped) != len(items) {
		return nil, fmt.Errorf("job %s: %d selected items but %d requested, %d imported, %d refused before and %d oversize; nothing was written",
			name, len(items), requested, copied, len(refusedSkipped), len(oversized))
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"job": name, "route": spec.Route, "model": spec.Model, "effort": spec.Effort, "prompt": spec.Prompt,
		"selection": selection, "packing": packing}, nil
}

// labelingRetryRequest re-packs items whose attempt was invalid or missing
// into a new pending request, within one transaction.
func labelingRetryRequest(tx *sql.Tx, job, retryOf string, items []string, outputPerItem float64) error {
	if len(items) == 0 {
		return nil
	}
	var position int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(position),0)+1 FROM requests WHERE job=?`, job).Scan(&position); err != nil {
		return err
	}
	var overhead float64
	if err := tx.QueryRow(`SELECT est_input_tokens - (SELECT COALESCE(SUM(tokens),0) FROM request_items WHERE request_id=?) FROM requests WHERE id=?`,
		retryOf, retryOf).Scan(&overhead); err != nil {
		return err
	}
	id := fmt.Sprintf("%s:%06d", job, position)
	input, chars := overhead, 0
	for index, item := range items {
		var hash string
		var tokens float64
		var length int
		if err := tx.QueryRow(`SELECT contract_hash,tokens,chars FROM request_items WHERE request_id=? AND item_id=?`, retryOf, item).Scan(&hash, &tokens, &length); err != nil {
			return err
		}
		input += tokens
		chars += length
		if _, err := tx.Exec(`INSERT INTO request_items(request_id,position,item_id,contract_hash,tokens,chars) VALUES(?,?,?,?,?,?)`, id, index, item, hash, tokens, length); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO requests(id,job,position,state,retry_of,items,chars,est_input_tokens,est_output_tokens,updated_at,note) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		id, job, position, requestPending, retryOf, len(items), chars, input, outputPerItem*float64(len(items)), now(),
		"retry of invalid or missing items (--retry-invalid)")
	return err
}

// labelingRequestEstimate prices a request's estimated tokens for the
// ceiling: nil when the model has no price.
func labelingRequestEstimate(book priceBook, model, day string, input, output float64) *float64 {
	cost := labelingExactCost(book, model, day, map[string]int64{"uncached_input_tokens": int64(math.Ceil(input)), "output_tokens": int64(math.Ceil(output))})
	if cost.status == "unpriced" || cost.cost == nil {
		return nil
	}
	return cost.cost
}

// labelingSpend is a job's money: known spend priced from usage, tokens
// whose price is partial or unknown, the estimated exposure of attempts
// with unknown outcome and of requests in flight, and what the CLIs said
// they cost (non-authoritative).
type labelingSpend struct {
	Known            float64                     `json:"known_usd"`
	PartialTokens    map[string]map[string]int64 `json:"partial_or_unpriced_tokens"`
	UnpricedAttempts int                         `json:"attempts_with_unpriced_usage"`
	UnknownExposure  float64                     `json:"possibly_charged_exposure_usd"`
	UnknownAttempts  int                         `json:"possibly_charged_attempts"`
	InFlight         float64                     `json:"in_flight_estimate_usd"`
	CLIReported      float64                     `json:"cli_reported_usd"`
	// EstimatedUsage is the part of the exposure from usage with known
	// tokens but no price of its own (a held or re-queued served model).
	EstimatedUsage float64 `json:"estimated_usage_usd"`
	// Auxiliary is what models other than the labeling model cost (such
	// as a CLI's housekeeping calls), by model; it is part of Known.
	Auxiliary         float64            `json:"auxiliary_usd"`
	AuxiliaryModels   map[string]float64 `json:"auxiliary_models"`
	CLIReportedNote   string             `json:"cli_reported_note"`
	EstimateDay       string             `json:"estimate_day"`
	EstimateUnpriced  bool               `json:"estimate_unpriced,omitempty"`
	CommittedEstimate float64            `json:"committed_usd"`
}

// labelingEstimatedStatus reports a usage cost status whose cost_usd is an
// estimate, not a price: estimated (pricing-v3) and estimated_upper_bound
// (pricing-v4). Such a cost is exposure, never known spend.
func labelingEstimatedStatus(status string) bool {
	return status == "estimated" || status == costUpperBound
}

// labelingCostStatusRank orders cost statuses from best to worst; an
// unknown (newer) status ranks worst, so it is never hidden.
func labelingCostStatusRank(status string) int {
	if rank, ok := map[string]int{"": 0, "priced": 1, "assumed": 2, "estimated": 3, costUpperBound: 4, "partial": 5, "unpriced": 6}[status]; ok {
		return rank
	}
	return 7
}

// labelingAttemptMoney is what an attempt's usage rows say about money:
// known spend, exposure (estimates), the worst cost status and how the
// estimates were made.
type labelingAttemptMoney struct {
	known, estimate *float64
	status          string
	basis           []string
}

// labelingUsageMoney reads the usage of a job's attempts, by attempt, as
// status and export both count it: cost_usd is known spend unless its
// status is an estimate (labelingEstimatedStatus), and estimate_usd (since
// pricing-v5, beside an unpriced row) is always exposure.
func labelingUsageMoney(ctx context.Context, q queryer, job string) (map[string]*labelingAttemptMoney, error) {
	// Ledgers from before pricing-v5 have no estimate columns.
	estimateColumns := "NULL estimate_usd,NULL estimate_basis"
	if labelingHasColumn(q, "usage", "estimate_usd") && labelingHasColumn(q, "usage", "estimate_basis") {
		estimateColumns = "u.estimate_usd,u.estimate_basis"
	}
	rows, err := queryMapsContext(ctx, q, `SELECT u.attempt_id,u.cost_usd,u.cost_status,`+estimateColumns+` FROM usage u JOIN attempts a ON a.id=u.attempt_id
		WHERE a.job=? ORDER BY u.attempt_id,u.model`, job)
	if err != nil {
		return nil, err
	}
	add := func(total **float64, value float64) {
		if *total != nil {
			value += **total
		}
		*total = &value
	}
	money := map[string]*labelingAttemptMoney{}
	for _, row := range rows {
		id := firstString(row["attempt_id"])
		attempt := money[id]
		if attempt == nil {
			attempt = &labelingAttemptMoney{}
			money[id] = attempt
		}
		status := firstString(row["cost_status"])
		if cost, ok := row["cost_usd"].(float64); ok {
			if labelingEstimatedStatus(status) {
				add(&attempt.estimate, cost)
			} else {
				add(&attempt.known, cost)
			}
		}
		if estimate, ok := row["estimate_usd"].(float64); ok {
			add(&attempt.estimate, estimate)
		}
		if basis := firstString(row["estimate_basis"]); basis != "" && !slices.Contains(attempt.basis, basis) {
			attempt.basis = append(attempt.basis, basis)
		}
		if labelingCostStatusRank(status) > labelingCostStatusRank(attempt.status) {
			attempt.status = status
		}
	}
	return money, nil
}

func computeLabelingSpend(db *sql.DB, job labelingJob, book priceBook, day string) (labelingSpend, error) {
	spend := labelingSpend{PartialTokens: map[string]map[string]int64{}, EstimateDay: day,
		CLIReportedNote: "the CLIs' own cost estimates, not authoritative: known spend is priced from usage with the Pharos price book"}
	// One transaction: a request finishing between two reads can then be
	// counted neither as known nor as in flight.
	tx, err := db.Begin()
	if err != nil {
		return spend, err
	}
	defer tx.Rollback()
	var reported sql.NullFloat64
	// Known spend is usage priced as itself; estimates (usage that cannot
	// be priced as itself) are exposure.
	money, err := labelingUsageMoney(context.Background(), tx, job.Name)
	if err != nil {
		return spend, err
	}
	for _, attempt := range money {
		if attempt.known != nil {
			spend.Known += *attempt.known
		}
		if attempt.estimate != nil {
			spend.EstimatedUsage += *attempt.estimate
		}
	}
	spend.AuxiliaryModels = map[string]float64{}
	auxiliary := []map[string]any{}
	if labelingHasColumn(tx, "usage", "role") {
		if auxiliary, err = queryMaps(tx, `SELECT u.model,SUM(COALESCE(u.cost_usd,0)) cost FROM usage u JOIN attempts a ON a.id=u.attempt_id WHERE a.job=? AND u.role='auxiliary' GROUP BY u.model`, job.Name); err != nil {
			return spend, err
		}
	}
	for _, row := range auxiliary {
		cost, _ := number(row["cost"])
		spend.AuxiliaryModels[firstString(row["model"])] = cost
		spend.Auxiliary += cost
	}
	rows, err := queryMaps(tx, `SELECT u.attempt_id,u.model,u.tokens,u.cost_status FROM usage u JOIN attempts a ON a.id=u.attempt_id
		WHERE a.job=? AND u.cost_status IN ('partial','unpriced','estimated_upper_bound')`, job.Name)
	if err != nil {
		return spend, err
	}
	unpriced := map[string]bool{}
	for _, row := range rows {
		unpriced[firstString(row["attempt_id"])] = true
		var tokens map[string]int64
		json.Unmarshal([]byte(firstString(row["tokens"])), &tokens)
		model := firstString(row["model"]) + " (" + firstString(row["cost_status"]) + ")"
		if spend.PartialTokens[model] == nil {
			spend.PartialTokens[model] = map[string]int64{}
		}
		for key, value := range tokens {
			spend.PartialTokens[model][key] += value
		}
	}
	spend.UnpricedAttempts = len(unpriced)
	// Estimates are linear in tokens, so sums price as a whole.
	estimate := func(input, output float64) float64 {
		if input == 0 && output == 0 {
			return 0
		}
		cost := labelingRequestEstimate(book, job.Model, day, input, output)
		if cost == nil {
			spend.EstimateUnpriced = true
			return 0
		}
		return *cost
	}
	var input, output sql.NullFloat64
	// Attempts that may have been charged without reporting usage: unknown
	// outcomes, failures after the CLI started, and results whose usage
	// could not be read. They stay in the job's lifetime spend even when
	// re-queued.
	if err := tx.QueryRow(`SELECT COUNT(*),SUM(r.est_input_tokens),SUM(r.est_output_tokens) FROM attempts a JOIN requests r ON r.id=a.request_id
		WHERE a.job=? AND a.usage_missing=1`, job.Name).
		Scan(&spend.UnknownAttempts, &input, &output); err != nil {
		return spend, err
	}
	spend.UnknownExposure = estimate(input.Float64, output.Float64) + spend.EstimatedUsage
	if err := tx.QueryRow(`SELECT SUM(est_input_tokens),SUM(est_output_tokens) FROM requests WHERE job=? AND state IN ('leased','submitted')`, job.Name).
		Scan(&input, &output); err != nil {
		return spend, err
	}
	spend.InFlight = estimate(input.Float64, output.Float64)
	tx.QueryRow(`SELECT SUM(cli_cost_usd) FROM attempts WHERE job=?`, job.Name).Scan(&reported)
	spend.CLIReported = reported.Float64
	spend.CommittedEstimate = spend.Known + spend.UnknownExposure + spend.InFlight
	return spend, nil
}

// labelingJobStatus summarizes a job: request states, item outcomes, spend,
// attempts, throughput and an ETA.
func labelingJobStatus(db *sql.DB, name string, now time.Time) (map[string]any, error) {
	job, err := loadLabelingJob(db, name)
	if err != nil {
		return nil, err
	}
	states := map[string]int{}
	for _, state := range labelingRequestStates {
		states[state] = 0
	}
	rows, err := queryMaps(db, `SELECT state,COUNT(*) n FROM requests WHERE job=? GROUP BY state`, name)
	if err != nil {
		return nil, err
	}
	total := 0
	for _, row := range rows {
		states[firstString(row["state"])] = int(integer(row["n"]))
		total += int(integer(row["n"]))
	}
	// An item's outcome is its latest result.
	outcomes := map[string]int{}
	rows, err = queryMaps(db, `SELECT outcome,COUNT(*) n FROM (SELECT r.item_id, r.outcome, ROW_NUMBER() OVER (PARTITION BY r.item_id ORDER BY r.created_at DESC, r.rowid DESC) k
		FROM item_results r WHERE r.job=?) WHERE k=1 GROUP BY outcome`, name)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		outcomes[firstString(row["outcome"])] = int(integer(row["n"]))
	}
	var selectionText, settings, halted, runStarted sql.NullString
	db.QueryRow(`SELECT selection,run_settings,halted,run_started_at FROM jobs WHERE name=?`, name).Scan(&selectionText, &settings, &halted, &runStarted)
	var selection map[string]any
	json.Unmarshal([]byte(selectionText.String), &selection)
	attempts := map[string]int{}
	rows, err = queryMaps(db, `SELECT COALESCE(outcome,state) outcome,COUNT(*) n FROM attempts WHERE job=? GROUP BY 1`, name)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		attempts[firstString(row["outcome"])] = int(integer(row["n"]))
	}
	// A whole-request refusal marks every item; those packed with others
	// may be innocent, but are not re-sent on this route.
	var coPacked int
	db.QueryRow(`SELECT COUNT(*) FROM item_results r JOIN requests q ON q.id=r.request_id WHERE r.job=? AND r.outcome='refused' AND q.items>1`, name).Scan(&coPacked)
	// Distinct CLI warnings (stderr tags such as an unrecognized model),
	// each shown once.
	cliWarnings := []string{}
	warned := map[string]bool{}
	warningRows := []map[string]any{}
	if labelingHasColumn(db, "attempts", "cli_warnings") {
		if warningRows, err = queryMaps(db, `SELECT cli_warnings FROM attempts WHERE job=? AND cli_warnings IS NOT NULL AND cli_warnings<>'[]' ORDER BY submitted_at`, name); err != nil {
			return nil, err
		}
	}
	for _, row := range warningRows {
		var list []string
		json.Unmarshal([]byte(firstString(row["cli_warnings"])), &list)
		for _, warning := range list {
			if !warned[warning] {
				warned[warning] = true
				cliWarnings = append(cliWarnings, warning)
			}
		}
	}
	var cleaned, recovered int
	db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE job=? AND cleanup IS NOT NULL AND cleanup<>'[]'`, name).Scan(&cleaned)
	db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE job=? AND recovered=1`, name).Scan(&recovered)
	book, err := embeddedPriceBook(context.Background(), now)
	if err != nil {
		return nil, err
	}
	spend, err := computeLabelingSpend(db, job, book, now.UTC().Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	status := map[string]any{"job": name, "route": job.Route, "model": job.Model, "effort": job.Effort, "prompt": job.Prompt, "cli_version": job.CLIVersion,
		"requests": total, "request_states": states, "item_outcomes": outcomes, "selection": selection, "attempts": attempts,
		"attempts_cleaned_up": cleaned, "attempts_recovered_from_stream": recovered, "spend": spend,
		"refused_items_co_packed": coPacked, "cli_warnings": cliWarnings, "coverage": labelingCoverage(db, name, coPacked)}
	if halted.Valid {
		status["halted"] = halted.String
	}
	if settings.Valid {
		var value any
		json.Unmarshal([]byte(settings.String), &value)
		status["last_run_settings"] = value
	}
	// Throughput over the last run: finished attempts per hour since it began.
	if runStarted.Valid {
		var finished int
		db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE job=? AND finished_at>=?`, name, runStarted.String).Scan(&finished)
		if started, err := time.Parse(timeLayout, runStarted.String); err == nil {
			elapsed := now.Sub(started).Hours()
			if elapsed > 0 && finished > 0 {
				rate := float64(finished) / elapsed
				status["requests_per_hour"] = rate
				status["eta_hours"] = float64(states[requestPending]+states[requestLeased]+states[requestSubmitted]) / rate
			}
		}
	}
	return status, nil
}

// labelingCoverage counts a job's selected items that will never be
// labeled by it: refused items packed with others (permanently blocked
// although the refusal may belong to a neighbour; no re-send path),
// items a refusal elsewhere blocked, and items too large for a request.
func labelingCoverage(db *sql.DB, job string, coPacked int) map[string]any {
	count := func(outcome string) int {
		var n int
		db.QueryRow(`SELECT COUNT(DISTINCT item_id) FROM item_results WHERE job=? AND outcome=?`, job, outcome).Scan(&n)
		return n
	}
	oversize := count(itemBlockedOversize)
	var pinned, echoes, waiting int
	db.QueryRow(`SELECT COUNT(DISTINCT r.item_id) FROM item_results r JOIN requests q ON q.id=r.request_id WHERE r.job=? AND r.outcome='refused' AND q.items=1`, job).Scan(&pinned)
	db.QueryRow(`SELECT COUNT(*) FROM item_results WHERE job=? AND evidence LIKE '%"echoes_context":true%'`, job).Scan(&echoes)
	db.QueryRow(`SELECT COUNT(*) FROM requests WHERE job=? AND state=?`, job, requestWaitingSuspicion).Scan(&waiting)
	return map[string]any{"refused_pinned": pinned, "refused_co_packed_blocked": coPacked, "refused_elsewhere": count(itemRefusedElsewhere),
		"requests_waiting_on_suspicion": waiting, "blocked_oversize": oversize, "incomplete": oversize > 0, "quote_echoes_context": echoes}
}

// labelingStatusLine is the one-line progress summary of a job.
func labelingStatusLine(status map[string]any) string {
	states := status["request_states"].(map[string]int)
	parts := []string{}
	for _, state := range labelingRequestStates {
		if states[state] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", state, states[state]))
		}
	}
	spend := status["spend"].(labelingSpend)
	line := fmt.Sprintf("%s: %d requests (%s); known $%.4f, in flight ~$%.4f, possibly charged ~$%.4f (%d unknown or failed attempts without usage), CLI-reported $%.4f",
		status["job"], status["requests"], strings.Join(parts, ", "), spend.Known, spend.InFlight, spend.UnknownExposure, spend.UnknownAttempts, spend.CLIReported)
	if len(spend.AuxiliaryModels) > 0 {
		names := []string{}
		for model := range spend.AuxiliaryModels {
			names = append(names, model)
		}
		sort.Strings(names)
		line += fmt.Sprintf("; auxiliary models: %s $%.4f (in known)", strings.Join(names, ", "), spend.Auxiliary)
	}
	if spend.UnpricedAttempts > 0 {
		line += fmt.Sprintf(", %d attempts with unpriced usage", spend.UnpricedAttempts)
	}
	if coverage, ok := status["coverage"].(map[string]any); ok {
		if n := coverage["refused_co_packed_blocked"].(int); n > 0 {
			line += fmt.Sprintf("; coverage: %d co-packed items permanently blocked by a request's refusal", n)
		}
		if n := coverage["refused_elsewhere"].(int); n > 0 {
			line += fmt.Sprintf("; %d items blocked by refusals elsewhere", n)
		}
		if n := coverage["blocked_oversize"].(int); n > 0 {
			line += fmt.Sprintf("; %d items too large for a request (job incomplete)", n)
		}
	}
	if warnings, ok := status["cli_warnings"].([]string); ok && len(warnings) > 0 {
		line += "; CLI warnings: " + strings.Join(warnings, " | ")
	}
	if rate, ok := status["requests_per_hour"].(float64); ok {
		line += fmt.Sprintf("; %.1f requests/h, ETA %.1f h", rate, status["eta_hours"])
	}
	return line
}

// labelingJobsSummary lists a dataset's jobs for plain status.
func labelingJobsSummary(dir string, w io.Writer) error {
	db, err := openLabelingDataset(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	if !labelingHasLedger(db) {
		return nil
	}
	rows, err := queryMaps(db, `SELECT name FROM jobs ORDER BY created_at, name`)
	if err != nil {
		return err
	}
	for _, row := range rows {
		status, err := labelingJobStatus(db, firstString(row["name"]), time.Now())
		if err != nil {
			return err
		}
		fmt.Fprintln(w, labelingStatusLine(status))
	}
	return nil
}

// reconcileLabelingJob lists requests whose outcome needs a decision and,
// when asked, re-queues unknown or failed ones. Nothing is re-queued
// implicitly: an unknown request may have been charged.
// labelingReconcileOptions are the explicit re-queue decisions.
type labelingReconcileOptions struct {
	RetryUnknown, RetryFailed, RequeueHeld, RetrySuspectedRefusals, RetryToolUse bool
	// Dir is the dataset directory, for a job without a recorded registry.
	Dir string
}

func reconcileLabelingJob(db *sql.DB, name string, options labelingReconcileOptions, w io.Writer) error {
	if _, err := loadLabelingJob(db, name); err != nil {
		return err
	}
	rows, err := queryMaps(db, `SELECT r.id,r.state,r.attempts,a.id attempt,a.error,a.exit_code,a.dir,a.problems,a.usage_missing FROM requests r
		LEFT JOIN attempts a ON a.request_id=r.id AND a.number=r.attempts WHERE r.job=? AND r.state IN ('unknown','failed','refused','invalid','held','refusal_suspected') ORDER BY r.position`, name)
	if err != nil {
		return err
	}
	// Attempts whose cost is only estimated, whatever their outcome: no
	// usage was read (an answer saved without a terminal event, or a
	// terminal event without usage).
	estimated, err := queryMaps(db, `SELECT a.id,a.outcome,a.problems,a.dir FROM attempts a JOIN requests r ON r.id=a.request_id
		WHERE a.job=? AND a.usage_missing=1 AND a.outcome NOT IN ('unknown','failed') ORDER BY r.position,a.number`, name)
	if err != nil {
		return err
	}
	for _, row := range estimated {
		fmt.Fprintf(w, "%s %s without usage (cost estimated; problems %s; receipt %s)\n", row["id"], row["outcome"], firstString(row["problems"]), firstString(row["dir"]))
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "No unknown, failed, refused, invalid or held requests.")
		return nil
	}
	explain := map[string]string{"no_stream_events": "the CLI started but printed no event; it may still have reached the provider",
		"answer_without_terminal": "an answer was saved without the turn's end; it was validated, its usage is unknown",
		"tool_use":                "the model used a tool; not re-sent unless --retry-tool-use",
		"refusal_suspected":       "refusal words in a reply that was not a clear refusal; re-sent only with --retry-suspected-refusals"}
	requeue := []string{}
	for _, row := range rows {
		state := firstString(row["state"])
		fmt.Fprintf(w, "%s %s after %d attempts (last %s, exit %v, error %q, receipt %s)\n", row["id"], state, integer(row["attempts"]),
			row["attempt"], row["exit_code"], firstString(row["error"]), firstString(row["dir"]))
		for key, text := range explain {
			if strings.Contains(firstString(row["problems"]), key) {
				fmt.Fprintf(w, "  %s: %s\n", key, text)
			}
		}
		toolUse := state == requestInvalid && strings.Contains(firstString(row["problems"]), "tool_use")
		if state == requestUnknown && options.RetryUnknown || state == requestFailed && options.RetryFailed || state == requestHeld && options.RequeueHeld ||
			state == requestRefusalSuspected && options.RetrySuspectedRefusals || toolUse && options.RetryToolUse {
			requeue = append(requeue, firstString(row["id"]))
		}
	}
	if len(requeue) == 0 {
		if options == (labelingReconcileOptions{Dir: options.Dir}) {
			fmt.Fprintln(w, "Unknown and failed requests may have been charged; re-queue them explicitly with --retry-unknown or --retry-failed, held ones with --requeue-held (or accept the model with run --accept-served-model), suspected refusals with --retry-suspected-refusals, tool-use attempts with --retry-tool-use. Refused requests are never re-sent by this tool, on any route (co-packed items included: re-sending them is an operator decision outside its defaults); invalid items are retried by the run's --retry-invalid.")
		}
		return nil
	}
	// Re-queueing never re-sends refused material: items a refusal elsewhere
	// now blocks leave the request first.
	var registry sql.NullString
	if err := db.QueryRow(`SELECT refusal_registry FROM jobs WHERE name=?`, name).Scan(&registry); err != nil {
		return err
	}
	check, err := loadLabelingRefusalCheck(defaultString(registry.String, labelingDefaultRegistry(options.Dir)), nil)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	blockedItems := 0
	for _, id := range requeue {
		if _, err := tx.Exec(`UPDATE requests SET state='pending',updated_at=?,note=? WHERE id=?`, now(), "re-queued by reconcile", id); err != nil {
			return err
		}
		blocked, _, _, err := applyLabelingRefusals(tx, check, name, id)
		if err != nil {
			return err
		}
		// A request held for a suspicion goes back to pending: the next run
		// checks it again before sending.
		if _, err := tx.Exec(`UPDATE requests SET state='pending' WHERE id=? AND state=?`, id, requestWaitingSuspicion); err != nil {
			return err
		}
		blockedItems += blocked
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Fprintf(w, "Re-queued %d requests; their earlier attempts stay in the ledger and in spend.\n", len(requeue))
	if blockedItems > 0 {
		fmt.Fprintf(w, "%d of their items are blocked by a refusal elsewhere and will not be sent.\n", blockedItems)
	}
	return nil
}

// importLabelingResults creates job name in dir from the job of the same
// name in an earlier dataset: same route, model, effort, prompt and
// selection, with the usable results of items whose model-visible contract
// hash is unchanged copied (and their evidence re-mapped to this dataset's
// bytes). Everything else is packed as pending.
func importLabelingResults(ctx context.Context, dir, from, name, registry string, w io.Writer) error {
	old, err := openLabelingDataset(from)
	if err != nil {
		return err
	}
	defer old.Close()
	if !labelingHasLedger(old) {
		return fmt.Errorf("%s has no labeling jobs", from)
	}
	var route, model, effort, prompt, selectionText, packingText, schemaHash, taxonomyHash string
	var templateHash sql.NullString
	if err := old.QueryRowContext(ctx, `SELECT route,model,effort,prompt_version,selection,packing,schema_sha256,taxonomy_sha256,prompt_template_sha256 FROM jobs WHERE name=?`, name).
		Scan(&route, &model, &effort, &prompt, &selectionText, &packingText, &schemaHash, &taxonomyHash, &templateHash); err != nil {
		return fmt.Errorf("job %q in %s: %w", name, from, err)
	}
	// Results are reusable only under the contract this build produces.
	for _, check := range []struct{ name, stored, current string }{{"schema", schemaHash, labelingSchemaHash()},
		{"taxonomy", taxonomyHash, hashBytes(labelingTaxonomy)}, {"prompt " + prompt, templateHash.String, labelingPromptTemplateHash(prompt)}} {
		if check.stored != check.current {
			return fmt.Errorf("job %s in %s was labeled with a different %s than this build produces; its results cannot be imported", name, from, check.name)
		}
	}
	var selection, packing map[string]any
	json.Unmarshal([]byte(selectionText), &selection)
	json.Unmarshal([]byte(packingText), &packing)
	strs := func(value any) []string {
		out := []string{}
		if list, ok := value.([]any); ok {
			for _, v := range list {
				out = append(out, firstString(v))
			}
		}
		return out
	}
	spec := labelingJobSpec{Route: route, Model: model, Effort: effort, Prompt: prompt, Cohorts: strs(selection["cohorts"]), Splits: strs(selection["splits"]),
		Items: strs(selection["items"]), LimitItems: int(integer(selection["limit_items"])), IncludeDuplicates: selection["include_duplicates"] == true,
		ItemsPerRequest: int(integer(packing["items_per_request"])), MaxRequestChars: int(integer(packing["max_request_chars"])),
		MaxPerText: int(integer(selection["max_per_text"]))}
	spec.MaxRequestTokens, _ = number(packing["max_request_tokens"])
	// The latest usable or refused result per item in the old job: a
	// refusal is carried, so the same material is not sent again.
	previous, err := queryMaps(old, `SELECT * FROM (SELECT r.*, ROW_NUMBER() OVER (PARTITION BY r.item_id ORDER BY r.created_at DESC, r.rowid DESC) k
		FROM item_results r WHERE r.job=?) WHERE k=1 AND outcome IN ('usable','usable_with_evidence_issues','refused')`, name)
	if err != nil {
		return err
	}
	db, err := openLabelingLedger(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	items, _, _, err := selectLabelingItems(ctx, db, spec)
	if err != nil {
		return err
	}
	current := map[string]labelingSelectedItem{}
	for _, item := range items {
		current[item.row.ID] = item
	}
	sources, err := loadLabelingItemSources(ctx, db, itemIDs(items))
	if err != nil {
		return err
	}
	labels := labelingLabels()
	copies := []labelingImported{}
	stale := 0
	for _, row := range previous {
		id := firstString(row["item_id"])
		item, ok := current[id]
		if !ok {
			continue
		}
		hash := labelingContractHash(prompt, item.row)
		if hash != firstString(row["contract_hash"]) {
			stale++
			continue
		}
		if firstString(row["outcome"]) == itemRefused {
			var issues []string
			json.Unmarshal([]byte(firstString(row["issues"])), &issues)
			refused := labelingItemResult{ItemID: id, Outcome: itemRefused, Labels: []string{}, Uncertain: []string{}, Evidence: []labelingEvidence{}, Issues: issues}
			copies = append(copies, labelingImported{result: refused, hash: hash, from: from + "#" + firstString(row["attempt_id"]), served: firstString(row["served_model"])})
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(firstString(row["annotation"])), &fields) != nil {
			stale++
			continue
		}
		result := validateLabelingItem(sources[id], fields, json.RawMessage(firstString(row["annotation"])), labels)
		if result.Outcome != itemUsable && result.Outcome != itemEvidenceIssues {
			stale++
			continue
		}
		copies = append(copies, labelingImported{result: result, hash: hash, from: from + "#" + firstString(row["attempt_id"]), served: firstString(row["served_model"])})
	}
	// Every item the earlier dataset refused, in any job, stays refused; the
	// registry carries pinned refusals' content.
	importRefused, _, err := labelingLedgerRefusals(old)
	if err != nil {
		return err
	}
	if registry == "" {
		registry = labelingDefaultRegistry(dir)
	}
	summary, err := createLabelingJob(ctx, db, name, spec, copies, from, registry, importRefused)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Imported %d usable results from %s (%d stale: contract changed or no longer valid); %d items left to label.\n",
		len(copies), from, stale, len(items)-len(copies))
	return writeLabelingJSON(w, summary)
}

func itemIDs(items []labelingSelectedItem) []string {
	ids := make([]string, len(items))
	for index, item := range items {
		ids[index] = item.row.ID
	}
	return ids
}

func insertLabelingItemResult(tx *sql.Tx, attempt, job, request string, result labelingItemResult, hash, importedFrom, served string) error {
	var need any
	if result.NeedContext != nil {
		need = *result.NeedContext
	}
	var annotation any
	if len(result.Annotation) > 0 {
		annotation = string(result.Annotation)
	}
	labelsEvidence := result.LabelsEvidence
	if labelsEvidence == nil {
		labelsEvidence = map[string]string{}
	}
	_, err := tx.Exec(`INSERT INTO item_results(attempt_id,item_id,job,request_id,outcome,labels,uncertain_labels,evidence,need_context,issues,annotation,
		contract_hash,imported_from,served_model,labels_evidence,held_outcome,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, attempt, result.ItemID, job,
		nilIfEmpty(request), result.Outcome, jsonText(result.Labels), jsonText(result.Uncertain), jsonText(result.Evidence), need, jsonText(result.Issues), annotation,
		hash, nilIfEmpty(importedFrom), nilIfEmpty(served), jsonText(labelsEvidence), nilIfEmpty(result.HeldOutcome), now())
	return err
}

// loadLabelingItemSources reads what prompts and validation need for items.
func loadLabelingItemSources(ctx context.Context, db *sql.DB, ids []string) (map[string]labelingItemSource, error) {
	sources := map[string]labelingItemSource{}
	units := map[string][]string{}
	for _, id := range ids {
		var source labelingItemSource
		var markers string
		source.id = id
		if err := db.QueryRowContext(ctx, `SELECT i.unit_id,i.text,i.visible_start,u.message_text,u.context_prompt,u.marker_ranges FROM items i
			JOIN units u ON u.id=i.unit_id WHERE i.id=?`, id).Scan(&source.unitID, &source.text, &source.visibleStart, &source.message, &source.context, &markers); err != nil {
			return nil, fmt.Errorf("item %s: %w", id, err)
		}
		json.Unmarshal([]byte(markers), &source.markers)
		sources[id] = source
		units[source.unitID] = append(units[source.unitID], id)
	}
	for unit, members := range units {
		rows, err := db.QueryContext(ctx, `SELECT start_byte,end_byte,visible_start,visible_end,COALESCE(transform,'') FROM pieces
			WHERE unit_id=? AND role='target' AND visible_start IS NOT NULL ORDER BY visible_start`, unit)
		if err != nil {
			return nil, err
		}
		targets := []labelingTargetPiece{}
		for rows.Next() {
			var piece labelingTargetPiece
			if err := rows.Scan(&piece.start, &piece.end, &piece.visibleStart, &piece.visibleEnd, &piece.transform); err != nil {
				rows.Close()
				return nil, err
			}
			targets = append(targets, piece)
		}
		rows.Close()
		sort.Slice(targets, func(i, j int) bool { return targets[i].visibleStart < targets[j].visibleStart })
		for _, id := range members {
			source := sources[id]
			source.targets = targets
			sources[id] = source
		}
	}
	return sources, nil
}

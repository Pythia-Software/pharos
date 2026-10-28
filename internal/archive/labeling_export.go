package archive

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Training export (labeling-export-v1): one job's results as files a
// separate student-model project reads without this code. Student inputs,
// labels, provenance and the manifest are separate files; held-out labels
// (test, fresh, quarantine) are in their own files, so a trainer that loads
// train and dev never reads them. See docs/library-labeling.md.
const (
	labelingExportVersion = "labeling-export-v1"
	// labelingMergeVersion merges a chunked unit's chunk labels by union.
	// It has not been validated against labels of the whole text.
	labelingMergeVersion = "chunk-union-v1"
	labelingExportsDir   = "exports"
)

// Export statuses. Only usable rows carry positives; every other status
// keeps the teacher's reply apart (returned) and is never a negative.
const (
	exportUsable            = "usable"
	exportNeedContext       = "need_context"
	exportToolUse           = "tool_use"
	exportUnlabeled         = "unlabeled"
	exportPartial           = "partial"
	labelingSplitUnassigned = "unassigned"
)

// The export's files. Labels of held-out splits are in the -heldout files.
const (
	exportInputs        = "inputs.jsonl"
	exportProvenance    = "provenance.jsonl"
	exportLabels        = "labels.jsonl"
	exportLabelsHeldOut = "labels-heldout.jsonl"
	exportUnits         = "unit-labels.jsonl"
	exportUnitsHeldOut  = "unit-labels-heldout.jsonl"
	exportManifest      = "export-manifest.json"
)

var labelingExportFiles = []string{exportInputs, exportProvenance, exportLabels, exportLabelsHeldOut, exportUnits, exportUnitsHeldOut}

// labelingTrainingSplit reports whether a split's labels go to the
// training files; everything else (test, fresh, quarantine, a unit without
// a split) is held out.
func labelingTrainingSplit(split string) bool { return split == "train" || split == "dev" }

// labelingExportInput is what a student may see at inference time: the
// model-visible text and context the teacher saw, and the item's kind. No
// labels, teacher, evidence, later messages or label-derived decisions.
type labelingExportInput struct {
	ItemID          string  `json:"item_id"`
	DatasetID       string  `json:"dataset_id"`
	UnitsVersion    string  `json:"units_version"`
	UnitID          string  `json:"unit_id"`
	Split           string  `json:"split"`
	Cohort          string  `json:"cohort"`
	ActorKind       string  `json:"actor_kind"`
	Sender          string  `json:"sender"`
	Provider        string  `json:"provider"`
	Kind            string  `json:"kind"`
	Channel         string  `json:"channel"`
	ReasoningFormat *string `json:"reasoning_format"`
	Depth           *int64  `json:"depth"`
	Text            string  `json:"text"`
	Context         string  `json:"context"`
	ContextExcerpt  bool    `json:"context_excerpt"`
	ChunkIndex      int     `json:"chunk_index"`
	ChunkCount      int     `json:"chunk_count"`
	Chars           int     `json:"chars"`
}

// labelingExportLabel is one item's authoritative teacher result.
type labelingExportLabel struct {
	ItemID string `json:"item_id"`
	UnitID string `json:"unit_id"`
	Split  string `json:"split"`
	// Status is usable, need_context, refused, refusal_suspected, invalid,
	// tool_use, missing, held or unlabeled. Only usable rows train.
	Status  string `json:"status"`
	Outcome string `json:"outcome"`
	// Labels are the positives: accepted labels (leaves, or broad parents
	// as returned) with supported evidence. Null unless usable.
	Labels []string `json:"labels"`
	// UnsupportedLabels are accepted labels without valid evidence (label:
	// evidence status): neither positive nor negative.
	UnsupportedLabels map[string]string `json:"unsupported_labels"`
	UncertainLabels   []string          `json:"uncertain_labels"`
	// ImpliedAncestors are the ancestors of the positives, apart: a child
	// implies its parent, a parent never implies a child.
	ImpliedAncestors []string `json:"implied_ancestors"`
	NeedContext      *bool    `json:"need_context"`
	// Returned is what the teacher returned for a row that is not usable.
	Returned *labelingExportReturned `json:"returned"`
	// Evidence is as the ledger stored it (quotes, statuses, occurrences).
	Evidence json.RawMessage       `json:"evidence"`
	Issues   []string              `json:"issues"`
	Flags    []string              `json:"flags"`
	Teacher  labelingExportTeacher `json:"teacher"`
	// Receipt identifies the result: its request and attempt, the cost
	// status of that attempt, and every result the job holds for the item.
	Receipt labelingExportReceipt `json:"receipt"`
}

type labelingExportReturned struct {
	Labels          []string `json:"labels"`
	UncertainLabels []string `json:"uncertain_labels"`
}

type labelingExportTeacher struct {
	Job             string `json:"job"`
	Route           string `json:"route"`
	Model           string `json:"model"`
	Effort          string `json:"effort"`
	ServedModel     string `json:"served_model"`
	CLIVersion      string `json:"cli_version"`
	PromptVersion   string `json:"prompt_version"`
	SchemaVersion   string `json:"schema_version"`
	TaxonomyVersion string `json:"taxonomy_version"`
}

type labelingExportReceipt struct {
	RequestID     string                  `json:"request_id"`
	AttemptID     string                  `json:"attempt_id"`
	ImportedFrom  string                  `json:"imported_from"`
	RequestState  string                  `json:"request_state"`
	CostStatus    string                  `json:"cost_status"`
	AttemptCost   *float64                `json:"attempt_cost_usd"`
	RequestItems  int                     `json:"request_items"`
	ContractHash  string                  `json:"contract_hash"`
	History       []labelingExportAttempt `json:"history"`
	RequestsTried []string                `json:"requests"`
}

type labelingExportAttempt struct {
	AttemptID    string `json:"attempt_id"`
	RequestID    string `json:"request_id"`
	Outcome      string `json:"outcome"`
	ImportedFrom string `json:"imported_from"`
	CreatedAt    string `json:"created_at"`
}

// labelingExportProvenance is where an item came from: source message,
// original byte ranges, pieces, actions, lineage, family and the dataset's
// duplicate and leakage marks. It holds no labels.
type labelingExportProvenance struct {
	ItemID              string           `json:"item_id"`
	UnitID              string           `json:"unit_id"`
	Split               string           `json:"split"`
	FamilyID            string           `json:"family_id"`
	MessageID           string           `json:"message_id"`
	ConversationID      string           `json:"conversation_id"`
	CanonicalID         string           `json:"canonical_conversation_id"`
	WorkspaceID         string           `json:"workspace_id"`
	RootConversationID  string           `json:"root_conversation_id"`
	RunID               string           `json:"run_id"`
	SessionID           string           `json:"session_id"`
	SessionRole         string           `json:"session_role"`
	DepthStatus         string           `json:"depth_status"`
	CreatedAt           string           `json:"created_at"`
	ContentHash         string           `json:"content_hash"`
	MessageSHA256       string           `json:"message_sha256"`
	AuthorshipReason    string           `json:"authorship_reason"`
	LengthBucket        string           `json:"length_bucket"`
	TargetHash          string           `json:"target_hash"`
	DuplicateOf         string           `json:"duplicate_of"`
	DuplicateRelation   string           `json:"duplicate_relation"`
	CrossSplitDuplicate bool             `json:"cross_split_duplicate"`
	SeenInTrain         bool             `json:"seen_in_train"`
	CommonShortText     bool             `json:"common_short_text"`
	VisibleRange        [2]int           `json:"visible_range"`
	SourceRanges        []map[string]any `json:"source_ranges"`
	Markers             [][2]int         `json:"markers"`
	Pieces              []map[string]any `json:"pieces"`
	Sentences           []map[string]any `json:"sentences"`
	Actions             []map[string]any `json:"actions"`
	ContextRefs         json.RawMessage  `json:"context_refs"`
}

// labelingExportUnit is a chunked unit's merged view.
type labelingExportUnit struct {
	UnitID            string                    `json:"unit_id"`
	Split             string                    `json:"split"`
	ChunkingVersion   string                    `json:"chunking_version"`
	MergeVersion      string                    `json:"merge_version"`
	UnvalidatedMerge  bool                      `json:"unvalidated_merge"`
	Status            string                    `json:"status"`
	ChunkCount        int                       `json:"chunk_count"`
	Labels            []string                  `json:"labels"`
	UnsupportedLabels []string                  `json:"unsupported_labels"`
	UncertainLabels   []string                  `json:"uncertain_labels"`
	ImpliedAncestors  []string                  `json:"implied_ancestors"`
	NeedContext       *bool                     `json:"need_context"`
	Chunks            []labelingExportUnitChunk `json:"chunks"`
}

type labelingExportUnitChunk struct {
	ItemID string   `json:"item_id"`
	Index  int      `json:"index"`
	Status string   `json:"status"`
	Labels []string `json:"labels"`
}

// labelingExportResult is one item_results row.
type labelingExportResult struct {
	attempt, request, outcome, served, importedFrom, created, contractHash string
	labels, uncertain, issues                                              []string
	evidence                                                               json.RawMessage
	need                                                                   *bool
	labelsEvidence                                                         map[string]string
}

type labelingExportAttemptInfo struct {
	problems     []string
	usageMissing bool
	cliVersion   string
	costStatus   string
	cost         *float64
}

// jsonlWriter writes JSON lines and hashes them as written.
type jsonlWriter struct {
	file   *os.File
	buffer *bufio.Writer
	hash   hash.Hash
	rows   int
	bytes  int64
}

func newJSONLWriter(path string) (*jsonlWriter, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &jsonlWriter{file: file, buffer: bufio.NewWriterSize(file, 1<<20), hash: sha256.New()}, nil
}

func (w *jsonlWriter) write(value any) error {
	data, err := labelingExportJSON(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	w.hash.Write(data)
	w.rows++
	w.bytes += int64(len(data))
	_, err = w.buffer.Write(data)
	return err
}

func (w *jsonlWriter) close() error {
	if err := w.buffer.Flush(); err != nil {
		w.file.Close()
		return err
	}
	if err := w.file.Sync(); err != nil {
		w.file.Close()
		return err
	}
	return w.file.Close()
}

// labelingExportJSON is compact JSON with map keys sorted and no HTML
// escaping, so the same values give the same bytes.
func labelingExportJSON(value any) ([]byte, error) {
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(out.String(), "\n")), nil
}

// labelingTaxonomyParents maps each taxonomy id to its parent ("" for a root).
func labelingTaxonomyParents() (map[string]string, []string, error) {
	rows, err := labelingTaxonomyRows()
	if err != nil {
		return nil, nil, err
	}
	parents, order := map[string]string{}, []string{}
	for _, row := range rows {
		order = append(order, row.ID)
		if _, ok := parents[row.ID]; !ok {
			parents[row.ID] = ""
		}
		for _, child := range row.Children {
			parents[child] = row.ID
		}
	}
	return parents, order, nil
}

// labelingAncestors returns the ancestors of labels that are not
// themselves among them, sorted.
func labelingAncestors(labels []string, parents map[string]string) []string {
	own, implied := map[string]bool{}, map[string]bool{}
	for _, label := range labels {
		own[label] = true
	}
	for _, label := range labels {
		for parent := parents[label]; parent != ""; parent = parents[parent] {
			if !own[parent] {
				implied[parent] = true
			}
		}
	}
	return sortedKeys(implied)
}

func sortedKeys[V any](set map[string]V) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// labelingAuthoritative chooses an item's result: the latest usable one,
// else the latest of any outcome. Results are in creation order.
func labelingAuthoritative(results []labelingExportResult) *labelingExportResult {
	for index := len(results) - 1; index >= 0; index-- {
		if outcome := results[index].outcome; outcome == itemUsable || outcome == itemEvidenceIssues {
			return &results[index]
		}
	}
	if len(results) == 0 {
		return nil
	}
	return &results[len(results)-1]
}

// labelingExportStatus is a result's export status.
func labelingExportStatus(result *labelingExportResult) string {
	switch {
	case result == nil:
		return exportUnlabeled
	case result.outcome == itemUsable || result.outcome == itemEvidenceIssues:
		if result.need != nil && *result.need {
			return exportNeedContext
		}
		return exportUsable
	case result.outcome == itemInvalid && slices.Contains(result.issues, "tool_use"):
		return exportToolUse
	}
	return result.outcome
}

// exportLabeling writes a training export of one job and returns its
// directory and manifest. It reads the dataset in one read-only
// transaction and writes nothing but the export.
func exportLabeling(ctx context.Context, dir, job, out string) (string, map[string]any, error) {
	if !labelingJobName.MatchString(job) {
		return "", nil, fmt.Errorf("job name %q: use letters, digits, '.', '_' and '-'", job)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, err
	}
	shared, err := fileLease(filepath.Join(dir, labelingLockFile), false)
	if err != nil {
		return "", nil, fmt.Errorf("a labeling prepare is using %s", dir)
	}
	defer shared.Close()
	datasetManifest, err := os.ReadFile(filepath.Join(dir, labelingManifestFile))
	if err != nil {
		return "", nil, fmt.Errorf("%s has no complete prepared dataset (no %s)", dir, labelingManifestFile)
	}
	db, err := openLabelingDataset(dir)
	if err != nil {
		return "", nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if !labelingHasLedger(db) {
		return "", nil, fmt.Errorf("no labeling jobs in %s", dir)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	parent := filepath.Join(dir, labelingExportsDir)
	if out != "" {
		if out, err = filepath.Abs(expandPath(out)); err != nil {
			return "", nil, err
		}
		parent = filepath.Dir(out)
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", nil, err
	}
	temporary, err := os.MkdirTemp(parent, ".export-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(temporary)
	manifest, err := writeLabelingExport(ctx, tx, datasetManifest, job, temporary)
	if err != nil {
		return "", nil, err
	}
	if out == "" {
		out = filepath.Join(parent, manifest["export_id"].(string))
	}
	if existing, err := os.ReadFile(filepath.Join(out, exportManifest)); err == nil {
		written, _ := os.ReadFile(filepath.Join(temporary, exportManifest))
		if string(existing) == string(written) {
			return out, manifest, nil
		}
		return "", nil, fmt.Errorf("%s already holds a different export; choose another --out", out)
	}
	if entries, err := os.ReadDir(out); err == nil {
		if len(entries) > 0 {
			return "", nil, fmt.Errorf("%s is not empty; choose another --out", out)
		}
		if err := os.Remove(out); err != nil {
			return "", nil, err
		}
	}
	if err := os.Rename(temporary, out); err != nil {
		return "", nil, err
	}
	if handle, err := os.Open(parent); err == nil {
		handle.Sync()
		handle.Close()
	}
	return out, manifest, nil
}

// writeLabelingExport writes the export's files into dir from one read
// transaction.
func writeLabelingExport(ctx context.Context, tx *sql.Tx, datasetManifestBytes []byte, job, dir string) (map[string]any, error) {
	jobs, err := queryMapsContext(ctx, tx, `SELECT * FROM jobs WHERE name=?`, job)
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("no job %q in this dataset", job)
	}
	jobRow := jobs[0]
	text := func(row map[string]any, key string) string {
		value, _ := row[key].(string)
		return value
	}
	var datasetManifest map[string]any
	if err := json.Unmarshal(datasetManifestBytes, &datasetManifest); err != nil {
		return nil, fmt.Errorf("dataset manifest: %w", err)
	}
	datasetSum := sha256.Sum256(datasetManifestBytes)
	datasetID := hex.EncodeToString(datasetSum[:8])
	parents, taxonomyOrder, err := labelingTaxonomyParents()
	if err != nil {
		return nil, err
	}
	teacher := labelingExportTeacher{Job: job, Route: text(jobRow, "route"), Model: text(jobRow, "model"), Effort: text(jobRow, "effort"),
		ServedModel: text(jobRow, "served_model"), CLIVersion: text(jobRow, "cli_version"), PromptVersion: text(jobRow, "prompt_version"),
		SchemaVersion: text(jobRow, "schema_version"), TaxonomyVersion: text(jobRow, "taxonomy_version")}

	// The job's results, oldest first per item.
	results := map[string][]labelingExportResult{}
	rows, err := tx.QueryContext(ctx, `SELECT attempt_id,item_id,COALESCE(request_id,''),outcome,labels,uncertain_labels,evidence,need_context,issues,
		COALESCE(imported_from,''),COALESCE(served_model,''),labels_evidence,created_at,contract_hash FROM item_results WHERE job=? ORDER BY item_id,created_at,rowid`, job)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var result labelingExportResult
		var item, labels, uncertain, evidence, issues, labelsEvidence string
		var need sql.NullInt64
		if err := rows.Scan(&result.attempt, &item, &result.request, &result.outcome, &labels, &uncertain, &evidence, &need, &issues,
			&result.importedFrom, &result.served, &labelsEvidence, &result.created, &result.contractHash); err != nil {
			rows.Close()
			return nil, err
		}
		for _, field := range []struct {
			data   string
			target any
		}{{labels, &result.labels}, {uncertain, &result.uncertain}, {issues, &result.issues}, {labelsEvidence, &result.labelsEvidence}} {
			if err := json.Unmarshal([]byte(field.data), field.target); err != nil {
				rows.Close()
				return nil, fmt.Errorf("item %s result %s: %w", item, result.attempt, err)
			}
		}
		result.evidence = json.RawMessage(defaultString(evidence, "[]"))
		if need.Valid {
			value := need.Int64 != 0
			result.need = &value
		}
		results[item] = append(results[item], result)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Attempts: problems, usage and the cost status of each.
	attempts := map[string]*labelingExportAttemptInfo{}
	attemptRows, err := queryMapsContext(ctx, tx, `SELECT id,COALESCE(problems,'[]') problems,usage_missing,COALESCE(cli_version,'') cli_version FROM attempts WHERE job=?`, job)
	if err != nil {
		return nil, err
	}
	for _, row := range attemptRows {
		info := &labelingExportAttemptInfo{usageMissing: integer(row["usage_missing"]) != 0, cliVersion: text(row, "cli_version"), problems: []string{}}
		json.Unmarshal([]byte(text(row, "problems")), &info.problems)
		attempts[text(row, "id")] = info
	}
	usageRows, err := queryMapsContext(ctx, tx, `SELECT u.attempt_id,u.cost_usd,u.cost_status FROM usage u JOIN attempts a ON a.id=u.attempt_id WHERE a.job=? ORDER BY u.attempt_id,u.model`, job)
	if err != nil {
		return nil, err
	}
	statusRank := map[string]int{"priced": 1, "assumed": 2, "estimated": 3, "estimated_upper_bound": 4, "partial": 5, "unpriced": 6}
	for _, row := range usageRows {
		info := attempts[text(row, "attempt_id")]
		if info == nil {
			continue
		}
		if cost, ok := row["cost_usd"].(float64); ok {
			sum := cost
			if info.cost != nil {
				sum += *info.cost
			}
			info.cost = &sum
		}
		if status := text(row, "cost_status"); statusRank[status] > statusRank[info.costStatus] || statusRank[status] == 0 && info.costStatus == "" {
			info.costStatus = status
		}
	}

	// Requests holding each item, in order, and their states.
	itemRequests := map[string][]string{}
	requestStates, requestSizes := map[string]string{}, map[string]int{}
	requestRows, err := queryMapsContext(ctx, tx, `SELECT r.id,r.state,r.items,i.item_id FROM request_items i JOIN requests r ON r.id=i.request_id
		WHERE r.job=? ORDER BY r.position,i.position`, job)
	if err != nil {
		return nil, err
	}
	stateCounts := map[string]int{}
	for _, row := range requestRows {
		id := text(row, "id")
		if _, seen := requestStates[id]; !seen {
			stateCounts[text(row, "state")]++
		}
		requestStates[id], requestSizes[id] = text(row, "state"), int(integer(row["items"]))
		itemRequests[text(row, "item_id")] = append(itemRequests[text(row, "item_id")], id)
	}
	wanted := map[string]bool{}
	for item := range itemRequests {
		wanted[item] = true
	}
	for item := range results {
		wanted[item] = true
	}

	// Units of the job's items, and their pieces, sentences and actions.
	unitOf := map[string]string{}
	units := map[string]bool{}
	idRows, err := tx.QueryContext(ctx, `SELECT id,unit_id FROM items`)
	if err != nil {
		return nil, err
	}
	for idRows.Next() {
		var id, unit string
		if err := idRows.Scan(&id, &unit); err != nil {
			idRows.Close()
			return nil, err
		}
		if wanted[id] {
			unitOf[id], units[unit] = unit, true
		}
	}
	idRows.Close()
	for item := range wanted {
		if unitOf[item] == "" {
			return nil, fmt.Errorf("job %s names item %s, which this dataset does not hold", job, item)
		}
	}
	pieces, err := labelingExportGroup(ctx, tx, `SELECT unit_id key,position,role,status,category,reason,start_byte,end_byte,visible_start,visible_end,transform,
		source_conversation_id,source_message_id,source_start,source_end FROM pieces ORDER BY unit_id,position`, units)
	if err != nil {
		return nil, err
	}
	sentences, err := labelingExportGroup(ctx, tx, `SELECT unit_id key,position,piece_position,start_byte,end_byte,transform FROM sentences ORDER BY unit_id,position`, units)
	if err != nil {
		return nil, err
	}
	messages := map[string]bool{}
	unitMessages, err := queryMapsContext(ctx, tx, `SELECT id,message_id FROM units`)
	if err != nil {
		return nil, err
	}
	for _, row := range unitMessages {
		if units[text(row, "id")] {
			messages[text(row, "message_id")] = true
		}
	}
	actions, err := labelingExportGroup(ctx, tx, `SELECT message_id key,position,actor,actor_evidence,kind,status,source,question,option,answer_text,title_start,title_end,
		answer_start,answer_end,question_conversation_id,question_message_id,question_call_id FROM actions ORDER BY message_id,position`, messages)
	if err != nil {
		return nil, err
	}

	writers := map[string]*jsonlWriter{}
	for _, name := range labelingExportFiles {
		if writers[name], err = newJSONLWriter(filepath.Join(dir, name)); err != nil {
			return nil, err
		}
	}
	defer func() {
		for _, writer := range writers {
			writer.file.Close()
		}
	}()

	// Counts for the manifest.
	labelSupport := map[string]map[string]map[string]int{}
	for _, label := range taxonomyOrder {
		labelSupport[label] = map[string]map[string]int{}
	}
	support := func(label, split, kind string) {
		if labelSupport[label] == nil {
			labelSupport[label] = map[string]map[string]int{}
		}
		if labelSupport[label][split] == nil {
			labelSupport[label][split] = map[string]int{"positive": 0, "unsupported": 0, "uncertain": 0, "implied": 0}
		}
		labelSupport[label][split][kind]++
	}
	splitsSeen := map[string]bool{}
	byDimension := map[string]map[string]map[string]int{}
	count := func(dimension, value, status string) {
		if byDimension[dimension] == nil {
			byDimension[dimension] = map[string]map[string]int{}
		}
		if byDimension[dimension][value] == nil {
			byDimension[dimension][value] = map[string]int{}
		}
		byDimension[dimension][value][status]++
	}
	strata := map[string]map[string]int{}
	stratum := func(split, name string) {
		if strata[split] == nil {
			strata[split] = map[string]int{"duplicates": 0, "seen_in_train": 0, "common_short_text": 0, "cross_split_duplicate": 0}
		}
		strata[split][name]++
	}
	flagCounts := map[string]int{}
	familySplits := map[string]map[string]bool{}
	transforms := map[string]int{"verified": 0, "unverified": 0}
	merges := map[string]*labelingExportUnit{}
	itemsWritten := 0

	itemRows, err := tx.QueryContext(ctx, `SELECT i.id,i.unit_id,i.chunk_index,i.chunk_count,i.visible_start,i.visible_end,i.text,i.chars,i.source_ranges,i.target_hash,
		i.cross_split_duplicate,i.seen_in_train,i.common_short_text,u.message_id,u.conversation_id,COALESCE(u.canonical_conversation_id,''),u.workspace_id,
		COALESCE(u.root_conversation_id,''),COALESCE(u.run_id,''),u.depth,u.depth_status,COALESCE(u.session_id,''),COALESCE(u.session_role,''),u.cohort,u.actor_kind,
		u.sender_role,u.provider,u.kind,u.channel,u.reasoning_format,COALESCE(u.created_at,''),COALESCE(u.content_hash,''),u.message_sha256,u.message_text,
		COALESCE(u.authorship_reason,''),u.visible_text,u.marker_ranges,u.length_bucket,u.context_prompt,u.context_excerpt,u.context_refs,COALESCE(u.duplicate_of,''),
		COALESCE(u.duplicate_relation,''),COALESCE(u.family_id,''),COALESCE(u.split,'') FROM items i JOIN units u ON u.id=i.unit_id ORDER BY i.id`)
	if err != nil {
		return nil, err
	}
	defer itemRows.Close()
	for itemRows.Next() {
		var input labelingExportInput
		var prov labelingExportProvenance
		var visibleStart, visibleEnd int
		var sourceRanges, message, visible, markerRanges, contextRefs string
		var depth sql.NullInt64
		var reasoning sql.NullString
		if err := itemRows.Scan(&input.ItemID, &input.UnitID, &input.ChunkIndex, &input.ChunkCount, &visibleStart, &visibleEnd, &input.Text, &input.Chars,
			&sourceRanges, &prov.TargetHash, &prov.CrossSplitDuplicate, &prov.SeenInTrain, &prov.CommonShortText, &prov.MessageID, &prov.ConversationID,
			&prov.CanonicalID, &prov.WorkspaceID, &prov.RootConversationID, &prov.RunID, &depth, &prov.DepthStatus, &prov.SessionID, &prov.SessionRole,
			&input.Cohort, &input.ActorKind, &input.Sender, &input.Provider, &input.Kind, &input.Channel, &reasoning, &prov.CreatedAt, &prov.ContentHash,
			&prov.MessageSHA256, &message, &prov.AuthorshipReason, &visible, &markerRanges, &prov.LengthBucket, &input.Context, &input.ContextExcerpt,
			&contextRefs, &prov.DuplicateOf, &prov.DuplicateRelation, &prov.FamilyID, &input.Split); err != nil {
			return nil, err
		}
		if !wanted[input.ItemID] {
			continue
		}
		itemsWritten++
		if input.Split == "" {
			input.Split = labelingSplitUnassigned
		}
		if depth.Valid {
			input.Depth = &depth.Int64
		}
		if reasoning.Valid {
			input.ReasoningFormat = &reasoning.String
		}
		input.DatasetID, input.UnitsVersion = datasetID, labelingUnitsVersion
		// Text is exported exactly as prepared: prepare excluded messages
		// that were not valid UTF-8, and offsets are bytes of this text.
		for name, value := range map[string]string{"text": input.Text, "context": input.Context, "message": message, "visible text": visible} {
			if !utf8.ValidString(value) {
				return nil, fmt.Errorf("item %s: its %s is not valid UTF-8", input.ItemID, name)
			}
		}
		if visibleStart < 0 || visibleEnd > len(visible) || visible[visibleStart:visibleEnd] != input.Text {
			return nil, fmt.Errorf("item %s: its text is not its unit's visible text at %d..%d", input.ItemID, visibleStart, visibleEnd)
		}
		prov.ItemID, prov.UnitID, prov.Split = input.ItemID, input.UnitID, input.Split
		prov.VisibleRange = [2]int{visibleStart, visibleEnd}
		prov.ContextRefs = json.RawMessage(defaultString(contextRefs, "[]"))
		prov.Pieces = defaultSlice(pieces[input.UnitID])
		prov.Sentences = defaultSlice(sentences[input.UnitID])
		prov.Actions = defaultSlice(actions[prov.MessageID])
		var markers [][2]int
		json.Unmarshal([]byte(markerRanges), &markers)
		prov.Markers = [][2]int{}
		for _, marker := range markers {
			if marker[0] < visibleEnd && marker[1] > visibleStart {
				prov.Markers = append(prov.Markers, [2]int{max(marker[0], visibleStart) - visibleStart, min(marker[1], visibleEnd) - visibleStart})
			}
		}
		if prov.SourceRanges, err = labelingExportSourceRanges(input.ItemID, sourceRanges, message, visible, visibleStart, visibleEnd, prov.Pieces, transforms); err != nil {
			return nil, err
		}

		// The authoritative result and its status.
		history := results[input.ItemID]
		result := labelingAuthoritative(history)
		status := labelingExportStatus(result)
		label := labelingExportLabel{ItemID: input.ItemID, UnitID: input.UnitID, Split: input.Split, Status: status, Outcome: "no_result",
			Evidence: json.RawMessage("[]"), Issues: []string{}, Flags: []string{}, Teacher: teacher}
		label.Receipt.History = []labelingExportAttempt{}
		label.Receipt.RequestsTried = defaultSlice(itemRequests[input.ItemID])
		if requests := itemRequests[input.ItemID]; len(requests) > 0 {
			last := requests[len(requests)-1]
			label.Receipt.RequestID, label.Receipt.RequestState, label.Receipt.RequestItems = last, requestStates[last], requestSizes[last]
		}
		for _, previous := range history {
			label.Receipt.History = append(label.Receipt.History, labelingExportAttempt{AttemptID: previous.attempt, RequestID: previous.request,
				Outcome: previous.outcome, ImportedFrom: previous.importedFrom, CreatedAt: previous.created})
		}
		if result != nil {
			label.Outcome, label.Issues, label.NeedContext = result.outcome, defaultSlice(result.issues), result.need
			label.Receipt.AttemptID, label.Receipt.ImportedFrom, label.Receipt.ContractHash = result.attempt, result.importedFrom, result.contractHash
			if result.request != "" {
				label.Receipt.RequestID, label.Receipt.RequestState, label.Receipt.RequestItems = result.request, requestStates[result.request], requestSizes[result.request]
			}
			label.Evidence = result.evidence
			if err := labelingCheckEvidence(input.ItemID, input.Text, label.Evidence); err != nil {
				return nil, err
			}
			flags := map[string]bool{}
			for _, issue := range result.issues {
				if issue == "refused_copacked_unresolved" || issue == servedModelDiffers {
					flags[issue] = true
				}
			}
			if result.served != "" && result.served != servedModelVerified {
				flags[result.served] = true
			}
			if result.importedFrom != "" {
				flags["imported"] = true
			}
			if info := attempts[result.attempt]; info != nil {
				label.Receipt.CostStatus, label.Receipt.AttemptCost = info.costStatus, info.cost
				if label.Receipt.CostStatus == "" {
					label.Receipt.CostStatus = "no_usage"
				}
				if slices.Contains(info.problems, "answer_without_terminal") {
					flags["answer_without_terminal"] = true
				}
				if info.usageMissing {
					flags["usage_missing"] = true
				}
				if info.cliVersion != "" {
					label.Teacher.CLIVersion = info.cliVersion
				}
			} else if result.importedFrom != "" {
				label.Receipt.CostStatus = "imported"
			}
			label.Flags = sortedKeys(flags)
			if status == exportUsable {
				label.Labels, label.UnsupportedLabels, label.UncertainLabels = []string{}, map[string]string{}, defaultSlice(result.uncertain)
				for _, accepted := range result.labels {
					if evidence := result.labelsEvidence[accepted]; evidence == "supported" {
						label.Labels = append(label.Labels, accepted)
					} else {
						label.UnsupportedLabels[accepted] = defaultString(evidence, "missing")
					}
				}
				sort.Strings(label.Labels)
				sort.Strings(label.UncertainLabels)
				label.ImpliedAncestors = labelingAncestors(label.Labels, parents)
			} else {
				label.Returned = &labelingExportReturned{Labels: defaultSlice(result.labels), UncertainLabels: defaultSlice(result.uncertain)}
			}
		}
		for _, flag := range label.Flags {
			flagCounts[flag]++
		}
		splitsSeen[input.Split] = true
		if status == exportUsable {
			for _, value := range label.Labels {
				support(value, input.Split, "positive")
			}
			for value := range label.UnsupportedLabels {
				support(value, input.Split, "unsupported")
			}
			for _, value := range label.UncertainLabels {
				support(value, input.Split, "uncertain")
			}
			for _, value := range label.ImpliedAncestors {
				support(value, input.Split, "implied")
			}
		}
		depthKey := "unresolved"
		if input.Depth != nil {
			depthKey = fmt.Sprint(*input.Depth)
		}
		count("split", input.Split, status)
		count("cohort", input.Cohort, status)
		count("provider", input.Provider, status)
		count("depth", depthKey, status)
		count("length_bucket", prov.LengthBucket, status)
		count("outcome", label.Outcome, status)
		if strata[input.Split] == nil {
			strata[input.Split] = map[string]int{"duplicates": 0, "seen_in_train": 0, "common_short_text": 0, "cross_split_duplicate": 0}
		}
		if prov.DuplicateOf != "" {
			stratum(input.Split, "duplicates")
		}
		if prov.SeenInTrain {
			stratum(input.Split, "seen_in_train")
		}
		if prov.CommonShortText {
			stratum(input.Split, "common_short_text")
		}
		if prov.CrossSplitDuplicate {
			stratum(input.Split, "cross_split_duplicate")
		}
		family := defaultString(prov.FamilyID, "conversation:"+prov.ConversationID)
		if familySplits[family] == nil {
			familySplits[family] = map[string]bool{}
		}
		familySplits[family][input.Split] = true
		if input.ChunkCount > 1 {
			merge := merges[input.UnitID]
			if merge == nil {
				merge = &labelingExportUnit{UnitID: input.UnitID, Split: input.Split, ChunkingVersion: labelingChunkVersion, MergeVersion: labelingMergeVersion,
					UnvalidatedMerge: true, ChunkCount: input.ChunkCount}
				merges[input.UnitID] = merge
			}
			merge.Chunks = append(merge.Chunks, labelingExportUnitChunk{ItemID: input.ItemID, Index: input.ChunkIndex, Status: status, Labels: label.Labels})
		}
		labelsFile := exportLabels
		if !labelingTrainingSplit(input.Split) {
			labelsFile = exportLabelsHeldOut
		}
		for _, write := range []struct {
			file  string
			value any
		}{{exportInputs, input}, {exportProvenance, prov}, {labelsFile, label}} {
			if err := writers[write.file].write(write.value); err != nil {
				return nil, err
			}
		}
	}
	if err := itemRows.Err(); err != nil {
		return nil, err
	}
	if itemsWritten != len(wanted) {
		return nil, fmt.Errorf("job %s: %d items, %d exported", job, len(wanted), itemsWritten)
	}

	// Chunked units: union of the chunks' labels, usable only when every
	// chunk is.
	chunked := map[string]int{"units": 0, "usable": 0, "partial": 0}
	for _, unit := range sortedKeys(merges) {
		merge := merges[unit]
		sort.Slice(merge.Chunks, func(i, j int) bool { return merge.Chunks[i].Index < merge.Chunks[j].Index })
		chunked["units"]++
		complete := len(merge.Chunks) == merge.ChunkCount
		for _, chunk := range merge.Chunks {
			complete = complete && chunk.Status == exportUsable
		}
		merge.Status = exportPartial
		if complete {
			merge.Status = exportUsable
			positives, unsupported, uncertain := map[string]bool{}, map[string]bool{}, map[string]bool{}
			need := false
			for _, chunk := range merge.Chunks {
				for _, value := range chunk.Labels {
					positives[value] = true
				}
				result := labelingAuthoritative(results[chunk.ItemID])
				for _, value := range result.uncertain {
					uncertain[value] = true
				}
				for _, value := range result.labels {
					if result.labelsEvidence[value] != "supported" {
						unsupported[value] = true
					}
				}
				need = need || result.need != nil && *result.need
			}
			for value := range positives {
				delete(uncertain, value)
				delete(unsupported, value)
			}
			merge.Labels, merge.UnsupportedLabels, merge.UncertainLabels = sortedKeys(positives), sortedKeys(unsupported), sortedKeys(uncertain)
			merge.ImpliedAncestors = labelingAncestors(merge.Labels, parents)
			merge.NeedContext = &need
		}
		chunked[merge.Status]++
		file := exportUnits
		if !labelingTrainingSplit(merge.Split) {
			file = exportUnitsHeldOut
		}
		if err := writers[file].write(merge); err != nil {
			return nil, err
		}
	}

	files := map[string]any{}
	digest := sha256.New()
	for _, name := range labelingExportFiles {
		writer := writers[name]
		if err := writer.close(); err != nil {
			return nil, err
		}
		sum := hex.EncodeToString(writer.hash.Sum(nil))
		files[name] = map[string]any{"sha256": sum, "rows": writer.rows, "bytes": writer.bytes}
		fmt.Fprintf(digest, "%s %s\n", name, sum)
	}
	for _, label := range labelSupport {
		for split := range splitsSeen {
			if label[split] == nil {
				label[split] = map[string]int{"positive": 0, "unsupported": 0, "uncertain": 0, "implied": 0}
			}
		}
	}
	familiesBySplit := map[string]int{}
	spanning := 0
	for _, splits := range familySplits {
		if len(splits) > 1 {
			spanning++
		}
		for split := range splits {
			familiesBySplit[split]++
		}
	}
	taxonomy := []map[string]any{}
	for _, id := range taxonomyOrder {
		taxonomy = append(taxonomy, map[string]any{"id": id, "parent": parents[id]})
	}
	heldOut := []string{}
	for split := range splitsSeen {
		if !labelingTrainingSplit(split) {
			heldOut = append(heldOut, split)
		}
	}
	sort.Strings(heldOut)
	var selection any
	json.Unmarshal([]byte(text(jobRow, "selection")), &selection)
	complete := true
	for state := range stateCounts {
		if state == requestPending || state == requestLeased || state == requestSubmitted {
			complete = false
		}
	}
	exportID := hex.EncodeToString(digest.Sum(nil))[:16]
	manifest := map[string]any{
		"export_version": labelingExportVersion,
		"export_id":      exportID,
		"statement": "Teacher-model labels of the items one labeling job selected from a prepared dataset: provisional training data, not reviewed labels " +
			"and not a description of the whole library.",
		"dataset": map[string]any{"id": datasetID, "manifest_sha256": hex.EncodeToString(datasetSum[:]), "population": datasetManifest["population"],
			"snapshot": datasetManifest["snapshot"], "contracts": datasetManifest["contracts"]},
		"job": map[string]any{"name": job, "route": teacher.Route, "model": teacher.Model, "effort": teacher.Effort, "served_model": teacher.ServedModel,
			"prompt_version": teacher.PromptVersion, "prompt_template_sha256": text(jobRow, "prompt_template_sha256"), "schema_version": teacher.SchemaVersion,
			"schema_sha256": text(jobRow, "schema_sha256"), "taxonomy_version": teacher.TaxonomyVersion, "taxonomy_sha256": text(jobRow, "taxonomy_sha256"),
			"created_at": text(jobRow, "created_at"), "pricing_rule": text(jobRow, "pricing_rule"), "selection": selection, "request_states": stateCounts,
			"complete": complete},
		"contracts": map[string]any{"export": labelingExportVersion, "chunk_merge": labelingMergeVersion, "units": labelingUnitsVersion,
			"chunking": labelingChunkVersion, "splits": labelingSplitsVersion, "sentences": labelingSentenceVersion, "transform": feedbackJSONTransform},
		"files":           files,
		"training_files":  []string{exportLabels, exportUnits},
		"heldout_files":   []string{exportLabelsHeldOut, exportUnitsHeldOut},
		"training_splits": []string{"dev", "train"},
		"heldout_splits":  heldOut,
		"items":           itemsWritten,
		"counts":          byDimension,
		"label_support":   labelSupport,
		"strata":          strata,
		"families":        map[string]any{"by_split": familiesBySplit, "in_several_splits": spanning},
		"chunked_units":   chunked,
		"flags":           flagCounts,
		"transforms":      transforms,
		"taxonomy":        taxonomy,
		"rules": []string{
			"Only status usable rows train. Every other status (need_context, refused, refusal_suspected, invalid, tool_use, missing, held, unlabeled) is present " +
				"with its status and null labels; the teacher's reply, if any, is under returned. None is an all-negative row.",
			"labels are the positives: accepted labels with supported evidence, leaves or broad parents as returned. unsupported_labels and uncertain_labels are " +
				"neither positive nor negative. implied_ancestors are the positives' ancestors, listed apart; a parent label is never a positive for its children.",
			"Labels of the training splits (train, dev) are in labels.jsonl and unit-labels.jsonl; all other splits' in the -heldout files. inputs.jsonl and " +
				"provenance.jsonl hold no labels.",
			"Families never span splits (except quarantine, which is neither training nor evaluation); family_id lets a trainer check. Drop rows with " +
				"duplicate_of (delegation copies, later fork copies) to avoid counting one text twice. Exclude seen_in_train rows from evaluation and report " +
				"common_short_text as its own stratum.",
			"Chunked units: each chunk is a row; unit-labels files hold a union merge flagged unvalidated_merge, usable only when every chunk is usable.",
			"Byte offsets (item_start/item_end, visible_range, markers) are into the UTF-8 bytes of the item text; source ranges (start/end) into the stored " +
				"message, whose bytes are not exported: sha256 lets a reader check a literal range, and a json-string-v1 range is a whole decoded piece.",
		},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(dir, exportManifest), append(data, '\n'), 0o600); err != nil {
		return nil, err
	}
	return manifest, nil
}

func defaultSlice[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

// labelingExportGroup reads rows keyed by their "key" column, keeping only
// keys in the set, with NULL columns omitted.
func labelingExportGroup(ctx context.Context, tx *sql.Tx, statement string, keys map[string]bool) (map[string][]map[string]any, error) {
	rows, err := tx.QueryContext(ctx, statement)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	groups := map[string][]map[string]any{}
	values, pointers := make([]any, len(columns)), make([]any, len(columns))
	for index := range values {
		pointers[index] = &values[index]
	}
	for rows.Next() {
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row, key := map[string]any{}, ""
		for index, column := range columns {
			value := values[index]
			if data, ok := value.([]byte); ok {
				value = string(data)
			}
			switch {
			case column == "key":
				key, _ = value.(string)
			case value != nil:
				row[column] = value
			}
		}
		if keys[key] {
			groups[key] = append(groups[key], row)
		}
	}
	return groups, rows.Err()
}

// labelingExportSourceRanges maps an item's source ranges to item-relative
// byte offsets and checks them: a literal range must be the same bytes in
// the stored message and the item, on character boundaries; a transformed
// piece must decode to what the unit shows.
func labelingExportSourceRanges(item, stored, message, visible string, visibleStart, visibleEnd int, pieces []map[string]any, transforms map[string]int) ([]map[string]any, error) {
	var ranges []map[string]any
	if err := json.Unmarshal([]byte(defaultString(stored, "[]")), &ranges); err != nil {
		return nil, fmt.Errorf("item %s source ranges: %w", item, err)
	}
	out := []map[string]any{}
	for _, source := range ranges {
		start, end := int(integer(source["start"])), int(integer(source["end"]))
		if start < 0 || end < start || end > len(message) {
			return nil, fmt.Errorf("item %s: source range %d..%d outside its message", item, start, end)
		}
		original := message[start:end]
		if transform, _ := source["transform"].(string); transform != "" {
			if transform != feedbackJSONTransform {
				return nil, fmt.Errorf("item %s: unknown transform %s", item, transform)
			}
			pieceStart, pieceEnd := -1, -1
			for _, piece := range pieces {
				if int(integer(piece["start_byte"])) == start && int(integer(piece["end_byte"])) == end && piece["visible_start"] != nil {
					pieceStart, pieceEnd = int(integer(piece["visible_start"])), int(integer(piece["visible_end"]))
				}
			}
			if pieceStart < 0 || pieceEnd > len(visible) || pieceEnd <= visibleStart || pieceStart >= visibleEnd {
				return nil, fmt.Errorf("item %s: transformed range %d..%d has no visible piece in the item", item, start, end)
			}
			var decoded string
			verified := json.Unmarshal([]byte(`"`+original+`"`), &decoded) == nil && strings.Contains(decoded, visible[pieceStart:pieceEnd])
			if verified {
				transforms["verified"]++
			} else {
				transforms["unverified"]++
			}
			from, to := max(pieceStart, visibleStart)-visibleStart, min(pieceEnd, visibleEnd)-visibleStart
			out = append(out, map[string]any{"start": start, "end": end, "transform": transform, "item_start": from, "item_end": to,
				"source_sha256": hashBytes([]byte(original)), "piece_sha256": hashBytes([]byte(visible[pieceStart:pieceEnd])),
				"whole_piece": pieceStart >= visibleStart && pieceEnd <= visibleEnd, "transform_verified": verified})
			continue
		}
		from, to := int(integer(source["visible_start"]))-visibleStart, int(integer(source["visible_end"]))-visibleStart
		if from < 0 || to < from || to > visibleEnd-visibleStart {
			return nil, fmt.Errorf("item %s: source range %d..%d maps outside the item", item, start, end)
		}
		shown := visible[visibleStart+from : visibleStart+to]
		if original != shown || !utf8.ValidString(original) {
			return nil, fmt.Errorf("item %s: bytes %d..%d of its message are not the item's bytes %d..%d", item, start, end, from, to)
		}
		out = append(out, map[string]any{"start": start, "end": end, "item_start": from, "item_end": to, "sha256": hashBytes([]byte(original))})
	}
	return out, nil
}

// labelingCheckEvidence checks that every evidence occurrence is its quote's
// bytes in the item text, on character boundaries.
func labelingCheckEvidence(item, text string, raw json.RawMessage) error {
	var evidence []labelingEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		return fmt.Errorf("item %s evidence: %w", item, err)
	}
	for _, entry := range evidence {
		for _, occurrence := range entry.Occurrences {
			start, end := occurrence.ItemStart, occurrence.ItemEnd
			if start < 0 || end < start || end > len(text) || text[start:end] != entry.Quote || !utf8.ValidString(text[start:end]) {
				return fmt.Errorf("item %s: evidence %q is not the item's bytes %d..%d", item, entry.Quote, start, end)
			}
		}
	}
	return nil
}

// runLabelingExport is the export command.
func runLabelingExport(dir, job, out string, stdout io.Writer) error {
	path, manifest, err := exportLabeling(context.Background(), dir, job, out)
	if err != nil {
		return err
	}
	counts, _ := manifest["counts"].(map[string]map[string]map[string]int)
	fmt.Fprintf(stdout, "Exported %v items of job %s to %s\n", manifest["items"], job, path)
	if counts != nil {
		statuses := map[string]int{}
		for _, perStatus := range counts["split"] {
			for status, n := range perStatus {
				statuses[status] += n
			}
		}
		fmt.Fprintf(stdout, "By status: %s\nBy split: %s\n", compactJSON(statuses), compactJSON(counts["split"]))
	}
	if job, _ := manifest["job"].(map[string]any); job != nil && job["complete"] == false {
		fmt.Fprintln(stdout, "The job still has pending or in-flight requests: their items are exported as unlabeled.")
	}
	return nil
}

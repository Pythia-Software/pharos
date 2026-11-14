package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	labelrt "github.com/gbdubs/pharos/internal/labeler"
)

// Engagement labels (docs/engagement-labels.md) mark spans of the text a
// person typed with how they engage the agent: what they ask for, the bounds
// they set, what they accept, and so on. A token-label model (the NUTHATCH
// study's; labeler.TokenLabelModel) reads each typed message with the tail
// of the agent message before it, on the local labeler runtime, and the pass
// (engagement_run.go) stores the spans as byte ranges of messages.text.
//
// This is a preview: off unless engagement_enabled is set, with the model a
// local directory (engagement_model_dir). The label list is the model's
// (model.json); nothing here names a label.

// engagementSchemaDDL is created by the first run: a library that never
// runs the pass has none of it.
const engagementSchemaDDL = `
CREATE TABLE IF NOT EXISTS engagement_messages (
  message_id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL,
  -- What the spans were made from: the target text, where each of its bytes
  -- lies in messages.text, and the context (engagementInput.hash).
  input_hash TEXT NOT NULL,
  -- What made them: the export's name and file, its input contract and
  -- framing, the target-text rules and any tuned thresholds
  -- (engagementModel.identity).
  model TEXT NOT NULL,
  -- outcome is 'scored', or why the model was not asked: 'empty' (nothing
  -- typed is left after trimming) or 'no_letters'.
  outcome TEXT NOT NULL,
  tokens INTEGER NOT NULL,
  spans INTEGER NOT NULL,
  processed_at TEXT NOT NULL
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS engagement_messages_conversation_idx ON engagement_messages(conversation_id);
-- A span of one label, in reading order. start_byte and end_byte index
-- messages.text and lie inside text the person typed.
CREATE TABLE IF NOT EXISTS engagement_spans (
  message_id TEXT NOT NULL,
  position INTEGER NOT NULL,
  label TEXT NOT NULL,
  start_byte INTEGER NOT NULL,
  end_byte INTEGER NOT NULL,
  score REAL NOT NULL,
  PRIMARY KEY(message_id, position)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS engagement_spans_label_idx ON engagement_spans(label);
CREATE TABLE IF NOT EXISTS engagement_dirty (
  conversation_id TEXT PRIMARY KEY,
  token TEXT NOT NULL
);
`

// engagementSchemaVersion 1: the tables above and the dirty triggers.
const engagementSchemaVersion = "1"

// engagementTargetVersion names how a message becomes the model's input
// (engagementTargetText, engagementContext). It is part of the model
// identity, so a change scores every message again.
const engagementTargetVersion = "target-v1"

// The input rules, as the training data was built (the study's
// sample_library.py): the target keeps at most engagementTargetRunes code
// points, the context the last engagementContextRunes of the agent message
// before it.
const (
	engagementTargetRunes  = 6000
	engagementContextRunes = 1200
)

// engagementFloor is the lowest probability a worker returns (lower when a
// label's threshold is): the rest of the [tokens × labels] output never
// leaves the worker.
const engagementFloor = 0.3

func (c *Catalog) ensureEngagementSchema(ctx context.Context) error {
	if version, err := c.metaValue(ctx, "engagement_schema_version"); err == nil && version == engagementSchemaVersion {
		return nil
	}
	return c.writeTransaction(ctx, "create the engagement tables", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, engagementSchemaDDL); err != nil {
			return err
		}
		// The labels ledger's triggers: a conversation is marked when a
		// message's text or a message's authorship spans change, among
		// changes that do not matter here and cost one hash comparison.
		for _, statement := range itemDirtyTriggers("engagement_dirty", "engagement_dirty_") {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("%s: %w", statement[:60], err)
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('engagement_schema_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, engagementSchemaVersion)
		return err
	})
}

// engagementPresent is whether this library has the engagement tables.
func engagementPresent(ctx context.Context, q *sql.DB) bool {
	var tables int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('engagement_messages','engagement_spans','engagement_dirty')`).Scan(&tables)
	return err == nil && tables == 3
}

// EngagementSettings locate the model. The worker, its runtime and the
// budget are the labeler's (LabelerSettings): every model shares one runtime.
type EngagementSettings struct {
	Enabled bool
	// ModelDir holds the export: model.json, the ONNX files and
	// tokenizer.json. Variant is model.json's variant ("fp32" when empty).
	ModelDir, Variant string
	// Threads per worker (engagement_threads) and padded tokens per batch
	// (engagement_batch_tokens); 0 uses the labeler's.
	Threads, BatchTokens int
}

func engagementSettingsFrom(root map[string]any, base string) EngagementSettings {
	dir := expandPath(os.Getenv("PHAROS_ENGAGEMENT_MODEL_DIR"))
	if dir == "" {
		dir = resolvePath(base, stringValue(root, "engagement_model_dir", ""))
	}
	return EngagementSettings{Enabled: boolValue(root, "engagement_enabled", false), ModelDir: dir, Variant: stringValue(root, "engagement_variant", ""),
		Threads: intValue(root, "engagement_threads", 0), BatchTokens: intValue(root, "engagement_batch_tokens", 0)}
}

// labeler is the labeler settings the pass runs with: the labeler's worker,
// runtime and budget, with its own threads per worker and batch size.
func (settings EngagementSettings) labeler(labels LabelerSettings) LabelerSettings {
	if settings.Threads > 0 {
		labels.Threads = settings.Threads
	}
	if settings.BatchTokens > 0 {
		labels.BatchTokens = settings.BatchTokens
	} else if labels.BatchTokens <= 0 || labels.BatchTokens > engagementBatchTokens {
		labels.BatchTokens = engagementBatchTokens
	}
	return labels
}

// engagementBatchTokens is the padded tokens per batch unless configured:
// windows run to 1,024 tokens, and batches of 4,096 measured about a tenth
// faster than the labeler's 16,384 with under half the worker memory
// (docs/engagement-labels.md).
const engagementBatchTokens = 4096

// engagementModel is a loaded model and the identity its rows carry.
type engagementModel struct {
	*labelrt.TokenLabelModel
	identity string
	floor    float64
}

func loadEngagementModel(settings EngagementSettings) (*engagementModel, error) {
	if settings.ModelDir == "" {
		return nil, fmt.Errorf("%w: set engagement_model_dir (or PHAROS_ENGAGEMENT_MODEL_DIR) to a token-label model directory", labelrt.ErrModelUnavailable)
	}
	model, err := labelrt.LoadTokenLabelModel(settings.ModelDir, settings.Variant)
	if err != nil {
		return nil, err
	}
	out := &engagementModel{TokenLabelModel: model, identity: model.Identity() + "/" + engagementTargetVersion, floor: engagementFloor}
	tuned := false
	for _, threshold := range model.Thresholds {
		tuned = tuned || threshold != labelrt.TokenLabelThreshold
		out.floor = math.Min(out.floor, float64(threshold))
	}
	if tuned {
		raw, _ := json.Marshal(model.Thresholds)
		sum := sha256.Sum256(raw)
		out.identity += "/t" + hex.EncodeToString(sum[:4])
	}
	return out, nil
}

// engagementDimension is a label's dimension: the part before its first dot.
func engagementDimension(label string) string {
	dimension, _, _ := strings.Cut(label, ".")
	return dimension
}

// engagementPiece is a stretch of the target text and where it came from.
// source.Start < 0 marks text that is in no message (a marker). exact pieces
// map byte for byte; any other (a replaced invalid byte) maps as a whole.
type engagementPiece struct {
	target, source labelrt.ByteRange
	exact          bool
}

// engagementTarget is the model's text for one message, with the map back
// to messages.text.
type engagementTarget struct {
	text   string
	pieces []engagementPiece
}

// engagementTargetText is the study's target_text: the message's typed
// spans, in order, with each run of other spans of one category replaced by
// one " ⟦category⟧ " marker; then white space trimmed from both ends and the
// first engagementTargetRunes code points kept. Bytes that are not UTF-8
// become U+FFFD one by one.
func engagementTargetText(text string, spans []authorSpan) engagementTarget {
	var b strings.Builder
	pieces := []engagementPiece{}
	add := func(value string, source labelrt.ByteRange, exact bool) {
		if value == "" {
			return
		}
		start := b.Len()
		b.WriteString(value)
		pieces = append(pieces, engagementPiece{target: labelrt.ByteRange{Start: start, End: b.Len()}, source: source, exact: exact})
	}
	marker := ""
	for _, span := range spans {
		if span.Category != "typed" {
			if marker != span.Category {
				add(" ⟦"+span.Category+"⟧ ", labelrt.ByteRange{Start: -1, End: -1}, false)
				marker = span.Category
			}
			continue
		}
		marker = ""
		start, end := max(0, min(span.Start, len(text))), max(0, min(span.End, len(text)))
		for at := start; at < end; {
			// A run of valid text, then one invalid byte.
			run := at
			for run < end {
				r, size := utf8.DecodeRuneInString(text[run:end])
				if r == utf8.RuneError && size <= 1 {
					break
				}
				run += size
			}
			add(text[at:run], labelrt.ByteRange{Start: at, End: run}, true)
			if at = run; at < end {
				add("�", labelrt.ByteRange{Start: at, End: at + 1}, false)
				at++
			}
		}
	}
	whole := b.String()
	start, end := labelrt.TrimSpaceRange(whole, 0, len(whole))
	runes := 0
	for i := range whole[start:end] {
		if runes == engagementTargetRunes {
			end = start + i
			break
		}
		runes++
	}
	target := engagementTarget{text: whole[start:end]}
	for _, piece := range pieces {
		lo, hi := max(piece.target.Start, start), min(piece.target.End, end)
		if lo >= hi {
			continue
		}
		clipped := engagementPiece{target: labelrt.ByteRange{Start: lo - start, End: hi - start}, source: piece.source, exact: piece.exact}
		if piece.exact {
			clipped.source = labelrt.ByteRange{Start: piece.source.Start + lo - piece.target.Start, End: piece.source.Start + hi - piece.target.Start}
		}
		target.pieces = append(target.pieces, clipped)
	}
	return target
}

// scorable is why the model is not asked about a target ("" when it is):
// the training data left out targets that are empty or hold no letter.
func (t engagementTarget) scorable() string {
	if t.text == "" {
		return "empty"
	}
	if strings.IndexFunc(t.text, unicode.IsLetter) < 0 {
		return "no_letters"
	}
	return ""
}

// sourceRanges maps a span of the target text to the bytes of messages.text
// it covers: one range per stretch of typed text, split wherever a marker (or
// text the message does not have between two typed spans) lies inside the
// span, each trimmed of white space. A span never covers marker text.
func (t engagementTarget) sourceRanges(source string, start, end int) []labelrt.ByteRange {
	ranges := []labelrt.ByteRange{}
	joined := false
	for _, piece := range t.pieces {
		lo, hi := max(piece.target.Start, start), min(piece.target.End, end)
		if lo >= hi {
			if piece.target.Start >= end {
				break
			}
			continue
		}
		if piece.source.Start < 0 {
			joined = false
			continue
		}
		mapped := piece.source
		if piece.exact {
			mapped = labelrt.ByteRange{Start: piece.source.Start + lo - piece.target.Start, End: piece.source.Start + hi - piece.target.Start}
		}
		if n := len(ranges); joined && n > 0 && ranges[n-1].End == mapped.Start {
			ranges[n-1].End = mapped.End
		} else {
			ranges = append(ranges, mapped)
		}
		joined = true
	}
	out := ranges[:0]
	for _, r := range ranges {
		if r.Start, r.End = labelrt.TrimSpaceRange(source, r.Start, r.End); r.End > r.Start {
			out = append(out, r)
		}
	}
	return out
}

// engagementContext is the model's context from the agent message before:
// its last engagementContextRunes code points, after "…" when it was cut.
// tail is that message's last code points (at most the limit) and runes its
// whole length.
func engagementContext(tail string, runes int) string {
	tail = strings.ToValidUTF8(tail, "�")
	if runes > engagementContextRunes {
		return "…" + tail
	}
	return tail
}

// engagementInputHash is what a message's spans depend on besides the model:
// the target text, where its pieces lie in the message, and the context.
func engagementInputHash(target engagementTarget, context string) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%d\x00%s\x00%d\x00%s\x00", len(target.text), target.text, len(context), context)
	for _, piece := range target.pieces {
		fmt.Fprintf(hash, "%d,%d,%d,%d,%t;", piece.target.Start, piece.target.End, piece.source.Start, piece.source.End, piece.exact)
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

// engagementSpan is one stored span.
type engagementSpan struct {
	label      string
	start, end int
	score      float64
}

// engagementSpans turns the model's spans of the target text into spans of
// the message, in reading order (then longest first, then label order).
func engagementSpans(model *engagementModel, source string, target engagementTarget, decoded []labelrt.LabelSpan) []engagementSpan {
	type ordered struct {
		engagementSpan
		label int
	}
	spans := []ordered{}
	for _, span := range decoded {
		for _, r := range target.sourceRanges(source, span.Start, span.End) {
			spans = append(spans, ordered{engagementSpan{label: model.Labels[span.Label], start: r.Start, end: r.End, score: float64(span.Score)}, span.Label})
		}
	}
	sort.SliceStable(spans, func(i, j int) bool {
		a, b := spans[i], spans[j]
		switch {
		case a.start != b.start:
			return a.start < b.start
		case a.end != b.end:
			return a.end > b.end
		}
		return a.label < b.label
	})
	out := make([]engagementSpan, len(spans))
	for i, span := range spans {
		out[i] = span.engagementSpan
	}
	return out
}

// engagementAttacher gives the reader a conversation's spans: attach adds a
// message's as "engagement", each with its label, dimension, score and its
// range of the message's text in UTF-16 code units (JavaScript's string
// indexes). Spans that no longer fit the text (it changed since they were
// made) are left out. summary is nil for a conversation the pass never
// read, so the reader shows no Engagement control.
func (c *Catalog) engagementAttacher(ctx context.Context, conversationID any) (attach func(map[string]any), summary func() (map[string]any, error), err error) {
	if !engagementPresent(ctx, c.reader()) {
		return func(map[string]any) {}, func() (map[string]any, error) { return nil, nil }, nil
	}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT s.message_id,s.label,s.start_byte,s.end_byte,s.score FROM engagement_messages e
		JOIN engagement_spans s ON s.message_id=e.message_id WHERE e.conversation_id=? ORDER BY s.message_id,s.position`, conversationID)
	if err != nil {
		return nil, nil, err
	}
	byMessage := map[string][]map[string]any{}
	for _, row := range rows {
		byMessage[firstString(row["message_id"])] = append(byMessage[firstString(row["message_id"])], row)
	}
	attached := 0
	dimensions := map[string]bool{}
	attach = func(message map[string]any) {
		spans := byMessage[firstString(message["id"])]
		text, ok := message["text"].(string)
		if len(spans) == 0 || !ok {
			return
		}
		offsets := make([]int, 0, 2*len(spans))
		for _, span := range spans {
			offsets = append(offsets, int(integer(span["start_byte"])), int(integer(span["end_byte"])))
		}
		units, ok := utf16Offsets(text, offsets)
		if !ok {
			// Made from text the message no longer has.
			return
		}
		out := make([]map[string]any, 0, len(spans))
		for _, span := range spans {
			label := firstString(span["label"])
			score, _ := span["score"].(float64)
			dimensions[engagementDimension(label)] = true
			out = append(out, map[string]any{"label": label, "dimension": engagementDimension(label), "score": math.Round(score*1000) / 1000,
				"start": units[int(integer(span["start_byte"]))], "end": units[int(integer(span["end_byte"]))]})
		}
		message["engagement"] = out
		attached++
	}
	summary = func() (map[string]any, error) {
		var read int
		var models sql.NullString
		if err := c.reader().QueryRowContext(ctx, `SELECT COUNT(*),group_concat(DISTINCT model) FROM engagement_messages WHERE conversation_id=?`, conversationID).Scan(&read, &models); err != nil {
			return nil, err
		}
		if read == 0 {
			return nil, nil
		}
		out := map[string]any{"messages_read": read, "messages_with_spans": attached, "model": models.String, "dimensions": slices.Sorted(maps.Keys(dimensions))}
		var dirty bool
		if err := c.reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM engagement_dirty WHERE conversation_id=?)`, conversationID).Scan(&dirty); err != nil {
			return nil, err
		}
		if dirty {
			out["pending"] = true
		}
		return out, nil
	}
	return attach, summary, nil
}

// engagementLaneTextLimit is the longest message text a Timeline lane
// carries for its Human events: the page applies the reader's display rules
// (reader-model.js: threshold, whole words, typed text only), which need the
// stored text. A longer message, nearly always a paste, shows no markers
// there; the reader still shows its annotations.
var engagementLaneTextLimit = 64 << 10

// attachLaneEngagement gives the person's messages among a Timeline lane's
// rows what the reader's payload gives them (engagementAttacher): engagement,
// the spans in UTF-16 units, with engagement_text, the stored text those
// index, since a lane otherwise holds only an excerpt. It reads spans by
// message ID (the spans table's key) for the rows shown and nothing else,
// and does nothing in a library without the engagement tables, which exist
// only once engagement_enabled has been on.
func (c *Catalog) attachLaneEngagement(ctx context.Context, q queryer, rows []map[string]any) error {
	byID := map[string]map[string]any{}
	ids := []string{}
	for _, row := range rows {
		if firstString(row["role"]) == "user" && firstString(row["kind"]) == "message" {
			byID[firstString(row["id"])] = row
			ids = append(ids, firstString(row["id"]))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	encoded, _ := json.Marshal(ids)
	spans, err := queryMapsContext(ctx, q, `SELECT s.message_id,s.label,s.start_byte,s.end_byte,s.score FROM json_each(?) j
		JOIN engagement_spans s ON s.message_id=j.value ORDER BY s.message_id,s.position`, string(encoded))
	if err != nil || len(spans) == 0 {
		return err
	}
	byMessage := map[string][]map[string]any{}
	for _, span := range spans {
		byMessage[firstString(span["message_id"])] = append(byMessage[firstString(span["message_id"])], span)
	}
	encoded, _ = json.Marshal(slices.Sorted(maps.Keys(byMessage)))
	texts, err := queryMapsContext(ctx, q, `SELECT m.id,m.text FROM json_each(?) j JOIN messages m ON m.id=j.value
		WHERE length(CAST(COALESCE(m.text,'') AS BLOB))<=?`, string(encoded), engagementLaneTextLimit)
	if err != nil {
		return err
	}
	for _, row := range texts {
		id := firstString(row["id"])
		text, _ := row["text"].(string)
		offsets := []int{}
		for _, span := range byMessage[id] {
			offsets = append(offsets, int(integer(span["start_byte"])), int(integer(span["end_byte"])))
		}
		units, ok := utf16Offsets(text, offsets)
		if !ok {
			continue
		}
		out := make([]map[string]any, 0, len(byMessage[id]))
		for _, span := range byMessage[id] {
			label := firstString(span["label"])
			score, _ := span["score"].(float64)
			out = append(out, map[string]any{"label": label, "dimension": engagementDimension(label), "score": math.Round(score*1000) / 1000,
				"start": units[int(integer(span["start_byte"]))], "end": units[int(integer(span["end_byte"]))]})
		}
		byID[id]["engagement"], byID[id]["engagement_text"] = out, text
	}
	return nil
}

// engagementStatus is pharos engagement status: whether the pass is on, what
// it would run, and what the tables hold.
func (c *Catalog) engagementStatus(ctx context.Context, config Config) (map[string]any, error) {
	labels, settings := config.Labels.withAssets(), config.Engagement
	status := map[string]any{"enabled": settings.Enabled, "model_dir": settings.ModelDir, "worker": labels.Worker,
		"runtime": defaultString(labels.Runtime, labelsAssetStatus(labelsAssets.Runtime)), "threads": settings.labeler(labels).Threads}
	identity := ""
	if settings.ModelDir != "" {
		if model, err := loadEngagementModel(settings); err != nil {
			status["model_error"] = err.Error()
		} else {
			identity = model.identity
			status["model"], status["labels"], status["codebook"] = model.identity, len(model.Labels), model.Codebook
		}
	}
	var candidates int
	if err := c.reader().QueryRowContext(ctx, `SELECT count(*) FROM (`+engagementCandidatesSQL+`)`).Scan(&candidates); err != nil {
		return nil, err
	}
	status["typed_messages"] = candidates
	if !engagementPresent(ctx, c.reader()) {
		status["messages"], status["spans"], status["pending"] = 0, 0, candidates
		return status, nil
	}
	var messages, current, withSpans, spans, dirty int
	var models sql.NullString
	if err := c.reader().QueryRowContext(ctx, `SELECT count(*), COALESCE(sum(model=?),0), COALESCE(sum(spans>0),0), COALESCE(sum(spans),0), group_concat(DISTINCT model) FROM engagement_messages`, identity).
		Scan(&messages, &current, &withSpans, &spans, &models); err != nil {
		return nil, err
	}
	if err := c.reader().QueryRowContext(ctx, `SELECT count(*) FROM engagement_dirty`).Scan(&dirty); err != nil {
		return nil, err
	}
	status["messages"], status["messages_with_spans"], status["spans"], status["models"] = messages, withSpans, spans, models.String
	// Typed messages with no row from this model; changed conversations are
	// looked at again whether or not their messages turn out to differ.
	status["pending"], status["conversations_changed"] = max(0, candidates-current), dirty
	if complete, _ := c.metaValue(ctx, "engagement_complete"); complete != "" {
		status["complete_for"] = complete
	}
	return status, nil
}

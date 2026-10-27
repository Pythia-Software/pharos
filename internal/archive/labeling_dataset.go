package archive

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"unicode/utf8"
)

// labelingDatasetSQL is the prepared dataset's schema. Units are messages
// (labeling-units-v1); pieces are their classified byte ranges; items are
// what a teacher model labels (one per chunk of a unit's visible text).
// Original message text is copied so evidence can be checked against exact
// bytes without the catalog.
const labelingDatasetSQL = `
CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE units(
  id TEXT PRIMARY KEY, message_id TEXT NOT NULL UNIQUE, conversation_id TEXT NOT NULL, canonical_conversation_id TEXT,
  workspace_id TEXT NOT NULL, root_conversation_id TEXT, run_id TEXT, depth INTEGER, depth_status TEXT NOT NULL,
  session_id TEXT, session_role TEXT, cohort TEXT NOT NULL, actor_kind TEXT NOT NULL, sender_role TEXT NOT NULL, sender TEXT,
  provider TEXT NOT NULL, kind TEXT NOT NULL, channel TEXT NOT NULL, reasoning_format TEXT, created_at TEXT,
  content_hash TEXT, message_sha256 TEXT NOT NULL, message_text TEXT NOT NULL, authorship_reason TEXT,
  visible_text TEXT NOT NULL, visible_chars INTEGER NOT NULL, length_bucket TEXT NOT NULL, chunk_count INTEGER NOT NULL,
  context_text TEXT NOT NULL, context_prompt TEXT NOT NULL, context_excerpt INTEGER NOT NULL, context_refs TEXT NOT NULL,
  duplicate_of TEXT, duplicate_relation TEXT, family_id TEXT, split TEXT);
CREATE TABLE pieces(
  unit_id TEXT NOT NULL, position INTEGER NOT NULL, role TEXT NOT NULL, status TEXT NOT NULL, category TEXT, reason TEXT,
  start_byte INTEGER NOT NULL, end_byte INTEGER NOT NULL, visible_start INTEGER, visible_end INTEGER, transform TEXT,
  source_conversation_id TEXT, source_message_id TEXT, PRIMARY KEY(unit_id, position)) WITHOUT ROWID;
CREATE TABLE items(
  id TEXT PRIMARY KEY, unit_id TEXT NOT NULL, chunk_index INTEGER NOT NULL, chunk_count INTEGER NOT NULL,
  visible_start INTEGER NOT NULL, visible_end INTEGER NOT NULL, text TEXT NOT NULL, chars INTEGER NOT NULL, source_ranges TEXT NOT NULL);
CREATE INDEX items_unit_idx ON items(unit_id);
CREATE TABLE actions(
  message_id TEXT NOT NULL, position INTEGER NOT NULL, conversation_id TEXT NOT NULL, actor TEXT NOT NULL, kind TEXT NOT NULL,
  status TEXT NOT NULL, question TEXT NOT NULL, option TEXT NOT NULL, title_start INTEGER NOT NULL, title_end INTEGER NOT NULL,
  answer_start INTEGER NOT NULL, answer_end INTEGER NOT NULL, question_conversation_id TEXT, question_message_id TEXT,
  PRIMARY KEY(message_id, position));
CREATE TABLE exclusions(message_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, cohort TEXT NOT NULL, reason TEXT NOT NULL, detail TEXT);
CREATE TABLE families(conversation_id TEXT PRIMARY KEY, family_id TEXT NOT NULL, split TEXT NOT NULL, first_at TEXT);
`

// labelingWriter writes a dataset in batches of conversations.
type labelingWriter struct {
	db *sql.DB
	tx *sql.Tx
}

func createLabelingDataset(path string) (*labelingWriter, error) {
	// The file exists first, so SQLite gives its journal files the same mode.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	file.Close()
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	writer := &labelingWriter{db: db}
	if _, err := db.Exec(labelingDatasetSQL); err != nil {
		db.Close()
		return nil, err
	}
	if writer.tx, err = db.Begin(); err != nil {
		db.Close()
		return nil, err
	}
	return writer, nil
}

func (w *labelingWriter) exec(statement string, args ...any) error {
	_, err := w.tx.Exec(statement, args...)
	return err
}

func (w *labelingWriter) commit() error {
	if err := w.tx.Commit(); err != nil {
		return err
	}
	var err error
	w.tx, err = w.db.Begin()
	return err
}

func (w *labelingWriter) close() {
	if w.tx != nil {
		w.tx.Rollback()
	}
	w.db.Close()
}

func (w *labelingWriter) unit(unit *labelingUnit) error {
	refs, _ := json.Marshal(unit.contextRefs)
	if err := w.exec(`INSERT INTO units(id,message_id,conversation_id,canonical_conversation_id,workspace_id,root_conversation_id,run_id,depth,depth_status,
		session_id,session_role,cohort,actor_kind,sender_role,sender,provider,kind,channel,reasoning_format,created_at,content_hash,message_sha256,message_text,
		authorship_reason,visible_text,visible_chars,length_bucket,chunk_count,context_text,context_prompt,context_excerpt,context_refs)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		unit.id, unit.messageID, unit.conversation, nilIfEmpty(unit.canonical), unit.workspace, nilIfEmpty(unit.root), nilIfEmpty(unit.run), unit.depth, unit.depthStatus,
		nilIfEmpty(unit.session), nilIfEmpty(unit.sessionRole), unit.cohort, unit.actor, unit.role, nilIfEmpty(unit.sender), unit.provider, unit.kind, unit.channel,
		nilIfEmpty(unit.format), nilIfEmpty(unit.createdAt), nilIfEmpty(unit.contentHash), hashBytes([]byte(unit.text)), unit.text, nilIfEmpty(unit.authorship),
		unit.visible, utf8.RuneCountInString(unit.visible), unit.length, len(unit.chunks), unit.context, unit.contextPrompt, unit.contextExcerpt, string(refs)); err != nil {
		return err
	}
	for position, piece := range unit.pieces {
		var visibleStart, visibleEnd any
		if piece.VisibleStart >= 0 {
			visibleStart, visibleEnd = piece.VisibleStart, piece.VisibleEnd
		}
		if err := w.exec(`INSERT INTO pieces(unit_id,position,role,status,category,reason,start_byte,end_byte,visible_start,visible_end,transform,source_conversation_id,source_message_id)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, unit.id, position, piece.Role, piece.Status, nilIfEmpty(piece.Category), nilIfEmpty(piece.Reason), piece.Start, piece.End,
			visibleStart, visibleEnd, nilIfEmpty(piece.Transform), nilIfEmpty(piece.SourceConversation), nilIfEmpty(piece.SourceMessage)); err != nil {
			return err
		}
	}
	for index, chunk := range unit.chunks {
		ranges, _ := json.Marshal(chunkSourceRanges(unit.pieces, chunk))
		text := unit.visible[chunk.Start:chunk.End]
		if err := w.exec(`INSERT INTO items(id,unit_id,chunk_index,chunk_count,visible_start,visible_end,text,chars,source_ranges) VALUES(?,?,?,?,?,?,?,?,?)`,
			labelingID("i_", labelingUnitsVersion, labelingChunkVersion, unit.messageID, fmt.Sprint(index)), unit.id, index, len(unit.chunks),
			chunk.Start, chunk.End, text, utf8.RuneCountInString(text), string(ranges)); err != nil {
			return err
		}
	}
	return nil
}

// chunkSourceRanges maps a chunk of visible text to byte ranges of the
// original message: exactly for literal pieces, and the whole piece, marked,
// for a transformed one.
func chunkSourceRanges(pieces []labelingPiece, chunk labelingChunk) []map[string]any {
	ranges := []map[string]any{}
	for _, piece := range pieces {
		if piece.VisibleStart < 0 || piece.VisibleEnd <= chunk.Start || piece.VisibleStart >= chunk.End {
			continue
		}
		if piece.Transform != "" {
			ranges = append(ranges, map[string]any{"start": piece.Start, "end": piece.End, "transform": piece.Transform})
			continue
		}
		start, end := max(piece.VisibleStart, chunk.Start), min(piece.VisibleEnd, chunk.End)
		ranges = append(ranges, map[string]any{"start": piece.Start + start - piece.VisibleStart, "end": piece.Start + end - piece.VisibleStart,
			"visible_start": start, "visible_end": end})
	}
	return ranges
}

func (w *labelingWriter) exclusion(message, conversation, cohort, reason, detail string) error {
	return w.exec(`INSERT INTO exclusions(message_id,conversation_id,cohort,reason,detail) VALUES(?,?,?,?,?)`, message, conversation, cohort, reason, nilIfEmpty(detail))
}

func (w *labelingWriter) action(message, conversation string, position int, answer questionAnswer) error {
	return w.exec(`INSERT INTO actions(message_id,position,conversation_id,actor,kind,status,question,option,title_start,title_end,answer_start,answer_end,
		question_conversation_id,question_message_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, message, position, conversation, "human", "submitted_option",
		"recognized_question_form", answer.Title, answer.Option, answer.TitleStart, answer.TitleEnd, answer.Start, answer.End,
		nilIfEmpty(answer.Origin.conversation), nilIfEmpty(answer.Origin.message))
}

func (w *labelingWriter) duplicate(message, of, relation string) error {
	return w.exec(`UPDATE units SET duplicate_of=?,duplicate_relation=? WHERE message_id=?`, of, relation, message)
}

// finish records families and splits, stores the manifest, and commits.
func (w *labelingWriter) finish(families map[string]labelingFamily, manifest map[string]any) error {
	for conversation, family := range families {
		if err := w.exec(`INSERT INTO families(conversation_id,family_id,split,first_at) VALUES(?,?,?,?)`, conversation, family.id, family.split, nilIfEmpty(family.first)); err != nil {
			return err
		}
	}
	if err := w.exec(`UPDATE units SET family_id=f.family_id,split=f.split FROM families f WHERE f.conversation_id=units.conversation_id`); err != nil {
		return err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	for key, value := range map[string]string{"manifest": string(data), "state": "complete", "units_version": labelingUnitsVersion} {
		if err := w.exec(`INSERT INTO meta(key,value) VALUES(?,?)`, key, value); err != nil {
			return err
		}
	}
	if err := w.tx.Commit(); err != nil {
		return err
	}
	w.tx = nil
	if _, err := w.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return err
	}
	return w.db.Close()
}

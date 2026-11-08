package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"
)

// labelsItemBuilder builds the items the local labeler labels from a live
// catalog, with labeling prepare's own pass (labeling_prepare.go), so they are
// the items the model was trained on. Prepare classifies authorship in memory
// over its snapshot; this reads the stored ledger (message_authorship), which
// the analysis chain keeps current before labels run. Each conversation is
// read in short statements, never one long read transaction.
type labelsItemBuilder struct {
	p *labelingPass
	// sessions lists each conversation's agent sessions with their kinds,
	// for its structure fingerprint.
	sessions map[string][]string
}

// newLabelsItemBuilder reads the catalog-wide inputs the pass shares, once,
// from one read snapshot (labelsItemSnapshot): the execution dependency
// digests, the mirrors, conversations, sessions and reasoning state, and the
// occurrence decisions (one resolution of that snapshot and its occurrence
// claims). A publication's dependency check then compares against digests of
// exactly the evidence the units were selected with: evidence that changes
// and changes back between capture and publication (A, B, A) can never
// publish units chosen at B. The snapshot is released before it returns;
// later per-conversation reads (messages, stored authorship) go through db,
// guarded by the conversation's own dirty token at publication.
func newLabelsItemBuilder(ctx context.Context, db labelingReader) (*labelsItemBuilder, error) {
	return buildLabelsItems(ctx, db, func(ctx context.Context, q queryer) (*labelingOccurrences, error) {
		res, err := resolveExecutionsFrom(ctx, q)
		if err != nil {
			return nil, err
		}
		index, err := res.Occurrences(ctx, q, nil)
		return preloadedOccurrences("snapshot", res, index), err
	})
}

// newLabelsItemBuilderFor is newLabelsItemBuilder for a builder that reads
// c: its occurrence decisions are c's cached execution snapshot and
// catalog-wide occurrence index at the builder's snapshot, instead of a
// second resolution.
func (c *Catalog) newLabelsItemBuilderFor(ctx context.Context, db labelingReader) (*labelsItemBuilder, error) {
	return buildLabelsItems(ctx, db, func(ctx context.Context, q queryer) (*labelingOccurrences, error) {
		s, err := c.executionSnapshotAt(ctx, q)
		if err != nil {
			return nil, err
		}
		index, err := s.catalogOccurrences(ctx, q)
		return preloadedOccurrences("catalog", s.resolution, index), err
	})
}

// labelsItemSnapshot is the read snapshot a builder captures its shared
// inputs from. A *sql.DB (anything that begins transactions) gets a
// read-only transaction, released by done; any other reader is a
// transaction the caller holds, whose snapshot is used as is (the
// caller's precondition).
func labelsItemSnapshot(ctx context.Context, db labelingReader) (snapshot labelingReader, done func(), err error) {
	opener, ok := db.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	})
	if !ok {
		return db, func() {}, nil
	}
	tx, err := opener.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	// committedReads: the digest cache may fill from it.
	return committedReads{tx}, func() { _ = tx.Rollback() }, nil
}

func buildLabelsItems(ctx context.Context, db labelingReader, occurrences func(context.Context, queryer) (*labelingOccurrences, error)) (*labelsItemBuilder, error) {
	snapshot, done, err := labelsItemSnapshot(ctx, db)
	if err != nil {
		return nil, err
	}
	defer done()
	p := newLabelingPass(db, true)
	p.itemsOnly = true
	if p.executionDigests, err = executionDependencyDigests(ctx, snapshot); err != nil {
		return nil, err
	}
	if p.occurrences, err = occurrences(ctx, snapshot); err != nil {
		return nil, err
	}
	_, _, sessions, _, err := loadLabelingPass(ctx, snapshot, p)
	if err != nil {
		return nil, err
	}
	byConversation := map[string][]string{}
	for _, row := range sessions {
		conversation := firstString(row["conversation_id"])
		byConversation[conversation] = append(byConversation[conversation], labelsSessionEntry(row))
	}
	for _, list := range byConversation {
		sort.Strings(list)
	}
	// Delegation duplicates are bookkeeping for training, not item inputs.
	p.childOwners = map[string]bool{}
	p.loadAuthorship = func(id string) (*authorResult, error) {
		return loadStoredAuthorship(ctx, db, `SELECT m.id,m.conversation_id,m.text,a.spans_json,a.total_chars FROM message_authorship a JOIN messages m ON m.id=a.message_id WHERE a.message_id=?`, id)
	}
	return &labelsItemBuilder{p: p, sessions: byConversation}, nil
}

// labelsSessionEntry is how a structure fingerprint lists one agent session
// (a row of loadLabelingPass's sessions query).
func labelsSessionEntry(row map[string]any) string {
	return firstString(row["id"]) + ":" + firstString(row["kind"]) + ":" + firstString(row["child"]) + ":" + firstString(row["delegation"])
}

// fork shares only the catalog-wide read-only inputs. Conversation caches,
// counters and callbacks belong to this worker; a shallow copy of p would race
// on fork history, authorship and rendering bookkeeping.
func (b *labelsItemBuilder) fork(ctx context.Context) *labelsItemBuilder {
	p := newLabelingPass(b.p.tx, b.p.reasoningStore)
	p.itemsOnly = true
	p.conversations, p.representative = b.p.conversations, b.p.representative
	p.mainSession, p.sessionKind, p.current = b.p.mainSession, b.p.sessionKind, b.p.current
	p.lineage, p.executionDigests, p.occurrences = b.p.lineage, b.p.executionDigests, b.p.occurrences
	p.loadAuthorship = func(id string) (*authorResult, error) {
		return loadStoredAuthorship(ctx, p.tx, `SELECT m.id,m.conversation_id,m.text,a.spans_json,a.total_chars FROM message_authorship a JOIN messages m ON m.id=a.message_id WHERE a.message_id=?`, id)
	}
	return &labelsItemBuilder{p: p, sessions: b.sessions}
}

// conversation returns the units of one conversation, in transcript order:
// none for a mirrored work's or a conversation no longer in the catalog.
func (b *labelsItemBuilder) conversation(ctx context.Context, id string) ([]*labelingUnit, error) {
	p := b.p
	if p.conversations[id] == nil {
		return nil, nil
	}
	// Classifications and resolved re-sent sources are read per conversation,
	// so a long run holds only one conversation's.
	p.authorship, p.targetCache, p.authorshipErr = map[string]*authorResult{}, map[string][]bool{}, nil
	rows, err := p.tx.QueryContext(ctx, `SELECT m.id,m.conversation_id,m.text,a.spans_json,a.total_chars FROM messages m JOIN message_authorship a ON a.message_id=m.id
		WHERE m.conversation_id=? AND m.role='user'`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		result, err := scanStoredAuthorship(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if result != nil {
			p.authorship[result.Input.ID] = result
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	units := []*labelingUnit{}
	p.emit = func(unit *labelingUnit) error {
		units = append(units, unit)
		return nil
	}
	defer func() { p.emit = nil }()
	if err := p.conversation(ctx, id); err != nil {
		return nil, err
	}
	return units, nil
}

type rowScanner interface{ Scan(...any) error }

// scanStoredAuthorship rebuilds what the pass reads of a classification: the
// message's text and its spans as the ledger stored them. A row whose spans
// no longer fit the message (its text changed after it was classified, and
// the ledger has not caught up) is no classification: the message counts as
// unclassified until the next authorship run, which marks its conversation
// dirty again.
func scanStoredAuthorship(row rowScanner) (*authorResult, error) {
	var result authorResult
	var spans string
	var chars int
	if err := row.Scan(&result.Input.ID, &result.Input.ConversationID, &result.Input.Text, &spans, &chars); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(spans), &result.Spans); err != nil {
		return nil, nil
	}
	if utf8.RuneCountInString(strings.TrimSpace(result.Input.Text)) != chars {
		return nil, nil
	}
	at := 0
	for _, span := range result.Spans {
		if span.Start < at || span.End < span.Start || span.End > len(result.Input.Text) {
			return nil, nil
		}
		at = span.End
	}
	return &result, nil
}

func loadStoredAuthorship(ctx context.Context, db labelingReader, query string, args ...any) (*authorResult, error) {
	result, err := scanStoredAuthorship(db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return result, err
}

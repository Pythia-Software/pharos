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

// newLabelsItemBuilder reads the catalog-wide inputs the pass needs once
// (mirrors, conversations, sessions, reasoning state).
func newLabelsItemBuilder(ctx context.Context, db labelingReader) (*labelsItemBuilder, error) {
	p := newLabelingPass(db, true)
	p.itemsOnly = true
	_, _, sessions, _, err := loadLabelingPass(ctx, db, p)
	if err != nil {
		return nil, err
	}
	byConversation := map[string][]string{}
	for _, row := range sessions {
		conversation := firstString(row["conversation_id"])
		byConversation[conversation] = append(byConversation[conversation], firstString(row["id"])+":"+firstString(row["kind"])+":"+firstString(row["child"])+":"+firstString(row["delegation"]))
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

// conversation returns the units of one conversation, in transcript order:
// none for a mirrored work's or a conversation no longer in the catalog.
func (b *labelsItemBuilder) conversation(ctx context.Context, id string) ([]*labelingUnit, error) {
	p := b.p
	if p.conversations[id] == nil {
		return nil, nil
	}
	// Classifications and resolved re-sent sources are read per conversation,
	// so a long run holds only one conversation's.
	p.authorship, p.targetCache, p.parents, p.authorshipErr = map[string]*authorResult{}, map[string][]bool{}, map[string][]labelingParentMessage{}, nil
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

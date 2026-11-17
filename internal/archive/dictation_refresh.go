package archive

import (
	"context"
	"database/sql"
	"fmt"
)

// dictationRefreshBatch bounds the messages estimated per write turn.
var dictationRefreshBatch = 500

// dictationPending is independent of the authorship generation: installing new
// dictation rules must admit work even when no message has changed.
func (c *Catalog) dictationPending(ctx context.Context) (bool, error) {
	version, err := c.metaValue(ctx, "dictation_version")
	return version != dictationVersion, err
}

func (c *Catalog) dictationRefreshPending() bool {
	pending, err := c.dictationPending(context.Background())
	return err != nil || pending
}

// refreshDictation gives every message with typed words an estimate of the
// current dictationVersion, read from its stored spans, and removes estimates
// of messages no longer counted. Authorship writes keep the table current;
// this catches up a library classified before the table existed, or by older
// dictation rules. It writes only message_dictation, so a rule change never
// reclassifies writing or marks labels and findings pending. A finished
// refresh is recorded in meta; an interrupted one resumes where it stopped.
func (c *Catalog) refreshDictation(ctx context.Context) error {
	spelling := newSpellingCounts(ctx, c.reader())
	done := 0
	for {
		n, err := c.refreshDictationBatch(ctx, spelling)
		if err != nil || n == 0 {
			return err
		}
		done += n
		reportWork(ctx, fmt.Sprintf("estimating dictation: %d messages", done), "message", done, 0)
	}
}

// refreshDictationBatch yields after one bounded writer turn. Callers hold
// the authorship lease; a deadline or cancellation leaves the version pending
// so later admitted work can resume from the rows already saved.
func (c *Catalog) refreshDictationBatch(ctx context.Context, spelling *spellingCounts) (int, error) {
	pending, err := c.dictationPending(ctx)
	if err != nil || !pending {
		return 0, err
	}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT a.message_id,a.conversation_id,a.typed_words,m.text,COALESCE(c.harness,'') harness
		FROM message_authorship a JOIN messages m ON m.id=a.message_id JOIN conversations c ON c.id=a.conversation_id
		WHERE a.typed_words>0 AND NOT EXISTS(SELECT 1 FROM message_dictation d WHERE d.message_id=a.message_id AND d.version=?)
		ORDER BY a.message_id LIMIT ?`, dictationVersion, max(1, dictationRefreshBatch))
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, c.writeTransaction(ctx, "complete dictation estimates", func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM message_dictation WHERE NOT EXISTS(SELECT 1 FROM message_authorship a WHERE a.message_id=message_dictation.message_id AND a.typed_words>0)`); err != nil {
				return err
			}
			return setMeta(tx, "dictation_version", dictationVersion)
		})
	}
	estimates := make([][]any, 0, len(rows))
	for _, row := range rows {
		id := asString(row["message_id"])
		spans, err := storedTypedSpans(ctx, c.reader(), id)
		if err != nil {
			return 0, err
		}
		voice, err := voiceTranscript(ctx, c.reader(), asString(row["harness"]), id)
		if err != nil {
			return 0, err
		}
		estimate := estimateDictation(asString(row["text"]), spans, int(integer(row["typed_words"])), voice, spelling)
		estimates = append(estimates, estimate.row(id, asString(row["conversation_id"])))
	}
	err = c.writeTransaction(ctx, "estimate dictation", func(tx *sql.Tx) error {
		statement, err := tx.PrepareContext(ctx, `INSERT INTO message_dictation(`+joinColumns(dictationColumns)+`) VALUES(`+placeholders(len(dictationColumns))+`)
			ON CONFLICT(message_id) DO UPDATE SET conversation_id=excluded.conversation_id,typed_words=excluded.typed_words,p_dictated=excluded.p_dictated,
			input_method=excluded.input_method,marks=excluded.marks,evidence=excluded.evidence,version=excluded.version`)
		if err != nil {
			return err
		}
		defer statement.Close()
		for _, values := range estimates {
			if _, err := statement.ExecContext(ctx, values...); err != nil {
				return err
			}
		}
		return nil
	})
	return len(estimates), err
}

// storedTypedSpans reads a message's stored typed spans, in order, with a
// placeholder span between them for the text classified as anything else.
func storedTypedSpans(ctx context.Context, q queryer, messageID string) ([]authorSpan, error) {
	rows, err := queryMapsContext(ctx, q, `SELECT category,start_byte,end_byte FROM message_authorship_spans WHERE message_id=? ORDER BY position`, messageID)
	if err != nil {
		return nil, err
	}
	spans := make([]authorSpan, 0, len(rows))
	for _, row := range rows {
		spans = append(spans, authorSpan{Category: asString(row["category"]), Start: int(integer(row["start_byte"])), End: int(integer(row["end_byte"]))})
	}
	return spans, nil
}

func joinColumns(columns []string) string {
	out := ""
	for i, column := range columns {
		if i > 0 {
			out += ","
		}
		out += column
	}
	return out
}

package archive

import (
	"context"
	"database/sql"
	"time"
)

// workPopulationReport describes how a membership freeze read its revisions.
// Trusted is false when the snapshot lacked the revision triggers (a
// read-only catalog); Fallbacks counts live members whose revision hashed
// their rows because no stored input digest was usable.
type workPopulationReport struct {
	Members   int
	Fallbacks int
	Trusted   bool
}

// freezeWorkPopulation is an admitted membership phase. A single metadata
// snapshot freezes identities and input revisions; later historical inserts do
// not silently join it. Explicit IDs remain members even if already deleted.
// from/until qualify activity, including new messages in an old conversation.
// Zero bounds select all known conversations. This never stores transcript
// bodies or a persistent scheduler completion marker.
func (c *Catalog) freezeWorkPopulation(ctx context.Context, b *workInventoryBuilder, from, until time.Time, ids []string) error {
	report, err := c.freezeWorkPopulationReport(ctx, b, from, until, ids)
	taskResourcesFrom(ctx).progress("membership input-digest fallbacks", "conversation", float64(report.Fallbacks), float64(report.Members))
	return err
}

// freezeWorkPopulationReport installs the revision triggers before its
// snapshot, then reads every member's conversation row, stored input digest
// and agent sessions in one read transaction. Only members without a usable
// digest (never ingested since a repair or direct edit removed it) read their
// message rows; the report counts them as the coarse exception.
func (c *Catalog) freezeWorkPopulationReport(ctx context.Context, b *workInventoryBuilder, from, until time.Time, ids []string) (workPopulationReport, error) {
	report := workPopulationReport{}
	if err := c.ensureWorkRevisionInputs(ctx); err != nil {
		return report, err
	}
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	reader, err := newWorkRevisionReader(ctx, tx)
	if err != nil {
		return report, err
	}
	report.Trusted = reader.trusted
	add := func(conversation workRevisionConversation) error {
		revision, err := reader.revisionOf(conversation)
		if err != nil {
			return err
		}
		if err := b.Add(conversation.ID, revision); err != nil {
			return err
		}
		report.Members++
		return nil
	}
	if ids != nil {
		for _, id := range workDomainIDs(ids) {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			conversation, err := reader.lookup(id)
			if err != nil {
				return report, err
			}
			if err := add(conversation); err != nil {
				return report, err
			}
		}
		report.Fallbacks = reader.Fallbacks
		return report, nil
	}
	query := `SELECT ` + workRevisionConversationColumns + `,d.fingerprint FROM conversations c
 LEFT JOIN conversation_derivation_inputs d ON d.conversation_id=c.id`
	args := []any{}
	if !from.IsZero() || !until.IsZero() {
		query += ` WHERE EXISTS(SELECT 1 FROM messages m WHERE m.conversation_id=c.id`
		if !from.IsZero() {
			query += ` AND m.created_at>=?`
			args = append(args, formatTime(from))
		}
		if !until.IsZero() {
			query += ` AND m.created_at<?`
			args = append(args, formatTime(until))
		}
		query += `)`
	}
	query += ` ORDER BY c.id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		conversation, err := scanWorkRevisionConversation(rows)
		if err != nil {
			return report, err
		}
		if err := add(conversation); err != nil {
			return report, err
		}
	}
	report.Fallbacks = reader.Fallbacks
	return report, rows.Err()
}

// workConversationRevision fingerprints one conversation's canonical framing
// inputs (see work_revision.go). Derived labels/authorship are deliberately
// absent: producing them must not supersede its own request. Domain
// dirty/publication checks still decide readiness and dependent scope. It is
// called only in admitted phases, never from Next. The hash is an input
// identity, not a historical snapshot. Batches should prefer
// workConversationRevisions, which checks the triggers once.
func workConversationRevision(ctx context.Context, q queryer, id string) (string, error) {
	reader, err := newWorkRevisionReader(ctx, q)
	if err != nil {
		return "", err
	}
	return reader.revision(id)
}

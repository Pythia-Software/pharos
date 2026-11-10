package archive

import (
	"context"
	"database/sql"
	"time"
)

// workPopulationReport describes how a membership freeze read its revisions.
// Fallbacks counts live members whose revision hashed their rows because no
// stored input digest was usable.
type workPopulationReport struct {
	Members   int
	Fallbacks int
}

// workPopulationPage bounds each membership read transaction, so a
// library-wide backfill never pins the WAL for the whole scan.
var workPopulationPage = 512

// freezeWorkPopulation is an admitted membership phase. It freezes identities
// and input revisions; later historical inserts do not silently join it.
// Explicit IDs remain members even if already deleted. from/until qualify
// activity, including new messages in an old conversation. Zero bounds select
// all known conversations. This never stores transcript bodies or a persistent
// scheduler completion marker.
func (c *Catalog) freezeWorkPopulation(ctx context.Context, b *workInventoryBuilder, from, until time.Time, ids []string) error {
	report, err := c.freezeWorkPopulationReport(ctx, b, from, until, ids)
	taskResourcesFrom(ctx).progress("membership input-digest fallbacks", "conversation", float64(report.Fallbacks), float64(report.Members))
	return err
}

// freezeWorkPopulationReport reads every member's conversation row, stored
// input digest and agent sessions in short read transactions of
// workPopulationPage members, walking conversation IDs in order. Each page is
// consistent; a conversation created behind the cursor during the scan is not
// a member, one created ahead of it may be, exactly as if the scan had started
// a moment later. Domain dirty state, not membership, decides what publishes.
// Only members without a usable digest read their message rows; the report
// counts them as the coarse exception.
func (c *Catalog) freezeWorkPopulationReport(ctx context.Context, b *workInventoryBuilder, from, until time.Time, ids []string) (workPopulationReport, error) {
	report := workPopulationReport{}
	page := func(read func(*workRevisionReader, *sql.Tx) error) error {
		tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		reader := newWorkRevisionReader(ctx, tx)
		err = read(reader, tx)
		report.Fallbacks += reader.Fallbacks
		return err
	}
	add := func(reader *workRevisionReader, conversation workRevisionConversation) error {
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
		members := workDomainIDs(ids)
		for start := 0; start < len(members); start += workPopulationPage {
			chunk := members[start:min(start+workPopulationPage, len(members))]
			if err := page(func(reader *workRevisionReader, _ *sql.Tx) error {
				for _, id := range chunk {
					if err := ctx.Err(); err != nil {
						return err
					}
					conversation, err := reader.lookup(id)
					if err != nil {
						return err
					}
					if err := add(reader, conversation); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				return report, err
			}
		}
		return report, nil
	}
	query := `SELECT ` + workRevisionConversationColumns + `,d.fingerprint FROM conversations c
 LEFT JOIN conversation_derivation_inputs d ON d.conversation_id=c.id WHERE c.id>?`
	filters := []any{}
	if !from.IsZero() || !until.IsZero() {
		query += ` AND EXISTS(SELECT 1 FROM messages m WHERE m.conversation_id=c.id`
		if !from.IsZero() {
			query += ` AND m.created_at>=?`
			filters = append(filters, formatTime(from))
		}
		if !until.IsZero() {
			query += ` AND m.created_at<?`
			filters = append(filters, formatTime(until))
		}
		query += `)`
	}
	query += ` ORDER BY c.id LIMIT ?`
	for cursor, more := "", true; more; {
		err := page(func(reader *workRevisionReader, tx *sql.Tx) error {
			args := append(append([]any{cursor}, filters...), workPopulationPage)
			rows, err := tx.QueryContext(ctx, query, args...)
			if err != nil {
				return err
			}
			// Collect the page first: revisionOf queries the same connection.
			page := []workRevisionConversation{}
			for rows.Next() {
				conversation, err := scanWorkRevisionConversation(rows)
				if err != nil {
					rows.Close()
					return err
				}
				page = append(page, conversation)
			}
			if err := rows.Close(); err != nil {
				return err
			}
			if err := rows.Err(); err != nil {
				return err
			}
			for _, conversation := range page {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := add(reader, conversation); err != nil {
					return err
				}
			}
			more = len(page) == workPopulationPage
			if len(page) > 0 {
				cursor = page[len(page)-1].ID
			}
			return nil
		})
		if err != nil {
			return report, err
		}
	}
	return report, nil
}

// workConversationRevision fingerprints one conversation's canonical framing
// inputs (see work_revision.go). Derived labels/authorship are deliberately
// absent: producing them must not supersede its own request. Domain
// dirty/publication checks still decide readiness and dependent scope. It is
// called only in admitted phases, never from Next. The hash is an input
// identity, not a historical snapshot.
func workConversationRevision(ctx context.Context, q queryer, id string) (string, error) {
	return newWorkRevisionReader(ctx, q).revision(id)
}

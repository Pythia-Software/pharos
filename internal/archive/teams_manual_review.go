package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
)

// This version identifies both the material reviewed and the manual-review
// policy. A consumer must recompute it in its own scoped read transaction;
// the stored clear state alone is never evidence of current eligibility.
const teamsManualReviewPrefix = "manual-review-v1:"

type teamsManualReviewEntry struct {
	NativeID    string `json:"nativeId"`
	Role        string `json:"role"`
	Kind        string `json:"kind"`
	Text        string `json:"text"`
	RawText     string `json:"rawText"`
	CreatedAt   string `json:"createdAt"`
	SourceOrder int64  `json:"sourceOrder"`
	Selected    int64  `json:"selected"`
}

func teamsReviewField(h hash.Hash, s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	h.Write(n[:])
	h.Write([]byte(s))
}

func teamsManualReviewInput(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (string, error) {
	return teamsManualReviewSnapshot(ctx, q, id, nil)
}

func teamsManualReviewSnapshot(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string, entries *[]teamsManualReviewEntry) (string, error) {
	var provider, account, native string
	if err := q.QueryRowContext(ctx, `SELECT provider,account,native_id FROM conversations WHERE id=?`, id).Scan(&provider, &account, &native); err != nil {
		return "", err
	}
	if provider == "" || account == "" || native == "" {
		return "", errTeamsSourceUnavailable
	}
	rows, err := q.QueryContext(ctx, `SELECT native_id,role,kind,text,COALESCE(raw_text,''),COALESCE(created_at,''),COALESCE(source_order,-1),selected FROM messages WHERE conversation_id=? ORDER BY native_id LIMIT 5001`, id)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	h := sha256.New()
	teamsReviewField(h, teamsManualReviewPrefix)
	teamsReviewField(h, provider)
	teamsReviewField(h, account)
	teamsReviewField(h, native)
	count, size := 0, 0
	for rows.Next() {
		var mid, role, kind, body, raw, at string
		var order, selected int64
		if err := rows.Scan(&mid, &role, &kind, &body, &raw, &at, &order, &selected); err != nil {
			return "", err
		}
		count++
		size += len(mid) + len(role) + len(kind) + len(body) + len(raw) + len(at)
		if count > 5000 || size > 1<<20 {
			return "", errTeamsSourceUnavailable
		}
		if entries != nil {
			*entries = append(*entries, teamsManualReviewEntry{mid, role, kind, body, raw, at, order, selected})
		}
		for _, s := range []string{mid, role, kind, body, raw, at} {
			teamsReviewField(h, s)
		}
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(order))
		h.Write(n[:])
		binary.BigEndian.PutUint64(n[:], uint64(selected))
		h.Write(n[:])
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if count == 0 {
		return "", errTeamsSourceUnavailable
	}
	return teamsManualReviewPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

// The reviewed bytes and privacy revision are compared under the same writer
// transaction. Explicit private marks survive this action.
func (c *Catalog) manuallyClearTeamsInput(ctx context.Context, id, input string, expected int64) (teamsPrivacy, error) {
	var result teamsPrivacy
	if input == "" {
		return result, errTeamsPrivacyStale
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return result, err
	}
	defer lease.Close()
	err = c.writeTransaction(ctx, "teams-manual-review", func(tx *sql.Tx) error {
		p, e := readTeamsPrivacy(ctx, tx, id)
		if e != nil {
			return e
		}
		if p.Revision != expected {
			return errTeamsPrivacyStale
		}
		current, e := teamsManualReviewInput(ctx, tx, id)
		if e != nil {
			return e
		}
		if current != input {
			return errTeamsPrivacyStale
		}
		if p.UserPrivate {
			return errors.New("remove the explicit private mark separately before clearing")
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO teams_conversation_privacy(conversation_id,provider,account,native_id,user_private,classifier_state,classifier_input,classifier_reviewed) VALUES(?,?,?,?,0,'clear',?,1) ON CONFLICT DO UPDATE SET provider=excluded.provider,account=excluded.account,native_id=excluded.native_id,classifier_state='clear',classifier_input=excluded.classifier_input,classifier_reviewed=1`, id, p.provider, p.account, p.native, input)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE teams_privacy_revision SET revision=revision+1 WHERE id=1`); e != nil {
			return e
		}
		p.ClassifierState = "clear"
		p.ClassifierInput = input
		p.ClassifierReviewed = true
		p.Revision++
		p.resolve()
		result = p
		return nil
	})
	return result, err
}

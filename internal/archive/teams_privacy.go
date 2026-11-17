package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// teamsPrivacy is local library policy, not an authorization to submit data.
// A private conversation is excluded from org LLM input and content queries;
// the separate query policy decides which statistical operations may include it.
type teamsPrivacy struct {
	provider, account, native                   string
	storedConversationID                        string
	storedProvider, storedAccount, storedNative string
	ConversationID                              string `json:"conversationId"`
	UserPrivate                                 bool   `json:"userPrivate"`
	ClassifierState                             string `json:"classifierState"`
	ClassifierInput                             string `json:"classifierInput,omitempty"`
	ClassifierReviewed                          bool   `json:"classifierReviewed"`
	Private                                     bool   `json:"private"`
	SensitivityHold                             bool   `json:"sensitivityHold"`
	Revision                                    int64  `json:"revision"`
}

func (p *teamsPrivacy) resolve() {
	p.Private = p.UserPrivate || p.ClassifierState == "pending" || (p.ClassifierState == "flagged" && !p.ClassifierReviewed)
	// An incomplete or failed check cannot certify an unattended send. A flagged
	// result requires review even if the underlying conversation was reviewed.
	p.SensitivityHold = p.ClassifierState != "clear" && !(p.ClassifierState == "flagged" && p.ClassifierReviewed)
}

func readTeamsPrivacy(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (teamsPrivacy, error) {
	p := teamsPrivacy{ConversationID: id}
	var matches, mismatches int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN p.conversation_id=c.id AND p.provider<>'' AND (p.provider<>c.provider OR p.account<>c.account OR p.native_id<>c.native_id) THEN 1 ELSE 0 END),0)
 FROM conversations c JOIN teams_conversation_privacy p ON (p.provider=c.provider AND p.account=c.account AND p.native_id=c.native_id) OR p.conversation_id=c.id WHERE c.id=?`, id).Scan(&matches, &mismatches); err != nil {
		return p, err
	}
	if matches > 1 || mismatches > 0 {
		return p, errTeamsSourceUnavailable
	}
	err := q.QueryRowContext(ctx, `SELECT c.provider,c.account,c.native_id,COALESCE(p.conversation_id,c.id),COALESCE(p.provider,c.provider),COALESCE(p.account,c.account),COALESCE(p.native_id,c.native_id),COALESCE(p.user_private,0),COALESCE(p.classifier_state,'unclassified'),COALESCE(p.classifier_input,''),COALESCE(p.classifier_reviewed,0),r.revision
 FROM conversations c CROSS JOIN teams_privacy_revision r LEFT JOIN teams_conversation_privacy p ON (p.provider=c.provider AND p.account=c.account AND p.native_id=c.native_id) OR p.conversation_id=c.id WHERE c.id=? AND r.id=1`, id).Scan(&p.provider, &p.account, &p.native, &p.storedConversationID, &p.storedProvider, &p.storedAccount, &p.storedNative, &p.UserPrivate, &p.ClassifierState, &p.ClassifierInput, &p.ClassifierReviewed, &p.Revision)
	if err != nil {
		return p, err
	}
	p.resolve()
	return p, nil
}

func (c *Catalog) teamsPrivacy(ctx context.Context, id string) (teamsPrivacy, error) {
	return readTeamsPrivacy(ctx, c.reader(), id)
}

// updateTeamsPrivacy runs under the catalog writer; the policy and revision
// change together, so a send can compare its captured revision before release.
var errTeamsPrivacyStale = errors.New("privacy setting changed; review it again")

func (c *Catalog) updateTeamsPrivacy(ctx context.Context, id string, expected *int64, retireExplicit bool, change func(*teamsPrivacy) error) (teamsPrivacy, error) {
	var result teamsPrivacy
	lease, err := c.teamsSharingLease()
	if err != nil {
		return result, err
	}
	defer lease.Close()
	err = c.writeTransaction(ctx, "teams-privacy", func(tx *sql.Tx) error {
		p, err := readTeamsPrivacy(ctx, tx, id)
		if err != nil {
			return err
		}
		if expected != nil && p.Revision != *expected {
			return errTeamsPrivacyStale
		}
		before := p
		if err = change(&p); err != nil {
			return err
		}
		if retireExplicit && !p.UserPrivate {
			retirement := p
			retirement.ConversationID = p.storedConversationID
			retirement.provider, retirement.account, retirement.native = p.storedProvider, p.storedAccount, p.storedNative
			err = teamsRetireExplicitProtection(tx, retirement)
			if err != nil {
				return err
			}
		}
		if p.UserPrivate == before.UserPrivate && p.ClassifierState == before.ClassifierState && p.ClassifierInput == before.ClassifierInput && p.ClassifierReviewed == before.ClassifierReviewed {
			result = p
			return nil
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO teams_conversation_privacy(conversation_id,provider,account,native_id,user_private,classifier_state,classifier_input,classifier_reviewed) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT DO UPDATE SET provider=excluded.provider,account=excluded.account,native_id=excluded.native_id,user_private=excluded.user_private,classifier_state=excluded.classifier_state,classifier_input=excluded.classifier_input,classifier_reviewed=excluded.classifier_reviewed`, id, p.provider, p.account, p.native, p.UserPrivate, p.ClassifierState, p.ClassifierInput, p.ClassifierReviewed)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE teams_privacy_revision SET revision=revision+1 WHERE id=1`); err != nil {
			return err
		}
		p.Revision++
		p.resolve()
		result = p
		return nil
	})
	return result, err
}

func (c *Catalog) setTeamsPrivate(ctx context.Context, id string, private bool, expected ...*int64) (teamsPrivacy, error) {
	var revision *int64
	if len(expected) > 0 {
		revision = expected[0]
	}
	return c.updateTeamsPrivacy(ctx, id, revision, !private, func(p *teamsPrivacy) error { p.UserPrivate = private; return nil })
}

// A new input invalidates a previous review. A late classifier result for an
// older input is rejected rather than replacing the newer pending check.
func (c *Catalog) setTeamsClassification(ctx context.Context, id, input, state string) (teamsPrivacy, error) {
	if input == "" {
		return teamsPrivacy{}, errors.New("classification input is required")
	}
	switch state {
	case "pending", "flagged", "clear", "failed":
	default:
		return teamsPrivacy{}, fmt.Errorf("invalid classifier state")
	}
	return c.updateTeamsPrivacy(ctx, id, nil, false, func(p *teamsPrivacy) error {
		if state != "pending" && (p.ClassifierInput != input || p.ClassifierState != "pending") {
			return errors.New("classification is no longer current")
		}
		if p.ClassifierInput != input || state == "pending" {
			p.ClassifierReviewed = false
		}
		p.ClassifierInput = input
		p.ClassifierState = state
		return nil
	})
}

func (c *Catalog) reviewTeamsClassification(ctx context.Context, id, input string, private bool, expected ...*int64) (teamsPrivacy, error) {
	var revision *int64
	if len(expected) > 0 {
		revision = expected[0]
	}
	return c.updateTeamsPrivacy(ctx, id, revision, false, func(p *teamsPrivacy) error {
		if p.ClassifierState != "flagged" || p.ClassifierInput != input || p.ClassifierReviewed {
			return errTeamsPrivacyStale
		}
		p.ClassifierReviewed = true
		// Reviewing a classifier suggestion may add a private mark, but only
		// the separate explicit mark action may remove one the user made.
		if private {
			p.UserPrivate = true
		}
		return nil
	})
}

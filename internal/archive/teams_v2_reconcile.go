package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

// Reconcile only the exact persisted submission, independently of later
// indexing or registration state. A missing receipt never authorizes a new
// body or key; only the existing gated sender may retry those exact bytes.
func (c *Catalog) reconcileTeamsV2Receipt(ctx context.Context, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential, workID string) (teams.Receipt, error) {
	var empty teams.Receipt
	u, err := readTeamsV2Upload(ctx, c.reader(), workID)
	if err != nil {
		return empty, err
	}
	draft, err := readTeamsV2Draft(ctx, c.reader(), workID)
	if err != nil {
		return empty, err
	}
	if credential == nil || u.LibraryID != d.ID || u.InstanceID != d.Instance || u.OrgID != credential.OrgID || u.InstallationID != credential.InstallationID || (draft.State != "sending" && draft.State != "needs_reconcile" && draft.State != "accepted") || len(draft.Submission) == 0 || teamsPayloadHash(draft.Submission) != draft.SubmissionHash || !teams.ValidUniqueJSON(draft.Submission) {
		return empty, errTeamsConsentStale
	}
	var submitted teams.Submission
	if json.Unmarshal(draft.Submission, &submitted) != nil || submitted.IdempotencyKey != draft.IdempotencyKey || submitted.PayloadSHA256 != draft.PayloadHash || submitted.ConfigSHA256 != u.ConfigHash || submitted.ManifestSHA256 != u.InventoryHash || submitted.InventoryRevision != u.InventoryRevision || submitted.LocalRevision != u.LocalRevision || submitted.EvaluationRevision != u.EvaluationRevision || submitted.SourceBindingID == "" || submitted.AssignmentSHA256 == "" {
		return empty, errTeamsConsentStale
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	receipt, err := client.Receipt(bounded, workID, draft.PayloadHash, draft.SubmissionHash, draft.IdempotencyKey)
	if err != nil {
		return empty, err
	}
	if receipt.WorkID != workID || receipt.InstallationID != u.InstallationID || receipt.PayloadSHA256 != draft.PayloadHash || receipt.SubmissionSHA256 != draft.SubmissionHash || receipt.IdempotencyKey != draft.IdempotencyKey || receipt.SourceBindingID != submitted.SourceBindingID || receipt.AssignmentSHA256 != submitted.AssignmentSHA256 || receipt.InventoryRevision != submitted.InventoryRevision || receipt.CatalogRevision != submitted.CatalogRevision || receipt.ResultRevision < 1 || receipt.AcceptedAt.IsZero() {
		return empty, teams.ErrReceiptMismatch
	}
	switch receipt.PublicationState {
	case "staged":
		if !receipt.ProjectionPending {
			return empty, teams.ErrReceiptMismatch
		}
	case "published", "withheld", "superseded":
	default:
		return empty, teams.ErrReceiptMismatch
	}
	if draft.State != "accepted" {
		return c.recordTeamsV2Receipt(ctx, u, draft, receipt)
	}
	var prior teams.Receipt
	if json.Unmarshal([]byte(draft.ReceiptJSON), &prior) != nil || prior.ID != receipt.ID || prior.ResultRevision != receipt.ResultRevision || !prior.AcceptedAt.Equal(receipt.AcceptedAt) || prior.ProjectionPending != receipt.ProjectionPending {
		return empty, teams.ErrReceiptMismatch
	}
	raw, e := json.Marshal(receipt)
	if e != nil {
		return empty, e
	}
	e = c.writeTransaction(ctx, "teams-v2-refresh-receipt", func(tx *sql.Tx) error {
		current, e := readTeamsV2Draft(ctx, tx, workID)
		if e != nil {
			return e
		}
		if current.State != "accepted" || current.SubmissionHash != draft.SubmissionHash || current.ReceiptJSON != draft.ReceiptJSON {
			return errTeamsConsentStale
		}
		if _, e = tx.ExecContext(ctx, `UPDATE teams_v2_results SET receipt_json=? WHERE work_id=?`, string(raw), workID); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_send_log SET receipt_json=? WHERE work_id=? AND library_id=? AND instance_id=? AND org_id=? AND installation_id=?`, string(raw), workID, u.LibraryID, u.InstanceID, u.OrgID, u.InstallationID)
		return e
	})
	if e != nil {
		return empty, e
	}
	return receipt, nil
}

func teamsReceiptDefinitelyAbsent(err error) bool {
	var response *teams.HTTPError
	return errors.As(err, &response) && response.Status == 404 && response.Code == "receipt_not_found"
}

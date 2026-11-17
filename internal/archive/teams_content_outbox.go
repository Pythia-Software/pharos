package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

type teamsContentRun struct {
	WorkID                                              string
	Config, BindingJSON, SelectedJSON                   []byte
	StartID, StartedAt, State                           string
	Payload                                             []byte
	PayloadHash, ApprovalID, ApprovedAt, IdempotencyKey string
	Submission                                          []byte
	SubmissionHash, ReceiptJSON                         string
}

func readTeamsContentRun(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, workID string) (teamsContentRun, error) {
	var r teamsContentRun
	err := q.QueryRowContext(ctx, `SELECT work_id,config,binding_json,selected_json,start_id,started_at,state,payload,payload_sha256,approval_id,approved_at,idempotency_key,submission,submission_sha256,receipt_json FROM teams_content_runs WHERE work_id=?`, workID).Scan(&r.WorkID, &r.Config, &r.BindingJSON, &r.SelectedJSON, &r.StartID, &r.StartedAt, &r.State, &r.Payload, &r.PayloadHash, &r.ApprovalID, &r.ApprovedAt, &r.IdempotencyKey, &r.Submission, &r.SubmissionHash, &r.ReceiptJSON)
	return r, err
}

func (c *Catalog) approveTeamsContent(ctx context.Context, d LibraryDescriptor, credential *teams.EnrollmentCredential, workID, hash string) error {
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	return c.writeTransaction(ctx, "teams-content-approve", func(tx *sql.Tx) error {
		u, e := readTeamsV2Upload(ctx, tx, workID)
		if e != nil {
			return e
		}
		run, e := readTeamsContentRun(ctx, tx, workID)
		if e != nil {
			return e
		}
		settings, e := readTeamsSettings(ctx, tx)
		if e != nil {
			return e
		}
		var p, s int64
		if e = tx.QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&p); e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&s); e != nil {
			return e
		}
		g, e := readCacheGenerations(ctx, tx)
		if e != nil {
			return e
		}
		if credential == nil || credential.LibraryID != d.ID || u.LibraryID != d.ID || u.InstanceID != d.Instance || u.OrgID != credential.OrgID || u.InstallationID != credential.InstallationID || !settings.Enabled || settings.SettingsRevision != u.SettingsRevision || p != u.PrivacyRevision || s != u.SourceRevision || g.all != u.Generation || run.State != "awaiting_review" || hash == "" || run.PayloadHash != hash || teamsPayloadHash(run.Payload) != hash {
			return errTeamsConsentStale
		}
		start, e := time.Parse(time.RFC3339Nano, run.StartedAt)
		if e != nil {
			return errTeamsConsentStale
		}
		now := time.Now().UTC()
		if now.Before(start) {
			return errTeamsConsentStale
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_content_runs SET state='approved',approval_id=?,approved_at=?,idempotency_key=? WHERE work_id=? AND state='awaiting_review'`, randomUUID(), now.Format(time.RFC3339Nano), randomUUID(), workID)
		return e
	})
}

// Receipt lookup does not read source content and is safe even after authority
// changes. Only an exact accepted envelope is recorded as delivered.
func (c *Catalog) reconcileTeamsContent(ctx context.Context, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential, workID string) (teams.Receipt, error) {
	var empty teams.Receipt
	u, e := readTeamsV2Upload(ctx, c.reader(), workID)
	if e != nil {
		return empty, e
	}
	r, e := readTeamsContentRun(ctx, c.reader(), workID)
	if e != nil {
		return empty, e
	}
	if credential == nil || credential.LibraryID != d.ID || u.LibraryID != d.ID || u.InstanceID != d.Instance || u.OrgID != credential.OrgID || u.InstallationID != credential.InstallationID || (r.State != "sending" && r.State != "needs_reconcile" && r.State != "accepted") || len(r.Submission) == 0 || teamsPayloadHash(r.Submission) != r.SubmissionHash {
		return empty, errTeamsConsentStale
	}
	receipt, e := client.Receipt(ctx, workID, r.PayloadHash, r.SubmissionHash, r.IdempotencyKey)
	if e != nil {
		return empty, e
	}
	if receipt.ID == "" || receipt.ResultRevision < 1 || receipt.AcceptedAt.IsZero() {
		return empty, teams.ErrReceiptMismatch
	}
	if r.State == "accepted" {
		return receipt, nil
	}
	raw, _ := json.Marshal(receipt)
	e = c.writeTransaction(ctx, "teams-content-receipt", func(tx *sql.Tx) error {
		current, e := readTeamsContentRun(ctx, tx, workID)
		if e != nil {
			return e
		}
		if current.SubmissionHash != r.SubmissionHash || current.IdempotencyKey != r.IdempotencyKey || (current.State != "sending" && current.State != "needs_reconcile") {
			return errTeamsConsentStale
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO teams_send_log(work_id,version_id,library_id,instance_id,org_id,installation_id,config,config_hash,payload,payload_hash,submission,receipt_json,sent_at) SELECT work_id,version_id,library_id,instance_id,org_id,installation_id,?,config_sha256,?,?,?,?,? FROM teams_v2_uploads WHERE work_id=?`, r.Config, r.Payload, r.PayloadHash, r.Submission, string(raw), receipt.AcceptedAt.UTC().Format(time.RFC3339Nano), workID); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_content_runs SET state='accepted',receipt_json=? WHERE work_id=?`, string(raw), workID)
		return e
	})
	if e != nil {
		return empty, e
	}
	return receipt, nil
}

func teamsContentReceiptMissing(err error) bool {
	var response *teams.HTTPError
	return errors.As(err, &response) && response.Status == 404 && response.Code == "receipt_not_found"
}

// sendTeamsContent sends a reviewed immutable artifact only after fresh local
// and server authority checks. An uncertain attempt can only retry its exact
// stored bytes and key, after receipt lookup and the same checks.
func (c *Catalog) sendTeamsContent(ctx context.Context, cfg Config, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential, work teams.Work, manifest teams.CapabilityManifest) (teams.Receipt, error) {
	var empty teams.Receipt
	if !teamsBuildEligible(manifest, "source_snapshot_v2") || !teamsBuildEligible(manifest, "scoped_artifact_v1") {
		return empty, errTeamsQueryUnsupported
	}
	run, err := readTeamsContentRun(ctx, c.reader(), work.ID)
	if err != nil {
		return empty, err
	}
	if run.State == "accepted" || run.State == "sending" || run.State == "needs_reconcile" {
		receipt, e := c.reconcileTeamsContent(ctx, client, d, credential, work.ID)
		if e == nil {
			return receipt, nil
		}
		if run.State == "accepted" || !teamsContentReceiptMissing(e) {
			return empty, e
		}
	}
	if run.State != "approved" && run.State != "sending" && run.State != "needs_reconcile" {
		return empty, errTeamsConsentStale
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return empty, err
	}
	defer lease.Close()
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ctx = bounded
	u, err := readTeamsV2Upload(ctx, c.reader(), work.ID)
	if err != nil {
		return empty, err
	}
	if credential == nil || credential.LibraryID != d.ID || u.LibraryID != d.ID || u.InstanceID != d.Instance || u.OrgID != credential.OrgID || u.InstallationID != credential.InstallationID || u.ConfigHash != work.ConfigSHA256 || u.EvaluationRevision != work.EvaluationRevision || !bytes.Equal(run.Config, work.Config) {
		return empty, errTeamsConsentStale
	}
	if _, err = teamsLLMFromWork(work, manifest.AppVersion); err != nil {
		return empty, err
	}
	var b teams.ContentBinding
	var selected []string
	var sourceAuth teams.SourceAuthorization
	if json.Unmarshal(run.BindingJSON, &b) != nil || json.Unmarshal(run.SelectedJSON, &selected) != nil || selected == nil || json.Unmarshal(u.Authorization, &sourceAuth) != nil || sourceAuth.Mode != "reviewed" {
		return empty, errTeamsConsentStale
	}
	inventory, err := c.teamsObserveV2Content(ctx, cfg, d, credential)
	if err != nil {
		return empty, err
	}
	if err = c.teamsRevalidateContent(ctx, cfg, d, credential, inventory, selected); err != nil {
		return empty, err
	}
	if inventory.SourceRevision != u.SourceRevision || inventory.PrivacyRevision != u.PrivacyRevision || inventory.Generation != u.Generation {
		return empty, errTeamsSnapshotChanged
	}
	expected, err := teamsContentClosure(inventory, selected)
	if err != nil {
		return empty, err
	}
	req := teams.ContentBindRequest{BindingID: u.BindingID, SnapshotID: b.SnapshotID, InventoryRevision: b.InventoryRevision, InventorySHA256: b.InventorySHA256, LocalRevision: b.LocalRevision, CatalogRevision: b.CatalogRevision, EvaluationRevision: b.EvaluationRevision, ConfigSHA256: b.ConfigSHA256, PrivacyRevision: b.PrivacyRevision, OwnershipRevision: b.OwnershipRevision, DecisionID: sourceAuth.DecisionID, Processing: b.Processing, Components: selected}
	checked, err := client.BindContent(ctx, work.ID, d.ID, req, expected)
	if err != nil || checked.SHA256 != b.SHA256 {
		return empty, errTeamsSnapshotChanged
	}
	poll, err := client.Poll(ctx, teams.PollRequest{AppVersion: manifest.AppVersion, LibraryIDs: []string{d.ID}, CapabilityManifest: &manifest})
	if err != nil {
		return empty, err
	}
	found := false
	for _, w := range poll.Work {
		if w.ID == work.ID && w.ConfigSHA256 == work.ConfigSHA256 && w.EvaluationRevision == work.EvaluationRevision && bytes.Equal(w.Config, work.Config) {
			found = true
		}
	}
	for _, cancelled := range poll.Cancellations {
		if cancelled.WorkID == work.ID {
			return empty, errTeamsConsentStale
		}
	}
	if !found {
		return empty, errTeamsConsentStale
	}
	err = c.writeTransaction(ctx, "teams-content-send", func(tx *sql.Tx) error {
		current, e := readTeamsContentRun(ctx, tx, work.ID)
		if e != nil {
			return e
		}
		s, e := readTeamsSettings(ctx, tx)
		if e != nil {
			return e
		}
		if !s.Enabled || s.SettingsRevision != u.SettingsRevision || current.PayloadHash != run.PayloadHash || teamsPayloadHash(current.Payload) != current.PayloadHash || current.ApprovalID == "" || current.StartID == "" {
			return errTeamsConsentStale
		}
		if current.State == "approved" {
			at, e := time.Parse(time.RFC3339Nano, current.ApprovedAt)
			if e != nil {
				return errTeamsConsentStale
			}
			submission := teams.ArtifactSubmission{IdempotencyKey: current.IdempotencyKey, ContentBindingID: u.BindingID, ContentStartID: current.StartID, SourceDecisionID: sourceAuth.DecisionID, ContentBindingSHA256: b.SHA256, ConfigSHA256: u.ConfigHash, EvaluationRevision: u.EvaluationRevision, OwnershipRevision: b.OwnershipRevision, Approval: teams.Approval{Mode: "reviewed", DecisionID: current.ApprovalID, DecidedAt: at, PrivacyRevision: u.PrivacyRevision, SharedPrivacyRevision: b.PrivacyRevision, SettingsRevision: u.SettingsRevision}, PayloadSHA256: current.PayloadHash, Payload: current.Payload}
			raw, e := json.Marshal(submission)
			if e != nil {
				return e
			}
			current.Submission = raw
			current.SubmissionHash = teamsPayloadHash(raw)
			if _, e = tx.ExecContext(ctx, `UPDATE teams_content_runs SET submission=?,submission_sha256=?,state='sending' WHERE work_id=? AND state='approved'`, raw, current.SubmissionHash, work.ID); e != nil {
				return e
			}
		} else if (current.State != "sending" && current.State != "needs_reconcile") || len(current.Submission) == 0 || teamsPayloadHash(current.Submission) != current.SubmissionHash || current.SubmissionHash != run.SubmissionHash {
			return errTeamsConsentStale
		}
		run = current
		return nil
	})
	if err != nil {
		return empty, err
	}
	if _, err = client.SubmitArtifact(ctx, work.ID, run.Submission, run.PayloadHash); err != nil {
		state, _ := teamsSubmitFailure(err)
		_ = c.writeTransaction(context.Background(), "teams-content-outcome", func(tx *sql.Tx) error {
			_, e := tx.Exec(`UPDATE teams_content_runs SET state=? WHERE work_id=? AND state IN ('sending','needs_reconcile')`, state, work.ID)
			return e
		})
		return empty, err
	}
	return c.reconcileTeamsContent(ctx, client, d, credential, work.ID)
}

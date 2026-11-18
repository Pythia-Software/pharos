package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

func teamsV2ReceiptMatches(receipt teams.Receipt, u teamsV2Upload, b teams.SourceBinding, draft teamsV2Draft) bool {
	if receipt.WorkID != u.WorkID || receipt.InstallationID != u.InstallationID || receipt.PayloadSHA256 != draft.PayloadHash || receipt.SubmissionSHA256 != draft.SubmissionHash || receipt.IdempotencyKey != draft.IdempotencyKey || receipt.SourceBindingID != b.BindingID || receipt.AssignmentSHA256 != b.AssignmentSHA256 || receipt.InventoryRevision != u.InventoryRevision || receipt.CatalogRevision != b.CatalogRevision || receipt.ResultRevision < 1 || receipt.AcceptedAt.IsZero() {
		return false
	}
	switch receipt.PublicationState {
	case "staged":
		return receipt.ProjectionPending
	case "published", "withheld", "superseded":
		return true
	default:
		return false
	}
}

// sendTeamsV2Approved revalidates work, source assignments and local consent
// under the sharing gate. A lost response is reconciled by receipt first; any
// retry reuses the exact persisted body and idempotency key.
func (c *Catalog) sendTeamsV2Approved(ctx context.Context, cfg Config, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential, workID string, manifest teams.CapabilityManifest) (teams.Receipt, error) {
	var empty teams.Receipt
	if client == nil || credential == nil || !teamsNumericBuildEligible(manifest) {
		return empty, errTeamsConsentStale
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return empty, err
	}
	defer lease.Close()
	declinedErr := teamsNumericNotDeclined(ctx, c.reader(), workID)
	if declinedErr != nil && !errors.Is(declinedErr, errTeamsConsentStale) {
		return empty, declinedErr
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u, err := readTeamsV2Upload(ctx, c.reader(), workID)
	if err != nil {
		return empty, err
	}
	draft, err := readTeamsV2Draft(ctx, c.reader(), workID)
	if err != nil {
		return empty, err
	}
	if u.LibraryID != d.ID || u.InstanceID != d.Instance || u.OrgID != credential.OrgID || u.InstallationID != credential.InstallationID {
		return empty, errTeamsConsentStale
	}
	if draft.State == "accepted" || draft.State == "sending" || draft.State == "needs_reconcile" {
		receipt, e := c.reconcileTeamsV2Receipt(ctx, client, d, credential, workID)
		if e == nil {
			return receipt, nil
		}
		if errors.Is(e, teams.ErrReceiptMismatch) {
			c.markTeamsV2Outcome(workID, "unconfirmed")
			return empty, e
		}
		if declinedErr != nil {
			return empty, declinedErr
		}
		if draft.State == "accepted" || !teamsReceiptDefinitelyAbsent(e) {
			return empty, e
		}
	} else if declinedErr != nil || draft.State != "approved" {
		return empty, errTeamsConsentStale
	}
	var savedBinding teams.SourceBinding
	var savedAssignments []sourcewire.Assignment
	if json.Unmarshal(u.BindingJSON, &savedBinding) != nil || json.Unmarshal(u.AssignmentsJSON, &savedAssignments) != nil || savedAssignments == nil || u.State != "bound" {
		return empty, errTeamsConsentStale
	}
	if !teamsSourceConfigCurrent(cfg) {
		return empty, errTeamsSnapshotChanged
	}
	snapshot, err := c.teamsObserveV2Codex(ctx, cfg, d, credential)
	if err != nil {
		return empty, err
	}
	poll, err := client.Poll(ctx, teams.PollRequest{AppVersion: manifest.AppVersion, LibraryIDs: []string{d.ID}, CapabilityManifest: &manifest})
	if err != nil {
		return empty, err
	}
	for _, cancelled := range poll.Cancellations {
		if cancelled.WorkID == workID {
			return empty, errTeamsConsentStale
		}
	}
	var work *teams.Work
	for i := range poll.Work {
		if poll.Work[i].ID == workID {
			if work != nil {
				return empty, errTeamsConsentStale
			}
			work = &poll.Work[i]
		}
	}
	if work == nil || work.LibraryID != d.ID || work.VersionID != u.VersionID || work.ConfigSHA256 != u.ConfigHash || !bytes.Equal(work.Config, draft.Config) || work.EvaluationRevision != u.EvaluationRevision || work.RequiredLocalRevision > u.LocalRevision || work.BaselineRevision < 0 {
		return empty, errTeamsConsentStale
	}
	if !teamsNumericWorkEligible(*work, manifest) || !u.matchesSourceProof(work.ScheduledAssignment) {
		return empty, errTeamsQueryUnsupported
	}
	plan, err := teamsV2PlanFromWork(*work)
	if err != nil || len(plan.Metrics) != 1 || plan.Metrics[0].Field != "total_tokens" {
		return empty, errTeamsQueryUnsupported
	}
	watermark, err := client.SourceWatermark(ctx, d.ID)
	if err != nil {
		return empty, err
	}
	if watermark.AcceptedInventoryRevision != u.InventoryRevision || watermark.AcceptedInventorySHA256 != u.InventoryHash || watermark.LocalRevision > u.LocalRevision {
		return empty, errTeamsSnapshotChanged
	}
	binding, err := client.BindSourceInventory(ctx, workID, savedBinding.SourceBindRequest)
	if err != nil {
		return empty, err
	}
	if binding.BindingState != "ready" || binding.AssignmentSHA256 != savedBinding.AssignmentSHA256 {
		return empty, errTeamsSnapshotChanged
	}
	expected := make([]string, len(snapshot.Items))
	for i, item := range snapshot.Items {
		expected[i] = item.Token
	}
	assignments, err := client.SourceAssignments(ctx, workID, binding, expected)
	if err != nil {
		return empty, err
	}
	assignmentRaw, _ := json.Marshal(assignments)
	if !bytes.Equal(assignmentRaw, u.AssignmentsJSON) {
		return empty, errTeamsSnapshotChanged
	}
	total, caveats, err := teamsV2Total(snapshot, assignments)
	if err != nil {
		return empty, err
	}
	var p teams.Payload
	if !teams.ValidUniqueJSON(draft.Payload) || teamsPayloadHash(draft.Payload) != draft.PayloadHash || json.Unmarshal(draft.Payload, &p) != nil || p.Outcome != "completed" || p.Form != "snapshot" || p.Coverage.SourceState != "partial" || p.Coverage.Measurement != "measured" || len(p.Rows) != 1 || len(p.Rows[0].Key) != 0 || len(p.Rows[0].Values) != 1 || string(p.Rows[0].Values[plan.Metrics[0].ID]) != string(mustTeamsInteger(total)) || !equalTeamsStrings(p.Coverage.Caveats, caveats) {
		return empty, errTeamsConsentStale
	}
	seal, err := sourcewire.DecodeSeal(u.Seal)
	if err != nil {
		return empty, errTeamsConsentStale
	}
	pages, err := teamsBuildV2Pages(snapshot, u.SnapshotID, u.LocalRevision, u.InventoryRevision, u.EvaluationRevision, seal.ExpectedPriorSHA256)
	if err != nil || pages.InventorySHA256 != u.InventoryHash || !bytes.Equal(pages.Seal, u.Seal) {
		return empty, errTeamsSnapshotChanged
	}
	err = c.writeTransaction(ctx, "teams-v2-reserve-send", func(tx *sql.Tx) error {
		settings, e := readTeamsSettings(ctx, tx)
		if e != nil {
			return e
		}
		var sourceRev, privacyRev int64
		if e = tx.QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&sourceRev); e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&privacyRev); e != nil {
			return e
		}
		g, e := readCacheGenerations(ctx, tx)
		if e != nil {
			return e
		}
		current, e := readTeamsV2Draft(ctx, tx, workID)
		if e != nil {
			return e
		}
		if !settings.Enabled || settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.SettingsRevision != u.SettingsRevision || settings.OrgID != u.OrgID || settings.InstallationID != u.InstallationID || sourceRev != u.SourceRevision || sourceRev != snapshot.SourceRevision || privacyRev != u.PrivacyRevision || privacyRev != snapshot.PrivacyRevision || g.all != u.Generation || g.all != snapshot.Generation || current.PayloadHash != draft.PayloadHash || current.AssignmentHash != binding.AssignmentSHA256 {
			return errTeamsConsentStale
		}
		if u.Mode == "automatic_metrics" && (!poll.MetricsDefault.Value || settings.MetricsOptOut || settings.AcknowledgedPolicy != u.PolicyVersion || poll.MetricsDefault.PolicyVersion != u.PolicyVersion) {
			return errTeamsConsentStale
		}
		if current.State == "approved" {
			at, e := time.Parse(time.RFC3339Nano, current.ApprovedAt)
			if e != nil {
				return errTeamsConsentStale
			}
			if u.Mode == "reviewed" && current.DecisionID == "" {
				return errTeamsConsentStale
			}
			submission := teams.Submission{ScheduledAssignment: work.ScheduledAssignment, IdempotencyKey: current.IdempotencyKey, ConfigSHA256: u.ConfigHash, EvaluationRevision: u.EvaluationRevision, LocalRevision: u.LocalRevision, ManifestSHA256: u.InventoryHash, SourceBindingID: binding.BindingID, AssignmentSHA256: binding.AssignmentSHA256, InventoryRevision: u.InventoryRevision, CatalogRevision: binding.CatalogRevision, OwnershipRevision: binding.OwnershipRevision, BaselineRevision: work.BaselineRevision, PayloadSHA256: current.PayloadHash, Approval: teams.Approval{Mode: u.Mode, DecisionID: current.DecisionID, DecidedAt: at, PrivacyRevision: u.PrivacyRevision, SharedPrivacyRevision: binding.PrivacyRevision, SettingsRevision: u.SettingsRevision, OrgPolicyVersion: u.PolicyVersion}, Payload: current.Payload}
			body, e := json.Marshal(submission)
			if e != nil {
				return e
			}
			current.Submission = body
			current.SubmissionHash = teamsPayloadHash(body)
			if _, e = tx.ExecContext(ctx, `UPDATE teams_v2_results SET submission=?,submission_sha256=?,state='sending' WHERE work_id=? AND state='approved'`, body, current.SubmissionHash, workID); e != nil {
				return e
			}
		} else if current.State == "sending" || current.State == "needs_reconcile" {
			var submitted teams.Submission
			if len(current.Submission) == 0 || teamsPayloadHash(current.Submission) != current.SubmissionHash || current.SubmissionHash != draft.SubmissionHash || !teams.ValidUniqueJSON(current.Submission) || json.Unmarshal(current.Submission, &submitted) != nil || !sameTeamsScheduledAssignment(submitted.ScheduledAssignment, work.ScheduledAssignment) || submitted.BaselineRevision != work.BaselineRevision || submitted.EvaluationRevision != u.EvaluationRevision || submitted.LocalRevision != u.LocalRevision || submitted.CatalogRevision != binding.CatalogRevision || submitted.SourceBindingID != binding.BindingID || submitted.AssignmentSHA256 != binding.AssignmentSHA256 || submitted.ConfigSHA256 != u.ConfigHash || submitted.PayloadSHA256 != current.PayloadHash || submitted.IdempotencyKey != current.IdempotencyKey {
				return errTeamsConsentStale
			}
			if work.ScheduledAssignment != nil {
				at, e := time.Parse(time.RFC3339Nano, current.ApprovedAt)
				a := submitted.Approval
				if e != nil || a.Mode != u.Mode || a.OrgPolicyVersion != u.PolicyVersion || a.SettingsRevision != u.SettingsRevision ||
					a.DecisionID != current.DecisionID || !a.DecidedAt.Equal(at) || a.PrivacyRevision != u.PrivacyRevision || a.SharedPrivacyRevision != binding.PrivacyRevision {
					return errTeamsConsentStale
				}
			}
		} else {
			return errTeamsConsentStale
		}
		draft = current
		return nil
	})
	if err != nil {
		return empty, err
	}
	receipt, err := client.Submit(ctx, workID, draft.Submission, draft.PayloadHash)
	if err != nil {
		state, _ := teamsSubmitFailure(err)
		c.markTeamsV2Outcome(workID, state)
		return empty, err
	}
	if !teamsV2ReceiptMatches(receipt, u, binding, draft) {
		c.markTeamsV2Outcome(workID, "unconfirmed")
		return empty, teams.ErrReceiptMismatch
	}
	return c.recordTeamsV2Receipt(ctx, u, draft, receipt)
}

func (c *Catalog) markTeamsV2Outcome(workID, state string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.writeTransaction(ctx, "teams-v2-send-outcome", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE teams_v2_results SET state=? WHERE work_id=? AND state IN ('sending','needs_reconcile')`, state, workID)
		return err
	})
}

func equalTeamsStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (c *Catalog) recordTeamsV2Receipt(ctx context.Context, u teamsV2Upload, draft teamsV2Draft, receipt teams.Receipt) (teams.Receipt, error) {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return teams.Receipt{}, err
	}
	err = c.writeTransaction(ctx, "teams-v2-receipt", func(tx *sql.Tx) error {
		current, e := readTeamsV2Draft(ctx, tx, u.WorkID)
		if e != nil {
			return e
		}
		if current.IdempotencyKey != draft.IdempotencyKey || current.SubmissionHash != draft.SubmissionHash || teamsPayloadHash(current.Submission) != current.SubmissionHash {
			return errTeamsConsentStale
		}
		if current.State == "accepted" {
			return nil
		}
		if current.State != "sending" && current.State != "needs_reconcile" {
			return errTeamsConsentStale
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO teams_send_log(work_id,version_id,library_id,instance_id,org_id,installation_id,config,config_hash,payload,payload_hash,submission,receipt_json,sent_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, u.WorkID, u.VersionID, u.LibraryID, u.InstanceID, u.OrgID, u.InstallationID, current.Config, u.ConfigHash, current.Payload, current.PayloadHash, current.Submission, string(raw), receipt.AcceptedAt.UTC().Format(time.RFC3339Nano)); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_v2_results SET state='accepted',receipt_json=? WHERE work_id=?`, string(raw), u.WorkID)
		return e
	})
	if err != nil {
		return teams.Receipt{}, err
	}
	return receipt, nil
}

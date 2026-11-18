package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

type teamsV2Draft struct {
	WorkID, PayloadHash, AssignmentHash, IdempotencyKey, State, ApprovedAt, DecisionID, SubmissionHash, ReceiptJSON string
	Config, Payload, Submission                                                                                     []byte
}

func readTeamsV2Draft(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, workID string) (teamsV2Draft, error) {
	var d teamsV2Draft
	err := q.QueryRowContext(ctx, `SELECT work_id,config,payload,payload_sha256,assignment_sha256,idempotency_key,state,approved_at,decision_id,submission,submission_sha256,receipt_json FROM teams_v2_results WHERE work_id=?`, workID).Scan(&d.WorkID, &d.Config, &d.Payload, &d.PayloadHash, &d.AssignmentHash, &d.IdempotencyKey, &d.State, &d.ApprovedAt, &d.DecisionID, &d.Submission, &d.SubmissionHash, &d.ReceiptJSON)
	return d, err
}

func teamsV2Total(snapshot teamsV2Inventory, assignments []sourcewire.Assignment) (int64, []string, error) {
	var total int64
	owned := 0
	caveats := []string{"incomplete_inventory"}
	for _, assignment := range assignments {
		if assignment.Decision != "owned" {
			continue
		}
		value, ok := snapshot.values[assignment.Token]
		if !ok || value < 0 || total > 9007199254740991-value {
			return 0, nil, errTeamsSourceUnavailable
		}
		total += value
		owned++
	}
	if owned == 0 {
		return 0, nil, errTeamsSourceUnavailable
	}
	if snapshot.Coverage.IdentityAmbiguous > 0 {
		caveats = append(caveats, "identity_ambiguous")
	}
	if snapshot.Coverage.OverlapUnresolved > 0 {
		caveats = append(caveats, "overlap_unresolved")
	}
	for _, assignment := range assignments {
		if assignment.Decision == "unavailable" {
			caveats = append(caveats, "unavailable_source")
			break
		}
	}
	return total, caveats, nil
}

// stageTeamsV2Result re-observes the authorized numeric subset, then compares
// its canonical inventory and local revisions under the sharing gate. It
// stores a single immutable draft; it does not send or grant content access.
func (c *Catalog) stageTeamsV2Result(ctx context.Context, cfg Config, d LibraryDescriptor, credential *teams.EnrollmentCredential, registration teamsV2Registration) (teamsV2Draft, error) {
	var out teamsV2Draft
	current, err := c.teamsObserveV2Codex(ctx, cfg, d, credential)
	if err != nil {
		return out, err
	}
	total, caveats, err := teamsV2Total(current, registration.Assignments)
	if err != nil {
		return out, err
	}
	plan, err := teamsV2PlanFromWork(registration.Work)
	if err != nil || len(plan.Metrics) != 1 || plan.Metrics[0].Field != "total_tokens" {
		return out, errTeamsQueryUnsupported
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return out, err
	}
	defer lease.Close()
	err = c.writeTransaction(ctx, "teams-v2-stage-result", func(tx *sql.Tx) error {
		if e := teamsNumericNotDeclined(ctx, tx, registration.Work.ID); e != nil {
			return e
		}
		u, e := readTeamsV2Upload(ctx, tx, registration.Work.ID)
		if e != nil {
			return e
		}
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
		bindingRaw, _ := json.Marshal(registration.Binding)
		assignmentsRaw, _ := json.Marshal(registration.Assignments)
		if !u.matchesSourceProof(registration.Work.ScheduledAssignment) || u.State != "bound" || u.LibraryID != d.ID || u.InstanceID != d.Instance || u.OrgID != credential.OrgID || u.InstallationID != credential.InstallationID || u.ConfigHash != registration.Work.ConfigSHA256 || !bytes.Equal(u.BindingJSON, bindingRaw) || !bytes.Equal(u.AssignmentsJSON, assignmentsRaw) || !settings.Enabled || settings.SettingsRevision != u.SettingsRevision || sourceRev != current.SourceRevision || sourceRev != u.SourceRevision || privacyRev != current.PrivacyRevision || privacyRev != u.PrivacyRevision || g.all != current.Generation || g.all != u.Generation {
			return errTeamsSnapshotChanged
		}
		seal, e := sourcewire.DecodeSeal(u.Seal)
		if e != nil {
			return errTeamsConsentStale
		}
		pages, e := teamsBuildV2Pages(current, u.SnapshotID, u.LocalRevision, u.InventoryRevision, u.EvaluationRevision, seal.ExpectedPriorSHA256)
		if e != nil || pages.InventorySHA256 != u.InventoryHash || !bytes.Equal(pages.Seal, u.Seal) {
			return errTeamsSnapshotChanged
		}
		old, e := readTeamsV2Draft(ctx, tx, u.WorkID)
		if e == nil {
			var payload teams.Payload
			if old.AssignmentHash != registration.Binding.AssignmentSHA256 || teamsPayloadHash(old.Payload) != old.PayloadHash || !teams.ValidUniqueJSON(old.Payload) || !bytes.Equal(old.Config, registration.Work.Config) || json.Unmarshal(old.Payload, &payload) != nil || payload.Outcome != "completed" || payload.Form != "snapshot" || payload.ObservedAt.IsZero() || payload.ComputedAt.IsZero() || len(payload.Rows) != 1 || len(payload.Rows[0].Key) != 0 || len(payload.Rows[0].Values) != 1 || string(payload.Rows[0].Values[plan.Metrics[0].ID]) != string(mustTeamsInteger(total)) || payload.Coverage.SourceState != "partial" || payload.Coverage.Measurement != "measured" || !slices.Equal(payload.Coverage.Caveats, caveats) || payload.DeleteKeys == nil || len(payload.DeleteKeys) != 0 {
				return errTeamsConsentStale
			}
			out = old
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		now := time.Now().UTC()
		payload := teams.Payload{Outcome: "completed", Form: "snapshot", ObservedAt: current.ObservedAt, ComputedAt: now, Coverage: teams.Coverage{SourceState: "partial", Measurement: "measured", Caveats: caveats}, Rows: []teams.Row{{Key: []json.RawMessage{}, Values: map[string]json.RawMessage{plan.Metrics[0].ID: mustTeamsInteger(total)}}}, DeleteKeys: [][]json.RawMessage{}}
		data, e := json.Marshal(payload)
		if e != nil {
			return e
		}
		hash := teamsPayloadHash(data)
		state := "awaiting_review"
		approved := ""
		if u.Mode == "automatic_metrics" {
			state = "approved"
			approved = now.Format(time.RFC3339Nano)
		}
		idempotency := randomUUID()
		_, e = tx.ExecContext(ctx, `INSERT INTO teams_v2_results(work_id,config,payload,payload_sha256,assignment_sha256,idempotency_key,state,approved_at) VALUES(?,?,?,?,?,?,?,?)`, u.WorkID, registration.Work.Config, data, hash, registration.Binding.AssignmentSHA256, idempotency, state, approved)
		if e != nil {
			return e
		}
		out = teamsV2Draft{WorkID: u.WorkID, Config: bytes.Clone(registration.Work.Config), Payload: data, PayloadHash: hash, AssignmentHash: registration.Binding.AssignmentSHA256, IdempotencyKey: idempotency, State: state, ApprovedAt: approved}
		return nil
	})
	return out, err
}

func mustTeamsInteger(n int64) json.RawMessage { b, _ := json.Marshal(n); return b }

// reviewTeamsV2Draft approves exactly the local payload shown, not future
// computations or a standing permission. A fresh send still rechecks gates.
func (c *Catalog) reviewTeamsV2Draft(ctx context.Context, d LibraryDescriptor, workID, payloadHash string) error {
	return c.reviewTeamsV2DraftWithConfig(ctx, d, workID, payloadHash, "")
}
func (c *Catalog) reviewTeamsV2DraftWithConfig(ctx context.Context, d LibraryDescriptor, workID, payloadHash, configHash string) error {
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	return c.writeTransaction(ctx, "teams-v2-review", func(tx *sql.Tx) error {
		u, e := readTeamsV2Upload(ctx, tx, workID)
		if e != nil {
			return e
		}
		draft, e := readTeamsV2Draft(ctx, tx, workID)
		if e != nil {
			return e
		}
		s, e := readTeamsSettings(ctx, tx)
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
		if u.State != "bound" || !validTeamsSeriesReview(draft.Config, u.ConfigHash, configHash) {
			return errTeamsConsentStale
		}
		if u.LibraryID != d.ID || u.InstanceID != d.Instance || !s.Enabled || s.SettingsRevision != u.SettingsRevision || s.OrgID != u.OrgID || s.InstallationID != u.InstallationID || sourceRev != u.SourceRevision || privacyRev != u.PrivacyRevision || g.all != u.Generation || draft.State != "awaiting_review" || draft.PayloadHash != payloadHash || teamsPayloadHash(draft.Payload) != payloadHash {
			return errTeamsConsentStale
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_v2_results SET state='approved',approved_at=?,decision_id=? WHERE work_id=? AND state='awaiting_review'`, time.Now().UTC().Format(time.RFC3339Nano), randomUUID(), workID)
		return e
	})
}

// A series approval must match both the displayed config and the stored
// source binding, in addition to the existing exact payload review.
func validTeamsSeriesReview(config []byte, boundHash, displayedHash string) bool {
	if !teams.ValidUniqueJSON(config) {
		return false
	}
	var arms map[string]json.RawMessage
	if json.Unmarshal(config, &arms) != nil || arms == nil {
		return false
	}
	for key := range arms {
		if strings.EqualFold(key, "publication_v2") && key != "publication_v2" {
			return false
		}
	}
	raw, series := arms["publication_v2"]
	if series && !teamsConfigExactKeys(config) {
		_, err := parseTeamsScheduledConfig(config)
		return err == nil && displayedHash != "" && displayedHash == boundHash && teamsPayloadHash(config) == boundHash
	}
	if displayedHash != "" && (displayedHash != boundHash || teamsPayloadHash(config) != displayedHash) {
		return false
	}
	if !series {
		return true
	}
	if displayedHash == "" {
		return false
	}
	_, err := parseTeamsReportSeries(raw)
	return err == nil
}

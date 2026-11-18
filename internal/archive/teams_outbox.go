package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

var teamsVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

var errTeamsConsentStale = errors.New("Teams consent or library state changed; review again")
var errTeamsNotEnrolled = errors.New("Teams enrollment needs reconciliation")

type teamsLocalSettings struct {
	LibraryID, InstanceID, OrgID, InstallationID, KeyFingerprint                                     string
	Enabled, MetricsOptOut                                                                           bool
	AcknowledgedPolicy, SettingsRevision, LocalRevision, ObservedGeneration, ObservedPrivacyRevision int64
}

func readTeamsSettings(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (teamsLocalSettings, error) {
	var s teamsLocalSettings
	err := q.QueryRowContext(ctx, `SELECT library_id,instance_id,org_id,installation_id,key_fingerprint,enabled,metrics_opt_out,acknowledged_policy,settings_revision,local_revision,observed_generation,observed_privacy_revision FROM teams_local_settings WHERE id=1`).Scan(&s.LibraryID, &s.InstanceID, &s.OrgID, &s.InstallationID, &s.KeyFingerprint, &s.Enabled, &s.MetricsOptOut, &s.AcknowledgedPolicy, &s.SettingsRevision, &s.LocalRevision, &s.ObservedGeneration, &s.ObservedPrivacyRevision)
	return s, err
}

// setTeamsLocalSettings is called only after enrollment has durably stored the
// installation credential and checked the descriptor. Recovery does not call
// this: it must reconcile identity and receipts first.
func (c *Catalog) setTeamsLocalSettings(ctx context.Context, libraryID, instanceID, orgID, installationID, keyFingerprint string, enabled, optOut bool, policy int64) (teamsLocalSettings, error) {
	var out teamsLocalSettings
	lease, err := c.teamsSharingLease()
	if err != nil {
		return out, err
	}
	defer lease.Close()
	fingerprintBytes, fingerprintErr := hex.DecodeString(keyFingerprint)
	if libraryID == "" || instanceID == "" || orgID == "" || fingerprintErr != nil || len(fingerprintBytes) != sha256.Size || hex.EncodeToString(fingerprintBytes) != keyFingerprint || policy < 0 {
		return out, errTeamsNotEnrolled
	}
	if _, err := teams.WorkPath(installationID, "status"); err != nil {
		return out, errTeamsNotEnrolled
	}
	err = c.writeTransaction(ctx, "teams-settings", func(tx *sql.Tx) error {
		s, e := readTeamsSettings(ctx, tx)
		if e != nil {
			return e
		}
		if s.LibraryID != "" && (s.LibraryID != libraryID || s.InstanceID != instanceID || s.OrgID != orgID || s.InstallationID != installationID || s.KeyFingerprint != keyFingerprint) {
			return errTeamsNotEnrolled
		}
		if s.LibraryID == libraryID && s.InstanceID == instanceID && s.OrgID == orgID && s.InstallationID == installationID && s.KeyFingerprint == keyFingerprint && s.Enabled == enabled && s.MetricsOptOut == optOut && s.AcknowledgedPolicy == policy {
			out = s
			return nil
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_local_settings SET library_id=?,instance_id=?,org_id=?,installation_id=?,key_fingerprint=?,enabled=?,metrics_opt_out=?,acknowledged_policy=?,settings_revision=settings_revision+1 WHERE id=1`, libraryID, instanceID, orgID, installationID, keyFingerprint, enabled, optOut, policy)
		if e != nil {
			return e
		}
		out, e = readTeamsSettings(ctx, tx)
		return e
	})
	return out, err
}

func teamsRevisionsInTx(ctx context.Context, tx *sql.Tx, expected teamsSourceSnapshot) (teamsLocalSettings, error) {
	s, err := readTeamsSettings(ctx, tx)
	if err != nil {
		return s, err
	}
	if !s.Enabled || s.LibraryID == "" || s.InstanceID == "" || s.OrgID == "" || s.InstallationID == "" || s.KeyFingerprint == "" {
		return s, errTeamsNotEnrolled
	}
	g, err := readCacheGenerations(ctx, tx)
	if err != nil {
		return s, err
	}
	var privacy int64
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&privacy); err != nil {
		return s, err
	}
	if g.all != expected.Generation || privacy != expected.PrivacyRevision {
		return s, errTeamsSnapshotChanged
	}
	if s.ObservedGeneration != g.all || s.ObservedPrivacyRevision != privacy {
		_, err = tx.ExecContext(ctx, `UPDATE teams_local_settings SET local_revision=local_revision+1,observed_generation=?,observed_privacy_revision=? WHERE id=1`, g.all, privacy)
		if err != nil {
			return s, err
		}
		s, err = readTeamsSettings(ctx, tx)
	}
	return s, err
}

func teamsPayloadHash(payload []byte) string {
	v := sha256.Sum256(payload)
	return hex.EncodeToString(v[:])
}

type teamsStaged struct {
	WorkID, VersionID, LibraryID, InstanceID, OrgID, InstallationID, ConfigHash, PayloadHash, IdempotencyKey, State string
	Payload, Config                                                                                                 []byte
	ExpiresAt                                                                                                       time.Time
	LocalRevision, Generation, PrivacyRevision, SettingsRevision, SharedPrivacyRevision, OwnershipRevision          int64
	ApprovedAt                                                                                                      string
	EvaluationRevision, BaselineRevision                                                                            int64
	ManifestHash, OwnedHash, ApprovalMode                                                                           string
	Manifest                                                                                                        []byte
	PolicyVersion                                                                                                   int64
	Submission                                                                                                      []byte
	SubmissionHash                                                                                                  string
}

func readTeamsOutbox(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, workID string) (teamsStaged, error) {
	var v teamsStaged
	var expiry string
	err := q.QueryRowContext(ctx, `SELECT work_id,version_id,library_id,instance_id,org_id,installation_id,config,config_hash,payload,payload_hash,expires_at,idempotency_key,state,local_revision,generation,privacy_revision,settings_revision,shared_privacy_revision,ownership_revision,evaluation_revision,baseline_revision,manifest,manifest_hash,owned_hash,approval_mode,policy_version,approved_at,submission,submission_hash FROM teams_outbox WHERE work_id=?`, workID).Scan(&v.WorkID, &v.VersionID, &v.LibraryID, &v.InstanceID, &v.OrgID, &v.InstallationID, &v.Config, &v.ConfigHash, &v.Payload, &v.PayloadHash, &expiry, &v.IdempotencyKey, &v.State, &v.LocalRevision, &v.Generation, &v.PrivacyRevision, &v.SettingsRevision, &v.SharedPrivacyRevision, &v.OwnershipRevision, &v.EvaluationRevision, &v.BaselineRevision, &v.Manifest, &v.ManifestHash, &v.OwnedHash, &v.ApprovalMode, &v.PolicyVersion, &v.ApprovedAt, &v.Submission, &v.SubmissionHash)
	if err != nil {
		return v, err
	}
	v.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiry)
	return v, err
}

// validateTeamsNumericPayload ensures the payload shown for approval is the
// exact bounded numeric calculation, including its coverage labels. It does
// not grant permission to send it.
func validateTeamsNumericPayload(plan teamsQueryPlan, result teamsNumericResult, raw []byte) error {
	if !teams.ValidUniqueJSON(raw) || !teamsPayloadExactKeys(raw, plan) {
		return errors.New("invalid Teams payload")
	}
	var p teams.Payload
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil {
		return errors.New("invalid Teams payload")
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("invalid Teams payload")
	}
	measurement := "unknown"
	switch {
	case result.Estimated && result.Measured:
		measurement = "mixed"
	case result.Estimated:
		measurement = "estimated"
	case result.Measured:
		measurement = "measured"
	}
	if p.Outcome != "completed" || p.Form != "snapshot" || p.ObservedAt.IsZero() || p.ComputedAt.IsZero() || p.Coverage.SourceState != "complete" || p.Coverage.Measurement != measurement || p.Coverage.Caveats == nil || len(result.Caveats) != 0 || len(p.Coverage.Caveats) != len(result.Caveats) || len(p.Rows) != 1 || p.Rows[0].Key == nil || len(p.Rows[0].Key) != 0 || p.DeleteKeys == nil || len(p.DeleteKeys) != 0 || len(p.Rows[0].Values) != len(plan.Metrics) || len(result.Values) != len(plan.Metrics) {
		return errors.New("Teams payload does not match calculation")
	}
	for _, m := range plan.Metrics {
		b, ok := p.Rows[0].Values[m.ID]
		var value int64
		expected, found := result.Values[m.ID]
		if !ok || !found || bytes.Equal(bytes.TrimSpace(b), []byte("null")) || json.Unmarshal(b, &value) != nil || expected != value {
			return errors.New("Teams payload does not match calculation")
		}
	}
	return nil
}

func teamsPlanFromWork(w teams.Work) (teamsQueryPlan, error) {
	if w.ScheduledAssignment != nil || w.VerifyConfig() != nil || w.Kind != "deterministic" || w.PopulationKey != "current" || w.ExpiresAt.IsZero() || !time.Now().Before(w.ExpiresAt) || (len(w.RunDecision) != 0 && !bytes.Equal(bytes.TrimSpace(w.RunDecision), []byte("null"))) {
		return teamsQueryPlan{}, errTeamsQueryUnsupported
	}
	var config struct {
		Schedule       *teamsSchedule  `json:"schedule"`
		PublicationV2  json.RawMessage `json:"publication_v2"`
		QueryURL       string          `json:"query_url"`
		Question       string          `json:"question"`
		ApprovalPolicy string          `json:"approval_policy"`
		Requester      struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"requester"`
		Audiences struct {
			ResponseReaders []struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
			} `json:"response_readers"`
			Publications []teamsReportPublication `json:"publications"`
			Exports      []teamsFeedExport        `json:"exports"`
		} `json:"audiences"`
		MinAppVersion   string `json:"min_app_version"`
		RetentionPolicy struct {
			Mode          string `json:"mode"`
			DurationDays  *int   `json:"duration_days"`
			PolicyVersion int64  `json:"policy_version"`
		} `json:"retention_policy"`
		ResultSchema struct {
			Columns []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Role     string `json:"role"`
				Merge    string `json:"merge"`
				MetricID string `json:"metric_id"`
				Unit     string `json:"unit"`
				Nullable bool   `json:"nullable"`
			} `json:"columns"`
		} `json:"result_schema"`
	}
	if !teamsConfigExactKeys(w.Config) {
		return teamsQueryPlan{}, errTeamsQueryUnsupported
	}
	decoder := json.NewDecoder(bytes.NewReader(w.Config))
	decoder.DisallowUnknownFields()
	if !teams.ValidUniqueJSON(w.Config) || decoder.Decode(&config) != nil || config.Question == "" || config.Requester.ID == "" || config.Requester.DisplayName == "" || !teamsVersionPattern.MatchString(config.MinAppVersion) || config.RetentionPolicy.Mode != "organization_controlled" || config.RetentionPolicy.PolicyVersion < 1 || (config.RetentionPolicy.DurationDays != nil && *config.RetentionPolicy.DurationDays < 1) || config.Audiences.ResponseReaders == nil || config.Audiences.Publications == nil || config.Audiences.Exports == nil || (config.ApprovalPolicy != "metrics_or_review" && config.ApprovalPolicy != "review") {
		return teamsQueryPlan{}, errTeamsQueryUnsupported
	}
	if !validTeamsSchedule(config.Schedule) {
		return teamsQueryPlan{}, errTeamsQueryUnsupported
	}
	if _, err := parseTeamsPeriod(w.Period, config.Schedule); err != nil {
		return teamsQueryPlan{}, err
	}
	readers := map[string]bool{}
	for _, reader := range config.Audiences.ResponseReaders {
		if reader.Kind != "person" || reader.ID == "" || readers[reader.ID] {
			return teamsQueryPlan{}, errTeamsQueryUnsupported
		}
		readers[reader.ID] = true
	}
	plan, err := compileTeamsQuery(config.QueryURL)
	if err != nil {
		return plan, err
	}
	if len(config.PublicationV2) > 0 {
		if _, e := parseTeamsReportSeries(config.PublicationV2); e != nil || config.Schedule != nil || config.ApprovalPolicy != "review" || len(config.Audiences.Publications) != 0 || len(config.Audiences.Exports) != 0 || len(plan.Metrics) != 1 || plan.Metrics[0].ID != "total_tokens" || plan.Metrics[0].Field != "total_tokens" {
			return teamsQueryPlan{}, errTeamsQueryUnsupported
		}
	}
	if !validTeamsReports(config.Audiences.Publications, config.ApprovalPolicy, plan) {
		return teamsQueryPlan{}, errTeamsQueryUnsupported
	}
	if !validTeamsExports(config.Audiences.Exports, config.ApprovalPolicy, plan) {
		return teamsQueryPlan{}, errTeamsQueryUnsupported
	}
	if len(config.ResultSchema.Columns) != len(plan.Metrics) {
		return plan, errTeamsQueryUnsupported
	}
	columns := map[string]bool{}
	for _, col := range config.ResultSchema.Columns {
		var field string
		for _, m := range plan.Metrics {
			if m.ID == col.ID {
				field = m.Field
				break
			}
		}
		capability := teamsNumericCapabilities[field]
		if capability.MetricID == "" || columns[col.ID] || col.Type != "integer" || col.Role != "metric" || col.Merge != "sum" || col.Nullable || col.Unit != capability.Unit || col.MetricID != capability.MetricID {
			return plan, errTeamsQueryUnsupported
		}
		columns[col.ID] = true
	}
	return plan, nil
}

// stageTeamsNumeric stores a review-first draft locally; it does not authorize
// a network send. Existing approved drafts are replaced only by returning to
// review, and an accepted/uncertain work item may never be overwritten.
func (c *Catalog) stageTeamsNumeric(ctx context.Context, w teams.Work, descriptor LibraryDescriptor, snapshot teamsSourceSnapshot, result teamsNumericResult, prepared teamsPreparedManifest, idem string, payload []byte) (teamsStaged, error) {
	var out teamsStaged
	lease, err := c.teamsSharingLease()
	if err != nil {
		return out, err
	}
	defer lease.Close()
	w.Config = bytes.Clone(w.Config)
	plan, err := teamsPlanFromWork(w)
	if err != nil {
		return out, err
	}
	if _, err = teams.WorkPath(w.ID, "result"); err != nil {
		return out, err
	}
	if _, err = teams.WorkPath(w.VersionID, "result"); err != nil {
		return out, err
	}
	if _, err = teams.WorkPath(idem, "result"); err != nil {
		return out, err
	}
	registration := result.Registration
	manifestHash, ownership, sharedPrivacy := registration.ManifestHash, registration.OwnershipRevision, registration.SharedPrivacyRevision
	_, registrationHash, registrationErr := registration.Manifest.Bytes()
	decodedHash, hashErr := hex.DecodeString(manifestHash)
	if hashErr != nil || len(decodedHash) != sha256.Size || hex.EncodeToString(decodedHash) != manifestHash || registrationErr != nil || registrationHash != manifestHash || !registration.valid || result.RegistrationHash != teamsRegistrationHash(registration) || w.EvaluationRevision < 1 || w.BaselineRevision < 0 || w.LibraryID != descriptor.ID || descriptor.Instance == "" || sharedPrivacy < 0 || ownership < 1 || result.Generation != snapshot.Generation || result.PrivacyRevision != snapshot.PrivacyRevision || result.OwnedHash != teamsOwnedHash(registration.Owned) || result.SharedPrivateHash != teamsOwnedHash(registration.SharedPrivate) || result.PlanHash != teamsPlanHash(plan) {
		return out, errTeamsConsentStale
	}
	if prepared.VersionID != w.VersionID || prepared.ConfigHash != w.ConfigSHA256 || prepared.LibraryID != w.LibraryID {
		return out, errTeamsConsentStale
	}
	var signedPolicy struct {
		ApprovalPolicy string `json:"approval_policy"`
	}
	if json.Unmarshal(w.Config, &signedPolicy) != nil || (prepared.Mode == "automatic_metrics" && signedPolicy.ApprovalPolicy != "metrics_or_review") {
		return out, errTeamsConsentStale
	}
	manifestBytes, preparedHash, preparedErr := prepared.Manifest.Bytes()
	if preparedErr != nil || preparedHash != manifestHash || len(prepared.Manifest.SourceManifest) != len(snapshot.Entries) {
		return out, errTeamsConsentStale
	}
	for i, entry := range snapshot.Entries {
		source := prepared.Manifest.SourceManifest[i]
		if source.Token != entry.Token || source.EvidenceRevision != entry.EvidenceRevision || source.Protection != entry.Protection {
			return out, errTeamsConsentStale
		}
	}
	payload = bytes.Clone(payload)
	if err = validateTeamsNumericPayload(plan, result, payload); err != nil {
		return out, err
	}
	hash := teamsPayloadHash(payload)
	err = c.writeTransaction(ctx, "teams-stage", func(tx *sql.Tx) error {
		settings, e := teamsRevisionsInTx(ctx, tx, snapshot)
		if e != nil {
			return e
		}
		if settings.LibraryID != descriptor.ID || settings.InstanceID != descriptor.Instance {
			return errTeamsNotEnrolled
		}
		if w.RequiredLocalRevision > settings.LocalRevision || prepared.Manifest.LocalRevision != settings.LocalRevision || prepared.SettingsRevision != settings.SettingsRevision {
			return errTeamsConsentStale
		}
		state, approvedAt := "awaiting_review", ""
		switch prepared.Mode {
		case "reviewed":
			var grant int
			if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM teams_request_grants WHERE version_id=? AND library_id=? AND instance_id=? AND org_id=? AND installation_id=? AND config_hash=? AND settings_revision=?`, w.VersionID, w.LibraryID, settings.InstanceID, settings.OrgID, settings.InstallationID, w.ConfigSHA256, settings.SettingsRevision).Scan(&grant); e != nil {
				return e
			}
			if grant != 1 {
				return errTeamsConsentStale
			}
		case "automatic_metrics":
			if settings.MetricsOptOut || prepared.PolicyVersion < 1 || settings.AcknowledgedPolicy != prepared.PolicyVersion {
				return errTeamsConsentStale
			}
			state = "approved"
			approvedAt = time.Now().UTC().Format(time.RFC3339Nano)
		default:
			return errTeamsConsentStale
		}
		old, e := readTeamsOutbox(ctx, tx, w.ID)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e == nil && (old.State == "sent" || old.State == "sending" || old.State == "needs_reconcile" || old.State == "unconfirmed" || ((old.State == "rejected" || old.State == "withdrawn") && old.IdempotencyKey == idem)) {
			return errTeamsConsentStale
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO teams_outbox(work_id,version_id,library_id,instance_id,org_id,installation_id,config,config_hash,payload,payload_hash,expires_at,idempotency_key,state,local_revision,generation,privacy_revision,settings_revision,shared_privacy_revision,ownership_revision,evaluation_revision,baseline_revision,manifest,manifest_hash,owned_hash,approval_mode,policy_version,approved_at,receipt_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, '') ON CONFLICT(work_id) DO UPDATE SET version_id=excluded.version_id,library_id=excluded.library_id,instance_id=excluded.instance_id,org_id=excluded.org_id,installation_id=excluded.installation_id,config=excluded.config,config_hash=excluded.config_hash,payload=excluded.payload,payload_hash=excluded.payload_hash,expires_at=excluded.expires_at,idempotency_key=excluded.idempotency_key,state=excluded.state,local_revision=excluded.local_revision,generation=excluded.generation,privacy_revision=excluded.privacy_revision,settings_revision=excluded.settings_revision,shared_privacy_revision=excluded.shared_privacy_revision,ownership_revision=excluded.ownership_revision,evaluation_revision=excluded.evaluation_revision,baseline_revision=excluded.baseline_revision,manifest=excluded.manifest,manifest_hash=excluded.manifest_hash,owned_hash=excluded.owned_hash,approval_mode=excluded.approval_mode,policy_version=excluded.policy_version,approved_at=excluded.approved_at,submission=NULL,submission_hash='',receipt_json='',state_reason=''`, w.ID, w.VersionID, w.LibraryID, settings.InstanceID, settings.OrgID, settings.InstallationID, w.Config, w.ConfigSHA256, payload, hash, w.ExpiresAt.UTC().Format(time.RFC3339Nano), idem, state, settings.LocalRevision, snapshot.Generation, snapshot.PrivacyRevision, settings.SettingsRevision, sharedPrivacy, ownership, w.EvaluationRevision, w.BaselineRevision, manifestBytes, manifestHash, result.OwnedHash, prepared.Mode, prepared.PolicyVersion, approvedAt)
		if e != nil {
			return e
		}
		out, e = readTeamsOutbox(ctx, tx, w.ID)
		return e
	})
	return out, err
}

// markTeamsSendOutcome preserves an ambiguous submission until it is resolved.
// Callers may mark a definite rejection only after authenticated server response;
// stale state must be fetched and reviewed again before a new attempt.
func (c *Catalog) markTeamsSendOutcome(ctx context.Context, workID, state, reason string) error {
	if state != "needs_reconcile" && state != "unconfirmed" && state != "rejected" && state != "withdrawn" {
		return errTeamsConsentStale
	}
	switch reason {
	case "transport", "server", "authorization", "receipt_mismatch", "stale_state", "cancelled", "invalid_result":
	default:
		return errTeamsConsentStale
	}
	return c.writeTransaction(ctx, "teams-send-outcome", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE teams_outbox SET state=?,state_reason=? WHERE work_id=? AND state IN ('sending','needs_reconcile')`, state, reason, workID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errTeamsConsentStale
		}
		return nil
	})
}

func teamsCheckStaged(ctx context.Context, tx *sql.Tx, descriptor LibraryDescriptor, v teamsStaged, sharedPrivacy, ownership int64, policy teams.MetricsDefault) error {
	if descriptor.ID != v.LibraryID || !time.Now().Before(v.ExpiresAt) || teamsPayloadHash(v.Payload) != v.PayloadHash || teamsPayloadHash(v.Config) != v.ConfigHash || teamsPayloadHash(v.Manifest) != v.ManifestHash || sharedPrivacy != v.SharedPrivacyRevision || ownership != v.OwnershipRevision {
		return errTeamsConsentStale
	}
	var signedPolicy struct {
		ApprovalPolicy string `json:"approval_policy"`
	}
	if json.Unmarshal(v.Config, &signedPolicy) != nil || (v.ApprovalMode == "automatic_metrics" && signedPolicy.ApprovalPolicy != "metrics_or_review") {
		return errTeamsConsentStale
	}
	settings, err := teamsRevisionsInTx(ctx, tx, teamsSourceSnapshot{Generation: v.Generation, PrivacyRevision: v.PrivacyRevision})
	if err != nil {
		return err
	}
	if settings.LibraryID != descriptor.ID || settings.InstanceID != descriptor.Instance || settings.InstanceID != v.InstanceID || settings.OrgID != v.OrgID || settings.InstallationID != v.InstallationID || settings.SettingsRevision != v.SettingsRevision || settings.LocalRevision != v.LocalRevision || (v.ApprovalMode == "automatic_metrics" && (!policy.Value || policy.PolicyVersion != v.PolicyVersion || settings.AcknowledgedPolicy != v.PolicyVersion || settings.MetricsOptOut)) {
		return errTeamsConsentStale
	}
	return nil
}

// reviewTeamsDraft records the user's decision for exactly these bytes, not
// future edits or a changed request. Shared state must be freshly retrieved.
func (c *Catalog) reviewTeamsDraft(ctx context.Context, descriptor LibraryDescriptor, workID, payloadHash string, sharedPrivacy, ownership int64, policy teams.MetricsDefault) (teamsStaged, error) {
	var out teamsStaged
	lease, err := c.teamsSharingLease()
	if err != nil {
		return out, err
	}
	defer lease.Close()
	err = c.writeTransaction(ctx, "teams-review", func(tx *sql.Tx) error {
		v, e := readTeamsOutbox(ctx, tx, workID)
		if e != nil {
			return e
		}
		if v.State != "awaiting_review" || v.ApprovalMode != "reviewed" || v.PayloadHash != payloadHash {
			return errTeamsConsentStale
		}
		if e = teamsCheckStaged(ctx, tx, descriptor, v, sharedPrivacy, ownership, policy); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_outbox SET state='approved',approved_at=? WHERE work_id=?`, time.Now().UTC().Format(time.RFC3339Nano), workID)
		if e != nil {
			return e
		}
		out, e = readTeamsOutbox(ctx, tx, workID)
		return e
	})
	return out, err
}

// inspectTeamsApproved is not a send authorization by itself: a worker must
// still hold the library send/mark ordering gate, refresh shared epochs and
// recheck request cancellation immediately before emitting signed bytes.
func (c *Catalog) inspectTeamsApproved(ctx context.Context, descriptor LibraryDescriptor, workID string, sharedPrivacy, ownership int64, policy teams.MetricsDefault) (teamsStaged, error) {
	var out teamsStaged
	err := c.writeTransaction(ctx, "teams-send-check", func(tx *sql.Tx) error {
		v, e := readTeamsOutbox(ctx, tx, workID)
		if e != nil {
			return e
		}
		if v.State != "approved" {
			return errTeamsConsentStale
		}
		if e = teamsCheckStaged(ctx, tx, descriptor, v, sharedPrivacy, ownership, policy); e != nil {
			return e
		}
		out = v
		return nil
	})
	return out, err
}

// reserveTeamsReviewed freezes the exact signed-request body after local and
// shared revision checks. It must be called only while the worker holds a
// proven send/mark ordering gate; this storage primitive does not send bytes.
func (c *Catalog) reserveTeamsReviewed(ctx context.Context, descriptor LibraryDescriptor, workID string, sharedPrivacy, ownership int64, policy teams.MetricsDefault) (teamsStaged, error) {
	var out teamsStaged
	err := c.writeTransaction(ctx, "teams-reserve", func(tx *sql.Tx) error {
		v, e := readTeamsOutbox(ctx, tx, workID)
		if e != nil {
			return e
		}
		if v.State != "approved" {
			return errTeamsConsentStale
		}
		if e = teamsCheckStaged(ctx, tx, descriptor, v, sharedPrivacy, ownership, policy); e != nil {
			return e
		}
		decided, e := time.Parse(time.RFC3339Nano, v.ApprovedAt)
		if e != nil {
			return errTeamsConsentStale
		}
		submission := teams.Submission{IdempotencyKey: v.IdempotencyKey, ConfigSHA256: v.ConfigHash, EvaluationRevision: v.EvaluationRevision, LocalRevision: v.LocalRevision, ManifestSHA256: v.ManifestHash, OwnershipRevision: v.OwnershipRevision, BaselineRevision: v.BaselineRevision, PayloadSHA256: v.PayloadHash, Approval: teams.Approval{Mode: v.ApprovalMode, OrgPolicyVersion: v.PolicyVersion, DecidedAt: decided, PrivacyRevision: v.PrivacyRevision, SharedPrivacyRevision: v.SharedPrivacyRevision, SettingsRevision: v.SettingsRevision}, Payload: v.Payload}
		body, e := teams.SubmissionBytes(submission)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_outbox SET state='sending',submission=?,submission_hash=? WHERE work_id=?`, body, teamsPayloadHash(body), workID)
		if e != nil {
			return e
		}
		out, e = readTeamsOutbox(ctx, tx, workID)
		return e
	})
	return out, err
}

// A failed or interrupted send can have reached the service. Preserve its
// immutable body and require receipt reconciliation before another attempt.
func (c *Catalog) markTeamsUncertain(ctx context.Context, workID string) error {
	return c.writeTransaction(ctx, "teams-uncertain", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE teams_outbox SET state='needs_reconcile' WHERE work_id=? AND state='sending'`, workID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errTeamsConsentStale
		}
		return nil
	})
}

// recoverTeamsInterrupted must run before a worker resumes processing a
// library. A process can stop after the server accepted bytes but before the
// receipt was persisted, so no interrupted send can be simply retried.
func (c *Catalog) recoverTeamsInterrupted(ctx context.Context) error {
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	return c.writeTransaction(ctx, "teams-recover-interrupted", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE teams_outbox SET state='needs_reconcile',state_reason='transport' WHERE state='sending'`)
		return err
	})
}

// recordTeamsReceipt is called only on an authenticated, matching receipt. It
// stores what actually left the Mac, even if local privacy changed afterward.
func (c *Catalog) recordTeamsReceipt(ctx context.Context, descriptor LibraryDescriptor, orgID, installationID, workID string, receipt teams.Receipt) error {
	if receipt.WorkID != workID || receipt.PayloadSHA256 == "" || receipt.ResultRevision < 1 || receipt.AcceptedAt.IsZero() {
		return errTeamsConsentStale
	}
	if _, err := teams.WorkPath(receipt.ID, "receipt"); err != nil {
		return errTeamsConsentStale
	}
	return c.writeTransaction(ctx, "teams-receipt", func(tx *sql.Tx) error {
		v, err := readTeamsOutbox(ctx, tx, workID)
		if err != nil {
			return err
		}
		if v.LibraryID != descriptor.ID || v.InstanceID != descriptor.Instance || v.OrgID != orgID || v.InstallationID != installationID {
			return errTeamsConsentStale
		}
		if v.State == "sent" {
			var stored string
			if err = tx.QueryRowContext(ctx, `SELECT receipt_json FROM teams_send_log WHERE library_id=? AND work_id=?`, v.LibraryID, workID).Scan(&stored); err != nil {
				return err
			}
			var prior teams.Receipt
			if json.Unmarshal([]byte(stored), &prior) != nil || prior.ID != receipt.ID || prior.WorkID != receipt.WorkID || prior.PayloadSHA256 != receipt.PayloadSHA256 || prior.SubmissionSHA256 != receipt.SubmissionSHA256 || prior.IdempotencyKey != receipt.IdempotencyKey || prior.InstallationID != receipt.InstallationID || prior.ResultRevision != receipt.ResultRevision {
				return errTeamsConsentStale
			}
			return nil
		}
		if (v.State != "sending" && v.State != "needs_reconcile" && v.State != "unconfirmed") || receipt.PayloadSHA256 != v.PayloadHash || receipt.SubmissionSHA256 != v.SubmissionHash || receipt.IdempotencyKey != v.IdempotencyKey || receipt.InstallationID != v.InstallationID || len(v.Submission) == 0 || teamsPayloadHash(v.Submission) != v.SubmissionHash {
			return errTeamsConsentStale
		}
		encoded, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO teams_send_log(work_id,version_id,library_id,instance_id,org_id,installation_id,config,config_hash,payload,payload_hash,submission,receipt_json,sent_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.WorkID, v.VersionID, v.LibraryID, v.InstanceID, v.OrgID, v.InstallationID, v.Config, v.ConfigHash, v.Payload, v.PayloadHash, v.Submission, string(encoded), receipt.AcceptedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE teams_outbox SET state='sent',receipt_json=? WHERE work_id=?`, string(encoded), workID)
		return err
	})
}

type teamsPreparedManifest struct {
	Manifest                         teams.Manifest
	VersionID, ConfigHash, LibraryID string
	SettingsRevision                 int64
	Mode                             string
	PolicyVersion                    int64
}

// grantTeamsQuestion is invoked only by a local approval action after displaying
// this immutable config and its audience. It grants computation/registration,
// not delivery of a result that has not yet been reviewed.
func (c *Catalog) grantTeamsQuestion(ctx context.Context, w teams.Work, descriptor LibraryDescriptor, expectedSettings int64) error {
	return c.grantTeamsQuestionForView(ctx, w, descriptor, expectedSettings, "", "")
}
func (c *Catalog) grantTeamsQuestionForView(ctx context.Context, w teams.Work, descriptor LibraryDescriptor, expectedSettings int64, scope, expectedView string) error {
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	if w.Kind == "llm" {
		manifest := teamsWorkerCapabilities()
		if !teamsBuildEligible(manifest, "scoped_artifact_v1") {
			return errTeamsQueryUnsupported
		}
		if _, err := teamsLLMFromWork(w, manifest.AppVersion); err != nil {
			return err
		}
	} else if _, err := teamsV2PlanFromWork(w); err != nil {
		return err
	}
	if _, err := teams.WorkPath(w.VersionID, "status"); err != nil {
		return err
	}
	return c.writeTransaction(ctx, "teams-question-grant", func(tx *sql.Tx) error {
		s, err := readTeamsSettings(ctx, tx)
		if err != nil {
			return err
		}
		if !s.Enabled || s.LibraryID != descriptor.ID || s.InstanceID != descriptor.Instance || w.LibraryID != descriptor.ID || s.SettingsRevision != expectedSettings {
			return errTeamsConsentStale
		}
		if expectedView != "" {
			var source, privacy int64
			if e := tx.QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&source); e != nil {
				return e
			}
			if e := tx.QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&privacy); e != nil {
				return e
			}
			g, e := readCacheGenerations(ctx, tx)
			if e != nil {
				return e
			}
			current, e := teamsWorkViewBindingIn(ctx, tx, scope, w, s, source, privacy, g.all)
			if e != nil {
				return e
			}
			if current != expectedView {
				return errTeamsConsentStale
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO teams_request_grants(version_id,library_id,instance_id,org_id,installation_id,config_hash,settings_revision,granted_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(version_id) DO UPDATE SET library_id=excluded.library_id,instance_id=excluded.instance_id,org_id=excluded.org_id,installation_id=excluded.installation_id,config_hash=excluded.config_hash,settings_revision=excluded.settings_revision,granted_at=excluded.granted_at`, w.VersionID, w.LibraryID, s.InstanceID, s.OrgID, s.InstallationID, w.ConfigSHA256, s.SettingsRevision, time.Now().UTC().Format(time.RFC3339Nano))
		return err
	})
}

// prepareTeamsManifest authorizes the operational source enumeration before
// it leaves the Mac. A disabled default, opt-out, or unacknowledged policy
// requires an explicit local question grant even for numeric statistics.
func (c *Catalog) prepareTeamsManifest(ctx context.Context, w teams.Work, descriptor LibraryDescriptor, snapshot teamsSourceSnapshot, policy teams.MetricsDefault) (teamsPreparedManifest, error) {
	var out teamsPreparedManifest
	if _, err := teamsPlanFromWork(w); err != nil {
		return out, err
	}
	if _, err := teams.WorkPath(w.VersionID, "status"); err != nil {
		return out, err
	}
	var config struct {
		ApprovalPolicy string `json:"approval_policy"`
	}
	if json.Unmarshal(w.Config, &config) != nil {
		return out, errTeamsConsentStale
	}
	err := c.writeTransaction(ctx, "teams-prepare-manifest", func(tx *sql.Tx) error {
		settings, e := teamsRevisionsInTx(ctx, tx, snapshot)
		if e != nil {
			return e
		}
		if settings.LibraryID != descriptor.ID || settings.InstanceID != descriptor.Instance || w.LibraryID != descriptor.ID || w.RequiredLocalRevision > settings.LocalRevision {
			return errTeamsConsentStale
		}
		mode := "reviewed"
		if config.ApprovalPolicy == "metrics_or_review" && policy.Value && policy.PolicyVersion > 0 && settings.AcknowledgedPolicy == policy.PolicyVersion && !settings.MetricsOptOut {
			mode = "automatic_metrics"
		} else {
			var present int
			e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM teams_request_grants WHERE version_id=? AND library_id=? AND instance_id=? AND org_id=? AND installation_id=? AND config_hash=? AND settings_revision=?`, w.VersionID, w.LibraryID, settings.InstanceID, settings.OrgID, settings.InstallationID, w.ConfigSHA256, settings.SettingsRevision).Scan(&present)
			if e != nil {
				return e
			}
			if present != 1 {
				return errTeamsConsentStale
			}
		}
		sources := make([]teams.Source, len(snapshot.Entries))
		for i, entry := range snapshot.Entries {
			sources[i] = teams.Source{Token: entry.Token, EvidenceRevision: entry.EvidenceRevision, Protection: entry.Protection}
		}
		manifest := teams.Manifest{LocalRevision: settings.LocalRevision, Complete: true, SourceManifest: sources}
		if _, _, e = manifest.Bytes(); e != nil {
			return e
		}
		policyVersion := int64(0)
		if mode == "automatic_metrics" {
			policyVersion = policy.PolicyVersion
		}
		out = teamsPreparedManifest{Manifest: manifest, VersionID: w.VersionID, ConfigHash: w.ConfigSHA256, LibraryID: w.LibraryID, SettingsRevision: settings.SettingsRevision, Mode: mode, PolicyVersion: policyVersion}
		return nil
	})
	return out, err
}

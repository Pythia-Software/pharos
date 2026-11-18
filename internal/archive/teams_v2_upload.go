package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

type teamsV2Upload struct {
	WorkID, VersionID, LibraryID, InstanceID, OrgID, InstallationID, ConfigHash, SnapshotID, BindingID, InventoryHash, Mode, State     string
	InventoryRevision, LocalRevision, EvaluationRevision, SettingsRevision, SourceRevision, PrivacyRevision, Generation, PolicyVersion int64
	Pages                                                                                                                              [][]byte
	Seal                                                                                                                               []byte
	Authorization, SealedJSON, BindingJSON, AssignmentsJSON                                                                            []byte
}

func readTeamsV2Upload(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, workID string) (teamsV2Upload, error) {
	var u teamsV2Upload
	var pages []byte
	err := q.QueryRowContext(ctx, `SELECT work_id,version_id,library_id,instance_id,org_id,installation_id,config_sha256,snapshot_id,binding_id,inventory_revision,inventory_sha256,local_revision,evaluation_revision,settings_revision,source_revision,privacy_revision,generation,mode,policy_version,authorization_json,pages_json,seal_json,sealed_json,binding_json,assignments_json,state FROM teams_v2_uploads WHERE work_id=?`, workID).Scan(&u.WorkID, &u.VersionID, &u.LibraryID, &u.InstanceID, &u.OrgID, &u.InstallationID, &u.ConfigHash, &u.SnapshotID, &u.BindingID, &u.InventoryRevision, &u.InventoryHash, &u.LocalRevision, &u.EvaluationRevision, &u.SettingsRevision, &u.SourceRevision, &u.PrivacyRevision, &u.Generation, &u.Mode, &u.PolicyVersion, &u.Authorization, &pages, &u.Seal, &u.SealedJSON, &u.BindingJSON, &u.AssignmentsJSON, &u.State)
	if err != nil {
		return u, err
	}
	if json.Unmarshal(pages, &u.Pages) != nil || u.Pages == nil {
		return teamsV2Upload{}, errTeamsConsentStale
	}
	return u, nil
}

// prepareTeamsV2Upload must be called under the sharing lease after obtaining
// a fresh signed watermark. It commits identity exposure and exact page bytes
// before the first possible emission. It does not itself send anything.
func (c *Catalog) prepareTeamsV2Upload(ctx context.Context, w teams.Work, d LibraryDescriptor, credential *teams.EnrollmentCredential, snapshot teamsV2Inventory, policy teams.MetricsDefault, watermark teams.SourceWatermark, manifest teams.CapabilityManifest) (teamsV2Upload, error) {
	return c.prepareTeamsV2UploadAt(ctx, w, d, credential, snapshot, policy, watermark, manifest, time.Now())
}

func (c *Catalog) prepareTeamsV2UploadAt(ctx context.Context, w teams.Work, d LibraryDescriptor, credential *teams.EnrollmentCredential, snapshot teamsV2Inventory, policy teams.MetricsDefault, watermark teams.SourceWatermark, manifest teams.CapabilityManifest, now time.Time) (teamsV2Upload, error) {
	var out teamsV2Upload
	if credential == nil || credential.LibraryID != d.ID || w.LibraryID != d.ID {
		return out, errTeamsQueryUnsupported
	}
	if w.Kind == "llm" {
		if !teamsBuildEligible(manifest, "source_snapshot_v2") || !teamsBuildEligible(manifest, "scoped_artifact_v1") || !snapshot.Complete {
			return out, errTeamsQueryUnsupported
		}
		if _, err := teamsLLMFromWorkAt(w, manifest.AppVersion, now); err != nil {
			return out, err
		}
	} else {
		plan, err := teamsV2PlanFromWork(w)
		if err != nil || len(plan.Metrics) != 1 || plan.Metrics[0].Field != "total_tokens" || !teamsNumericWorkEligible(w, manifest) {
			return out, errTeamsQueryUnsupported
		}
	}
	var err error
	if _, err = teams.WorkPath(w.ID, "status"); err != nil {
		return out, err
	}
	if _, err = teams.WorkPath(w.VersionID, "status"); err != nil {
		return out, err
	}
	if w.EvaluationRevision < 1 || !w.ExpiresAt.After(now) || len(snapshot.values) == 0 || snapshot.Items == nil {
		return out, errTeamsSourceUnavailable
	}
	var config struct {
		ApprovalPolicy string `json:"approval_policy"`
		MinAppVersion  string `json:"min_app_version"`
	}
	if json.Unmarshal(w.Config, &config) != nil || !teamsVersionAtLeast(manifest.AppVersion, config.MinAppVersion) {
		return out, errTeamsQueryUnsupported
	}
	err = c.writeTransaction(ctx, "teams-v2-reserve-upload", func(tx *sql.Tx) error {
		s, e := readTeamsSettings(ctx, tx)
		if e != nil {
			return e
		}
		if !s.Enabled || s.LibraryID != d.ID || s.InstanceID != d.Instance || s.OrgID != credential.OrgID || s.InstallationID != credential.InstallationID {
			return errTeamsConsentStale
		}
		var sourceRevision, privacyRevision int64
		if e = tx.QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&sourceRevision); e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&privacyRevision); e != nil {
			return e
		}
		g, e := readCacheGenerations(ctx, tx)
		if e != nil {
			return e
		}
		if sourceRevision != snapshot.SourceRevision || privacyRevision != snapshot.PrivacyRevision || g.all != snapshot.Generation {
			return errTeamsSnapshotChanged
		}
		mode := "reviewed"
		policyVersion := int64(0)
		if config.ApprovalPolicy == "metrics_or_review" && policy.Value && policy.PolicyVersion > 0 && s.AcknowledgedPolicy == policy.PolicyVersion && !s.MetricsOptOut {
			mode = "automatic_metrics"
			policyVersion = policy.PolicyVersion
		} else {
			var present int
			if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM teams_request_grants WHERE version_id=? AND library_id=? AND instance_id=? AND org_id=? AND installation_id=? AND config_hash=? AND settings_revision=?`, w.VersionID, d.ID, d.Instance, s.OrgID, s.InstallationID, w.ConfigSHA256, s.SettingsRevision).Scan(&present); e != nil {
				return e
			}
			if present != 1 {
				return errTeamsConsentStale
			}
		}
		old, e := readTeamsV2Upload(ctx, tx, w.ID)
		if e == nil {
			if old.VersionID != w.VersionID || old.LibraryID != d.ID || old.InstanceID != d.Instance || old.OrgID != s.OrgID || old.InstallationID != s.InstallationID || old.ConfigHash != w.ConfigSHA256 || old.EvaluationRevision != w.EvaluationRevision || old.SettingsRevision != s.SettingsRevision || old.SourceRevision != sourceRevision || old.PrivacyRevision != privacyRevision || old.Generation != g.all || old.Mode != mode || old.PolicyVersion != policyVersion || old.State == "abandoned" {
				return errTeamsConsentStale
			}
			if !old.matchesSourceProof(w.ScheduledAssignment) {
				return errTeamsConsentStale
			}
			seal, e := sourcewire.DecodeSeal(old.Seal)
			if e != nil {
				return errTeamsConsentStale
			}
			if watermark.ReservedInventoryRevision > old.InventoryRevision || (watermark.ReservedInventoryRevision == old.InventoryRevision && watermark.ReservedInventorySHA256 != old.InventoryHash) {
				return errTeamsConsentStale
			}
			if watermark.AcceptedInventorySHA256 != seal.ExpectedPriorSHA256 && !(watermark.AcceptedInventoryRevision == old.InventoryRevision && watermark.AcceptedInventorySHA256 == old.InventoryHash) {
				return errTeamsConsentStale
			}
			check, e := teamsBuildV2Pages(snapshot, old.SnapshotID, old.LocalRevision, old.InventoryRevision, old.EvaluationRevision, seal.ExpectedPriorSHA256)
			if e != nil || check.InventorySHA256 != old.InventoryHash || len(check.Pages) != len(old.Pages) || !bytes.Equal(check.Seal, old.Seal) {
				return errTeamsConsentStale
			}
			for i := range check.Pages {
				if !bytes.Equal(check.Pages[i], old.Pages[i]) {
					return errTeamsConsentStale
				}
			}
			out = old
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var priorLocal int64
		if e = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(inventory_revision),0) FROM teams_v2_uploads`).Scan(&priorLocal); e != nil {
			return e
		}
		inventoryRevision := max(priorLocal, watermark.ReservedInventoryRevision)
		local := max(s.LocalRevision, watermark.LocalRevision, w.RequiredLocalRevision)
		if inventoryRevision == math.MaxInt64 || local == math.MaxInt64 {
			return errTeamsSourceUnavailable
		}
		inventoryRevision++
		local++
		snapshotID := randomUUID()
		bindingID := randomUUID()
		pages, e := teamsBuildV2Pages(snapshot, snapshotID, local, inventoryRevision, w.EvaluationRevision, watermark.AcceptedInventorySHA256)
		if e != nil {
			return e
		}
		decisionID := ""
		if mode == "reviewed" {
			decisionID = randomUUID()
		}
		authorization, e := json.Marshal(teams.SourceAuthorization{ScheduledAssignment: w.ScheduledAssignment, SnapshotID: snapshotID, InventoryRevision: inventoryRevision, InventorySHA256: pages.InventorySHA256, LocalRevision: local, EvaluationRevision: w.EvaluationRevision, ConfigSHA256: w.ConfigSHA256, Mode: mode, DecisionID: decisionID, DecidedAt: time.Now().UTC(), SettingsRevision: s.SettingsRevision, OrgPolicyVersion: policyVersion})
		if e != nil {
			return e
		}
		for binding, revision := range snapshot.exposures {
			if e = reserveTeamsSourceExposure(ctx, tx, d, credential, binding, revision); e != nil {
				return e
			}
		}
		encoded, e := json.Marshal(pages.Pages)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE teams_local_settings SET local_revision=? WHERE id=1`, local); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO teams_v2_uploads(work_id,version_id,library_id,instance_id,org_id,installation_id,config_sha256,snapshot_id,binding_id,inventory_revision,inventory_sha256,local_revision,evaluation_revision,settings_revision,source_revision,privacy_revision,generation,mode,policy_version,authorization_json,pages_json,seal_json,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, 'prepared')`, w.ID, w.VersionID, d.ID, d.Instance, s.OrgID, s.InstallationID, w.ConfigSHA256, snapshotID, bindingID, inventoryRevision, pages.InventorySHA256, local, w.EvaluationRevision, s.SettingsRevision, sourceRevision, privacyRevision, g.all, mode, policyVersion, authorization, encoded, pages.Seal)
		if e != nil {
			return e
		}
		out = teamsV2Upload{WorkID: w.ID, VersionID: w.VersionID, LibraryID: d.ID, InstanceID: d.Instance, OrgID: s.OrgID, InstallationID: s.InstallationID, ConfigHash: w.ConfigSHA256, SnapshotID: snapshotID, BindingID: bindingID, InventoryRevision: inventoryRevision, InventoryHash: pages.InventorySHA256, LocalRevision: local, EvaluationRevision: w.EvaluationRevision, SettingsRevision: s.SettingsRevision, SourceRevision: sourceRevision, PrivacyRevision: privacyRevision, Generation: g.all, Mode: mode, PolicyVersion: policyVersion, Authorization: authorization, Pages: pages.Pages, Seal: pages.Seal, State: "prepared"}
		return nil
	})
	return out, err
}

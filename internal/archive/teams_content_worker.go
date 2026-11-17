package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/gbdubs/pharos/internal/teams"
)

// Content work always requires an explicit source grant, a separate model
// account permission and a final exact-answer review. No standing or blind
// content permission is inferred by this initial path.
func (s *Server) checkTeamsContentWork(ctx context.Context, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential, work teams.Work, manifest teams.CapabilityManifest) teamsWorkerItem {
	item := teamsWorkerItem{Work: work, State: "needs_update"}
	if !teamsBuildEligible(manifest, "scoped_artifact_v1") {
		return item
	}
	if _, err := teamsLLMFromWork(work, manifest.AppVersion); err != nil {
		return item
	}
	run, err := readTeamsContentRun(ctx, s.Catalog.reader(), work.ID)
	if err == nil {
		item.Payload = bytesClone(run.Payload)
		item.PayloadHash = run.PayloadHash
		item.State = run.State
		switch run.State {
		case "accepted", "sending", "needs_reconcile":
			receipt, e := s.Catalog.reconcileTeamsContent(ctx, client, d, credential, work.ID)
			if teamsContentReceiptMissing(e) && run.State != "accepted" {
				receipt, e = s.Catalog.sendTeamsContent(ctx, s.Config(), client, d, credential, work, manifest)
			}
			if e != nil {
				item.State = "delivery_unconfirmed"
			} else {
				item.State = "sent"
				if receipt.Outcome != "completed" {
					item.State = receipt.Outcome
				}
			}
			return item
		case "approved":
			if _, e := s.Catalog.sendTeamsContent(ctx, s.Config(), client, d, credential, work, manifest); e != nil {
				item.State = "needs_refresh"
			} else {
				item.State = "sent"
			}
			return item
		case "awaiting_review", "started", "rejected", "unconfirmed":
			return item
		case "bound":
			u, e := readTeamsV2Upload(ctx, s.Catalog.reader(), work.ID)
			if e != nil {
				item.State = "unavailable"
				return item
			}
			var b teams.ContentBinding
			var selected []string
			if json.Unmarshal(run.BindingJSON, &b) != nil || json.Unmarshal(run.SelectedJSON, &selected) != nil {
				item.State = "unavailable"
				return item
			}
			inv, e := s.Catalog.teamsObserveV2Content(ctx, s.Config(), d, credential)
			if e != nil {
				item.State = "unavailable"
				return item
			}
			reg := teamsContentRegistration{Upload: u, Binding: b, Work: work, Inventory: inv, Selected: selected}
			if e = s.generateTeamsContent(ctx, s.Config(), client, d, credential, reg, manifest); e != nil {
				item.State = s.teamsContentFailureState(ctx, work.ID)
				return item
			}
			run, e = readTeamsContentRun(ctx, s.Catalog.reader(), work.ID)
			if e != nil {
				item.State = "unavailable"
				return item
			}
			item.State = run.State
			item.Payload = bytesClone(run.Payload)
			item.PayloadHash = run.PayloadHash
			return item
		default:
			return item
		}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		item.State = "unavailable"
		return item
	}
	if _, _, err = s.Catalog.loadTeamsModelPermission(ctx, d, credential); err != nil {
		item.State = "awaiting_model_permission"
		return item
	}
	settings, err := readTeamsSettings(ctx, s.Catalog.reader())
	if err != nil {
		item.State = "unavailable"
		return item
	}
	var grants int
	err = s.Catalog.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM teams_request_grants WHERE version_id=? AND library_id=? AND instance_id=? AND org_id=? AND installation_id=? AND config_hash=? AND settings_revision=?`, work.VersionID, d.ID, d.Instance, credential.OrgID, credential.InstallationID, work.ConfigSHA256, settings.SettingsRevision).Scan(&grants)
	if err != nil {
		item.State = "unavailable"
		return item
	}
	if grants != 1 {
		item.State = "awaiting_source_consent"
		return item
	}
	inv, err := s.Catalog.teamsObserveV2Content(ctx, s.Config(), d, credential)
	if err != nil {
		item.State = "unavailable"
		return item
	}
	reg, err := s.Catalog.registerTeamsContent(ctx, s.Config(), client, d, credential, work, inv, manifest)
	if err != nil {
		item.State = "unavailable"
		return item
	}
	if err = s.generateTeamsContent(ctx, s.Config(), client, d, credential, reg, manifest); err != nil {
		item.State = s.teamsContentFailureState(ctx, work.ID)
		return item
	}
	run, err = readTeamsContentRun(ctx, s.Catalog.reader(), work.ID)
	if err != nil {
		item.State = "unavailable"
		return item
	}
	item.State = run.State
	item.Payload = bytesClone(run.Payload)
	item.PayloadHash = run.PayloadHash
	return item
}

func (s *Server) teamsContentFailureState(ctx context.Context, workID string) string {
	run, err := readTeamsContentRun(ctx, s.Catalog.reader(), workID)
	if err == nil && run.State == "started" {
		return "started"
	}
	return "unavailable"
}

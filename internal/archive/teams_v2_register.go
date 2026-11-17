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

var errTeamsV2Pending = errors.New("Teams source ownership is waiting for reconciliation")

type teamsV2Registration struct {
	Upload      teamsV2Upload
	Binding     teams.SourceBinding
	Assignments []sourcewire.Assignment
	Snapshot    teamsV2Inventory
	Work        teams.Work
	Policy      teams.MetricsDefault
}

// registerTeamsV2 rechecks work, local policy, library identity and source
// revisions while holding the same sharing gate used by privacy mutations.
// Staging receipts never authorize reading or sending a result by themselves.
func (c *Catalog) registerTeamsV2(ctx context.Context, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential, requested teams.Work, snapshot teamsV2Inventory, manifest teams.CapabilityManifest) (teamsV2Registration, error) {
	var out teamsV2Registration
	if client == nil || credential == nil || manifest.AppVersion == "" {
		return out, errTeamsConsentStale
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return out, err
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	poll, err := client.Poll(ctx, teams.PollRequest{AppVersion: manifest.AppVersion, LibraryIDs: []string{d.ID}, CapabilityManifest: &manifest})
	if err != nil {
		return out, err
	}
	for _, cancellation := range poll.Cancellations {
		if cancellation.WorkID == requested.ID {
			return out, errTeamsConsentStale
		}
	}
	var current *teams.Work
	for i := range poll.Work {
		if poll.Work[i].ID == requested.ID {
			if current != nil {
				return out, errTeamsConsentStale
			}
			current = &poll.Work[i]
		}
	}
	if current == nil || current.LibraryID != d.ID || current.VersionID != requested.VersionID || current.ConfigSHA256 != requested.ConfigSHA256 || !bytes.Equal(current.Config, requested.Config) || current.EvaluationRevision != requested.EvaluationRevision || current.BaselineRevision != requested.BaselineRevision {
		return out, errTeamsConsentStale
	}
	watermark, err := client.SourceWatermark(ctx, d.ID)
	if err != nil {
		return out, err
	}
	upload, err := c.prepareTeamsV2Upload(ctx, *current, d, credential, snapshot, poll.MetricsDefault, watermark, manifest.AppVersion)
	if err != nil {
		return out, err
	}
	if _, err = client.AuthorizeSourceDisclosure(ctx, current.ID, upload.Authorization); err != nil {
		return out, err
	}
	for _, page := range upload.Pages {
		if _, err = client.StageSourcePage(ctx, current.ID, page); err != nil {
			return out, err
		}
	}
	sealed, err := client.SealSourceSnapshot(ctx, current.ID, upload.Seal)
	if err != nil {
		return out, err
	}
	sealedRaw, _ := json.Marshal(sealed)
	if err = c.writeTransaction(ctx, "teams-v2-sealed", func(tx *sql.Tx) error {
		currentUpload, e := readTeamsV2Upload(ctx, tx, current.ID)
		if e != nil {
			return e
		}
		if currentUpload.SnapshotID != upload.SnapshotID || currentUpload.InventoryHash != upload.InventoryHash || currentUpload.State == "abandoned" {
			return errTeamsConsentStale
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_v2_uploads SET sealed_json=?,state='sealed' WHERE work_id=?`, sealedRaw, current.ID)
		return e
	}); err != nil {
		return out, err
	}
	request := teams.SourceBindRequest{BindingID: upload.BindingID, SnapshotID: upload.SnapshotID, InventoryRevision: upload.InventoryRevision, InventorySHA256: upload.InventoryHash, LocalRevision: upload.LocalRevision, CatalogRevision: upload.LocalRevision, EvaluationRevision: upload.EvaluationRevision, ConfigSHA256: upload.ConfigHash, OwnershipRevision: sealed.OwnershipRevision, PrivacyRevision: sealed.PrivacyRevision}
	binding, err := client.BindSourceInventory(ctx, current.ID, request)
	if err != nil {
		return out, err
	}
	if binding.BindingState != "ready" {
		return out, errTeamsV2Pending
	}
	expected := make([]string, len(snapshot.Items))
	for i, item := range snapshot.Items {
		expected[i] = item.Token
	}
	assignments, err := client.SourceAssignments(ctx, current.ID, binding, expected)
	if err != nil {
		return out, err
	}
	bindingRaw, _ := json.Marshal(binding)
	assignmentsRaw, _ := json.Marshal(assignments)
	if err = c.writeTransaction(ctx, "teams-v2-bound", func(tx *sql.Tx) error {
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
		generation, e := readCacheGenerations(ctx, tx)
		if e != nil {
			return e
		}
		if !settings.Enabled || settings.SettingsRevision != upload.SettingsRevision || sourceRev != upload.SourceRevision || privacyRev != upload.PrivacyRevision || generation.all != upload.Generation {
			return errTeamsSnapshotChanged
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_v2_uploads SET binding_json=?,assignments_json=?,state='bound' WHERE work_id=? AND snapshot_id=? AND state='sealed'`, bindingRaw, assignmentsRaw, current.ID, upload.SnapshotID)
		return e
	}); err != nil {
		return out, err
	}
	upload.State = "bound"
	upload.SealedJSON = sealedRaw
	upload.BindingJSON = bindingRaw
	upload.AssignmentsJSON = assignmentsRaw
	return teamsV2Registration{Upload: upload, Binding: binding, Assignments: assignments, Snapshot: snapshot, Work: *current, Policy: poll.MetricsDefault}, nil
}

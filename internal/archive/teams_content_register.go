package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

type teamsContentRegistration struct {
	Upload    teamsV2Upload
	Binding   teams.ContentBinding
	Work      teams.Work
	Inventory teamsContentInventory
	Selected  []string
}

// Source metadata is disclosed only after the exact local question and model
// permission are in place. It does not authorize a provider handoff.
func (c *Catalog) registerTeamsContent(ctx context.Context, cfg Config, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential, requested teams.Work, inventory teamsContentInventory, manifest teams.CapabilityManifest) (teamsContentRegistration, error) {
	var out teamsContentRegistration
	if client == nil || credential == nil || !teamsBuildEligible(manifest, "source_snapshot_v2") || !teamsBuildEligible(manifest, "scoped_artifact_v1") || !inventory.Complete {
		return out, errTeamsConsentStale
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return out, err
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !teamsSourceConfigCurrent(cfg) {
		return out, errTeamsSnapshotChanged
	}
	selection, _, err := c.loadTeamsModelPermission(ctx, d, credential)
	if err != nil {
		return out, err
	}
	poll, err := client.Poll(ctx, teams.PollRequest{AppVersion: manifest.AppVersion, LibraryIDs: []string{d.ID}, CapabilityManifest: &manifest})
	if err != nil {
		return out, err
	}
	for _, v := range poll.Cancellations {
		if v.WorkID == requested.ID {
			return out, errTeamsConsentStale
		}
	}
	var work *teams.Work
	for i := range poll.Work {
		if poll.Work[i].ID == requested.ID {
			if work != nil {
				return out, errTeamsConsentStale
			}
			work = &poll.Work[i]
		}
	}
	if work == nil || work.LibraryID != d.ID || work.VersionID != requested.VersionID || work.ConfigSHA256 != requested.ConfigSHA256 || !bytes.Equal(work.Config, requested.Config) || work.EvaluationRevision != requested.EvaluationRevision {
		return out, errTeamsConsentStale
	}
	if _, err = teamsLLMFromWork(*work, manifest.AppVersion); err != nil {
		return out, err
	}
	selected := make([]string, 0, len(inventory.Candidates))
	for _, candidate := range inventory.Candidates {
		for _, item := range inventory.Items {
			if item.Token == candidate.ComponentToken && item.Protection == "public" {
				selected = append(selected, item.Token)
				break
			}
		}
	}
	if len(selected) == 0 {
		return out, errTeamsSourceUnavailable
	}
	sort.Strings(selected)
	expected, err := teamsContentClosure(inventory, selected)
	if err != nil {
		return out, err
	}
	watermark, err := client.SourceWatermark(ctx, d.ID)
	if err != nil {
		return out, err
	}
	upload, err := c.prepareTeamsV2Upload(ctx, *work, d, credential, inventory.teamsV2Inventory, poll.MetricsDefault, watermark, manifest)
	if err != nil {
		return out, err
	}
	var auth teams.SourceAuthorization
	if json.Unmarshal(upload.Authorization, &auth) != nil || auth.Mode != "reviewed" || auth.DecisionID == "" {
		return out, errTeamsConsentStale
	}
	if _, err = client.AuthorizeSourceDisclosure(ctx, work.ID, upload.Authorization); err != nil {
		return out, err
	}
	for _, page := range upload.Pages {
		if _, err = client.StageSourcePage(ctx, work.ID, page); err != nil {
			return out, err
		}
	}
	sealed, err := client.SealSourceSnapshot(ctx, work.ID, upload.Seal)
	if err != nil {
		return out, err
	}
	sealedRaw, _ := json.Marshal(sealed)
	if err = c.writeTransaction(ctx, "teams-content-sealed", func(tx *sql.Tx) error {
		current, e := readTeamsV2Upload(ctx, tx, work.ID)
		if e != nil {
			return e
		}
		if current.SnapshotID != upload.SnapshotID || current.InventoryHash != upload.InventoryHash || current.State == "abandoned" {
			return errTeamsConsentStale
		}
		if current.State == "bound" {
			return nil
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_v2_uploads SET sealed_json=?,state='sealed' WHERE work_id=? AND state='prepared'`, sealedRaw, work.ID)
		return e
	}); err != nil {
		return out, err
	}
	request := teams.ContentBindRequest{BindingID: upload.BindingID, SnapshotID: upload.SnapshotID, InventoryRevision: upload.InventoryRevision, InventorySHA256: upload.InventoryHash, LocalRevision: upload.LocalRevision, CatalogRevision: upload.LocalRevision, EvaluationRevision: upload.EvaluationRevision, ConfigSHA256: upload.ConfigHash, PrivacyRevision: sealed.PrivacyRevision, OwnershipRevision: sealed.OwnershipRevision, DecisionID: auth.DecisionID, Processing: teams.ContentProcessing{Execution: "local", Provider: "Claude", Model: selection.Model}, Components: selected}
	binding, err := client.BindContent(ctx, work.ID, d.ID, request, expected)
	if err != nil {
		return out, err
	}
	bindingRaw, _ := json.Marshal(binding)
	selectedRaw, _ := json.Marshal(selected)
	if err = c.writeTransaction(ctx, "teams-content-bound", func(tx *sql.Tx) error {
		var n int
		if e := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM teams_content_runs WHERE work_id=?`, work.ID).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			return errTeamsSnapshotChanged
		}
		res, e := tx.ExecContext(ctx, `UPDATE teams_v2_uploads SET binding_json=?,state='bound' WHERE work_id=? AND snapshot_id=? AND state='sealed'`, bindingRaw, work.ID, upload.SnapshotID)
		if e != nil {
			return e
		}
		changed, e := res.RowsAffected()
		if e != nil || changed != 1 {
			return errTeamsConsentStale
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO teams_content_runs(work_id,config,binding_json,selected_json,state) VALUES(?,?,?,?,'bound')`, work.ID, work.Config, bindingRaw, selectedRaw)
		return e
	}); err != nil {
		return out, err
	}
	upload.State = "bound"
	upload.SealedJSON = sealedRaw
	upload.BindingJSON = bindingRaw
	return teamsContentRegistration{upload, binding, *work, inventory, selected}, nil
}

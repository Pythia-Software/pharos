package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	// Decline and registration share this lease. A locally committed decline
	// wins even when its remote status response was lost or rejected.
	var declined bool
	if err = c.reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM teams_v2_declines WHERE work_id=?)`, requested.ID).Scan(&declined); err != nil {
		return out, err
	}
	if declined {
		return out, errTeamsConsentStale
	}
	if !teamsSourceConfigCurrent(cfg) {
		return out, errTeamsSnapshotChanged
	}
	// A contradictory same-snapshot scheduling row is already sufficient
	// to park. Check it under the lease before even polling the service.
	var priorSnapshot, priorState string
	e := c.reader().QueryRowContext(ctx, `SELECT snapshot_id,state FROM teams_v2_uploads WHERE work_id=?`, requested.ID).Scan(&priorSnapshot, &priorState)
	if e == nil && priorState == "prepared" {
		prior, retryErr := readTeamsContentRetry(ctx, c.reader(), requested.ID)
		if _, retryErr = teamsPreparedRetryTracking(prior, retryErr, priorSnapshot); retryErr != nil {
			return out, retryErr
		}
	} else if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return out, e
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
	checkedAt := poll.ServerTime
	if checkedAt.IsZero() {
		checkedAt = time.Now()
	}
	if _, err = teamsLLMFromWorkAt(*work, manifest.AppVersion, checkedAt); err != nil {
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
	upload, err := c.prepareTeamsV2UploadAt(ctx, *work, d, credential, inventory.teamsV2Inventory, poll.MetricsDefault, watermark, manifest, checkedAt)
	if err != nil {
		return out, err
	}
	if upload.State != "prepared" && upload.State != "sealed" {
		return out, errTeamsConsentStale
	}
	var auth teams.SourceAuthorization
	if json.Unmarshal(upload.Authorization, &auth) != nil || auth.Mode != "reviewed" || auth.DecisionID == "" {
		return out, errTeamsConsentStale
	}
	var sealed teams.SourceSealReceipt
	var sealedRaw []byte
	var retry teamsContentRetry
	tracked := true
	if upload.State == "sealed" {
		sealedRaw = upload.SealedJSON
		if json.Unmarshal(sealedRaw, &sealed) != nil {
			return out, errTeamsConsentStale
		}
		err = c.writeTransaction(ctx, "teams-content-check-attempt", func(tx *sql.Tx) error {
			current, e := readTeamsV2Upload(ctx, tx, work.ID)
			if e != nil {
				return e
			}
			if current.State != "sealed" || current.SnapshotID != upload.SnapshotID {
				return errTeamsConsentStale
			}
			retry, e = readTeamsContentRetry(ctx, tx, work.ID)
			if errors.Is(e, sql.ErrNoRows) {
				// Older sealed rows retain their exact snapshot and get one bind-only
				// check. Without its original receipt expiry no auto retry is inferred.
				retry = teamsContentRetry{Version: 1, Snapshot: upload.SnapshotID, State: "inflight", Last: time.Now().UTC()}
				return writeTeamsContentRetry(ctx, tx, work.ID, retry)
			}
			if e != nil {
				return e
			}
			if retry.Snapshot != upload.SnapshotID {
				return errTeamsContentRetryParked
			}
			if !work.ExpiresAt.IsZero() && work.ExpiresAt.Before(retry.WorkExpiry) {
				retry.WorkExpiry = work.ExpiresAt
			}
			retry, e = nextTeamsContentRetry(retry, time.Now().UTC(), poll.ServerTime)
			if e != nil {
				return e
			}
			return writeTeamsContentRetry(ctx, tx, work.ID, retry)
		})
		if err != nil {
			return out, err
		}
	} else {
		// Retry state cannot authorize or suppress a different prepared upload.
		// A same-snapshot row is ambiguous, so park before any disclosure.
		prior, e := readTeamsContentRetry(ctx, c.reader(), work.ID)
		tracked, e = teamsPreparedRetryTracking(prior, e, upload.SnapshotID)
		if e != nil {
			return out, e
		}

		receipt, e := client.AuthorizeSourceDisclosure(ctx, work.ID, upload.Authorization)
		if e != nil {
			return out, e
		}
		for _, page := range upload.Pages {
			if _, e = client.StageSourcePage(ctx, work.ID, page); e != nil {
				return out, e
			}
		}
		sealed, e = client.SealSourceSnapshot(ctx, work.ID, upload.Seal)
		if e != nil {
			return out, e
		}
		sealedRaw, _ = json.Marshal(sealed)
		retry = teamsContentRetry{Version: 1, Snapshot: upload.SnapshotID, State: "inflight", Last: time.Now().UTC(), AuthorizationExpiry: receipt.ExpiresAt, WorkExpiry: work.ExpiresAt}
		retryWriteFailed := false
		err = c.writeTransaction(ctx, "teams-content-sealed", func(tx *sql.Tx) error {
			current, e := readTeamsV2Upload(ctx, tx, work.ID)
			if e != nil {
				return e
			}
			if current.SnapshotID != upload.SnapshotID || current.InventoryHash != upload.InventoryHash || current.State != "prepared" {
				return errTeamsConsentStale
			}
			if tracked {
				if _, e = readTeamsContentRetry(ctx, tx, work.ID); !errors.Is(e, sql.ErrNoRows) {
					if e != nil {
						return e
					}
					return errTeamsContentRetryParked
				}
			}
			if _, e = tx.ExecContext(ctx, `UPDATE teams_v2_uploads SET sealed_json=?,state='sealed' WHERE work_id=? AND state='prepared'`, sealedRaw, work.ID); e != nil {
				return e
			}
			if !tracked {
				return nil
			}
			e = writeTeamsContentRetry(ctx, tx, work.ID, retry)
			retryWriteFailed = e != nil
			return e
		})
		if err != nil && retryWriteFailed {
			// Scheduling is optional. If the metadata write alone failed,
			// preserve the ordinary first bind, without a scheduled retry.
			// A later check can treat the sealed row as legacy and make one
			// bind-only check after storage recovers.
			tracked = false
			err = c.writeTransaction(ctx, "teams-content-sealed-without-retry", func(tx *sql.Tx) error {
				current, e := readTeamsV2Upload(ctx, tx, work.ID)
				if e != nil {
					return e
				}
				if current.State != "prepared" || current.SnapshotID != upload.SnapshotID || current.InventoryHash != upload.InventoryHash {
					return errTeamsConsentStale
				}
				_, e = tx.ExecContext(ctx, `UPDATE teams_v2_uploads SET sealed_json=?,state='sealed' WHERE work_id=? AND state='prepared'`, sealedRaw, work.ID)
				return e
			})
		}
		if err != nil {
			return out, err
		}
	}

	request := teams.ContentBindRequest{BindingID: upload.BindingID, SnapshotID: upload.SnapshotID, InventoryRevision: upload.InventoryRevision, InventorySHA256: upload.InventoryHash, LocalRevision: upload.LocalRevision, CatalogRevision: upload.LocalRevision, EvaluationRevision: upload.EvaluationRevision, ConfigSHA256: upload.ConfigHash, PrivacyRevision: sealed.PrivacyRevision, OwnershipRevision: sealed.OwnershipRevision, DecisionID: auth.DecisionID, Processing: teams.ContentProcessing{Execution: "local", Provider: "Claude", Model: selection.Model}, Components: selected}
	requestRaw, e := json.Marshal(request)
	if e != nil {
		return out, e
	}
	requestHash := teamsPayloadHash(requestRaw)
	if retry.RequestHash != "" && retry.RequestHash != requestHash {
		return out, errTeamsConsentStale
	}
	if tracked && retry.RequestHash == "" {
		retry.RequestHash = requestHash
		// An in-flight row already parks on restart. If this scheduling-only
		// update fails, the first bind remains allowed without a retry.
		if e = c.writeTransaction(ctx, "teams-content-check-bytes", func(tx *sql.Tx) error { return writeTeamsContentRetry(ctx, tx, work.ID, retry) }); e != nil {
			tracked = false
		}
	}
	binding, err := client.BindContent(ctx, work.ID, d.ID, request, expected)
	if err != nil {
		if !tracked {
			return out, err
		}
		retry.State = "parked"
		if teamsContentSafetyInProgress(err) && !poll.ServerTime.IsZero() && !retry.AuthorizationExpiry.IsZero() && !retry.WorkExpiry.IsZero() && poll.ServerTime.Before(retry.AuthorizationExpiry) && poll.ServerTime.Before(retry.WorkExpiry) {
			now := time.Now().UTC()
			if retry.First.IsZero() {
				retry.First = now
				retry.Last = now
			}
			if retry.Count < 4 && !now.After(retry.First.Add(2*time.Minute)) {
				retry.State = "waiting"
			}
		}
		if e := c.writeTransaction(ctx, "teams-content-check-result", func(tx *sql.Tx) error { return writeTeamsContentRetry(ctx, tx, work.ID, retry) }); e != nil {
			return out, e
		}
		if retry.State == "waiting" {
			return out, errTeamsContentRetryWaiting
		}
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
		if e != nil {
			return e
		}
		if tracked {
			// A retained scheduling row is inert once the run exists; do not
			// roll back a valid binding just because optional cleanup failed.
			_, _ = tx.ExecContext(ctx, `DELETE FROM meta WHERE key=?`, teamsContentRetryKey(work.ID))
		}
		return nil
	}); err != nil {
		return out, err
	}
	upload.State = "bound"
	upload.SealedJSON = sealedRaw
	upload.BindingJSON = bindingRaw
	return teamsContentRegistration{upload, binding, *work, inventory, selected}, nil
}

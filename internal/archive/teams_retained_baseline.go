package archive

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// teamsRetainedBaselineDraft reads an independently captured complete roster
// and every current local protection under the caller's capture lease. It does
// not release a hold or grant any transfer authority.
func teamsRetainedBaselineDraft(ctx context.Context, tx *sql.Tx, cfg Config, d LibraryDescriptor, credential *teams.EnrollmentCredential) (teamsV2Inventory, teamsOriginBatchScope, error) {
	return teamsRetainedBaselineDraftAllowAttempt(ctx, tx, cfg, d, credential, "")
}

// The only unsettled row allowed during a tightening send is the exact
// locally prepared restrictive observation whose bytes are being checked.
func teamsRetainedBaselineDraftAllowAttempt(ctx context.Context, tx *sql.Tx, cfg Config, d LibraryDescriptor, credential *teams.EnrollmentCredential, allowAttempt string) (teamsV2Inventory, teamsOriginBatchScope, error) {
	var empty teamsV2Inventory
	var scope teamsOriginBatchScope
	if !d.ManagedBirth || credential == nil || credential.LibraryID != d.ID {
		return empty, scope, errTeamsProtectionHeld
	}
	settings, err := readTeamsSettings(ctx, tx)
	if err != nil {
		return empty, scope, err
	}
	local, err := readTeamsProtectionAcknowledgement(ctx, tx, d, credential)
	if err != nil {
		return empty, scope, err
	}
	transfer, err := readTeamsSourceTransferAcknowledgement(ctx, tx, d, credential)
	if err != nil {
		return empty, scope, err
	}
	key := credential.SigningKey()
	if len(key) != ed25519.PrivateKeySize {
		clear(key)
		return empty, scope, errTeamsNotEnrolled
	}
	fingerprint := teamsPayloadHash(key.Public().(ed25519.PublicKey))
	clear(key)
	if !local || !transfer || !settings.Enabled || settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.OrgID != credential.OrgID || settings.InstallationID != credential.InstallationID || settings.KeyFingerprint != fingerprint {
		return empty, scope, errTeamsConsentStale
	}
	var unsafe bool
	// Retained history outside the live roster cannot be inferred away. This
	// narrow first lane never clears recovery guards or any earlier 031 outcome.
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM teams_protection_recovery_guards) OR EXISTS(SELECT 1 FROM teams_origin_outbox WHERE (state<>'accepted' OR receipt_json IS NULL) AND NOT (observation_id=? AND state='prepared' AND receipt_json IS NULL)) OR EXISTS(SELECT 1 FROM teams_protection_assertions a WHERE a.active=1 AND NOT EXISTS(SELECT 1 FROM conversations c WHERE a.provider<>'' AND a.account<>'' AND a.native_id<>'' AND c.provider=a.provider AND c.account=a.account AND c.native_id=a.native_id))`, allowAttempt).Scan(&unsafe)
	if err != nil {
		return empty, scope, err
	}
	if unsafe {
		return empty, scope, errTeamsProtectionHeld
	}
	snapshot, err := teamsIndependentRosterProof(ctx, tx, cfg, d, credential)
	if err != nil {
		return empty, scope, err
	}
	if !snapshot.Complete || snapshot.Coverage != (sourcewire.Coverage{}) || snapshot.Items == nil {
		return empty, scope, errTeamsSourceUnavailable
	}
	h, err := sourcewire.NewInventoryHasher(snapshot.Complete, snapshot.Coverage)
	if err != nil {
		return empty, scope, err
	}
	threads := 0
	for _, item := range snapshot.Items {
		if err = h.Add(item); err != nil {
			return empty, scope, err
		}
		if item.Kind == "codex_thread" {
			threads++
		}
	}
	if threads < 1 || threads > 500 {
		return empty, scope, errTeamsSourceUnavailable
	}
	hash, _, err := h.Finish()
	if err != nil {
		return empty, scope, err
	}
	scope = teamsOriginBatchScope{inventoryHash: hash, settingsRevision: settings.SettingsRevision, sourceRevision: snapshot.SourceRevision, privacyRevision: snapshot.PrivacyRevision, generation: snapshot.Generation}
	return snapshot, scope, nil
}

// An authenticated complete scan must preserve its monotone, retained floor.
// Its contents are reconciled with both local evidence and accepted receipts.
func teamsBaselineServerClear(scan *teamsRecoveryOriginScan, now time.Time) bool {
	return scan != nil && !scan.held && len(scan.byToken) == len(scan.byWitness) && scan.start.UnacceptedFloor >= 0 && scan.last.UnacceptedFloor >= scan.start.UnacceptedFloor && scan.last.NextCursor == "" && now.UTC().Unix() < scan.start.ExpiresAt
}

// A local baseline may not erase a server floor or prior accepted receipt.
// Every server origin must match one local witness and a retained accepted
// receipt; extra, ambiguous or less restrictive evidence stays held.
func teamsBaselineReconcileHistory(ctx context.Context, tx *sql.Tx, d LibraryDescriptor, credential *teams.EnrollmentCredential, snapshot teamsV2Inventory, scan *teamsRecoveryOriginScan) error {
	return teamsBaselineReconcileHistoryMode(ctx, tx, d, credential, snapshot, scan, false)
}

func teamsBaselineReconcileHistoryMode(ctx context.Context, tx *sql.Tx, d LibraryDescriptor, credential *teams.EnrollmentCredential, snapshot teamsV2Inventory, scan *teamsRecoveryOriginScan, tightening bool) error {
	if credential == nil || scan == nil {
		return errTeamsProtectionHeld
	}
	local := map[string]sourcewire.Item{}
	for _, item := range snapshot.Items {
		if _, ok := local[item.Token]; ok {
			return errTeamsProtectionHeld
		}
		local[item.Token] = item
	}
	for token, origin := range scan.byToken {
		item, ok := local[token]
		if !ok || item.Witness != origin.Witness {
			return fmt.Errorf("origin evidence: %w", errTeamsProtectionHeld)
		}
		stronger := (origin.Protection == "non_private" && (item.Protection == "private" || item.Protection == "unknown")) || (origin.Protection == "unknown" && item.Protection == "private") || (origin.EvidenceRevision != item.EvidenceRevision && ((origin.Protection == "explicit_private" && item.Protection == "private") || (origin.Protection == "unknown" && item.Protection == "unknown")))
		if item.EvidenceRevision != origin.EvidenceRevision && !(tightening && stronger) {
			return fmt.Errorf("origin evidence: %w", errTeamsProtectionHeld)
		}
		if tightening && stronger {
			continue
		}
		switch item.Protection {
		case "private":
			if origin.Protection != "explicit_private" && origin.Protection != "inherited_private" && origin.Protection != "flagged" {
				return errTeamsProtectionHeld
			}
		case "unknown":
			if origin.Protection != "unknown" {
				return errTeamsProtectionHeld
			}
		case "public":
			if origin.Protection != "non_private" || origin.FlagFloor == "unknown" || (origin.Classification.Required && origin.Classification.State != "clear") {
				return fmt.Errorf("public server protection: %w", errTeamsProtectionHeld)
			}
		default:
			return errTeamsProtectionHeld
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT origin,org_id,person_id,installation_id,library_id,instance_id,observation_json,observation_sha256,receipt_json,state FROM teams_origin_outbox ORDER BY observation_id LIMIT 2001`)
	if err != nil {
		return err
	}
	defer rows.Close()
	matched := map[string]bool{}
	highest := map[string]int64{}
	count := 0
	for rows.Next() {
		var origin, org, person, installation, library, instance, hash, state string
		var body, receipt []byte
		if err = rows.Scan(&origin, &org, &person, &installation, &library, &instance, &body, &hash, &receipt, &state); err != nil {
			return err
		}
		count++
		if count > 2000 {
			return errTeamsProtectionHeld
		}
		if origin != credential.Origin || org != credential.OrgID || person != credential.PersonID || installation != credential.InstallationID || library != d.ID || instance != d.Instance || state != "accepted" {
			return errTeamsProtectionHeld
		}
		observation, e := sourcewire.DecodeProtectionObservation(body)
		var accepted sourcewire.ProtectionReceipt
		id, idErr := sourcewire.ProtectionObservationID(observation, credential.InstallationID, d.ID)
		if e != nil || idErr != nil || id != observation.ObservationID || hash != sourcewire.ProtectionDigest(body) || !teams.ValidUniqueJSON(receipt) || json.Unmarshal(receipt, &accepted) != nil || accepted.Status != "accepted" || !teamsOriginReceiptMatches(observation, body, accepted) {
			return fmt.Errorf("retained receipt: %w", errTeamsProtectionHeld)
		}
		if observation.Revision > highest[observation.Token] {
			highest[observation.Token] = observation.Revision
		}
		current, ok := scan.byToken[observation.Token]
		if !ok || current.Witness != observation.Witness {
			return fmt.Errorf("receipt origin: %w", errTeamsProtectionHeld)
		}
		if (observation.Protection == "explicit_private" || observation.Protection == "inherited_private" || observation.Protection == "flagged") && current.Protection != observation.Protection && current.Protection != "explicit_private" {
			return errTeamsProtectionHeld
		}
		if observation.Protection == "unknown" && current.Protection == "non_private" {
			return errTeamsProtectionHeld
		}
		if current.Revision == observation.Revision && current.Protection == observation.Protection && current.EvidenceRevision == observation.EvidenceRevision {
			matched[observation.Token] = true
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for token, revision := range highest {
		if scan.byToken[token].Revision != revision {
			return fmt.Errorf("server revision rollback: %w", errTeamsProtectionHeld)
		}
	}
	if len(matched) != len(scan.byToken) {
		return fmt.Errorf("origin revision: %w", errTeamsProtectionHeld)
	}
	return nil
}

func teamsCheckRetainedBaselineFloor(ctx context.Context, tx *sql.Tx, scan *teamsRecoveryOriginScan) error {
	var key string
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT r.run_key,r.start_receipt_json FROM teams_protection_baseline b JOIN teams_origin_runs r ON r.run_key=b.proposal_sha256 AND r.token='baseline' AND r.state='completed' WHERE b.id=1 AND b.state IN ('current','held')`).Scan(&key, &raw); err != nil {
		return errTeamsProtectionHeld
	}
	var start sourcewire.RecoveryStartReceipt
	if !teams.ValidUniqueJSON(raw) || json.Unmarshal(raw, &start) != nil {
		return errTeamsProtectionHeld
	}
	rows, err := tx.QueryContext(ctx, `SELECT page_index,request_json,receipt_json FROM teams_origin_run_pages WHERE run_key=? ORDER BY page_index`, key)
	if err != nil {
		return err
	}
	defer rows.Close()
	var requests []sourcewire.RecoveryPageRequest
	var receipts []sourcewire.RecoveryPageReceipt
	for rows.Next() {
		var i int
		var reqRaw, resRaw []byte
		if err = rows.Scan(&i, &reqRaw, &resRaw); err != nil {
			return err
		}
		req, e := sourcewire.DecodeRecoveryPage(reqRaw)
		var res sourcewire.RecoveryPageReceipt
		if i != len(requests) || e != nil || !teams.ValidUniqueJSON(resRaw) || json.Unmarshal(resRaw, &res) != nil {
			return errTeamsProtectionHeld
		}
		requests = append(requests, req)
		receipts = append(receipts, res)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	prior, err := teamsBuildRecoveryOriginScan(start, requests, receipts)
	if err != nil || !teamsBaselineScanDominates(scan, prior) {
		return errTeamsProtectionHeld
	}
	return nil
}

// A newer complete scan can replace older scan bytes only if it retains every
// witness, monotone floor and all previously restrictive server protections.
func teamsBaselineScanDominates(newer, older *teamsRecoveryOriginScan) bool {
	if newer == nil || older == nil || newer.held || older.held || newer.last.UnacceptedFloor < older.last.UnacceptedFloor {
		return false
	}
	for token, old := range older.byToken {
		now, ok := newer.byToken[token]
		if !ok || now.Witness != old.Witness || now.Revision < old.Revision {
			return false
		}
		classificationClear := old.Classification.Required && old.Classification.State != "clear" && now.Classification.Required && now.Classification.State == "clear" && now.Classification.EvidenceRevision == now.EvidenceRevision && validRecordHash(now.Classification.PolicySHA256)
		if now.Revision == old.Revision && (now.Protection != old.Protection || now.EvidenceRevision != old.EvidenceRevision || now.FlagFloor != old.FlagFloor || (now.Classification != old.Classification && !classificationClear)) {
			return false
		}
		if old.FlagFloor != "" && now.FlagFloor != old.FlagFloor {
			return false
		}
		if old.Classification.Required && old.Classification.State != "clear" && (!now.Classification.Required || (now.Classification.State != old.Classification.State && !(now.Classification.State == "clear" && now.Classification.EvidenceRevision == now.EvidenceRevision && validRecordHash(now.Classification.PolicySHA256)))) {
			return false
		}
		if (old.Protection == "explicit_private" || old.Protection == "inherited_private" || old.Protection == "flagged") && now.Protection != old.Protection && now.Protection != "explicit_private" {
			return false
		}
		if old.Protection == "unknown" && now.Protection == "non_private" {
			return false
		}
	}
	return true
}

func teamsPruneSupersededBaselineRuns(ctx context.Context, tx *sql.Tx, d LibraryDescriptor, credential *teams.EnrollmentCredential, currentRun, token string, scan *teamsRecoveryOriginScan, allowHeld bool) error {
	rows, err := tx.QueryContext(ctx, `SELECT run_key,start_receipt_json FROM teams_origin_runs WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND token=? AND run_key<>? AND state='completed' ORDER BY rowid LIMIT 8`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, token, currentRun)
	if err != nil {
		return err
	}
	type candidate struct {
		key   string
		start []byte
	}
	var candidates []candidate
	for rows.Next() {
		var x candidate
		if err = rows.Scan(&x.key, &x.start); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, old := range candidates {
		if len(old.start) == 0 {
			// A definitive restart response before a start receipt carries no
			// server floor. Retire only the exact empty completed attempt.
			result, e := tx.ExecContext(ctx, `DELETE FROM teams_origin_runs WHERE run_key=? AND token=? AND state='completed' AND start_receipt_json IS NULL AND NOT EXISTS(SELECT 1 FROM teams_origin_run_pages WHERE run_key=?)`, old.key, token, old.key)
			if e != nil {
				return e
			}
			n, e := result.RowsAffected()
			if e != nil {
				return e
			}
			if n != 1 {
				return errTeamsProtectionHeld
			}
			continue
		}
		var start sourcewire.RecoveryStartReceipt
		if !teams.ValidUniqueJSON(old.start) || json.Unmarshal(old.start, &start) != nil {
			return errTeamsProtectionHeld
		}
		pages, err := tx.QueryContext(ctx, `SELECT page_index,request_json,receipt_json FROM teams_origin_run_pages WHERE run_key=? ORDER BY page_index`, old.key)
		if err != nil {
			return err
		}
		var requests []sourcewire.RecoveryPageRequest
		var receipts []sourcewire.RecoveryPageReceipt
		pending := false
		for pages.Next() {
			var i int
			var request, receipt []byte
			if err = pages.Scan(&i, &request, &receipt); err != nil {
				pages.Close()
				return err
			}
			req, e := sourcewire.DecodeRecoveryPage(request)
			var res sourcewire.RecoveryPageReceipt
			if i != len(requests) || e != nil || pending {
				pages.Close()
				return errTeamsProtectionHeld
			}
			requests = append(requests, req)
			if len(receipt) == 0 {
				pending = true
				continue
			}
			if !teams.ValidUniqueJSON(receipt) || json.Unmarshal(receipt, &res) != nil {
				pages.Close()
				return errTeamsProtectionHeld
			}
			receipts = append(receipts, res)
		}
		err = pages.Err()
		pages.Close()
		if err != nil {
			return err
		}
		if pending {
			expected := ""
			if len(receipts) > 0 {
				expected = receipts[len(receipts)-1].NextCursor
			}
			if requests[len(requests)-1].Cursor != expected || (len(receipts) > 0 && expected == "") {
				return errTeamsProtectionHeld
			}
			requests = requests[:len(receipts)]
		}
		if len(receipts) == 0 {
			if start.Held || scan.held || scan.last.UnacceptedFloor < start.UnacceptedFloor {
				return errTeamsProtectionHeld
			}
			if _, e := tx.ExecContext(ctx, `DELETE FROM teams_origin_runs WHERE run_key=? AND token=? AND state='completed'`, old.key, token); e != nil {
				return e
			}
			continue
		}
		// A complete newer scan can dominate an authenticated old prefix.
		// The original continuation remains intact until this deletion.
		if last := receipts[len(receipts)-1]; last.NextCursor != "" {
			if len(last.Items) == 0 {
				return errTeamsProtectionHeld
			}
			receipts[len(receipts)-1].NextCursor = ""
		}
		previous, e := teamsBuildRecoveryOriginScan(start, requests, receipts)
		comparison := scan
		if e == nil && allowHeld {
			oldCopy, newCopy := *previous, *scan
			oldCopy.held = false
			newCopy.held = false
			previous = &oldCopy
			comparison = &newCopy
		}
		if e != nil || !teamsBaselineScanDominates(comparison, previous) {
			return errTeamsProtectionHeld
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM teams_origin_runs WHERE run_key=? AND token=? AND state='completed'`, old.key, token); e != nil {
			return e
		}
	}
	return nil
}

type teamsBaselineAnchor struct {
	state, library, instance string
	generation               int64
}

func teamsReadBaselineAnchor(ctx context.Context, tx *sql.Tx, d LibraryDescriptor) (teamsBaselineAnchor, error) {
	var a teamsBaselineAnchor
	if err := tx.QueryRowContext(ctx, `SELECT state,generation,library_id,instance_id FROM teams_protection_baseline WHERE id=1`).Scan(&a.state, &a.generation, &a.library, &a.instance); err != nil {
		return a, err
	}
	if (a.state != "held" && a.state != "current") || a.generation < 0 || a.generation >= math.MaxInt64-1 || !((a.library == "" && a.instance == "") || (a.library == d.ID && a.instance == d.Instance)) {
		return a, errTeamsProtectionHeld
	}
	return a, nil
}

// teamsProduceRetainedBaseline bootstraps a new managed birth only after the
// authenticated bounded server scan reconciles every retained server origin.
// Conflicting or uncertain history remains held for explicit recovery.
func (c *Catalog) teamsProduceRetainedBaseline(ctx context.Context, cfg Config, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential) error {
	if !d.ManagedBirth || c.teamsCheckpoint == nil || c.teamsCheckpoint.writer.validateBinding == nil || client == nil || !client.MatchesCredential(credential) {
		return errTeamsProtectionHeld
	}
	if err := c.teamsPilotCheckpointCurrent(ctx, d); err != nil {
		return err
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	censusCtx, release, err := teamsCensusCaptureLease(ctx, cfg)
	if err != nil {
		lease.Close()
		return err
	}
	// A durable zero attempt marker proves that no PUT began. Retire only
	// those exact unsent rows under the same sharing/capture barriers.
	err = c.writeTransaction(censusCtx, "teams-baseline-retire-unsent", func(tx *sql.Tx) error {
		_, e := tx.ExecContext(censusCtx, `DELETE FROM teams_origin_outbox WHERE observation_id IN (SELECT o.observation_id FROM teams_origin_outbox o JOIN teams_origin_attempts a ON a.observation_id=o.observation_id WHERE o.origin=? AND o.org_id=? AND o.person_id=? AND o.installation_id=? AND o.library_id=? AND o.instance_id=? AND o.state IN ('prepared','held') AND o.receipt_json IS NULL AND ((o.state='prepared' AND a.first_attempt_at=0 AND a.terminal_code='') OR (a.first_attempt_at=0 AND a.terminal_code='first_rejected_source_issuance_expired') OR (o.state='prepared' AND a.first_attempt_at>0 AND a.terminal_code IN ('first_rejected_source_issuance_expired','first_rejected_source_resync_required','first_rejected_stale_protection_revision'))) ORDER BY o.observation_id LIMIT 8)`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance)
		return e
	})
	if err != nil {
		release()
		lease.Close()
		return err
	}
	tx, err := c.reader().BeginTx(censusCtx, nil)
	if err != nil {
		release()
		lease.Close()
		return err
	}
	anchor, err := teamsReadBaselineAnchor(censusCtx, tx, d)
	var snapshot teamsV2Inventory
	var scope teamsOriginBatchScope
	if err == nil {
		snapshot, scope, err = teamsRetainedBaselineDraft(censusCtx, tx, cfg, d, credential)
	}
	_ = tx.Rollback()
	release()
	lease.Close()
	if err != nil {
		return err
	}
	check := func(ctx context.Context, tx *sql.Tx) error {
		currentAnchor, e := teamsReadBaselineAnchor(ctx, tx, d)
		if e != nil {
			return e
		}
		if currentAnchor != anchor {
			return errTeamsSnapshotChanged
		}
		current, fresh, e := teamsRetainedBaselineDraft(ctx, tx, cfg, d, credential)
		if e != nil {
			return e
		}
		if fresh != scope || !teamsSamePilotSnapshot(current, snapshot) {
			return errTeamsSnapshotChanged
		}
		return nil
	}
	scan, err := c.teamsRecoveryPageForScope(ctx, cfg, client, d, credential, "", snapshot, scope, "baseline", check)
	if err != nil {
		return err
	}
	if !teamsBaselineServerClear(scan, time.Now()) {
		return errTeamsProtectionHeld
	}
	if attempted, e := c.teamsTightenRetainedOrigin(ctx, cfg, client, d, credential, snapshot, scope, scan, check); attempted || e != nil {
		if e != nil {
			return e
		}
		return errTeamsPilotMore
	}
	lease, err = c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	censusCtx, release, err = teamsCensusCaptureLease(ctx, cfg)
	if err != nil {
		return err
	}
	defer release()
	if err = c.teamsPilotCheckpointCurrent(censusCtx, d); err != nil {
		return err
	}
	return c.writeTransaction(censusCtx, "teams-retained-baseline", func(tx *sql.Tx) error {
		if e := check(censusCtx, tx); e != nil {
			return e
		}
		if e := teamsRequireRetainedRecoveryFloorIn(censusCtx, tx, d, credential, scan); e != nil {
			return e
		}
		if e := teamsBaselineReconcileHistory(censusCtx, tx, d, credential, snapshot, scan); e != nil {
			return fmt.Errorf("reconcile retained origins: %w", e)
		}
		var run string
		e := tx.QueryRowContext(censusCtx, `SELECT run_key FROM teams_origin_runs WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND work_id='' AND snapshot_id='' AND inventory_sha256=? AND settings_revision=? AND source_revision=? AND privacy_revision=? AND cache_generation=? AND token='baseline' AND state='ready' AND json_extract(start_receipt_json,'$.run_id')=? AND expires_at>?`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, scope.inventoryHash, scope.settingsRevision, scope.sourceRevision, scope.privacyRevision, scope.generation, scan.start.RunID, time.Now().UTC().Unix()).Scan(&run)
		if errors.Is(e, sql.ErrNoRows) {
			return errTeamsProtectionHeld
		}
		if e != nil {
			return e
		}
		var generation int64
		if e = tx.QueryRowContext(censusCtx, `SELECT generation FROM teams_protection_baseline WHERE id=1`).Scan(&generation); e != nil {
			return e
		}
		if generation >= math.MaxInt64-1 {
			return errTeamsProtectionHeld
		}
		var mapping int64
		if e = tx.QueryRowContext(censusCtx, `SELECT COALESCE(MAX(revision),0) FROM teams_source_mapping_revisions`).Scan(&mapping); e != nil {
			return e
		}
		result, e := tx.ExecContext(censusCtx, `UPDATE teams_protection_baseline SET state='current',generation=generation+1,library_id=?,instance_id=?,policy_sha256=?,inventory_sha256=?,proposal_sha256=?,source_revision=?,privacy_revision=?,mapping_revision=?,acknowledged_policy=1 WHERE id=1 AND state=? AND generation=? AND library_id=? AND instance_id=?`, d.ID, d.Instance, teamsLocalProtectionPolicyDigest(), scope.inventoryHash, run, scope.sourceRevision, scope.privacyRevision, mapping, anchor.state, anchor.generation, anchor.library, anchor.instance)
		if e != nil {
			return e
		}
		changed, e := result.RowsAffected()
		if e != nil || changed != 1 {
			return errTeamsSnapshotChanged
		}
		result, e = tx.ExecContext(censusCtx, `UPDATE teams_origin_runs SET state='completed' WHERE run_key=? AND token='baseline' AND state='ready'`, run)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil || n != 1 {
			return errTeamsProtectionHeld
		}
		if e = teamsPruneSupersededBaselineRuns(censusCtx, tx, d, credential, run, "baseline", scan, false); e != nil {
			return fmt.Errorf("retain baseline history: %w", e)
		}
		if e = teamsCompactRetainedReceiptsIn(censusCtx, tx, d, credential, scan); e != nil {
			return fmt.Errorf("compact superseded receipts: %w", e)
		}
		return nil
	})
}

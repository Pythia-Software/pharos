package archive

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// Every retained authenticated start/page contributes to the monotone
// enrollment floor, including safety and interrupted scans. The checkpointed
// journal bounds both rows and bytes; malformed retained authority holds.
func teamsRetainedRecoveryFloorIn(ctx context.Context, tx *sql.Tx, d LibraryDescriptor, credential *teams.EnrollmentCredential, exclude string) (int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.start_receipt_json,p.receipt_json FROM teams_origin_runs r LEFT JOIN teams_origin_run_pages p ON p.run_key=r.run_key WHERE r.origin=? AND r.org_id=? AND r.person_id=? AND r.installation_id=? AND r.library_id=? AND r.instance_id=? AND r.run_key<>? ORDER BY r.rowid,p.page_index`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, exclude)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var floor int64
	count := 0
	for rows.Next() {
		var startRaw, pageRaw []byte
		if err = rows.Scan(&startRaw, &pageRaw); err != nil {
			return 0, err
		}
		count++
		if count > teamsPilotMaxRetainedRuns*(teamsRecoveryMaxOriginPages+1) {
			return 0, errTeamsProtectionHeld
		}
		if len(startRaw) > 0 {
			var v sourcewire.RecoveryStartReceipt
			if !teams.ValidUniqueJSON(startRaw) || json.Unmarshal(startRaw, &v) != nil || v.UnacceptedFloor < 0 {
				return 0, errTeamsProtectionHeld
			}
			floor = max(floor, v.UnacceptedFloor)
		}
		if len(pageRaw) > 0 {
			var v sourcewire.RecoveryPageReceipt
			if !teams.ValidUniqueJSON(pageRaw) || json.Unmarshal(pageRaw, &v) != nil || v.UnacceptedFloor < 0 {
				return 0, errTeamsProtectionHeld
			}
			floor = max(floor, v.UnacceptedFloor)
		}
	}
	return floor, rows.Err()
}
func teamsRequireRetainedRecoveryFloorIn(ctx context.Context, tx *sql.Tx, d LibraryDescriptor, credential *teams.EnrollmentCredential, scan *teamsRecoveryOriginScan) error {
	if scan == nil {
		return errTeamsProtectionHeld
	}
	floor, err := teamsRetainedRecoveryFloorIn(ctx, tx, d, credential, "")
	if err != nil {
		return err
	}
	if scan.last.UnacceptedFloor < floor {
		return errTeamsProtectionHeld
	}
	return nil
}

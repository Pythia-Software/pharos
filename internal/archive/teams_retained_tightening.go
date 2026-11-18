package archive

import (
	"context"
	"database/sql"
	"errors"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// teamsTightenRetainedOrigin carries one strictly stronger local decision while
// the baseline stays held. Every retained receipt and server floor is checked
// first; an ambiguous effect still has to resolve through its original bytes.
func (c *Catalog) teamsTightenRetainedOrigin(ctx context.Context, cfg Config, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential, snapshot teamsV2Inventory, scope teamsOriginBatchScope, scan *teamsRecoveryOriginScan, check func(context.Context, *sql.Tx) error) (bool, error) {
	lease, err := c.teamsSharingLease()
	if err != nil {
		return false, err
	}
	locked, release, err := teamsCensusCaptureLease(ctx, cfg)
	if err != nil {
		lease.Close()
		return false, err
	}
	var body []byte
	scope.tightening = true
	err = c.writeTransaction(locked, "teams-retained-tightening", func(tx *sql.Tx) error {
		if e := check(locked, tx); e != nil {
			return e
		}
		// Only a complete retained baseline is a valid historical floor. A new
		// birth with no prior baseline has nothing to tighten.
		var proposal string
		if e := tx.QueryRowContext(locked, `SELECT proposal_sha256 FROM teams_protection_baseline WHERE id=1`).Scan(&proposal); e != nil {
			return e
		}
		if proposal == "" {
			return nil
		}
		if e := teamsRequireRetainedRecoveryFloorIn(locked, tx, d, credential, scan); e != nil {
			return e
		}
		if e := teamsCheckRetainedBaselineFloor(locked, tx, scan); e != nil {
			return e
		}
		if e := teamsBaselineReconcileHistoryMode(locked, tx, d, credential, snapshot, scan, true); e != nil {
			return e
		}
		for _, item := range snapshot.Items {
			if item.Kind != "codex_thread" {
				continue
			}
			old, ok := scan.byToken[item.Token]
			if !ok || old.Witness != item.Witness {
				continue
			}
			stronger := (old.Protection == "non_private" && (item.Protection == "private" || item.Protection == "unknown")) || (old.Protection == "unknown" && item.Protection == "private") || (old.EvidenceRevision != item.EvidenceRevision && ((old.Protection == "explicit_private" && item.Protection == "private") || (old.Protection == "unknown" && item.Protection == "unknown")))
			if !stronger {
				continue
			}
			var e error
			body, e = teamsJournalOriginIn(locked, tx, d, credential, teams.Work{}, snapshot, item.Token, scan, scope)
			if e != nil {
				return e
			}
			observation, e := sourcewire.DecodeProtectionObservation(body)
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(locked, `INSERT OR IGNORE INTO teams_origin_attempts(observation_id,first_attempt_at) VALUES(?,0)`, observation.ObservationID)
			return e
		}
		return nil
	})
	release()
	lease.Close()
	if err != nil {
		return false, err
	}
	if len(body) == 0 {
		return false, nil
	}
	observation, err := sourcewire.DecodeProtectionObservation(body)
	if err != nil {
		return true, err
	}
	err = c.sendTeamsLocalOrigin(ctx, cfg, client, d, credential, snapshot, scope, observation.ObservationID, false)
	if errors.Is(err, errTeamsPilotPending) {
		return true, err
	}
	return true, err
}

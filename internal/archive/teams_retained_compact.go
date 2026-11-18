package archive

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// Compact only receipts strictly superseded by the exact current signed scan.
// The newest accepted receipt and the complete scan remain in the same
// checkpointed transaction; uncertain and held observations are never touched.
func teamsCompactRetainedReceiptsIn(ctx context.Context, tx *sql.Tx, d LibraryDescriptor, credential *teams.EnrollmentCredential, scan *teamsRecoveryOriginScan) error {
	type entry struct {
		id          string
		body        []byte
		observation sourcewire.ProtectionObservation
	}
	rows, err := tx.QueryContext(ctx, `SELECT observation_id,observation_json,observation_sha256,receipt_json FROM teams_origin_outbox WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND state='accepted' ORDER BY rowid LIMIT 2001`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance)
	if err != nil {
		return err
	}
	var entries []entry
	latest := map[string]entry{}
	for rows.Next() {
		var x entry
		var hash string
		var receipt []byte
		if err = rows.Scan(&x.id, &x.body, &hash, &receipt); err != nil {
			rows.Close()
			return err
		}
		if len(entries) >= 2000 {
			rows.Close()
			return errTeamsProtectionHeld
		}
		x.observation, err = sourcewire.DecodeProtectionObservation(x.body)
		if err != nil {
			rows.Close()
			return errTeamsProtectionHeld
		}
		var accepted sourcewire.ProtectionReceipt
		id, e := sourcewire.ProtectionObservationID(x.observation, credential.InstallationID, d.ID)
		if e != nil || id != x.id || hash != sourcewire.ProtectionDigest(x.body) || !teams.ValidUniqueJSON(receipt) || json.Unmarshal(receipt, &accepted) != nil || accepted.Status != "accepted" || !teamsOriginReceiptMatches(x.observation, x.body, accepted) {
			rows.Close()
			return errTeamsProtectionHeld
		}
		entries = append(entries, x)
		if old, ok := latest[x.observation.Token]; !ok || old.observation.Revision < x.observation.Revision {
			latest[x.observation.Token] = x
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Preserve ordinary receipt history; compact only as the bounded journal
	// approaches its ceiling, and never more than eight rows per checkpoint.
	if len(entries) < 1000 {
		return nil
	}
	deleted := 0
	for _, old := range entries {
		if deleted >= 8 {
			break
		}
		newest := latest[old.observation.Token]
		a, b := old.observation, newest.observation
		current, ok := scan.byToken[b.Token]
		if old.id == newest.id || !ok || !teamsReceiptSupersedes(a, b, current) {
			continue
		}
		result, e := tx.ExecContext(ctx, `DELETE FROM teams_origin_outbox WHERE observation_id=? AND state='accepted' AND observation_json=? AND receipt_json IS NOT NULL`, old.id, old.body)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return errTeamsConsentStale
		}
		deleted++
	}
	return nil
}

func teamsReceiptSupersedes(a, b sourcewire.ProtectionObservation, current teamsRecoveryOrigin) bool {
	if a.Token != b.Token || a.Witness != b.Witness || a.Revision >= b.Revision || a.IssuedAt > b.IssuedAt || len(a.Dependencies) != 0 || len(b.Dependencies) != 0 {
		return false
	}
	compatible := a.Protection == b.Protection || (a.Protection == "non_private" && (b.Protection == "unknown" || b.Protection == "explicit_private")) || (a.Protection == "unknown" && b.Protection == "explicit_private")
	return compatible && current.Token == b.Token && current.Witness == b.Witness && current.Revision == b.Revision && current.Protection == b.Protection && current.EvidenceRevision == b.EvidenceRevision
}

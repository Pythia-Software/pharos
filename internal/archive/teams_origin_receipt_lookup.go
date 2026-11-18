package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

var errTeamsSourceReceiptUnavailable = errors.New("source receipt unavailable")

// Only receipts still inside the original seven-day issuance window can be
// retained by the current service. Local time is scheduling information only.
const teamsSourceLookupLiveIssuanceSQL = `CASE WHEN json_valid(o.observation_json) THEN json_type(o.observation_json,'$.issued_at')='integer' AND json_extract(o.observation_json,'$.issued_at')>? ELSE 0 END`

func teamsSourceLookupEarliestIssued() int64 {
	return time.Now().UTC().Unix() - int64(sourcewire.RecoveryHorizon/time.Second)
}

// reconcileTeamsSourceReceipt is invoked only by an explicit local action.
// It never sends a 031 command, clears a hold, or authorizes new disclosure.
func (s *Server) reconcileTeamsSourceReceipt(ctx context.Context) error {
	if !teamsSourcePilotEnabled {
		return errTeamsQueryUnsupported
	}
	s.teamsWorker.mu.Lock()
	if s.teamsWorker.sourceLookup {
		s.teamsWorker.mu.Unlock()
		return errTeamsSharingBusy
	}
	s.teamsWorker.sourceLookup = true
	s.teamsWorker.mu.Unlock()
	defer func() { s.teamsWorker.mu.Lock(); s.teamsWorker.sourceLookup = false; s.teamsWorker.mu.Unlock() }()
	var id, terminal, scope, origin, installation, library, keyHash string
	var raw []byte
	var signing ed25519.PrivateKey
	defer func() { clear(signing) }()
	err := s.withTeamsLibraryClient(ctx, func(_ *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential) error {
		lease, e := s.Catalog.teamsSharingLease()
		if e != nil {
			return e
		}
		defer lease.Close()
		if e = s.Catalog.teamsPilotCheckpointCurrent(ctx, d); e != nil {
			return e
		}
		scope = teamsCredentialScope(d, credential)
		s.teamsWorker.mu.Lock()
		if s.teamsWorker.sourceLookupScope != scope {
			s.teamsWorker.sourceLookupScope = scope
			s.teamsWorker.sourceLookupAfter = ""
		}
		after := s.teamsWorker.sourceLookupAfter
		s.teamsWorker.mu.Unlock()
		tx, e := s.Catalog.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if e != nil {
			return e
		}
		defer tx.Rollback()
		e = tx.QueryRowContext(ctx, `SELECT o.observation_id,a.terminal_code FROM teams_origin_outbox o JOIN teams_origin_attempts a ON a.observation_id=o.observation_id WHERE o.origin=? AND o.org_id=? AND o.person_id=? AND o.installation_id=? AND o.library_id=? AND o.instance_id=? AND o.work_id='' AND o.snapshot_id='' AND o.state='prepared' AND o.receipt_json IS NULL AND a.first_attempt_at>0 AND a.terminal_code IN ('','unknown_source_resync_required','unknown_stale_protection_revision') AND `+teamsSourceLookupLiveIssuanceSQL+` ORDER BY CASE WHEN o.observation_id>? THEN 0 ELSE 1 END,o.observation_id LIMIT 1`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, teamsSourceLookupEarliestIssued(), after).Scan(&id, &terminal)
		if errors.Is(e, sql.ErrNoRows) {
			return errTeamsPilotPending
		}
		if e != nil {
			return e
		}
		s.teamsWorker.mu.Lock()
		if s.teamsWorker.sourceLookupScope == scope {
			s.teamsWorker.sourceLookupAfter = id
		}
		s.teamsWorker.mu.Unlock()
		state, e := teamsLocalOriginStateCheckedIn(ctx, tx, s.Config(), d, credential, teamsV2Inventory{}, teamsOriginBatchScope{}, id, true, false, nil, true)
		if e != nil || !state.attempted {
			if e != nil {
				return e
			}
			return errTeamsConsentStale
		}
		raw = bytes.Clone(state.body)
		origin = credential.Origin
		library = d.ID
		installation = credential.InstallationID
		signing = credential.SigningKey()
		if len(signing) != ed25519.PrivateKeySize {
			return errTeamsConsentStale
		}
		keyHash = teamsPayloadHash(signing.Public().(ed25519.PublicKey))
		return nil
	})
	if err != nil {
		return err
	}
	// No library-use, sharing or capture lease, nor SQLite writer, crosses I/O.
	client, err := teamsDeviceClient(origin, installation, signing, false)
	if err != nil {
		return err
	}
	defer client.Destroy()
	observation, err := sourcewire.DecodeProtectionObservation(raw)
	if err != nil {
		return errTeamsConsentStale
	}

	bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
	receipt, err := client.LookupSourceProtectionReceipt(bounded, library, raw)
	cancel()
	if err != nil {
		var httpErr *teams.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == 404 {
			return errTeamsSourceReceiptUnavailable
		}
		return err
	}
	if receipt.Status != "accepted" || !teamsOriginReceiptMatches(observation, raw, receipt) {
		return errTeamsConsentStale
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	err = s.withTeamsLibraryClient(ctx, func(_ *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential) error {
		if teamsCredentialScope(d, credential) != scope {
			return errTeamsConsentStale
		}
		currentKey := credential.SigningKey()
		defer clear(currentKey)
		if len(currentKey) != ed25519.PrivateKeySize || teamsPayloadHash(currentKey.Public().(ed25519.PublicKey)) != keyHash {
			return errTeamsConsentStale
		}
		lease, e := s.Catalog.teamsSharingLease()
		if e != nil {
			return e
		}
		defer lease.Close()
		if e = s.Catalog.teamsPilotCheckpointCurrent(ctx, d); e != nil {
			return e
		}
		return s.Catalog.writeTransaction(ctx, "teams-source-receipt-lookup", func(tx *sql.Tx) error {
			result, e := tx.ExecContext(ctx, `UPDATE teams_origin_outbox SET state='accepted',receipt_json=? WHERE observation_id=? AND origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND work_id='' AND snapshot_id='' AND state='prepared' AND receipt_json IS NULL AND observation_json=? AND observation_sha256=? AND EXISTS(SELECT 1 FROM teams_origin_attempts a WHERE a.observation_id=teams_origin_outbox.observation_id AND a.first_attempt_at>0 AND a.terminal_code=?)`, receiptJSON, id, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, raw, sourcewire.ProtectionDigest(raw), terminal)
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
			result, e = tx.ExecContext(ctx, `UPDATE teams_origin_attempts SET terminal_code='reconciled_lookup' WHERE observation_id=? AND first_attempt_at>0 AND terminal_code=?`, id, terminal)
			if e != nil {
				return e
			}
			n, e = result.RowsAffected()
			if e != nil {
				return e
			}
			if n != 1 {
				return errTeamsConsentStale
			}
			return nil
		})
	})
	if err == nil {
		s.wakeTeams()
	}
	return err
}

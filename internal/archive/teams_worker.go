package archive

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

type teamsWorkerItem struct {
	Work        teams.Work
	State       string
	Payload     []byte
	PayloadHash string
	Labels      *teams.ReaderLabels
	LabelsAt    time.Time
	LabelsScope string
}
type teamsWorkerState struct {
	mu                    sync.Mutex
	wake                  chan struct{}
	items                 map[string]teamsWorkerItem
	generation            uint64
	scope                 string
	labelFlights          map[string]bool
	labelLast             map[string]time.Time
	next                  time.Time
	checking              bool
	problem               string
	reconcileAfter        string
	contentReconcileAfter string
}

// clearReaderLabelsLocked invalidates every display token while preserving ordinary work.
func (w *teamsWorkerState) clearReaderLabelsLocked() {
	w.generation++
	w.scope = ""
	w.labelLast = nil
	for id, item := range w.items {
		item.Labels = nil
		item.LabelsAt = time.Time{}
		item.LabelsScope = ""
		w.items[id] = item
	}
}

var teamsWorkerCapabilities = teamsBuildCapabilities

func teamsIdleDelay() time.Duration {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 15 * time.Minute
	}
	return 10*time.Minute + time.Duration(binary.BigEndian.Uint64(raw[:])%uint64(5*time.Minute))
}

// Retry scheduling may wake immediately once at startup, but a completed
// check never requests a zero-delay loop, even when work was not processed.
func teamsDelayAfterCheck(active bool) time.Duration {
	if active {
		return 30 * time.Second
	}
	return teamsIdleDelay()
}
func (s *Server) wakeTeams() {
	select {
	case s.teamsWorker.wake <- struct{}{}:
	default:
	}
}

// teamsLoop holds no connection while idle. A remote request waits for the
// next jittered check unless this Mac receives a separate local wake action.
func (s *Server) teamsLoop(ctx context.Context) {
	delay := teamsIdleDelay()
	if retryDelay, ok := s.teamsStartupRetryDelay(ctx, time.Now().UTC()); ok && retryDelay < delay {
		delay = retryDelay
	}
	for {
		s.teamsWorker.mu.Lock()
		s.teamsWorker.next = time.Now().Add(delay)
		s.teamsWorker.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.teamsWorker.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
		settings, settingsErr := readTeamsSettings(ctx, s.Catalog.reader())
		if settingsErr == nil && (!settings.Enabled || settings.LibraryID == "" || settings.InstallationID == "") {
			delay = teamsIdleDelay()
			continue
		}
		pass := s.Catalog.beginPass(ctx, "teams.check", triggerSchedule)
		active, err := s.checkTeamsWork(ctx)
		if errors.Is(err, errTeamsNotEnrolled) {
			err = nil
		}
		if active {
			pass.worked(1, "check")
		}
		pass.end(err)
		delay = teamsDelayAfterCheck(active)
	}
}

// checkTeamsWork is serialized by teamsLoop. It only operates on the open
// library and the bound installation; no background work starts on enrollment.
func (s *Server) checkTeamsWork(ctx context.Context) (bool, error) {
	s.teamsWorker.mu.Lock()
	if s.teamsWorker.checking {
		s.teamsWorker.mu.Unlock()
		return false, nil
	}
	s.teamsWorker.checking = true
	s.teamsWorker.problem = ""
	s.teamsWorker.mu.Unlock()
	defer func() { s.teamsWorker.mu.Lock(); s.teamsWorker.checking = false; s.teamsWorker.mu.Unlock() }()
	var active bool
	err := s.withTeamsLibraryClient(ctx, func(client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential) error {
		settings, e := readTeamsSettings(ctx, s.Catalog.reader())
		if e != nil {
			return e
		}
		if !settings.Enabled {
			s.teamsWorker.mu.Lock()
			s.teamsWorker.clearReaderLabelsLocked()
			s.teamsWorker.mu.Unlock()
			return nil
		}
		manifest := teamsWorkerCapabilities()
		if !teamsNumericBuildEligible(manifest) {
			s.teamsWorker.mu.Lock()
			s.teamsWorker.clearReaderLabelsLocked()
			s.teamsWorker.problem = "This Pharos build needs an update before it can process Teams requests."
			s.teamsWorker.mu.Unlock()
			return nil
		}
		// Reconcile persisted sends before any source observation or upload.
		s.teamsWorker.mu.Lock()
		after := s.teamsWorker.reconcileAfter
		s.teamsWorker.mu.Unlock()
		rows, e := s.Catalog.reader().QueryContext(ctx, `SELECT work_id FROM teams_v2_results WHERE work_id>? AND (state IN ('sending','needs_reconcile') OR (state='accepted' AND json_valid(receipt_json) AND json_extract(receipt_json,'$.publication_state')='staged')) ORDER BY work_id LIMIT 100`, after)
		if e != nil {
			return e
		}
		var toReconcile []string
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			toReconcile = append(toReconcile, id)
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			return e
		}
		rows.Close()
		s.teamsWorker.mu.Lock()
		if len(toReconcile) == 0 {
			s.teamsWorker.reconcileAfter = ""
		} else {
			s.teamsWorker.reconcileAfter = toReconcile[len(toReconcile)-1]
		}
		s.teamsWorker.mu.Unlock()
		for _, id := range toReconcile {
			_, reconcileErr := s.Catalog.reconcileTeamsV2Receipt(ctx, client, d, credential, id)
			if errors.Is(reconcileErr, teams.ErrReceiptMismatch) {
				s.Catalog.markTeamsV2Outcome(id, "unconfirmed")
			}
		}
		// Artifact receipts are likewise reconciled even if completed work no
		// longer appears in poll. A cursor prevents older uncertain items from
		// starving later ones; this path never starts another model run.
		s.teamsWorker.mu.Lock()
		contentAfter := s.teamsWorker.contentReconcileAfter
		s.teamsWorker.mu.Unlock()
		contentRows, e := s.Catalog.reader().QueryContext(ctx, `SELECT work_id FROM teams_content_runs WHERE work_id>? AND state IN ('sending','needs_reconcile') ORDER BY work_id LIMIT 100`, contentAfter)
		if e != nil {
			return e
		}
		var pendingContent []string
		for contentRows.Next() {
			var id string
			if e = contentRows.Scan(&id); e != nil {
				contentRows.Close()
				return e
			}
			pendingContent = append(pendingContent, id)
		}
		if e = contentRows.Err(); e != nil {
			contentRows.Close()
			return e
		}
		contentRows.Close()
		s.teamsWorker.mu.Lock()
		if len(pendingContent) == 0 {
			s.teamsWorker.contentReconcileAfter = ""
		} else {
			s.teamsWorker.contentReconcileAfter = pendingContent[len(pendingContent)-1]
		}
		s.teamsWorker.mu.Unlock()
		for _, id := range pendingContent {
			_, _ = s.Catalog.reconcileTeamsContent(ctx, client, d, credential, id)
		}
		poll, e := client.Poll(ctx, teams.PollRequest{AppVersion: manifest.AppVersion, LibraryIDs: []string{d.ID}, CapabilityManifest: &manifest})
		if e != nil {
			return e
		}
		if len(poll.Work) > 100 {
			return errors.New("too many Teams work items")
		}
		items := make(map[string]teamsWorkerItem, len(poll.Work))
		for _, work := range poll.Work {
			if work.LibraryID != d.ID {
				return errTeamsConsentStale
			}
			items[work.ID] = teamsWorkerItem{Work: work, State: "pending"}
		}
		if poll.CapabilityStatus == "needs_update" {
			s.teamsWorker.mu.Lock()
			s.teamsWorker.problem = "This Pharos build needs an update before it can process Teams requests."
			s.teamsWorker.mu.Unlock()
		}
		processed := 0
		for _, work := range poll.Work {
			if processed >= 5 {
				active = true
				break
			}
			processed++
			item := items[work.ID]
			if handled, declineErr := s.retryTeamsDecline(ctx, client, d, credential, work, manifest.AppVersion); handled {
				if declineErr != nil {
					item.State = "decline_pending"
					active = true
				} else {
					item.State = "declined"
				}
				items[work.ID] = item
				continue
			}
			if work.Kind == "llm" {
				items[work.ID] = s.checkTeamsContentWork(ctx, client, d, credential, work, manifest, poll.ServerTime)
				if items[work.ID].State == "source_safety_waiting" {
					active = true
				}
				continue
			}
			prior, priorErr := readTeamsV2Draft(ctx, s.Catalog.reader(), work.ID)
			if priorErr == nil && (prior.State == "approved" || prior.State == "awaiting_review") {
				item.Payload = bytesClone(prior.Payload)
				item.PayloadHash = prior.PayloadHash
				item.State = prior.State
				if prior.State == "approved" {
					receipt, sendErr := s.Catalog.sendTeamsV2Approved(ctx, s.Config(), client, d, credential, work.ID, manifest)
					if sendErr != nil {
						item.State = "needs_refresh"
					} else {
						item.State = receipt.PublicationState
					}
				}
				items[work.ID] = item
				continue
			}
			if priorErr == nil && (prior.State == "sending" || prior.State == "needs_reconcile" || prior.State == "accepted") {
				receipt, reconcileErr := s.Catalog.reconcileTeamsV2Receipt(ctx, client, d, credential, work.ID)
				if teamsReceiptDefinitelyAbsent(reconcileErr) && prior.State != "accepted" {
					receipt, reconcileErr = s.Catalog.sendTeamsV2Approved(ctx, s.Config(), client, d, credential, work.ID, manifest)
				}
				item.Payload = bytesClone(prior.Payload)
				item.PayloadHash = prior.PayloadHash
				if reconcileErr != nil {
					item.State = "unconfirmed"
				} else {
					item.State = receipt.PublicationState
				}
				items[work.ID] = item
				continue
			}
			if !teamsNumericWorkEligible(work, manifest) {
				item.State = "needs_update"
				items[work.ID] = item
				continue
			}
			if _, e = teamsPlanFromWork(work); e != nil {
				item.State = "needs_update"
				items[work.ID] = item
				continue
			}
			snapshot, e := s.Catalog.teamsObserveV2Codex(ctx, s.Config(), d, credential)
			if e != nil || len(snapshot.values) == 0 {
				item.State = "unavailable"
				items[work.ID] = item
				continue
			}
			registration, e := s.Catalog.registerTeamsV2(ctx, s.Config(), client, d, credential, work, snapshot, manifest)
			if e != nil {
				switch {
				case errors.Is(e, errTeamsConsentStale):
					item.State = "awaiting_source_consent"
				case errors.Is(e, errTeamsV2Pending):
					item.State = "ownership_pending"
					active = true
				default:
					item.State = "unavailable"
				}
				items[work.ID] = item
				continue
			}
			draft, e := s.Catalog.stageTeamsV2Result(ctx, s.Config(), d, credential, registration)
			if e != nil {
				item.State = "unavailable"
				items[work.ID] = item
				continue
			}
			item.Payload = bytesClone(draft.Payload)
			item.PayloadHash = draft.PayloadHash
			item.State = draft.State
			if draft.State == "accepted" {
				var receipt teams.Receipt
				if json.Unmarshal([]byte(draft.ReceiptJSON), &receipt) == nil {
					item.State = receipt.PublicationState
				} else {
					item.State = "unconfirmed"
				}
			}
			if draft.State == "approved" || draft.State == "sending" || draft.State == "needs_reconcile" {
				receipt, sendErr := s.Catalog.sendTeamsV2Approved(ctx, s.Config(), client, d, credential, work.ID, manifest)
				if sendErr != nil {
					item.State = "unconfirmed"
				} else {
					item.State = receipt.PublicationState
				}
			}
			items[work.ID] = item
		}
		s.teamsWorker.mu.Lock()
		s.teamsWorker.items = items
		s.teamsWorker.scope = teamsCredentialScope(d, credential)
		s.teamsWorker.generation++
		s.teamsWorker.labelLast = nil
		s.teamsWorker.mu.Unlock()
		return nil
	})
	if err != nil {
		s.teamsWorker.mu.Lock()
		s.teamsWorker.clearReaderLabelsLocked()
		if !errors.Is(err, errTeamsNotEnrolled) {
			s.teamsWorker.problem = "The Teams check could not complete. Try again."
		}
		s.teamsWorker.mu.Unlock()
		if errors.Is(err, errTeamsNotEnrolled) {
			return false, nil
		}
	}
	return active, err
}

func bytesClone(b []byte) []byte { return append([]byte(nil), b...) }

func (s *Server) retryTeamsDecline(ctx context.Context, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential, work teams.Work, appVersion string) (bool, error) {
	var library, instance, org, installation, hash, state string
	err := s.Catalog.reader().QueryRowContext(ctx, `SELECT library_id,instance_id,org_id,installation_id,config_hash,state FROM teams_v2_declines WHERE work_id=?`, work.ID).Scan(&library, &instance, &org, &installation, &hash, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if library != d.ID || instance != d.Instance || org != credential.OrgID || installation != credential.InstallationID || hash != work.ConfigSHA256 {
		return true, errTeamsConsentStale
	}
	if state == "sent" {
		return true, nil
	}
	lease, err := s.Catalog.teamsSharingLease()
	if err != nil {
		return true, err
	}
	defer lease.Close()
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err = client.Status(bounded, work.ID, teams.WorkStatus{State: "declined", AppVersion: appVersion}); err != nil {
		return true, err
	}
	err = s.Catalog.writeTransaction(ctx, "teams-v2-decline-sent", func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `UPDATE teams_v2_declines SET state='sent' WHERE work_id=? AND state='pending'`, work.ID)
		return e
	})
	return true, err
}

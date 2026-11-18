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
	sweep                 []string
	bindings              map[string]string
	workBindings          map[string]string
	settings              teamsLocalSettings
	settingsValid         bool
	generation            uint64
	scope                 string
	viewScope             string
	labelFlights          map[string]bool
	labelLast             map[string]time.Time
	next                  time.Time
	checking              bool
	problem               string
	sourceProblem         string
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

func (w *teamsWorkerState) clearWorkLocked() {
	w.clearReaderLabelsLocked()
	w.items = nil
	w.sweep = nil
	w.bindings = nil
	w.workBindings = nil
	w.settingsValid = false
	w.viewScope = ""
}

var errTeamsWorkerAuthorityChanged = errors.New("Teams worker authority changed")

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
			s.teamsWorker.mu.Lock()
			s.teamsWorker.clearWorkLocked()
			s.teamsWorker.mu.Unlock()
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
	s.teamsWorker.sourceProblem = ""
	s.teamsWorker.mu.Unlock()
	defer func() { s.teamsWorker.mu.Lock(); s.teamsWorker.checking = false; s.teamsWorker.mu.Unlock() }()
	var active bool
	authorityLoaded := false
	err := s.withTeamsLibraryClient(ctx, func(client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential) error {
		authorityLoaded = true
		settings, e := readTeamsSettings(ctx, s.Catalog.reader())
		if e != nil {
			return e
		}
		if !settings.Enabled {
			s.teamsWorker.mu.Lock()
			s.teamsWorker.clearWorkLocked()
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
		if len(toReconcile) == 100 {
			active = true
		}
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
		if len(pendingContent) == 100 {
			active = true
		}
		for _, id := range pendingContent {
			_, _ = s.Catalog.reconcileTeamsContent(ctx, client, d, credential, id)
		}
		poll, e := client.Poll(ctx, teams.PollRequest{AppVersion: manifest.AppVersion, LibraryIDs: []string{d.ID}, CapabilityManifest: &manifest})
		if e != nil {
			return e
		}
		if len(poll.Work) > 200 {
			return errors.New("too many Teams work items")
		}
		// The full signed work and local revision inputs bind every retained view.
		var sourceRev, privacyRev int64
		if e = s.Catalog.reader().QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&sourceRev); e != nil {
			return e
		}
		if e = s.Catalog.reader().QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&privacyRev); e != nil {
			return e
		}
		generations, e := readCacheGenerations(ctx, s.Catalog.reader())
		if e != nil {
			return e
		}
		cancelled := make(map[string]bool, len(poll.Cancellations))
		for _, x := range poll.Cancellations {
			cancelled[x.WorkID] = true
		}
		items := make(map[string]teamsWorkerItem, len(poll.Work))
		keys := make(map[string]string, len(poll.Work))
		workKeys := make(map[string]string, len(poll.Work))
		ordered := make([]string, 0, len(poll.Work))
		seen := make(map[string]bool, len(poll.Work))
		scope := teamsCredentialScope(d, credential)
		for _, work := range poll.Work {
			if work.LibraryID != d.ID || seen[work.ID] {
				return errTeamsConsentStale
			}
			seen[work.ID] = true
			if cancelled[work.ID] || (!poll.ServerTime.IsZero() && !work.ExpiresAt.After(poll.ServerTime)) {
				continue
			}
			key, e := s.teamsWorkViewBinding(ctx, scope, work, settings, sourceRev, privacyRev, generations.all)
			if e != nil {
				return e
			}
			workBinding, e := json.Marshal(struct {
				Scope string
				Work  teams.Work
			}{scope, work})
			if e != nil {
				return e
			}
			workKeys[work.ID] = teamsPayloadHash(workBinding)
			keys[work.ID] = key
			ordered = append(ordered, work.ID)
			items[work.ID] = teamsWorkerItem{Work: work, State: "pending"}
		}
		s.teamsWorker.mu.Lock()
		oldItems, oldKeys := s.teamsWorker.items, s.teamsWorker.bindings
		sweep := teamsMergeWorkSweep(s.teamsWorker.sweep, oldKeys, keys, s.teamsWorker.workBindings, workKeys, ordered, oldItems, items)
		s.teamsWorker.mu.Unlock()

		if poll.CapabilityStatus == "needs_update" {
			s.teamsWorker.mu.Lock()
			s.teamsWorker.problem = "This Pharos build needs an update before it can process Teams requests."
			s.teamsWorker.mu.Unlock()
		}
		if teamsSourcePilotEnabled {
			pilotErr := s.Catalog.teamsRunIndependentSourcePilot(ctx, s.Config(), client, d, credential, manifest, poll.NegotiatedManifestV2 && poll.CapabilityStatus != "needs_update")
			if pilotErr != nil {
				s.teamsWorker.mu.Lock()
				if errors.Is(pilotErr, errTeamsPilotMore) {
					s.teamsWorker.sourceProblem = "Source protection is progressing."
					active = true
				} else if errors.Is(pilotErr, errTeamsPilotRetry) {
					s.teamsWorker.sourceProblem = "Source protection is waiting to retry a connection."
					active = true
				} else {
					s.teamsWorker.sourceProblem = "Source protection is waiting for verified evidence or reconciliation. You can continue using this library."
				}
				s.teamsWorker.mu.Unlock()
			}
		}
		count := min(len(sweep), 5)
		visits := sweep[:count]
		sweep = append([]string(nil), sweep[count:]...)
		if len(sweep) > 0 {
			active = true
		}
		runnable := make(map[string]bool)
		for _, id := range visits {
			item := items[id]
			item.Payload = nil
			item.PayloadHash = ""
			work := item.Work
			if handled, declineErr := s.retryTeamsDecline(ctx, client, d, credential, work, manifest.AppVersion); handled {
				if declineErr != nil {
					item.State = "decline_pending"
					if !errors.Is(declineErr, errTeamsConsentStale) {
						active = true
						runnable[id] = true
					}
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
			if _, e = teamsV2PlanFromWork(work); e != nil {
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
		// Serialize publication with local decline transactions. A decline that
		// committed while this check was running must win over its older view.
		publicationLease, e := s.Catalog.teamsSharingLease()
		if e != nil {
			return e
		}
		defer publicationLease.Close()
		ids, e := json.Marshal(ordered)
		if e != nil {
			return e
		}
		declines, e := s.Catalog.reader().QueryContext(ctx, `SELECT work_id,config_hash,state FROM teams_v2_declines WHERE work_id IN (SELECT value FROM json_each(?)) AND library_id=? AND instance_id=? AND org_id=? AND installation_id=? LIMIT 201`, string(ids), d.ID, d.Instance, credential.OrgID, credential.InstallationID)
		if e != nil {
			return e
		}
		for declines.Next() {
			var id, hash, state string
			if e = declines.Scan(&id, &hash, &state); e != nil {
				declines.Close()
				return e
			}
			item, ok := items[id]
			if !ok || item.Work.ConfigSHA256 != hash {
				continue
			}
			item.Payload = nil
			item.PayloadHash = ""
			item.State = "decline_pending"
			if state == "sent" {
				item.State = "declined"
			}
			items[id] = item
		}
		if e = declines.Err(); e != nil {
			declines.Close()
			return e
		}
		if e = declines.Close(); e != nil {
			return e
		}

		// Visits can create durable drafts or receipts. Bind their views to the
		// post-visit local state, so the next poll does not invalidate them itself.
		latest, e := readTeamsSettings(ctx, s.Catalog.reader())
		if e != nil {
			return e
		}
		if !teamsSameWorkerAuthority(settings, latest) {
			return errors.Join(errTeamsWorkerAuthorityChanged, errTeamsConsentStale)
		}
		settings = latest
		if e = s.Catalog.reader().QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&sourceRev); e != nil {
			return e
		}
		if e = s.Catalog.reader().QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&privacyRev); e != nil {
			return e
		}
		generations, e = readCacheGenerations(ctx, s.Catalog.reader())
		if e != nil {
			return e
		}
		invalidated := make(map[string]bool)
		pendingSweep := make(map[string]bool, len(sweep))
		for _, id := range sweep {
			pendingSweep[id] = true
		}
		visited := make(map[string]bool, len(visits))
		for _, id := range visits {
			visited[id] = true
		}
		for _, id := range ordered {
			post, e := s.teamsWorkViewBinding(ctx, scope, items[id].Work, settings, sourceRev, privacyRev, generations.all)
			if e != nil {
				return e
			}
			if post != keys[id] {
				item := items[id]
				item.State = "pending"
				item.Payload = nil
				item.PayloadHash = ""
				item.Labels = nil
				item.LabelsAt = time.Time{}
				item.LabelsScope = ""
				items[id] = item
				invalidated[id] = true
				if visited[id] {
					runnable[id] = true
				} else if !pendingSweep[id] {
					sweep = append(sweep, id)
					pendingSweep[id] = true
				}
				active = true
			}
		}
		sweep = teamsAppendRunnable(sweep, visits, items, runnable)
		s.teamsWorker.mu.Lock()
		stable := s.teamsWorker.scope == scope
		for id := range keys {
			if invalidated[id] || s.teamsWorker.workBindings[id] != workKeys[id] {
				continue
			}
			// A foreground lookup may have completed while this poll processed.
			if live, ok := s.teamsWorker.items[id]; ok && live.LabelsScope == scope && validReaderLabels(items[id].Work, live.Labels, live.LabelsAt, false) {
				item := items[id]
				item.Labels, item.LabelsAt, item.LabelsScope = live.Labels, live.LabelsAt, live.LabelsScope
				items[id] = item
			}
		}
		s.teamsWorker.items = items
		s.teamsWorker.sweep = sweep
		s.teamsWorker.bindings = keys
		s.teamsWorker.workBindings = workKeys
		s.teamsWorker.settings = settings
		s.teamsWorker.settingsValid = true
		s.teamsWorker.scope = scope
		s.teamsWorker.viewScope = scope
		if !stable {
			s.teamsWorker.generation++
			s.teamsWorker.labelLast = nil
		}
		s.teamsWorker.mu.Unlock()
		return nil
	})
	if err != nil {
		s.teamsWorker.mu.Lock()
		var response *teams.HTTPError
		denied := errors.As(err, &response) && (response.Status == 401 || response.Status == 403)
		if !authorityLoaded || denied || errors.Is(err, errTeamsNotEnrolled) || errors.Is(err, errTeamsWorkerAuthorityChanged) {
			s.teamsWorker.clearWorkLocked()
		} else {
			s.teamsWorker.clearReaderLabelsLocked()
		}
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

// Visit processing may advance local catalog counters; it must not change
// the enrollment or consent that authorized this poll.
func teamsSameWorkerAuthority(a, b teamsLocalSettings) bool {
	a.LocalRevision, a.ObservedGeneration, a.ObservedPrivacyRevision = 0, 0, 0
	b.LocalRevision, b.ObservedGeneration, b.ObservedPrivacyRevision = 0, 0, 0
	return a == b
}

func (s *Server) teamsWorkViewBinding(ctx context.Context, scope string, work teams.Work, settings teamsLocalSettings, sourceRev, privacyRev, generation int64) (string, error) {
	return teamsWorkViewBindingIn(ctx, s.Catalog.reader(), scope, work, settings, sourceRev, privacyRev, generation)
}
func teamsWorkViewBindingIn(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, scope string, work teams.Work, settings teamsLocalSettings, sourceRev, privacyRev, generation int64) (string, error) {
	var local string
	err := q.QueryRowContext(ctx, `SELECT json_array(
		(SELECT json_array(library_id,instance_id,org_id,installation_id,host_id,provider,model,effort,session_digest,auth_digest,settings_revision,granted_at) FROM teams_model_permission WHERE id=1),
		(SELECT json_array(library_id,instance_id,org_id,installation_id,config_hash,settings_revision,granted_at) FROM teams_request_grants WHERE version_id=?),
		(SELECT json_array(state,payload_sha256,decision_id,approved_at,receipt_json) FROM teams_v2_results WHERE work_id=?),
		(SELECT json_array(state,payload_sha256,approval_id,approved_at,receipt_json) FROM teams_content_runs WHERE work_id=?),
		(SELECT json_array(library_id,instance_id,org_id,installation_id,config_hash,state) FROM teams_v2_declines WHERE work_id=?))`, work.VersionID, work.ID, work.ID, work.ID).Scan(&local)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(struct {
		Scope, Local           string
		Work                   teams.Work
		Settings               teamsLocalSettings
		Source, Privacy, Cache int64
	}{scope, local, work, settings, sourceRev, privacyRev, generation})
	if err != nil {
		return "", err
	}
	return teamsPayloadHash(body), nil
}

// Foreground actions cannot rely on a view after its local inputs changed.
func (s *Server) teamsRequireCurrentView(ctx context.Context, scope string, work teams.Work, settings teamsLocalSettings, expected string) error {
	if expected == "" {
		return errTeamsConsentStale
	}
	var source, privacy int64
	if e := s.Catalog.reader().QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&source); e != nil {
		return e
	}
	if e := s.Catalog.reader().QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&privacy); e != nil {
		return e
	}
	g, e := readCacheGenerations(ctx, s.Catalog.reader())
	if e != nil {
		return e
	}
	actual, e := s.teamsWorkViewBinding(ctx, scope, work, settings, source, privacy, g.all)
	if e != nil {
		return e
	}
	if actual != expected {
		return errTeamsConsentStale
	}
	return nil
}

func teamsAppendRunnable(sweep, visits []string, items map[string]teamsWorkerItem, runnable map[string]bool) []string {
	for _, id := range visits {
		if items[id].State == "ownership_pending" || items[id].State == "source_safety_waiting" || runnable[id] {
			sweep = append(sweep, id)
		}
	}
	return sweep
}

// teamsMergeWorkSweep retains the unvisited suffix when fresh work changes.
// Changed or new bindings join behind that suffix, and stale views are dropped.
func teamsMergeWorkSweep(previous []string, oldKeys, keys, oldWorkKeys, workKeys map[string]string, ordered []string, oldItems, items map[string]teamsWorkerItem) []string {
	var sweep []string
	queued := make(map[string]bool, len(ordered))
	for _, id := range previous {
		if keys[id] != "" && workKeys[id] == oldWorkKeys[id] && !queued[id] {
			sweep = append(sweep, id)
			queued[id] = true
		}
	}
	for _, id := range ordered {
		if keys[id] == oldKeys[id] {
			if old, ok := oldItems[id]; ok {
				old.Work = items[id].Work
				items[id] = old
			}
		}
		_, hadView := oldItems[id]
		if (keys[id] != oldKeys[id] || !hadView) && !queued[id] {
			sweep = append(sweep, id)
			queued[id] = true
		}
	}
	if len(sweep) == 0 {
		sweep = append(sweep, ordered...)
	}
	return sweep
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

package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

func teamsLocalAction(w http.ResponseWriter, r *http.Request, out any, allowed map[string]bool) error {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		return err
	}
	if _, err = teamsStrictObject(raw, allowed); err != nil || !teams.ValidUniqueJSON(raw) {
		return errTeamsConsentStale
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

func (c *Catalog) setTeamsUserPolicy(ctx context.Context, d LibraryDescriptor, credential *teams.EnrollmentCredential, expected int64, enabled, optOut bool, policyVersion int64) error {
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	return c.writeTransaction(ctx, "teams-user-policy", func(tx *sql.Tx) error {
		s, e := readTeamsSettings(ctx, tx)
		if e != nil {
			return e
		}
		if credential == nil || credential.LibraryID != d.ID || s.LibraryID != d.ID || s.InstanceID != d.Instance || s.OrgID != credential.OrgID || s.InstallationID != credential.InstallationID || s.SettingsRevision != expected || (enabled && policyVersion < 1) {
			return errTeamsConsentStale
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_local_settings SET enabled=?,metrics_opt_out=?,acknowledged_policy=?,settings_revision=settings_revision+1,local_revision=local_revision+1 WHERE id=1`, enabled, optOut, policyVersion)
		return e
	})
}

func (s *Server) teamsWorkerHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	err := s.withTeamsLibraryClient(r.Context(), func(client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential) error {
		settings, e := readTeamsSettings(r.Context(), s.Catalog.reader())
		if e != nil {
			return e
		}
		scope := teamsCredentialScope(d, credential)
		s.teamsWorker.mu.Lock()
		if s.teamsWorker.settingsValid && (s.teamsWorker.settings != settings || s.teamsWorker.viewScope != scope) {
			s.teamsWorker.clearWorkLocked()
		}
		s.teamsWorker.mu.Unlock()
		switch {
		case r.URL.Path == "/api/teams/worker" && r.Method == http.MethodGet:
			s.teamsWorker.mu.Lock()
			items := make([]teamsWorkerItem, 0, len(s.teamsWorker.items))
			for _, item := range s.teamsWorker.items {
				items = append(items, item)
			}
			next, checking, problem, sourceProblem := s.teamsWorker.next, s.teamsWorker.checking, s.teamsWorker.problem, s.teamsWorker.sourceProblem
			generation, workScope := s.teamsWorker.generation, s.teamsWorker.scope
			s.teamsWorker.mu.Unlock()
			type row struct {
				WorkID              string                     `json:"workId"`
				Question            string                     `json:"question"`
				Requester           string                     `json:"requester"`
				State               string                     `json:"state"`
				PayloadHash         string                     `json:"payloadHash"`
				ConfigHash          string                     `json:"configHash,omitempty"`
				PublicationV2       json.RawMessage            `json:"publicationV2,omitempty"`
				ScheduledTerms      *teamsScheduledDisclosure  `json:"scheduledTerms,omitempty"`
				ScheduledAssignment *teams.ScheduledAssignment `json:"scheduledAssignment,omitempty"`
				Readers             []string                   `json:"readers"`
				RequesterID         string                     `json:"requesterId"`
				RequesterFallback   string                     `json:"requesterFallback,omitempty"`
				Labels              *teams.ReaderLabels        `json:"readerLabels,omitempty"`
				LabelsReady         bool                       `json:"labelsReady"`
				LabelsToken         string                     `json:"labelsToken,omitempty"`
				LabelsExpireAt      string                     `json:"labelsExpireAt,omitempty"`
				Generation          uint64                     `json:"generation"`
				Payload             json.RawMessage            `json:"payload"`
				Prompt              string                     `json:"prompt"`
				Retention           json.RawMessage            `json:"retention"`
				Schedule            json.RawMessage            `json:"schedule"`
				Period              json.RawMessage            `json:"period"`
				Exports             []teamsFeedExport          `json:"exports"`
				Publications        []teamsReportPublication   `json:"publications"`
			}
			out := make([]row, 0, len(items))
			for _, item := range items {
				if workScope != scope || !settings.Enabled {
					item.State = "offline"
					item.Payload = nil
					item.PayloadHash = ""
				}
				if item.LabelsScope != scope || !validReaderLabels(item.Work, item.Labels, item.LabelsAt, false) {
					item.Labels = nil
					item.LabelsAt = time.Time{}
				}
				var config struct {
					PublicationV2 json.RawMessage `json:"publication_v2"`
					Question      string          `json:"question"`
					Prompt        string          `json:"prompt"`
					Retention     json.RawMessage `json:"retention_policy"`
					Schedule      json.RawMessage `json:"schedule"`
					Requester     struct {
						ID          string `json:"id"`
						DisplayName string `json:"display_name"`
					} `json:"requester"`
					Audiences struct {
						ResponseReaders []struct{ Kind, ID string } `json:"response_readers"`
						Exports         []teamsFeedExport           `json:"exports"`
						Publications    []teamsReportPublication    `json:"publications"`
					} `json:"audiences"`
				}
				if item.Work.Kind == "llm" {
					_, e = teamsLLMFromWork(item.Work, teamsWorkerCapabilities().AppVersion)
				} else {
					_, e = teamsV2PlanFromWork(item.Work)
				}
				if e != nil {
					out = append(out, row{WorkID: item.Work.ID, Question: "This request needs a newer Pharos build.", State: item.State})
					continue
				}
				_ = json.Unmarshal(item.Work.Config, &config)
				if config.PublicationV2 != nil && (!settings.Enabled || workScope != scope || (item.State != "awaiting_source_consent" && item.State != "awaiting_review")) {
					item.Labels = nil
					item.LabelsAt = time.Time{}
				}
				readers := []string{}
				for _, reader := range config.Audiences.ResponseReaders {
					readers = append(readers, reader.ID)
				}
				var configHash string
				if config.PublicationV2 != nil {
					configHash = item.Work.ConfigSHA256
				}
				expires := ""
				if item.Labels != nil {
					expires = item.LabelsAt.Add(5 * time.Minute).UTC().Format(time.RFC3339Nano)
				}
				fallback := ""
				if usableTeamsReaderName(config.Requester.DisplayName) {
					fallback = config.Requester.DisplayName
				}
				var scheduled *teamsScheduledDisclosure
				if item.Work.ScheduledAssignment != nil {
					c, err := parseTeamsScheduledConfig(item.Work.Config)
					if err != nil {
						return err
					}
					terms := teamsScheduledTerms(c)
					scheduled = &terms
				}
				out = append(out, row{ScheduledTerms: scheduled, ScheduledAssignment: item.Work.ScheduledAssignment, LabelsExpireAt: expires, RequesterFallback: fallback, WorkID: item.Work.ID, Question: config.Question, Prompt: config.Prompt, Retention: config.Retention, Requester: config.Requester.DisplayName, RequesterID: config.Requester.ID, Labels: item.Labels, LabelsReady: labelsForReview(item.Work, item.Labels, item.LabelsAt), LabelsToken: teamsReaderLabelToken(item, scope, generation), Generation: generation, State: item.State, PayloadHash: item.PayloadHash, ConfigHash: configHash, PublicationV2: config.PublicationV2, Readers: readers, Payload: json.RawMessage(item.Payload), Schedule: config.Schedule, Period: item.Work.Period, Exports: config.Audiences.Exports, Publications: config.Audiences.Publications})
			}
			var modelApproved bool
			if e = s.Catalog.reader().QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM teams_model_permission WHERE id=1 AND library_id=? AND instance_id=? AND org_id=? AND installation_id=? AND host_id=? AND settings_revision=?)`, d.ID, d.Instance, credential.OrgID, credential.InstallationID, currentHost().ID, settings.SettingsRevision).Scan(&modelApproved); e != nil {
				return e
			}
			response := map[string]any{"enabled": settings.Enabled, "optOut": settings.MetricsOptOut, "settingsRevision": settings.SettingsRevision, "acknowledgedPolicy": settings.AcknowledgedPolicy, "nextCheck": next, "checking": checking, "problem": problem, "items": out, "modelApproved": modelApproved}
			if teamsSourcePilotEnabled && workScope == scope && settings.Enabled && sourceProblem != "" {
				response["sourceProblem"] = sourceProblem
			}
			writeJSON(w, response, 200)
			return nil
		case r.URL.Path == "/api/teams/worker/protection-policy" && r.Method == http.MethodGet:
			acknowledged, e := readTeamsProtectionAcknowledgement(r.Context(), s.Catalog.reader(), d, credential)
			if e != nil {
				return e
			}
			continuity := "not_ready"
			if checkpoint := s.Catalog.teamsCheckpoint; checkpoint != nil {
				_, host, e := teamsCheckpointLoad(checkpoint.store, checkpoint.name)
				if e != nil || host.Scope != checkpoint.scope || host.Phase == "held" {
					continuity = "held"
				} else if host.Phase == "pending" {
					continuity = "reconciling"
				}
			}
			writeJSON(w, map[string]any{"policyId": teamsLocalProtectionPolicyID, "policyDigest": teamsLocalProtectionPolicyDigest(), "disclosure": teamsLocalProtectionDisclosure, "acknowledged": acknowledged, "settingsRevision": settings.SettingsRevision, "continuity": continuity}, 200)
			return nil
		case r.URL.Path == "/api/teams/worker/protection-policy" && r.Method == http.MethodPost:
			var body struct {
				Acknowledge      *bool  `json:"acknowledge"`
				SettingsRevision *int64 `json:"settingsRevision"`
			}
			if e = teamsLocalAction(w, r, &body, map[string]bool{"acknowledge": true, "settingsRevision": true}); e != nil || body.Acknowledge == nil || !*body.Acknowledge || body.SettingsRevision == nil {
				return errTeamsConsentStale
			}
			if e = s.Catalog.acknowledgeTeamsProtectionPolicy(r.Context(), d, credential, *body.SettingsRevision); e != nil {
				return e
			}
			s.wakeTeams()
			writeJSON(w, map[string]bool{"updated": true}, 200)
			return nil
		case r.URL.Path == "/api/teams/worker/policy" && r.Method == http.MethodGet:
			manifest := teamsWorkerCapabilities()
			if !teamsNumericBuildEligible(manifest) {
				return errTeamsQueryUnsupported
			}
			poll, e := client.Poll(r.Context(), teams.PollRequest{AppVersion: manifest.AppVersion, LibraryIDs: []string{d.ID}, CapabilityManifest: &manifest})
			if e != nil {
				return e
			}
			if poll.MetricsDefault.PolicyVersion < 1 {
				return errTeamsConsentStale
			}
			writeJSON(w, map[string]any{"value": poll.MetricsDefault.Value, "policyVersion": poll.MetricsDefault.PolicyVersion, "settingsRevision": settings.SettingsRevision}, 200)
			return nil
		case r.URL.Path == "/api/teams/worker/log" && r.Method == http.MethodGet:
			rows, e := s.Catalog.reader().QueryContext(r.Context(), `SELECT work_id,config,payload,sent_at FROM teams_send_log WHERE library_id=? AND org_id=? ORDER BY sent_at DESC LIMIT 20`, d.ID, credential.OrgID)
			if e != nil {
				return e
			}
			defer rows.Close()
			type sentRow struct {
				WorkID   string          `json:"workId"`
				Question string          `json:"question"`
				Payload  json.RawMessage `json:"payload"`
				SentAt   string          `json:"sentAt"`
			}
			items := []sentRow{}
			total := 0
			for rows.Next() {
				var row sentRow
				var config []byte
				if e = rows.Scan(&row.WorkID, &config, &row.Payload, &row.SentAt); e != nil {
					return e
				}
				total += len(config) + len(row.Payload)
				if total > 1<<20 {
					break
				}
				var v struct {
					Question string `json:"question"`
				}
				if json.Unmarshal(config, &v) != nil || !teams.ValidUniqueJSON(row.Payload) {
					return errTeamsConsentStale
				}
				row.Question = v.Question
				items = append(items, row)
			}
			if e = rows.Err(); e != nil {
				return e
			}
			writeJSON(w, map[string]any{"items": items}, 200)
			return nil
		case r.URL.Path == "/api/teams/worker/settings" && r.Method == http.MethodPost:
			var body struct {
				Enabled          *bool  `json:"enabled"`
				OptOut           *bool  `json:"optOut"`
				SettingsRevision *int64 `json:"settingsRevision"`
				PolicyVersion    *int64 `json:"policyVersion"`
			}
			if e = teamsLocalAction(w, r, &body, map[string]bool{"enabled": true, "optOut": true, "settingsRevision": true, "policyVersion": true}); e != nil || body.Enabled == nil || body.OptOut == nil || body.SettingsRevision == nil || body.PolicyVersion == nil {
				return errTeamsConsentStale
			}
			version := *body.PolicyVersion
			if *body.Enabled {
				manifest := teamsWorkerCapabilities()
				if !teamsNumericBuildEligible(manifest) {
					return errTeamsQueryUnsupported
				}
				poll, e := client.Poll(r.Context(), teams.PollRequest{AppVersion: manifest.AppVersion, LibraryIDs: []string{d.ID}, CapabilityManifest: &manifest})
				if e != nil {
					return e
				}
				if poll.MetricsDefault.PolicyVersion != version || version < 1 {
					return errTeamsConsentStale
				}
			}
			if e = s.Catalog.setTeamsUserPolicy(r.Context(), d, credential, *body.SettingsRevision, *body.Enabled, *body.OptOut, version); e != nil {
				return e
			}
			s.wakeTeams()
			writeJSON(w, map[string]bool{"updated": true}, 200)
			return nil
		case r.URL.Path == "/api/teams/worker/check" && r.Method == http.MethodPost:
			s.wakeTeams()
			writeJSON(w, map[string]bool{"requested": true}, 200)
			return nil
		case r.URL.Path == "/api/teams/worker/model/inspect" && r.Method == http.MethodPost:
			inspection, e := s.inspectTeamsModel(r.Context())
			if e != nil {
				return e
			}
			writeJSON(w, inspection, 200)
			return nil
		case r.URL.Path == "/api/teams/worker/model/approve" && r.Method == http.MethodPost:
			var requested teamsModelInspection
			if e = teamsLocalAction(w, r, &requested, map[string]bool{"provider": true, "model": true, "effort": true, "account": true, "sessionDigest": true, "authDigest": true, "settingsRevision": true}); e != nil {
				return errTeamsConsentStale
			}
			current, e := s.inspectTeamsModel(r.Context())
			if e != nil {
				return e
			}
			if requested != current {
				return errTeamsConsentStale
			}
			if e = s.Catalog.grantTeamsModel(r.Context(), d, credential, current); e != nil {
				return e
			}
			s.wakeTeams()
			writeJSON(w, map[string]bool{"approved": true}, 200)
			return nil
		case r.URL.Path == "/api/teams/worker/model/revoke" && r.Method == http.MethodPost:
			if e = s.Catalog.revokeTeamsModel(r.Context(), d, credential); e != nil {
				return e
			}
			s.wakeTeams()
			writeJSON(w, map[string]bool{"revoked": true}, 200)
			return nil

		case r.URL.Path == "/api/teams/worker/reader-labels" && r.Method == http.MethodPost:
			var body struct {
				WorkID     string `json:"workId"`
				ConfigHash string `json:"configHash"`
			}
			if e = teamsLocalAction(w, r, &body, map[string]bool{"workId": true, "configHash": true}); e != nil {
				return errTeamsConsentStale
			}
			s.teamsWorker.mu.Lock()
			item, ok := s.teamsWorker.items[body.WorkID]
			if !ok || !settings.Enabled || s.teamsWorker.scope != scope || item.Work.ConfigSHA256 != body.ConfigHash || (item.State != "awaiting_source_consent" && item.State != "awaiting_review") || !validTeamsSeriesReview(item.Work.Config, item.Work.ConfigSHA256, body.ConfigHash) {
				s.teamsWorker.mu.Unlock()
				return errTeamsConsentStale
			}
			key := body.WorkID + ":" + body.ConfigHash
			generation := s.teamsWorker.generation
			workBinding := s.teamsWorker.workBindings[body.WorkID]
			if s.teamsWorker.labelFlights == nil {
				s.teamsWorker.labelFlights = map[string]bool{}
			}
			if s.teamsWorker.labelLast == nil {
				s.teamsWorker.labelLast = map[string]time.Time{}
			}
			if s.teamsWorker.labelFlights[key] || time.Since(s.teamsWorker.labelLast[key]) < 2*time.Second {
				s.teamsWorker.mu.Unlock()
				return errTeamsConsentStale
			}
			s.teamsWorker.labelFlights[key] = true
			s.teamsWorker.labelLast[key] = time.Now()
			if ok {
				item.Labels = nil
				item.LabelsAt = time.Time{}
				s.teamsWorker.items[body.WorkID] = item
			}
			s.teamsWorker.mu.Unlock()
			defer func() { s.teamsWorker.mu.Lock(); delete(s.teamsWorker.labelFlights, key); s.teamsWorker.mu.Unlock() }()
			if !ok || item.Work.ConfigSHA256 != body.ConfigHash || (item.State != "awaiting_source_consent" && item.State != "awaiting_review") {
				return errTeamsConsentStale
			}
			labels, lookupErr := client.ReaderLabels(r.Context(), body.WorkID)
			if lookupErr != nil {
				return lookupErr
			}
			latest, stateErr := readTeamsSettings(r.Context(), s.Catalog.reader())
			if stateErr != nil || !latest.Enabled || latest.SettingsRevision != settings.SettingsRevision || latest.LibraryID != d.ID || latest.InstanceID != d.Instance || latest.OrgID != credential.OrgID || latest.InstallationID != credential.InstallationID {
				return errTeamsConsentStale
			}
			now := time.Now()
			if !validReaderLabels(item.Work, &labels, now, false) {
				return errTeamsConsentStale
			}
			s.teamsWorker.mu.Lock()
			current, ok := s.teamsWorker.items[body.WorkID]
			if ok && s.teamsWorker.scope == scope && s.teamsWorker.generation == generation && s.teamsWorker.workBindings[body.WorkID] == workBinding && r.Context().Err() == nil && current.Work.ConfigSHA256 == body.ConfigHash && current.State == item.State {
				current.Labels = &labels
				current.LabelsAt = now
				current.LabelsScope = scope
				s.teamsWorker.items[body.WorkID] = current
			} else {
				ok = false
			}
			s.teamsWorker.mu.Unlock()
			if !ok {
				return errTeamsConsentStale
			}
			writeJSON(w, map[string]bool{"loaded": true}, 200)
			return nil
		case r.URL.Path == "/api/teams/worker/allow-source" && r.Method == http.MethodPost:
			var body struct {
				WorkID           string `json:"workId"`
				SettingsRevision *int64 `json:"settingsRevision"`
				ConfigHash       string `json:"configHash,omitempty"`
				LabelsToken      string `json:"labelsToken,omitempty"`
			}
			if e = teamsLocalAction(w, r, &body, map[string]bool{"workId": true, "settingsRevision": true, "configHash": true, "labelsToken": true}); e != nil || body.SettingsRevision == nil {
				return errTeamsConsentStale
			}
			s.teamsWorker.mu.Lock()
			item, ok := s.teamsWorker.items[body.WorkID]
			series := ok && teamsHasSeriesConfig(item.Work.Config)
			live := ok && settings.Enabled && s.teamsWorker.scope == scope
			viewKey := s.teamsWorker.bindings[body.WorkID]
			tokenOK := live && body.LabelsToken != "" && body.LabelsToken == teamsReaderLabelToken(item, scope, s.teamsWorker.generation)
			s.teamsWorker.mu.Unlock()
			if !live || item.State != "awaiting_source_consent" {
				return errTeamsConsentStale
			}
			if e = s.teamsRequireCurrentView(r.Context(), scope, item.Work, settings, viewKey); e != nil {
				return e
			}
			if !validTeamsSeriesReview(item.Work.Config, item.Work.ConfigSHA256, body.ConfigHash) || (series && !tokenOK) {
				return errTeamsConsentStale
			}
			if e = s.Catalog.grantTeamsQuestionForView(r.Context(), item.Work, d, *body.SettingsRevision, scope, viewKey); e != nil {
				return e
			}
			s.wakeTeams()
			writeJSON(w, map[string]bool{"granted": true}, 200)
			return nil
		case r.URL.Path == "/api/teams/worker/approve-result" && r.Method == http.MethodPost:
			var body struct {
				WorkID      string `json:"workId"`
				PayloadHash string `json:"payloadHash"`
				ConfigHash  string `json:"configHash,omitempty"`
				LabelsToken string `json:"labelsToken,omitempty"`
			}
			if e = teamsLocalAction(w, r, &body, map[string]bool{"workId": true, "payloadHash": true, "configHash": true, "labelsToken": true}); e != nil {
				return errTeamsConsentStale
			}
			draft, draftErr := readTeamsV2Draft(r.Context(), s.Catalog.reader(), body.WorkID)
			if draftErr != nil && !errors.Is(draftErr, sql.ErrNoRows) {
				return draftErr
			}
			s.teamsWorker.mu.Lock()
			item, ok := s.teamsWorker.items[body.WorkID]
			series := (ok && teamsHasSeriesConfig(item.Work.Config)) || (draftErr == nil && teamsHasSeriesConfig(draft.Config))
			live := ok && item.State == "awaiting_review" && settings.Enabled && s.teamsWorker.scope == scope
			viewKey := s.teamsWorker.bindings[body.WorkID]
			tokenOK := live && body.LabelsToken != "" && body.LabelsToken == teamsReaderLabelToken(item, scope, s.teamsWorker.generation)
			s.teamsWorker.mu.Unlock()
			if !live {
				return errTeamsConsentStale
			}
			if e = s.teamsRequireCurrentView(r.Context(), scope, item.Work, settings, viewKey); e != nil {
				return e
			}
			if series && (!ok || draftErr != nil || !bytes.Equal(draft.Config, item.Work.Config) || !validTeamsSeriesReview(item.Work.Config, item.Work.ConfigSHA256, body.ConfigHash) || !tokenOK) {
				return errTeamsConsentStale
			}

			if _, contentErr := readTeamsContentRun(r.Context(), s.Catalog.reader(), body.WorkID); contentErr == nil {
				e = s.Catalog.approveTeamsContent(r.Context(), d, credential, body.WorkID, body.PayloadHash)
			} else if errors.Is(contentErr, sql.ErrNoRows) {
				e = s.Catalog.reviewTeamsV2DraftWithConfig(r.Context(), d, body.WorkID, body.PayloadHash, body.ConfigHash)
			} else {
				e = contentErr
			}
			if e != nil {
				return e
			}
			s.wakeTeams()
			writeJSON(w, map[string]bool{"approved": true}, 200)
			return nil
		case r.URL.Path == "/api/teams/worker/decline" && r.Method == http.MethodPost:
			var body struct {
				WorkID string `json:"workId"`
			}
			if e = teamsLocalAction(w, r, &body, map[string]bool{"workId": true}); e != nil {
				return errTeamsConsentStale
			}
			s.teamsWorker.mu.Lock()
			item, ok := s.teamsWorker.items[body.WorkID]
			binding := s.teamsWorker.workBindings[body.WorkID]
			valid := s.teamsWorker.scope == scope && settings.Enabled
			s.teamsWorker.mu.Unlock()
			if !valid || !ok || (item.State != "awaiting_source_consent" && item.State != "awaiting_model_permission" && item.State != "awaiting_review" && item.State != "decline_pending" && item.State != "started" && item.State != "unconfirmed" && item.State != "source_safety_waiting") {
				return errTeamsConsentStale
			}
			lease, e := s.Catalog.teamsSharingLease()
			if e != nil {
				return e
			}
			defer lease.Close()
			if e = s.Catalog.writeTransaction(r.Context(), "teams-v2-decline", func(tx *sql.Tx) error {
				content, contentErr := readTeamsContentRun(r.Context(), tx, body.WorkID)
				if contentErr != nil && !errors.Is(contentErr, sql.ErrNoRows) {
					return contentErr
				}
				if contentErr == nil && content.State != "awaiting_review" && content.State != "started" && content.State != "withdrawn" {
					return errTeamsConsentStale
				}
				if _, e = tx.ExecContext(r.Context(), `UPDATE teams_content_runs SET state='withdrawn' WHERE work_id=? AND state IN ('awaiting_review','started')`, body.WorkID); e != nil {
					return e
				}
				draft, e := readTeamsV2Draft(r.Context(), tx, body.WorkID)
				if e != nil && !errors.Is(e, sql.ErrNoRows) {
					return e
				}
				if e == nil && draft.State != "awaiting_review" && draft.State != "withdrawn" {
					return errTeamsConsentStale
				}
				if _, e = tx.ExecContext(r.Context(), `UPDATE teams_v2_results SET state='withdrawn' WHERE work_id=? AND state='awaiting_review'`, body.WorkID); e != nil {
					return e
				}
				_, e = tx.ExecContext(r.Context(), `INSERT INTO teams_v2_declines(work_id,library_id,instance_id,org_id,installation_id,config_hash,state) VALUES(?,?,?,?,?,?,'pending') ON CONFLICT(work_id) DO NOTHING`, body.WorkID, d.ID, d.Instance, credential.OrgID, credential.InstallationID, item.Work.ConfigSHA256)
				if e != nil {
					return e
				}
				var count int
				if e = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM teams_v2_declines WHERE work_id=? AND library_id=? AND instance_id=? AND org_id=? AND installation_id=? AND config_hash=?`, body.WorkID, d.ID, d.Instance, credential.OrgID, credential.InstallationID, item.Work.ConfigSHA256).Scan(&count); e != nil {
					return e
				}
				if count != 1 {
					return errTeamsConsentStale
				}
				return nil
			}); e != nil {
				return e
			}
			s.teamsWorker.mu.Lock()
			if current, ok := s.teamsWorker.items[body.WorkID]; ok && s.teamsWorker.scope == scope && s.teamsWorker.workBindings[body.WorkID] == binding {
				current.State = "decline_pending"
				s.teamsWorker.items[body.WorkID] = current
			}
			s.teamsWorker.mu.Unlock()
			manifest := teamsWorkerCapabilities()
			if !teamsNumericBuildEligible(manifest) {
				return errTeamsQueryUnsupported
			}
			bounded, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			if e = client.Status(bounded, body.WorkID, teams.WorkStatus{State: "declined", AppVersion: manifest.AppVersion}); e != nil {
				return e
			}
			if e = s.Catalog.writeTransaction(r.Context(), "teams-v2-decline-sent", func(tx *sql.Tx) error {
				_, e := tx.ExecContext(r.Context(), `UPDATE teams_v2_declines SET state='sent' WHERE work_id=?`, body.WorkID)
				return e
			}); e != nil {
				return e
			}
			s.teamsWorker.mu.Lock()
			if current, ok := s.teamsWorker.items[body.WorkID]; ok && s.teamsWorker.scope == scope && s.teamsWorker.workBindings[body.WorkID] == binding {
				current.State = "declined"
				s.teamsWorker.items[body.WorkID] = current
			}
			s.teamsWorker.mu.Unlock()
			writeJSON(w, map[string]bool{"declined": true}, 200)
			return nil
		default:
			return errors.New("unknown Teams worker action")
		}
	})
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errTeamsConsentStale) || errors.Is(err, errTeamsNotEnrolled) || errors.Is(err, errTeamsSharingBusy) || errors.Is(err, errTeamsSnapshotChanged) {
			code = http.StatusConflict
		}
		if errors.Is(err, errTeamsQueryUnsupported) {
			code = http.StatusServiceUnavailable
		}
		writeError(w, errors.New("Teams settings or request changed; refresh and try again"), code)
	}
}

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
		switch {
		case r.URL.Path == "/api/teams/worker" && r.Method == http.MethodGet:
			s.teamsWorker.mu.Lock()
			items := make([]teamsWorkerItem, 0, len(s.teamsWorker.items))
			for _, item := range s.teamsWorker.items {
				items = append(items, item)
			}
			next, checking, problem := s.teamsWorker.next, s.teamsWorker.checking, s.teamsWorker.problem
			s.teamsWorker.mu.Unlock()
			type row struct {
				WorkID        string                   `json:"workId"`
				Question      string                   `json:"question"`
				Requester     string                   `json:"requester"`
				State         string                   `json:"state"`
				PayloadHash   string                   `json:"payloadHash"`
				ConfigHash    string                   `json:"configHash,omitempty"`
				PublicationV2 *teamsReportSeries       `json:"publicationV2,omitempty"`
				Readers       []string                 `json:"readers"`
				Payload       json.RawMessage          `json:"payload"`
				Prompt        string                   `json:"prompt"`
				Retention     json.RawMessage          `json:"retention"`
				Schedule      json.RawMessage          `json:"schedule"`
				Period        json.RawMessage          `json:"period"`
				Exports       []teamsFeedExport        `json:"exports"`
				Publications  []teamsReportPublication `json:"publications"`
			}
			out := make([]row, 0, len(items))
			for _, item := range items {
				var config struct {
					PublicationV2 *teamsReportSeries `json:"publication_v2"`
					Question      string             `json:"question"`
					Prompt        string             `json:"prompt"`
					Retention     json.RawMessage    `json:"retention_policy"`
					Schedule      json.RawMessage    `json:"schedule"`
					Requester     struct {
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
					_, e = teamsPlanFromWork(item.Work)
				}
				if e != nil {
					out = append(out, row{WorkID: item.Work.ID, Question: "This request needs a newer Pharos build.", State: item.State})
					continue
				}
				_ = json.Unmarshal(item.Work.Config, &config)
				readers := []string{}
				for _, reader := range config.Audiences.ResponseReaders {
					readers = append(readers, reader.ID)
				}
				var configHash string
				if config.PublicationV2 != nil {
					configHash = item.Work.ConfigSHA256
				}
				out = append(out, row{WorkID: item.Work.ID, Question: config.Question, Prompt: config.Prompt, Retention: config.Retention, Requester: config.Requester.DisplayName, State: item.State, PayloadHash: item.PayloadHash, ConfigHash: configHash, PublicationV2: config.PublicationV2, Readers: readers, Payload: json.RawMessage(item.Payload), Schedule: config.Schedule, Period: item.Work.Period, Exports: config.Audiences.Exports, Publications: config.Audiences.Publications})
			}
			var modelApproved bool
			if e = s.Catalog.reader().QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM teams_model_permission WHERE id=1 AND library_id=? AND instance_id=? AND org_id=? AND installation_id=? AND host_id=? AND settings_revision=?)`, d.ID, d.Instance, credential.OrgID, credential.InstallationID, currentHost().ID, settings.SettingsRevision).Scan(&modelApproved); e != nil {
				return e
			}
			writeJSON(w, map[string]any{"enabled": settings.Enabled, "optOut": settings.MetricsOptOut, "settingsRevision": settings.SettingsRevision, "acknowledgedPolicy": settings.AcknowledgedPolicy, "nextCheck": next, "checking": checking, "problem": problem, "items": out, "modelApproved": modelApproved}, 200)
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
		case r.URL.Path == "/api/teams/worker/allow-source" && r.Method == http.MethodPost:
			var body struct {
				WorkID           string `json:"workId"`
				SettingsRevision *int64 `json:"settingsRevision"`
				ConfigHash       string `json:"configHash,omitempty"`
			}
			if e = teamsLocalAction(w, r, &body, map[string]bool{"workId": true, "settingsRevision": true, "configHash": true}); e != nil || body.SettingsRevision == nil {
				return errTeamsConsentStale
			}
			s.teamsWorker.mu.Lock()
			item, ok := s.teamsWorker.items[body.WorkID]
			s.teamsWorker.mu.Unlock()
			if !ok || item.State != "awaiting_source_consent" {
				return errTeamsConsentStale
			}
			if !validTeamsSeriesReview(item.Work.Config, item.Work.ConfigSHA256, body.ConfigHash) {
				return errTeamsConsentStale
			}
			if e = s.Catalog.grantTeamsQuestion(r.Context(), item.Work, d, *body.SettingsRevision); e != nil {
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
			}
			if e = teamsLocalAction(w, r, &body, map[string]bool{"workId": true, "payloadHash": true, "configHash": true}); e != nil {
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
			s.teamsWorker.mu.Unlock()
			if !ok || (item.State != "awaiting_source_consent" && item.State != "awaiting_model_permission" && item.State != "awaiting_review" && item.State != "decline_pending" && item.State != "started" && item.State != "unconfirmed") {
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
			item.State = "decline_pending"
			s.teamsWorker.items[body.WorkID] = item
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
			item.State = "declined"
			s.teamsWorker.items[body.WorkID] = item
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

package archive

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

const teamsInspectionSchema = `{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`

type teamsModelInspection struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	Effort           string `json:"effort"`
	Account          string `json:"account"`
	SessionDigest    string `json:"sessionDigest"`
	AuthDigest       string `json:"authDigest"`
	SettingsRevision int64  `json:"settingsRevision"`
}

// This is invoked only in response to an explicit local user action; it
// starts no model turn and sends no question or library material.
func (s *Server) inspectTeamsModel(ctx context.Context) (teamsModelInspection, error) {
	var out teamsModelInspection
	settings, err := s.askSettings(ctx)
	if err != nil {
		return out, err
	}
	selection := settings.Selection
	if selection.Provider != "Claude" || selection.Model == "" || selection.Effort == "" || selection.Effort == "Default" || selection.Fast {
		return out, errors.New("Select a Claude model and explicit effort in Ask settings first.")
	}
	var provider askProvider
	for _, p := range askInstalled() {
		if p.ID == "Claude" {
			provider = p
			break
		}
	}
	if provider.path == "" {
		return out, errTeamsClaudeIsolation
	}
	// The preflight verifies the actual selected model and effort through the
	// effective same-process control response. No ambient discovery is run.
	provider.Models = []askModel{{ID: selection.Model, Efforts: []string{selection.Effort}}}
	stream, err := teamsInspectClaude(ctx, provider, selection, []byte(teamsInspectionSchema), s.teamsOperationChild)
	if err != nil {
		return out, err
	}
	defer stream.stop()
	if err = stream.checkIdentity(ctx); err != nil {
		return out, err
	}
	local, err := readTeamsSettings(ctx, s.Catalog.reader())
	if err != nil {
		return out, err
	}
	return teamsModelInspection{Provider: "Claude", Model: selection.Model, Effort: selection.Effort, Account: stream.accountLabel, SessionDigest: hex.EncodeToString(stream.account[:]), AuthDigest: hex.EncodeToString(stream.auth[:]), SettingsRevision: local.SettingsRevision}, nil
}

func (c *Catalog) grantTeamsModel(ctx context.Context, d LibraryDescriptor, credential *teams.EnrollmentCredential, inspection teamsModelInspection) error {
	if inspection.Provider != "Claude" || inspection.Model == "" || inspection.Effort == "" || len(inspection.SessionDigest) != 64 || len(inspection.AuthDigest) != 64 {
		return errTeamsConsentStale
	}
	if b, e := hex.DecodeString(inspection.SessionDigest); e != nil || len(b) != 32 {
		return errTeamsConsentStale
	}
	if b, e := hex.DecodeString(inspection.AuthDigest); e != nil || len(b) != 32 {
		return errTeamsConsentStale
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	return c.writeTransaction(ctx, "teams-model-permission", func(tx *sql.Tx) error {
		settings, e := readTeamsSettings(ctx, tx)
		if e != nil {
			return e
		}
		if credential == nil || credential.LibraryID != d.ID || !settings.Enabled || settings.SettingsRevision != inspection.SettingsRevision || settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.OrgID != credential.OrgID || settings.InstallationID != credential.InstallationID {
			return errTeamsConsentStale
		}
		if _, e = tx.ExecContext(ctx, `UPDATE teams_local_settings SET settings_revision=settings_revision+1 WHERE id=1`); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO teams_model_permission(id,library_id,instance_id,org_id,installation_id,host_id,provider,model,effort,session_digest,auth_digest,settings_revision,granted_at) VALUES(1,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET library_id=excluded.library_id,instance_id=excluded.instance_id,org_id=excluded.org_id,installation_id=excluded.installation_id,host_id=excluded.host_id,provider=excluded.provider,model=excluded.model,effort=excluded.effort,session_digest=excluded.session_digest,auth_digest=excluded.auth_digest,settings_revision=excluded.settings_revision,granted_at=excluded.granted_at`, d.ID, d.Instance, credential.OrgID, credential.InstallationID, currentHost().ID, inspection.Provider, inspection.Model, inspection.Effort, inspection.SessionDigest, inspection.AuthDigest, settings.SettingsRevision+1, time.Now().UTC().Format(time.RFC3339Nano))
		return e
	})
}

func (c *Catalog) revokeTeamsModel(ctx context.Context, d LibraryDescriptor, credential *teams.EnrollmentCredential) error {
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	return c.writeTransaction(ctx, "teams-model-revoke", func(tx *sql.Tx) error {
		s, e := readTeamsSettings(ctx, tx)
		if e != nil {
			return e
		}
		if credential == nil || credential.LibraryID != d.ID || s.LibraryID != d.ID || s.InstanceID != d.Instance || s.OrgID != credential.OrgID || s.InstallationID != credential.InstallationID {
			return errTeamsConsentStale
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM teams_model_permission`); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_local_settings SET settings_revision=settings_revision+1 WHERE id=1`)
		return e
	})
}

// Loading is mandatory at each disclosure handoff. A moved/recovered library,
// changed settings or a different installation/host cannot inherit consent.
func (c *Catalog) loadTeamsModelPermission(ctx context.Context, d LibraryDescriptor, credential *teams.EnrollmentCredential) (askSelection, teamsClaudeReviewedAccount, error) {
	var selection askSelection
	var account teamsClaudeReviewedAccount
	if credential == nil {
		return selection, account, errTeamsConsentStale
	}
	var session, auth string
	err := c.reader().QueryRowContext(ctx, `SELECT p.provider,p.model,p.effort,p.session_digest,p.auth_digest FROM teams_model_permission p JOIN teams_local_settings s ON s.id=1 AND s.enabled=1 AND s.settings_revision=p.settings_revision WHERE p.id=1 AND p.library_id=? AND p.instance_id=? AND p.org_id=? AND p.installation_id=? AND p.host_id=? AND s.library_id=p.library_id AND s.instance_id=p.instance_id AND s.org_id=p.org_id AND s.installation_id=p.installation_id`, d.ID, d.Instance, credential.OrgID, credential.InstallationID, currentHost().ID).Scan(&selection.Provider, &selection.Model, &selection.Effort, &session, &auth)
	if err != nil {
		return selection, account, errTeamsConsentStale
	}
	a, e1 := hex.DecodeString(session)
	b, e2 := hex.DecodeString(auth)
	if e1 != nil || e2 != nil || len(a) != 32 || len(b) != 32 || selection.Provider != "Claude" {
		return askSelection{}, account, errTeamsConsentStale
	}
	copy(account.Session[:], a)
	copy(account.Auth[:], b)
	return selection, account, nil
}

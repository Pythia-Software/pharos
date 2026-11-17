package archive

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gbdubs/pharos/internal/teams"
)

type teamsSourceAccountRow struct {
	BindingID    string `json:"bindingId"`
	Provider     string `json:"provider"`
	SourceName   string `json:"sourceName"`
	AccountLabel string `json:"accountLabel"`
	Host         string `json:"host"`
	Root         string `json:"root"`
	Identifiable int    `json:"identifiable"`
	Ambiguous    int    `json:"ambiguous"`
	Mode         string `json:"mode"`
	Exposed      bool   `json:"exposed"`
	Unindexed    bool   `json:"unindexed"`
}

func (s *Server) teamsSourceAccountsHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	err := s.withTeamsLibraryClient(r.Context(), func(_ *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential) error {
		switch {
		case r.URL.Path == "/api/teams/source-accounts" && r.Method == http.MethodGet:
			tx, err := s.Catalog.reader().BeginTx(r.Context(), nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			settings, err := readTeamsSettings(r.Context(), tx)
			if err != nil {
				return err
			}
			var revision int64
			var requested bool
			if err = tx.QueryRowContext(r.Context(), `SELECT revision,proof_requested FROM teams_source_revision WHERE id=1`).Scan(&revision, &requested); err != nil {
				return err
			}
			rows, err := tx.QueryContext(r.Context(), `SELECT b.binding_id,b.provider,b.source_name,b.account_label,b.host_id,b.source_root,
 COALESCE(SUM(CASE WHEN p.status='header_independent_unvalidated' THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN p.status<>'header_independent_unvalidated' THEN 1 ELSE 0 END),0),
 COALESCE(m.mode,'unassigned'),EXISTS(WITH RECURSIVE lineage(binding_id) AS (
 SELECT b.binding_id UNION SELECT prior.binding_id FROM lineage l JOIN teams_source_bindings current ON current.binding_id=l.binding_id JOIN teams_source_bindings prior ON prior.host_id=current.host_id AND lower(prior.source_kind)=lower(current.source_kind) AND (prior.source_name=current.source_name OR prior.source_root=current.source_root)
) SELECT 1 FROM teams_source_mapping_exposure WHERE binding_id IN (SELECT binding_id FROM lineage))
 FROM teams_source_bindings b LEFT JOIN teams_native_source_proofs p ON p.binding_id=b.binding_id
 LEFT JOIN teams_source_mapping_current current ON current.binding_id=b.binding_id
 LEFT JOIN teams_source_mapping_revisions m ON m.revision=current.revision AND m.library_id=? AND m.instance_id=? AND m.origin=? AND m.org_id=? AND m.person_id=? AND m.installation_id=?
 WHERE b.superseded_at='' GROUP BY b.binding_id ORDER BY b.provider,b.source_name,b.host_id,b.binding_id`, d.ID, d.Instance, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID)
			if err != nil {
				return err
			}
			var list []teamsSourceAccountRow
			list = []teamsSourceAccountRow{}
			for rows.Next() {
				var v teamsSourceAccountRow
				if err = rows.Scan(&v.BindingID, &v.Provider, &v.SourceName, &v.AccountLabel, &v.Host, &v.Root, &v.Identifiable, &v.Ambiguous, &v.Mode, &v.Exposed); err != nil {
					rows.Close()
					return err
				}
				list = append(list, v)
			}
			if err = rows.Err(); err != nil {
				rows.Close()
				return err
			}
			if err = rows.Close(); err != nil {
				return err
			}
			missing, err := tx.QueryContext(r.Context(), `SELECT st.kind,st.source_name,st.host_id FROM source_states st WHERE NOT EXISTS (SELECT 1 FROM teams_source_bindings b WHERE b.host_id=st.host_id AND b.source_name=st.source_name AND b.superseded_at='') ORDER BY st.kind,st.source_name,st.host_id`)
			if err != nil {
				return err
			}
			for missing.Next() {
				v := teamsSourceAccountRow{Mode: "unavailable", Unindexed: true}
				if err = missing.Scan(&v.Provider, &v.SourceName, &v.Host); err != nil {
					missing.Close()
					return err
				}
				list = append(list, v)
			}
			if err = missing.Err(); err != nil {
				missing.Close()
				return err
			}
			if err = missing.Close(); err != nil {
				return err
			}
			if err = tx.Commit(); err != nil {
				return err
			}
			writeJSON(w, map[string]any{"sourceRevision": revision, "settingsRevision": settings.SettingsRevision, "proofRequested": requested, "sources": list}, http.StatusOK)
			return nil
		case r.URL.Path == "/api/teams/source-accounts/index" && r.Method == http.MethodPost:
			var body struct {
				SettingsRevision *int64 `json:"settingsRevision"`
			}
			if err := decodeTeamsSourceBody(w, r, &body); err != nil || body.SettingsRevision == nil {
				return errors.New("invalid source indexing request")
			}
			if err := s.Catalog.requestTeamsSourceProof(r.Context(), d, *body.SettingsRevision); err != nil {
				return err
			}
			writeJSON(w, map[string]bool{"requested": true}, http.StatusOK)
			return nil
		case r.URL.Path == "/api/teams/source-accounts/review" && r.Method == http.MethodPost:
			var body struct {
				BindingID        string `json:"sourceBindingId"`
				ExpectedRevision *int64 `json:"expectedRevision"`
				Mode             string `json:"mode"`
				Attested         bool   `json:"attested"`
			}
			if err := decodeTeamsSourceBody(w, r, &body); err != nil || body.ExpectedRevision == nil {
				return errors.New("invalid source review")
			}
			if err := s.Catalog.reviewTeamsSourceMapping(r.Context(), d, credential, body.BindingID, body.Mode, *body.ExpectedRevision, body.Attested); err != nil {
				return err
			}
			writeJSON(w, map[string]bool{"reviewed": true}, http.StatusOK)
			return nil
		default:
			return errors.New("unknown source accounts action")
		}
	})
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errTeamsConsentStale) || errors.Is(err, errTeamsNotEnrolled) || errors.Is(err, errTeamsSharingBusy) || errors.Is(err, errTeamsSourceRemapRequired) {
			code = http.StatusConflict
		}
		if code == http.StatusBadRequest {
			err = errors.New("Could not update source accounts")
		}
		writeError(w, err, code)
	}
}

func decodeTeamsSourceBody(w http.ResponseWriter, r *http.Request, out any) error {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		return err
	}
	allowed := map[string]bool{"settingsRevision": true}
	if r.URL.Path == "/api/teams/source-accounts/review" {
		allowed = map[string]bool{"sourceBindingId": true, "expectedRevision": true, "mode": true, "attested": true}
	}
	if _, err = teamsStrictObject(raw, allowed); err != nil || !teams.ValidUniqueJSON(raw) {
		return errors.New("invalid source request")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return errors.New("invalid source request")
	}
	return nil
}

package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

var errTeamsContentRetryWaiting = errors.New("content safety check waiting")
var errTeamsContentRetryParked = errors.New("content safety check parked")

func teamsContentSafetyInProgress(err error) bool {
	var h *teams.HTTPError
	return errors.As(err, &h) && h.Status == 409 && h.Code == "source_safety_in_progress"
}

// This record only schedules exact pre-start binding checks. It never grants
// permission; every check repeats the existing signed poll and consent gates.
type teamsContentRetry struct {
	Version             int       `json:"version"`
	Snapshot            string    `json:"snapshot"`
	State               string    `json:"state"`
	First               time.Time `json:"first"`
	Last                time.Time `json:"last"`
	Count               int       `json:"count"`
	AuthorizationExpiry time.Time `json:"authorization_expiry"`
	WorkExpiry          time.Time `json:"work_expiry"`
	RequestHash         string    `json:"request_hash"`
}

func teamsContentRetryKey(work string) string { return "teams_content_retry_v1:" + work }

// A prepared upload cannot have attempted a bind for the same snapshot.
// An unrelated or unreadable scheduling row is not authority over this upload.
func teamsPreparedRetryTracking(prior teamsContentRetry, err error, snapshot string) (bool, error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return true, nil
	case errors.Is(err, errTeamsContentRetryParked):
		return false, nil
	case err != nil:
		return false, err
	case prior.Snapshot == snapshot:
		return false, errTeamsContentRetryParked
	default:
		return false, nil
	}
}

type teamsRetryReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readTeamsContentRetry(ctx context.Context, q teamsRetryReader, work string) (teamsContentRetry, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, teamsContentRetryKey(work)).Scan(&raw); err != nil {
		return teamsContentRetry{}, err
	}
	return decodeTeamsContentRetry(raw)
}
func decodeTeamsContentRetry(raw string) (teamsContentRetry, error) {
	var r teamsContentRetry
	if len(raw) > 4096 {
		return r, errTeamsContentRetryParked
	}
	fields, err := teamsStrictObject([]byte(raw), map[string]bool{"version": true, "snapshot": true, "state": true, "first": true, "last": true, "count": true, "authorization_expiry": true, "work_expiry": true, "request_hash": true})
	if err != nil || len(fields) != 9 || json.Unmarshal([]byte(raw), &r) != nil || r.Version != 1 || r.Snapshot == "" || r.Last.IsZero() || r.Count < 0 || r.Count > 4 || (r.State != "inflight" && r.State != "waiting" && r.State != "parked") || (r.State == "waiting" && (r.First.IsZero() || r.Last.Before(r.First) || r.AuthorizationExpiry.IsZero() || r.WorkExpiry.IsZero() || len(r.RequestHash) != 64)) {
		return teamsContentRetry{}, errTeamsContentRetryParked
	}
	return r, nil
}

func writeTeamsContentRetry(ctx context.Context, tx *sql.Tx, work string, r teamsContentRetry) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, teamsContentRetryKey(work), string(raw))
	return err
}

// nextTeamsContentRetry accounts a follow-up before the request. Local time
// only schedules the interval; server time decides permission/work expiry.
func nextTeamsContentRetry(r teamsContentRetry, now, serverTime time.Time) (teamsContentRetry, error) {
	if r.State != "waiting" || r.First.IsZero() || r.Last.IsZero() || r.AuthorizationExpiry.IsZero() || r.WorkExpiry.IsZero() || serverTime.IsZero() || !serverTime.Before(r.AuthorizationExpiry) || !serverTime.Before(r.WorkExpiry) || now.Before(r.Last) || now.Before(r.First) || now.After(r.First.Add(2*time.Minute)) || r.Count >= 4 {
		return r, errTeamsContentRetryParked
	}
	if now.Before(r.Last.Add(30 * time.Second)) {
		return r, errTeamsContentRetryWaiting
	}
	r.Count++
	r.State = "inflight"
	r.Last = now
	return r, nil
}

// Startup is local-only: it does not poll unless this enrollment has a sealed
// upload with a still-pending retry and the current model and source grants.
func (s *Server) teamsStartupRetryDelay(ctx context.Context, now time.Time) (time.Duration, bool) {
	var delay time.Duration
	var found bool
	err := s.withTeamsLibraryClient(ctx, func(_ *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential) error {
		delay, found = s.teamsRetryDelayForEnrollment(ctx, now, d, credential)
		return nil
	})
	return delay, err == nil && found
}
func (s *Server) teamsRetryDelayForEnrollment(ctx context.Context, now time.Time, d LibraryDescriptor, credential *teams.EnrollmentCredential) (time.Duration, bool) {
	settings, err := readTeamsSettings(ctx, s.Catalog.reader())
	if err != nil || !settings.Enabled || settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.OrgID != credential.OrgID || settings.InstallationID != credential.InstallationID {
		return 0, false
	}
	// Scan at most 100 indexed retry keys; the existing work-queue fairness
	// policy determines which eligible work the subsequent poll processes.
	prefix := teamsContentRetryKey("")
	rows, err := s.Catalog.reader().QueryContext(ctx, `WITH candidates AS (SELECT key,value FROM meta WHERE key>=? AND key<? ORDER BY key LIMIT 100) SELECT u.snapshot_id,m.value FROM candidates m JOIN teams_v2_uploads u ON u.work_id=substr(m.key,?) WHERE u.state='sealed' AND u.org_id=? AND u.installation_id=? AND u.library_id=? AND u.instance_id=? AND u.settings_revision=? AND NOT EXISTS(SELECT 1 FROM teams_content_runs r WHERE r.work_id=u.work_id) AND EXISTS(SELECT 1 FROM teams_request_grants g WHERE g.version_id=u.version_id AND g.library_id=u.library_id AND g.instance_id=u.instance_id AND g.org_id=u.org_id AND g.installation_id=u.installation_id AND g.config_hash=u.config_sha256 AND g.settings_revision=u.settings_revision) AND EXISTS(SELECT 1 FROM teams_model_permission p WHERE p.id=1 AND p.library_id=u.library_id AND p.instance_id=u.instance_id AND p.org_id=u.org_id AND p.installation_id=u.installation_id AND p.host_id=? AND p.settings_revision=u.settings_revision)`, prefix, prefix+"\uffff", len(prefix)+1, settings.OrgID, settings.InstallationID, settings.LibraryID, settings.InstanceID, settings.SettingsRevision, currentHost().ID)
	if err != nil {
		return 0, false
	}
	defer rows.Close()
	var earliest time.Time
	for rows.Next() {
		var snapshot, raw string
		if rows.Scan(&snapshot, &raw) != nil {
			return 0, false
		}
		r, e := decodeTeamsContentRetry(raw)
		if e != nil || r.Snapshot != snapshot || r.State != "waiting" || r.Count >= 4 || now.Before(r.Last) || now.Before(r.First) || now.After(r.First.Add(2*time.Minute)) {
			continue
		}
		due := r.Last.Add(30 * time.Second)
		if due.Before(now) {
			due = now
		}
		if earliest.IsZero() || due.Before(earliest) {
			earliest = due
		}
	}
	if rows.Err() != nil {
		return 0, false
	}

	if earliest.IsZero() {
		return 0, false
	}
	return earliest.Sub(now), true
}

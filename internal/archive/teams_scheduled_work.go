package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

// Only the v2 source path can prepare a scheduled publication. The legacy
// manifest and result paths continue to reject this assignment.
func teamsV2PlanFromWork(w teams.Work) (teamsQueryPlan, error) {
	if w.ScheduledAssignment == nil {
		return teamsPlanFromWork(w)
	}
	if w.VerifyConfig() != nil || w.Kind != "deterministic" || w.PopulationKey != "current" ||
		!time.Now().Before(w.ExpiresAt) || (len(w.RunDecision) != 0 && !bytes.Equal(bytes.TrimSpace(w.RunDecision), []byte("null"))) {
		return teamsQueryPlan{}, errTeamsQueryUnsupported
	}
	config, err := parseTeamsScheduledConfig(w.Config)
	if err != nil {
		return teamsQueryPlan{}, err
	}
	period, err := parseTeamsPeriod(w.Period, &teamsSchedule{
		Cadence: config.Schedule.Cadence, Timezone: config.Schedule.Timezone, CatchUp: config.Schedule.CatchUp, ResponseWindowSeconds: config.Schedule.ResponseWindowSeconds,
	})
	if err != nil || period.StartAt == nil || period.EndAt == nil {
		return teamsQueryPlan{}, errTeamsQueryUnsupported
	}
	expected := teamsScheduledAssignment{AssignmentID: w.ID, PeriodKey: period.Key, Timezone: config.Schedule.Timezone, StartAt: *period.StartAt, DeadlineAt: w.ExpiresAt}
	if time.Now().Before(expected.StartAt) || !matchesTeamsScheduledPeriod(config, expected, *w.ScheduledAssignment, period.EndAt) {
		return teamsQueryPlan{}, errTeamsQueryUnsupported
	}
	return teamsQueryPlan{Metrics: []teamsMetric{{ID: "total_tokens", Field: "total_tokens"}}}, nil
}

func sameTeamsScheduledAssignment(a, b *teams.ScheduledAssignment) bool {
	if a == nil {
		return b == nil
	}
	return a.Equal(b)
}

// The first operational source decision persists the period as part of the
// same exact authorization bytes that the device presents to the service.
func matchesTeamsSourceAuthorization(raw []byte, current *teams.ScheduledAssignment) bool {
	var auth teams.SourceAuthorization
	return teams.ValidUniqueJSON(raw) && json.Unmarshal(raw, &auth) == nil && sameTeamsScheduledAssignment(auth.ScheduledAssignment, current)
}

// Scheduled receipt authority requires the same signed source mode, policy,
// scope and inventory that the local upload ledger retained.
func (u teamsV2Upload) matchesSourceProof(current *teams.ScheduledAssignment) bool {
	if current == nil {
		return matchesTeamsSourceAuthorization(u.Authorization, nil)
	}
	a, err := teams.DecodeSourceAuthorization(u.WorkID, u.Authorization)
	return err == nil && sameTeamsScheduledAssignment(a.ScheduledAssignment, current) &&
		a.Mode == u.Mode && a.OrgPolicyVersion == u.PolicyVersion && a.SettingsRevision == u.SettingsRevision &&
		a.SnapshotID == u.SnapshotID && a.ConfigSHA256 == u.ConfigHash && a.EvaluationRevision == u.EvaluationRevision &&
		a.LocalRevision == u.LocalRevision && a.InventoryRevision == u.InventoryRevision && a.InventorySHA256 == u.InventoryHash
}

// JSON integers can exceed JavaScript's safe range. Expose the approved decimal
// and original payload text without a float64 round trip in the review screen.
type teamsScheduledResultDisplay struct {
	Total       string `json:"total"`
	SourceState string `json:"sourceState"`
	ExactJSON   string `json:"exactJson"`
}

func teamsScheduledResultForDisplay(raw []byte, hash string) *teamsScheduledResultDisplay {
	var payload teams.Payload
	if len(raw) == 0 || teamsPayloadHash(raw) != hash || !teams.ValidUniqueJSON(raw) || json.Unmarshal(raw, &payload) != nil || payload.Outcome != "completed" || payload.Form != "snapshot" || len(payload.Rows) != 1 || len(payload.Rows[0].Values) != 1 || len(payload.Rows[0].Key) != 0 {
		return nil
	}
	total := string(payload.Rows[0].Values["total_tokens"])
	n, err := strconv.ParseInt(total, 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != total {
		return nil
	}
	if payload.Coverage.SourceState != "partial" && payload.Coverage.SourceState != "complete" {
		return nil
	}
	return &teamsScheduledResultDisplay{total, payload.Coverage.SourceState, string(raw)}
}

const teamsExpiredScheduleState = "source_authorization_expired"

// A definite, scoped expiry can retire this upload. An offline check, a missing
// authorization, and a missing result receipt cannot establish expiry.
func (c *Catalog) scheduledSourceFailure(ctx context.Context, w teams.Work, d LibraryDescriptor, credential *teams.EnrollmentCredential, err error, fallback string) string {
	var remote *teams.HTTPError
	if w.ScheduledAssignment == nil || credential == nil || !errors.As(err, &remote) || remote.Status != 409 || remote.Code != teamsExpiredScheduleState {
		return fallback
	}
	lease, e := c.teamsSharingLease()
	if e != nil {
		return fallback
	}
	defer lease.Close()
	e = c.writeTransaction(ctx, "teams-expired-scheduled-source", func(tx *sql.Tx) error {
		u, e := readTeamsV2Upload(ctx, tx, w.ID)
		if e != nil {
			return e
		}
		if u.LibraryID != d.ID || u.InstanceID != d.Instance || u.OrgID != credential.OrgID || u.InstallationID != credential.InstallationID || u.ConfigHash != w.ConfigSHA256 || !u.matchesSourceProof(w.ScheduledAssignment) {
			return errTeamsConsentStale
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_v2_uploads SET state='abandoned' WHERE work_id=?`, w.ID)
		return e
	})
	if e != nil {
		return fallback
	}
	return teamsExpiredScheduleState
}

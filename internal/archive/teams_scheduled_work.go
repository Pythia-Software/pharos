package archive

import (
	"bytes"
	"encoding/json"
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
	if err != nil {
		return teamsQueryPlan{}, err
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

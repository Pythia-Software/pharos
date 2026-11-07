package archive

import (
	"context"
)

// Events consume the committed item/writing context. They never read message
// labels, so their pipeline shares writing readiness without a labels edge.
func (s *Server) eventsWorkDefinition(authority *workAuthority) workDefinition {
	task := &modelWorkTask{server: s, authority: authority, kind: "analysis.agent_events", limit: agentEventsChainLimit,
		config: func(ctx context.Context, power powerState) modelWorkConfig {
			config := s.Config()
			settings, bulk := labelsForPower(config.AgentEvents.labeler(config.Labels), power)
			return modelWorkConfig{settings: settings, signature: jsonText([]any{settings, config.AgentEvents}), enabled: s.agentEventsEnabled(ctx), bulk: bulk}
		},
		// settings are already the pass's labeler settings for this power:
		// the session does not derive them again.
		create: func(settings LabelerSettings, allocation workAllocation) (workModelSession, error) {
			return s.Catalog.newAgentEventsSession(settings, s.Config().AgentEvents, allocation)
		},
		// Forks follow the record writer's generation; the horizon moves
		// daily, so a session older than today's horizon rediscovers once.
		inputs: func(ctx context.Context) (string, error) {
			generation, err := s.Catalog.labelingInputsGeneration(ctx)
			return generation + "|" + agentEventsHorizonDay(s.Config().AgentEvents.since()), err
		},
	}
	return workDefinition{Kind: "analysis.agent_events", Dependencies: []string{"analysis.authorship"}, Task: task}
}

func (s *Server) expandedWorkDefinitions(authority *workAuthority) ([]workDefinition, error) {
	defs, err := s.workDefinitions(authority)
	if err != nil {
		return nil, err
	}
	more, err := s.maintenanceWorkDefinitions(authority)
	if err != nil {
		return nil, err
	}
	defs = append(defs, s.eventsWorkDefinition(authority))
	return append(defs, more...), nil
}

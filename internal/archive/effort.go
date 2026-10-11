package archive

import (
	"strings"
)

func normalizeEffort(effort string) string {
	return strings.ToLower(strings.TrimSpace(effort))
}

// modelEffort labels a recorded pair without inventing a default effort.
func modelEffort(model, effort string) any {
	model, effort = strings.TrimSpace(model), normalizeEffort(effort)
	if model == "" && effort == "" {
		return nil
	}
	if model == "" {
		model = "Unknown model"
	}
	if effort == "" {
		effort = "Unknown effort"
	}
	return model + " · " + effort
}

func configurationPredicate(modelColumn, effortColumn, model, effort string) (string, []any) {
	clauses, args := []string{}, []any{}
	if model != "" {
		clauses = append(clauses, modelColumn+" LIKE ?")
		args = append(args, "%"+model+"%")
	}
	if effort != "" {
		clauses = append(clauses, effortColumn+"=?")
		args = append(args, effort)
	}
	return strings.Join(clauses, " AND "), args
}

// effortSetting reads explicit configuration only. Thinking token counts and
// budgets do not establish an effort level, and absence does not imply a default.
// The boolean distinguishes a recorded reset (null/empty) from an absent field.
func effortSetting(value map[string]any) (string, bool) {
	for _, key := range []string{"effort", "reasoning_effort", "reasoningEffort", "effortLevel", "effort_level", "model_reasoning_effort"} {
		if raw, exists := value[key]; exists {
			if raw == nil {
				return "", true
			}
			if text, ok := raw.(string); ok {
				return normalizeEffort(text), true
			}
		}
	}
	for _, pair := range []struct{ object, field string }{{"reasoning", "effort"}, {"output_config", "effort"}, {"outputConfig", "effort"}, {"settings", "effortLevel"}} {
		if nested := mapValue(value[pair.object]); nested != nil {
			if raw, exists := nested[pair.field]; exists {
				if raw == nil {
					return "", true
				}
				if text, ok := raw.(string); ok {
					return normalizeEffort(text), true
				}
			}
		}
	}
	return "", false
}

func recordedEffort(value map[string]any) string {
	effort, _ := effortSetting(value)
	return effort
}

func updatedEffort(previous string, values ...map[string]any) string {
	for _, value := range values {
		if effort, found := effortSetting(value); found {
			previous = effort
		}
	}
	return previous
}

func exportedEffort(message, conversation map[string]any) string {
	if effort, found := effortSetting(message); found {
		return effort
	}
	return recordedEffort(conversation)
}

// Existing hourly ledgers need a new primary key so two effort levels reported
// for the same model in the same hour cannot overwrite or combine each other.
func (c *Catalog) migrateEffortUsage() error {
	has, err := c.hasColumn("agent_session_usage", "effort")
	if err != nil || has {
		return err
	}
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE agent_session_usage RENAME TO agent_session_usage_before_effort`); err != nil {
		return err
	}
	start := strings.Index(schemaSQL(), "CREATE TABLE IF NOT EXISTS agent_session_usage (")
	end := start + strings.Index(schemaSQL()[start:], ");") + 2
	if _, err := tx.Exec(schemaSQL()[start:end]); err != nil {
		return err
	}
	fields := "agent_session_id,usage_hour,model,attribution," + strings.Join(usageTokenFields, ",")
	if _, err := tx.Exec("INSERT INTO agent_session_usage(" + fields + ") SELECT " + fields + " FROM agent_session_usage_before_effort"); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE agent_session_usage_before_effort;
 CREATE INDEX IF NOT EXISTS agent_session_usage_hour_idx ON agent_session_usage(usage_hour)`); err != nil {
		return err
	}
	return tx.Commit()
}

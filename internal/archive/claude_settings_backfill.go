package archive

import (
	"context"
	"net/http"
)

// claudeSelectionValue joins the latest saved conversation selection to an
// export without rewriting its transcript. Do not choose across organizations
// or conflicting observations, or resurrect a field a newer snapshot omitted.
// The alias is an internal SQL identifier and field is model or effort_level.
func claudeSelectionValue(alias, field string) string {
	return `(SELECT CASE WHEN COUNT(DISTINCT COALESCE(TRIM(o.` + field + `),''))=1
 THEN MAX(NULLIF(TRIM(o.` + field + `),'')) END
 FROM claude_chat_settings_observations o
 WHERE ` + alias + `.harness='claude-ai-export' AND o.account=` + alias + `.account
 AND o.conversation_uuid=` + alias + `.native_id
 AND julianday(o.observed_at)=(SELECT MAX(julianday(latest.observed_at)) FROM claude_chat_settings_observations latest
   WHERE latest.account=o.account AND latest.conversation_uuid=o.conversation_uuid)
 AND NOT EXISTS (SELECT 1 FROM claude_chat_settings_observations other
   WHERE other.account=o.account AND other.conversation_uuid=o.conversation_uuid AND other.organization_uuid<>o.organization_uuid))`
}

// A saved selection fills a conversation's model gap only when neither the
// conversation nor any assistant message recorded a model.
func claudeNoRecordedModel(alias string) string {
	return `NULLIF(TRIM(` + alias + `.model),'') IS NULL
 AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.conversation_id=` + alias + `.id
   AND m.model IS NOT NULL AND m.role='assistant' AND NULLIF(TRIM(m.model),'') IS NOT NULL)`
}

// A newer unknown or conflicting observation must not revive an older model.
var claudeMissingModelSelection = claudeNoRecordedModel("c") + `
 AND NOT EXISTS (SELECT 1 FROM claude_chat_settings_observations o
   WHERE o.account=c.account AND o.conversation_uuid=c.native_id
   AND NULLIF(TRIM(o.model),'') IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM claude_chat_settings_observations newer
     WHERE newer.account=o.account AND newer.conversation_uuid=o.conversation_uuid
     AND newer.organization_uuid=o.organization_uuid
     AND (julianday(newer.observed_at)>julianday(o.observed_at)
       OR (julianday(newer.observed_at)=julianday(o.observed_at)
         AND COALESCE(newer.model,'')<>o.model))))`

type claudeSettingsCoverage struct {
	Account       string `json:"account"`
	Conversations int64  `json:"conversations"`
	MissingModels int64  `json:"missing_models"`
}

func (c *Catalog) claudeSettingsCoverage(ctx context.Context, account, conversation string) ([]claudeSettingsCoverage, error) {
	rows, err := c.reader().QueryContext(ctx, `SELECT c.account,COUNT(*),
 SUM(CASE WHEN `+claudeMissingModelSelection+` THEN 1 ELSE 0 END)
 FROM conversations c WHERE c.harness='claude-ai-export'
 AND (?='' OR c.account=?) AND (?='' OR c.id=?) GROUP BY c.account ORDER BY c.account`, account, account, conversation, conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	coverage := []claudeSettingsCoverage{}
	for rows.Next() {
		var item claudeSettingsCoverage
		if err := rows.Scan(&item.Account, &item.Conversations, &item.MissingModels); err != nil {
			return nil, err
		}
		coverage = append(coverage, item)
	}
	return coverage, rows.Err()
}

func (s *Server) getClaudeSettingsCoverage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	coverage, err := s.Catalog.claudeSettingsCoverage(r.Context(), r.URL.Query().Get("account"), r.URL.Query().Get("conversation"))
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	type accountCoverage struct {
		claudeSettingsCoverage
		Setup claudeSettingsSetup `json:"setup"`
	}
	accounts := []accountCoverage{}
	for _, item := range coverage {
		setup, err := claudeSettingsForAccount(s.Config(), item.Account)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		accounts = append(accounts, accountCoverage{item, setup})
	}
	writeJSON(w, map[string]any{"accounts": accounts}, http.StatusOK)
}

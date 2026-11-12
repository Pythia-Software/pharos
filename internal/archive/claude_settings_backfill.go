package archive

import (
	"context"
	"net/http"
)

// A cached selection fills a metadata gap, never a historical execution claim.
// A newer unknown or conflicting observation must not revive an older model.
const claudeMissingModelSelection = `NULLIF(TRIM(c.model),'') IS NULL
 AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.conversation_id=c.id
   AND m.role='assistant' AND NULLIF(TRIM(m.model),'') IS NOT NULL)
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

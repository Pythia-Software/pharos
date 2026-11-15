package archive

import (
	"context"
	"slices"
	"strings"
	"time"
)

// The branch_usage dataset is Analyze → Spend by Branch's records: the
// priced usage of sessions in repositories, one row per session, branch,
// coverage, model and hour, the usage ledger's grain, so hourly trends and
// time ranges see when the spend happened. A ledger row of a session with branch
// shares (branch_usage.go) is split by them, turn by turn; one without is
// on the branch its source recorded for the whole session, or has no
// branch data. Folders and sessions without a project have no branch and
// are not in it, nor are mirrors, which the Usage dataset also counts once.

// Coverage of a row's branch, the split the dashboard reports:
//   - on_branch: on a recorded branch;
//   - unknown: in a session that recorded a branch, but before its first,
//     on a detached HEAD, or usage its transcript did not place on a turn;
//   - no_branch_data: a session that recorded no branch at all.
const (
	branchOnBranch     = "on_branch"
	branchUnknown      = "unknown"
	branchNoBranchData = "no_branch_data"
)

func (c *Catalog) branchUsageRows(ctx context.Context) ([]map[string]any, error) {
	return cachedValue(ctx, c, "branch-usage-rows", func(ctx context.Context) ([]map[string]any, error) {
		workspaces, err := queryMapsContext(ctx, c.reader(), `SELECT w.id,NULLIF(TRIM(COALESCE(w.branch,'')),'') branch
			FROM workspaces w`+projectJoin("w")+` WHERE pj.id IS NOT NULL`)
		if err != nil {
			return nil, err
		}
		sessionBranch := map[string]string{}
		for _, row := range workspaces {
			sessionBranch[firstString(row["id"])] = firstString(row["branch"])
		}
		stored, err := queryMapsContext(ctx, c.reader(), `SELECT agent_session_id,usage_hour,model,branch,tokens FROM agent_session_branch_shares`)
		if err != nil {
			return nil, err
		}
		shares := map[string]map[branchShareKey]map[string]int64{}
		for _, row := range stored {
			session := firstString(row["agent_session_id"])
			key := branchShareKey{session, firstString(row["usage_hour"]), firstString(row["model"])}
			if shares[session] == nil {
				shares[session] = map[branchShareKey]map[string]int64{}
			}
			if shares[session][key] == nil {
				shares[session][key] = map[string]int64{}
			}
			shares[session][key][firstString(row["branch"])] += integer(row["tokens"])
		}
		// The Usage dataset's ledger: a mirror (a Conductor workspace of a
		// Claude Code session) counts once, as its native session.
		ledger, err := c.localUsageLedger(ctx)
		if err != nil {
			return nil, err
		}
		origins, err := c.conversationOrigins(ctx)
		if err != nil {
			return nil, err
		}
		book, err := c.loadPriceBook()
		if err != nil {
			return nil, err
		}
		type rowKey struct{ workspace, branch, coverage, source, model, hour, conversation string }
		rows := map[rowKey]map[string]any{}
		add := func(entry map[string]any, key rowKey, day string, cost *float64, status string, tokens int64) {
			// Conversations sharing a workspace and hour can have different
			// initiating channels or attribution quality. Pool within each
			// conversation, keeping its identity stable as attribution improves.
			key.conversation = firstString(entry["conversation_id"])
			row := rows[key]
			if row == nil {
				origin := origins[key.conversation]
				row = map[string]any{"id": stableID("branch-usage", key.workspace, key.branch, key.coverage, key.source, key.model, key.hour, key.conversation),
					"workspace_id": key.workspace, "title": entry["title"], "source_kind": entry["source_kind"], "project_name": entry["project_name"],
					"channel": origin["channel"], "channel_status": defaultString(firstString(origin["channel_status"]), "unknown"),
					"branch": nilIfEmpty(key.branch), "project_branch": nil, "coverage": key.coverage, "branch_source": nilIfEmpty(key.source),
					"provider": nilIfEmpty(firstString(entry["provider"])), "model": key.model, "model_family": nilIfEmpty(modelFamily(key.model)),
					"day": day, "usage_at": key.hour, "cost_usd": nil, "tokens": int64(0), "price_status": ""}
				// Branch names repeat across projects (main, say): a branch is
				// counted and compared within its project.
				if key.branch != "" {
					row["project_branch"] = firstString(entry["project_name"]) + " · " + key.branch
				}
				rows[key] = row
			}
			row["cost_usd"] = addCost(row["cost_usd"], cost)
			row["tokens"] = row["tokens"].(int64) + tokens
			row["price_status"] = worsePriceStatus(firstString(row["price_status"]), status)
		}
		for _, entry := range ledger {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			workspace := firstString(entry["workspace_id"])
			branch, inRepository := sessionBranch[workspace]
			if !inRepository || integer(entry["current_group_member"]) == 0 {
				continue
			}
			day, hour := "", firstString(entry["usage_hour"])
			if at, ok := parseTime(hour); ok {
				day = localMidnight(at, time.Local).Format("2006-01-02")
				hour = at.UTC().Format(time.RFC3339)
			}
			model := ledgerModel(entry)
			tokens := ledgerTokens(entry)
			priced := book.cost(model, day, tokens)
			session := firstString(entry["agent_session_id"])
			whole := func(branch, coverage, source string) {
				add(entry, rowKey{workspace: workspace, branch: branch, coverage: coverage, source: source, model: model, hour: hour}, day, priced.cost, priced.status, tokens["total_tokens"])
			}
			switch turns := shares[session]; {
			case turns == nil && branch != "":
				whole(branch, branchOnBranch, "session")
			case turns == nil:
				whole("", branchNoBranchData, "")
			default:
				split := turns[branchShareKey{session, firstString(entry["usage_hour"]), firstString(entry["usage_model"])}]
				if firstString(entry["attribution"]) != "request" || len(split) == 0 {
					whole("", branchUnknown, "")
					continue
				}
				// Each branch takes the ledger row's cost and tokens in
				// proportion to the tokens its turns used in that hour.
				names := make([]string, 0, len(split))
				total := int64(0)
				for name, used := range split {
					names = append(names, name)
					total += used
				}
				slices.Sort(names)
				left := tokens["total_tokens"]
				for index, name := range names {
					part := float64(split[name]) / float64(total)
					taken := int64(float64(tokens["total_tokens"]) * part)
					if index == len(names)-1 {
						taken = left
					}
					left -= taken
					var cost *float64
					if priced.cost != nil {
						value := *priced.cost * part
						cost = &value
					}
					coverage, source := branchOnBranch, "turn"
					if name == "" {
						coverage, source = branchUnknown, ""
					}
					add(entry, rowKey{workspace: workspace, branch: name, coverage: coverage, source: source, model: model, hour: hour}, day, cost, priced.status, taken)
				}
			}
		}
		result := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			if row["price_status"] == "" {
				row["price_status"] = nil
			}
			result = append(result, row)
		}
		slices.SortFunc(result, func(a, b map[string]any) int {
			return strings.Compare(firstString(a["id"]), firstString(b["id"]))
		})
		return result, nil
	})
}

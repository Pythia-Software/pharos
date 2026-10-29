package archive

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

const menuBarPreferenceKey = "pharos-menu-bar"

var menuBarDefaultOrder = []string{"open", "status", "indexed", "index", "words", "tokens", "cost", "carbon", "backup", "space", "search", "analyze", "optimize", "capture", "health", "sources", "finder", "settings", "eject", "quit"}
var menuBarRequired = []string{"open", "status", "settings", "quit"}
var menuBarSeparatorPattern = regexp.MustCompile(`^separator:[1-9][0-9]*$`)

func menuBarGroup(id string) string {
	switch id {
	case "status", "indexed", "index":
		return "status"
	case "words", "tokens", "cost", "carbon":
		return "usage"
	case "backup", "space":
		return "library"
	case "search", "analyze", "optimize":
		return "navigation"
	case "capture", "health", "sources", "finder":
		return "manage"
	case "eject", "quit":
		return "quit"
	default:
		return id
	}
}

// Older preferences store visibility only. An explicit order becomes the source
// of visibility too, so every client receives the same normalized layout.
func menuBarOrder(values map[string]json.RawMessage, settings map[string]bool) []string {
	var saved struct {
		Order         []any `json:"order"`
		LayoutVersion any   `json:"layoutVersion"`
	}
	_ = json.Unmarshal(values[menuBarPreferenceKey], &saved)
	order := []string{}
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			order = append(order, id)
			seen[id] = true
		}
	}
	if saved.Order == nil {
		for _, id := range menuBarDefaultOrder {
			if settings[id] {
				add(id)
			}
		}
	} else {
		for _, value := range saved.Order {
			if id, ok := value.(string); ok {
				if _, known := settings[id]; known && id != "enabled" || menuBarSeparatorPattern.MatchString(id) {
					add(id)
				}
			}
		}
	}
	for _, id := range menuBarRequired {
		add(id)
	}
	for _, id := range menuBarDefaultOrder {
		settings[id] = seen[id]
	}
	hasSeparator := false
	for _, id := range order {
		hasSeparator = hasSeparator || menuBarSeparatorPattern.MatchString(id)
	}
	if saved.LayoutVersion != float64(2) && !hasSeparator {
		layout := []string{}
		previous, separator := "", 0
		for _, id := range order {
			group := menuBarGroup(id)
			if previous != "" && previous != group {
				separator++
				layout = append(layout, "separator:"+strconv.Itoa(separator))
			}
			layout = append(layout, id)
			previous = group
		}
		order = layout
	}
	return order
}

func menuBarSettings(values map[string]json.RawMessage) map[string]bool {
	settings := map[string]bool{"enabled": true, "indexed": true, "words": true, "tokens": true, "carbon": true,
		"cost": false, "backup": false, "space": false, "index": false, "eject": false,
		"open": true, "status": true, "settings": true, "quit": true,
		"search": false, "analyze": false, "optimize": false, "capture": false, "health": false, "sources": false, "finder": false}
	var saved map[string]any
	_ = json.Unmarshal(values[menuBarPreferenceKey], &saved)
	for key := range settings {
		if value, ok := saved[key].(bool); ok {
			settings[key] = value
		}
	}
	for _, id := range menuBarRequired {
		settings[id] = true
	}
	return settings
}

// This small snapshot can be polled with the window closed. Expensive usage
// totals have their own endpoint and are fetched at most once a minute.
func (s *Server) menuBarStatus(ctx context.Context) (map[string]any, error) {
	values, err := s.Catalog.UIPreferences(ctx)
	if err != nil {
		return nil, err
	}
	settings := menuBarSettings(values)
	order := menuBarOrder(values, settings)
	preferences := map[string]any{"order": order, "layoutVersion": 2}
	for key, value := range settings {
		preferences[key] = value
	}
	// With the unified activity flag on, the native menu also reads the
	// status snapshot (docs/background-work.md §9); off, it is unchanged.
	status := map[string]any{"settings": preferences, "unified_activity": s.Catalog.unifiedActivity(ctx)}
	if !settings["enabled"] {
		return status, nil
	}
	status["library"] = s.libraryStatus()
	var indexed string
	hostID := currentHost().ID
	err = s.Catalog.reader().QueryRowContext(ctx, `SELECT COALESCE(MAX(at),'') FROM (
		SELECT last_success_at at FROM source_states WHERE host_id=?
		UNION ALL SELECT indexed_at at FROM automatic_source_states WHERE host_id=?)`, hostID, hostID).Scan(&indexed)
	if err != nil {
		return nil, err
	}
	status["indexed_at"] = indexed
	// Prefer the most recent attempt for each source: a later successful
	// manual index clears an earlier automatic failure, and vice versa.
	issues, err := queryMapsContext(ctx, s.Catalog.reader(), `WITH attempts AS (
		SELECT source_name,last_attempt_at at,error FROM source_states WHERE host_id=?
		UNION ALL SELECT source_name,attempted_at at,error FROM automatic_source_states WHERE host_id=?
	), latest AS (SELECT *,ROW_NUMBER() OVER (PARTITION BY source_name ORDER BY at DESC) n FROM attempts)
	SELECT source_name,error FROM latest WHERE n=1 AND COALESCE(error,'')<>'' AND error NOT LIKE 'interrupted:%'`, hostID, hostID)
	if err != nil {
		return nil, err
	}
	problem := ""
	enabled := map[string]bool{}
	for _, source := range s.Config().Sources {
		if source.Enabled {
			enabled[source.Name] = true
		}
	}
	enabledSource := func(name string) bool {
		if enabled[name] {
			return true
		}
		// Capture-index runs use host/source labels.
		_, source, qualified := strings.Cut(name, "/")
		return qualified && enabled[source]
	}
	relevantSources := func(names []string) bool {
		// A service-wide failure can happen before source selection.
		if len(names) == 0 {
			return true
		}
		for _, name := range names {
			if enabledSource(name) {
				return true
			}
		}
		return false
	}
	for _, issue := range issues {
		if enabled[firstString(issue["source_name"])] {
			problem = firstString(issue["source_name"]) + ": " + firstString(issue["error"])
			break
		}
	}
	var openIssues int
	if err := s.Catalog.reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='open'").Scan(&openIssues); err != nil {
		return nil, err
	}
	if problem == "" && openIssues > 0 {
		problem = "Indexed conversations need verification. Open Capture & index for details."
	}
	// Runs can fail before recording source state. Ignore deliberate stops.
	s.runsMu.RLock()
	for _, run := range s.runs {
		if run.State == "running" {
			continue
		}
		if problem == "" && run.State == "failed" && !run.StopRequested {
			sourceErrors := map[string]bool{}
			for _, result := range run.Results {
				if result.Error == nil {
					continue
				}
				message := defaultString(result.Error, "Indexing failed.")
				sourceErrors[message] = true
				if enabledSource(result.Source) && problem == "" {
					problem = result.Source + ": " + message
				}
			}
			message := defaultString(run.Error, "Indexing failed. Open Capture & index for details.")
			if problem == "" && !sourceErrors[message] && relevantSources(run.Sources) {
				problem = message
			}
		}
		break
	}
	s.runsMu.RUnlock()
	if runs := s.captures.list(); problem == "" && len(runs) > 0 && runs[0].State == "failed" {
		run := runs[0]
		sourceErrors, sourceFailed, relevantFailure := map[string]bool{}, false, false
		for _, result := range run.Results {
			if result.State == "failed" || len(result.Errors) > 0 {
				sourceFailed = true
				relevantFailure = relevantFailure || enabledSource(result.Name)
			}
			for _, failure := range result.Errors {
				sourceErrors[failure.Error] = true
			}
		}
		generalFailure := !sourceFailed
		for _, failure := range run.Errors {
			generalFailure = generalFailure || !sourceErrors[failure.Error]
		}
		if relevantFailure || generalFailure && relevantSources(run.Sources) {
			problem = "Capture failed. Open Capture & index for details."
		}
	}
	_, backups := s.backups.status()
	if problem == "" && len(backups) > 0 && backups[0].State == "failed" {
		problem = "Backup failed. Open Backups for details."
		status["error_path"] = "/settings/backups"
	}
	status["error"] = problem
	if settings["backup"] {
		for _, backup := range loadBackupHistory(s.Config()).Backups {
			if !backup.Partial {
				status["backup_at"] = backup.FinishedAt
				break
			}
		}
	}
	if settings["space"] {
		status["storage"] = s.driveHealth()
	}
	return status, nil
}

func (s *Server) menuBarUsage(ctx context.Context) (map[string]any, error) {
	summary, err := s.Catalog.UsageSummary(ctx)
	if err != nil {
		return nil, err
	}
	prefs, err := s.Catalog.carbonPreferences(ctx)
	if err != nil {
		return nil, err
	}
	for _, window := range summary["windows"].([]*usageSummaryWindow) {
		if window.Key == "24h" {
			return map[string]any{"generated_at": summary["generated_at"], "human_words": window.HumanWords,
				"tokens": window.Tokens, "cost_usd": window.CostUSD,
				"co2e_g": menuBarCarbon(summary["carbon_factors"].(carbonDocument), window.CarbonTokens, prefs)}, nil
		}
	}
	return nil, nil
}

// Matches the Usage page, including custom grid, independent PUE scenario,
// and the Watershed input sensitivity. No new carbon assumptions.
func menuBarCarbon(doc carbonDocument, tokens map[string]map[string]float64, prefs map[string]any) float64 {
	a := doc.assumptions(prefs)
	wh := 0.0
	for _, tier := range doc.Tiers {
		for _, category := range carbonCategories {
			wh += a.wh(tier.Key, category, tokens[tier.Key][category])
		}
	}
	return a.grams(wh)
}

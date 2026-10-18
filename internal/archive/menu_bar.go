package archive

import (
	"context"
	"encoding/json"
	"strings"
)

const menuBarPreferenceKey = "pharos-menu-bar"

// The order belongs to the native menu, not to the stored preferences.
func menuBarSettings(values map[string]json.RawMessage) map[string]bool {
	settings := map[string]bool{"enabled": true, "indexed": true, "words": true, "tokens": true, "carbon": true,
		"cost": false, "backup": false, "space": false, "index": false, "eject": false}
	var saved map[string]any
	_ = json.Unmarshal(values[menuBarPreferenceKey], &saved)
	for key := range settings {
		if value, ok := saved[key].(bool); ok {
			settings[key] = value
		}
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
	status := map[string]any{"settings": settings}
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
		problem = "Backup failed. Open Library health for details."
		status["error_path"] = "/settings"
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
	values, err := s.Catalog.UIPreferences(ctx)
	if err != nil {
		return nil, err
	}
	var prefs map[string]any
	_ = json.Unmarshal(values["pharos-carbon-v1"], &prefs)
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
	scenario := func(key string) string {
		value := firstString(prefs[key])
		if value == "low" || value == "high" {
			return value
		}
		return "central"
	}
	gridKey := defaultString(prefs["grid"], doc.DefaultGrid)
	grid := 0.0
	found := false
	for _, candidate := range doc.Grids {
		if candidate.Key == gridKey {
			grid, found = candidate.GramsPerKWh, true
		}
	}
	if gridKey == "custom" {
		custom, _ := number(prefs["customGrid"])
		grid, found = max(0, custom), true
	}
	if !found {
		for _, candidate := range doc.Grids {
			if candidate.Key == doc.DefaultGrid {
				grid = candidate.GramsPerKWh
			}
		}
	}
	scale := 1.0
	if strings.EqualFold(firstString(prefs["inputSensitivity"]), "third") {
		scale = 5.0 / 3
	}
	wh := 0.0
	for _, tier := range doc.Tiers {
		for _, category := range carbonCategories {
			ratio := 1.0
			if category != "output" {
				ratio = doc.CategoryRatios[category].get(scenario("scenario")) * scale
			}
			wh += tokens[tier.Key][category] / 1e6 * tier.OutputWhPerMTok.get(scenario("scenario")) * ratio
		}
	}
	return wh * doc.PUE.get(scenario("pueScenario")) * grid / 1000
}

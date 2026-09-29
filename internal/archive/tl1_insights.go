package archive

import (
	"fmt"
	"path/filepath"
	"strings"
)

// TL1 prompt helpers for the flavor drill-down. TL1's concerns (error
// clusters, cost outliers, rework loops) are findings now (findings_tl1.go),
// measured after the user acts on them; the TL1 tab keeps its analysis.

func tl1Day(value string) string {
	if parsed, ok := parseTime(value); ok {
		return parsed.Local().Format("Jan 2 15:04")
	}
	return defaultString(value, "unknown")
}

// tl1PromptHeader names the project's files. A project run on several Macs
// has them on each, so each Mac's are listed under it.
func tl1PromptHeader(data *tl1Data, goal string) string {
	paths := [][2]string{{"Repository", "repository"}, {"TL1 config (flavor definitions)", "config_path"}, {"TL1 database (read-only; do not write)", "database_path"}, {"Transcripts", "transcripts_dir"}}
	lines := []string{goal, "", "## TL1 installation", "- Project: " + data.Project}
	indent := ""
	if len(data.Installations) > 1 {
		lines[2] = "## TL1 installations"
		lines = append(lines, fmt.Sprintf("- Runs on %d Macs; this analysis combines them, and each Mac's files are on that Mac:", len(data.Installations)))
		indent = "  "
	}
	for _, installation := range data.Installations {
		if indent != "" {
			lines = append(lines, fmt.Sprintf("- %s (%s tasks):", tl1Mac(installation), firstString(installation["tasks"])))
		}
		for _, item := range paths {
			if value := firstString(installation[item[1]]); value != "" {
				lines = append(lines, fmt.Sprintf("%s- %s: %s", indent, item[0], value))
			}
		}
	}
	if data.Since != "" || data.Until != "" {
		lines = append(lines, "- Analysis window: "+tl1WindowLabel(data)+". Pass since="+defaultString(nilIfEmpty(data.Since), "(none)")+tl1UntilArg(data)+" to Pharos TL1 tools to see the same window.")
	}
	lines = append(lines, "", "Pharos (local MCP server `pharos`) can help if connected: tl1_overview, tl1_flavor, tl1_errors, and tl1_candidate return this analysis; get_conversation_overview and search_conversation_passages open transcripts by conversation ID.")
	return strings.Join(lines, "\n")
}
func tl1TranscriptPath(data *tl1Data, task *tl1Task, attempt *tl1Attempt) string {
	directory := firstString(task.Installation["transcripts_dir"])
	if directory == "" || attempt == nil || len(attempt.Transcripts) == 0 {
		return ""
	}
	return filepath.Join(directory, task.ID, attempt.Transcripts[0])
}

// tl1Mac names the Mac an installation runs on.
func tl1Mac(installation map[string]any) string {
	return defaultString(installation["host_label"], "Unknown Mac")
}

// tl1OnMac says which Mac holds a task's files, when the analysis spans more
// than one.
func tl1OnMac(data *tl1Data, task *tl1Task) string {
	if len(data.Installations) < 2 {
		return ""
	}
	return " (on " + tl1Mac(task.Installation) + ")"
}
func tl1FlavorDefinition(data *tl1Data, name string, templateLimit int) string {
	flavor := data.Flavors[name]
	if flavor == nil {
		return "(flavor definition not found in the TL1 snapshot)"
	}
	lines := []string{fmt.Sprintf("- name: %s (%s)", flavor.Name, flavor.ExecutionClass)}
	if flavor.DefaultConfiguration != "" {
		lines = append(lines, "- default agent configuration: "+flavor.DefaultConfiguration)
	}
	if len(flavor.Configurations) > 0 {
		lines = append(lines, "- allowed agent configurations: "+jsonText(flavor.Configurations))
	}
	if len(flavor.Transitions) > 0 {
		lines = append(lines, "- outcome transitions: "+jsonText(flavor.Transitions))
	}
	if len(flavor.Budgets) > 0 {
		lines = append(lines, "- budgets: "+jsonText(flavor.Budgets))
	}
	if flavor.ShapeVersion != "" {
		lines = append(lines, "- current shape version: "+flavor.ShapeVersion)
	}
	if templateLimit > 0 && flavor.Template != "" {
		lines = append(lines, "- prompt template:\n```\n"+tl1Clip(flavor.Template, templateLimit)+"\n```")
	}
	return strings.Join(lines, "\n")
}
func tl1FlavorPrompt(data *tl1Data, flavor, concern string) string {
	cells := []string{}
	matrix, _ := tl1Matrix(data)
	for _, row := range matrix {
		if row["flavor"] != flavor {
			continue
		}
		cells = append(cells, fmt.Sprintf("- %s: %v runs, advance rate %s, agent error rate %s, median cost %v, cost per useful result %v",
			row["configuration"], row["attempts"], tl1Percent(row["advance_rate"]), tl1Percent(row["agent_error_rate"]), tl1Money(row["median_cost_usd"]), tl1Money(row["cost_per_advance_usd"])))
	}
	return strings.Join([]string{
		tl1PromptHeader(data, fmt.Sprintf("Improve the %s TL1 flavor. %s", flavor, concern)),
		"", "## Performance by agent configuration", defaultString(strings.Join(cells, "\n"), "(no LLM runs in this window)"),
		"", "## Flavor definition", tl1FlavorDefinition(data, flavor, 8000),
		"", "## What to do", "Read three successful and three unsuccessful transcripts of this flavor. Propose specific template, schema, budget, or configuration changes, each with the evidence behind it and how to measure the effect on the next runs.",
	}, "\n")
}
func tl1Money(value any) string {
	if number, ok := value.(float64); ok {
		return tl1Dollars(number)
	}
	return "—"
}

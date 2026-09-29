package archive

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// runFindingsPreview runs every detector against a catalog opened read-only
// and prints the candidates with their gate numbers, writing nothing. It is
// for tuning detectors against a live library.
func runFindingsPreview(catalogPath string, args []string) error {
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: catalogPath}).String()+"?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	catalog := &Catalog{Path: catalogPath, DB: db}
	ctx := context.Background()
	started := time.Now()
	env, err := catalog.loadFindingEnv(ctx, db, true, nil)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Read %d conversations in %s\n", len(env.convs), time.Since(started).Round(time.Millisecond))
	threshold := 10
	for index, arg := range args {
		if arg == "--threshold" && index+1 < len(args) {
			fmt.Sscan(args[index+1], &threshold)
		}
	}
	only := ""
	for index, arg := range args {
		if arg == "--detector" && index+1 < len(args) {
			only = args[index+1]
		}
	}
	candidates := []*findingCandidate{}
	for _, detector := range findingDetectors {
		if only != "" && detector.name != only {
			continue
		}
		began := time.Now()
		found, err := detector.run(env)
		if err != nil {
			return fmt.Errorf("%s: %w", detector.name, err)
		}
		fmt.Fprintf(os.Stderr, "%-13s %4d candidates in %s\n", detector.name, len(found), time.Since(began).Round(time.Millisecond))
		candidates = append(candidates, found...)
	}
	for _, candidate := range candidates {
		candidate.stats = env.stats(candidate)
		if candidate.write != nil {
			candidate.card = candidate.write(candidate, candidate.stats)
		}
	}
	candidates = dedupeFindingCandidates(candidates)
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].stats.Affected > candidates[j].stats.Affected })
	writer := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(writer, "PASS\tAFFECTED\tRECENT\tLIVE\tDAYS\tWS\tRATE\t$/MO\tSCOPE\tTITLE")
	verbose := slices.Contains(args, "--verbose")
	for _, candidate := range candidates {
		if candidate.stats.Affected < findingCheckpoints[0] {
			continue
		}
		pass := "-"
		if candidate.Hidden {
			pass = "hidden"
		} else if candidate.stats.passes(threshold) {
			pass = "yes"
		}
		impact := findingImpact(candidate.stats)
		fmt.Fprintf(writer, "%s\t%d\t%d\t%d\t%d\t%d\t%.3g\t%.2f\t%s\t%s\n", pass, candidate.stats.Affected, candidate.stats.Recent, candidate.stats.Live,
			candidate.stats.Days, candidate.stats.Workspaces, candidate.stats.Rate, impact["usd"], clipText(env.scopeName(candidate.Spec.Scope), 28), clipText(candidate.card.Title, 90))
		if verbose && (candidate.stats.passes(threshold) || candidate.Hidden) {
			writer.Flush()
			fmt.Printf("    %s\n    %s\n    impact: %s · change: %s\n", candidate.Spec.id(), candidate.card.Explanation, candidate.card.ImpactNote, firstStepChange(candidate.card))
		}
	}
	return writer.Flush()
}

func firstStepChange(card findingCard) string {
	if len(card.Steps) == 0 {
		return ""
	}
	return card.Steps[0].Change
}

// runFindingsCLI refreshes findings (--refresh runs the full pass now) and
// prints the visible ones.
func runFindingsCLI(catalog *Catalog, args []string) error {
	ctx := context.Background()
	if slices.Contains(args, "--refresh") {
		if err := catalog.RefreshFindings(ctx, true); err != nil {
			return err
		}
	}
	value, err := catalog.FindingsOverview(ctx, "")
	if err != nil {
		return err
	}
	if slices.Contains(args, "--json") {
		return printJSON(value)
	}
	findings, _ := value["findings"].([]map[string]any)
	for _, item := range findings {
		fmt.Printf("%-9s %s\n          %s\n", item["state"], item["title"], strings.TrimSpace(firstString(item["impact_line"])))
	}
	return nil
}

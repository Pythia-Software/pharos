package archive

import "flag"

// runTL1CLI prints the TL1 analysis as JSON: `pharos tl1 [--project NAME]
// [--installation ID] [--since latest|TIME] [--until TIME] [--days N] [--scope current]`.
// Its recommendations are findings (`pharos findings`).
func runTL1CLI(catalog *Catalog, args []string) error {
	flags := flag.NewFlagSet("tl1", flag.ContinueOnError)
	project := flags.String("project", "", "TL1 project, combined across the Macs that run it (default: most recently active)")
	installation := flags.String("installation", "", "TL1 installation ID, for one Mac's runs of a project")
	since := flags.String("since", "", "only tasks created at or after this ISO 8601 time, or \"latest\" for the latest large enqueue")
	until := flags.String("until", "", "only tasks created before this ISO 8601 time")
	days := flags.Int("days", 0, "only tasks created in the last N days (0 = all); ignored with --since")
	scope := flags.String("scope", "all", "all, or current to keep only runs of each flavor's current definition")
	if err := flags.Parse(args); err != nil {
		return err
	}
	overview, err := catalog.TL1Overview(tl1Selection{Project: *project, Installation: *installation}, tl1Window{Days: *days, Since: *since, Until: *until, Scope: *scope})
	if err != nil {
		return err
	}
	return printJSON(overview)
}

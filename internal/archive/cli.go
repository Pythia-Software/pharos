package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

func Run(arguments []string) error {
	configPath := ""
	filtered := []string{}
	for index := 0; index < len(arguments); index++ {
		if arguments[index] == "--config" && index+1 < len(arguments) {
			configPath = arguments[index+1]
			index++
			continue
		}
		filtered = append(filtered, arguments[index])
	}
	if len(filtered) == 0 {
		return usageError()
	}
	command := filtered[0]
	args := filtered[1:]
	if command == "init" {
		path := "archive.toml"
		if len(args) > 0 {
			path = expandPath(args[0])
		}
		if err := InitConfig(path); err != nil {
			return err
		}
		fmt.Printf("Wrote %s. Data defaults to Application Support; change archive_root if preferred and opt in source paths.\n", path)
		return nil
	}
	if command == "init-library" {
		if slices.ContainsFunc(args, func(arg string) bool { return strings.HasPrefix(arg, "--adopt") }) {
			return runAdoptCLI(args)
		}
		if len(args) != 1 {
			return fmt.Errorf("usage: pharos init-library DIR [--adopt CONFIG [--full-check]]")
		}
		config, err := InitLibrary(args[0], volumeIdentity)
		if err != nil {
			return err
		}
		fmt.Printf("Wrote %s and created its catalog at %s.\n", config.Path, config.CatalogPath)
		if config.VolumeID != "" {
			fmt.Printf("The library is pinned to volume %s.\n", config.VolumeID)
		}
		return nil
	}
	if command == "volume-id" {
		if len(args) != 1 {
			return fmt.Errorf("usage: pharos volume-id PATH")
		}
		fmt.Println(volumeIdentity(expandPath(args[0])))
		return nil
	}
	if command == "config-executable" {
		if len(args) != 2 {
			return fmt.Errorf("usage: pharos config-executable CONFIG EXECUTABLE")
		}
		return SetRootString(expandPath(args[0]), "executable", expandPath(args[1]))
	}
	if command == "mcp" {
		// An agent keeps this process for its whole session, so it starts, and
		// keeps answering, while the library is unavailable. It loads the
		// configuration and runs the library guards for each request instead.
		if len(args) != 0 {
			return fmt.Errorf("usage: pharos [--config PATH] mcp")
		}
		return RunMCP(configPath, os.Stdin, os.Stdout)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	if command == "doctor" {
		return runDoctor(config)
	}
	// dev-ui only talks to the running service, so it skips the guards below
	// and never opens the catalog.
	if command == "dev-ui" {
		return runDevUICLI(config, args)
	}
	if err := config.checkLibrary(volumeIdentity); err != nil {
		return err
	}
	// Capturing and backing up only copy files; they never open the catalog
	// for writing.
	if command == "capture" {
		return runCaptureCLI(config, args)
	}
	if command == "backup" {
		return runBackupCLI(config, args)
	}
	// A preview reads the catalog without writing, so it can run beside a
	// service that is indexing.
	if command == "findings" && slices.Contains(args, "--preview") {
		return runFindingsPreview(config.CatalogPath, args)
	}
	if command == "repositories" {
		if len(args) != 2 || args[0] != "--merge" || args[1] != "--dry-run" && args[1] != "--apply" {
			return fmt.Errorf("usage: pharos repositories --merge --dry-run|--apply")
		}
		if args[1] == "--dry-run" {
			path := (&url.URL{Scheme: "file", Path: config.CatalogPath}).String() + "?mode=ro"
			db, err := sql.Open("sqlite", path)
			if err != nil {
				return err
			}
			defer db.Close()
			items, err := loadRepositoryIdentities(db)
			if err != nil {
				return err
			}
			prepareRepositoryIdentities(items)
			resolveGitHubRepositories(context.Background(), items, nil)
			printRepositoryMergePlan(os.Stdout, planRepositoryMerges(items, config.RepositoryAliases, config.RepositorySeparate...))
			return nil
		}
		catalog, err := OpenCatalog(config.CatalogPath)
		if err != nil {
			return err
		}
		defer catalog.Close()
		items, err := loadRepositoryIdentities(catalog.DB)
		if err != nil {
			return err
		}
		prepareRepositoryIdentities(items)
		resolveGitHubRepositories(context.Background(), items, nil)
		if err := catalog.saveRepositoryEvidence(context.Background(), items); err != nil {
			return err
		}
		groups := planRepositoryMerges(items, config.RepositoryAliases, config.RepositorySeparate...)
		printRepositoryMergePlan(os.Stdout, groups)
		for _, group := range groups {
			if err := catalog.mergeRepositoryGroup(context.Background(), group); err != nil {
				return err
			}
		}
		return catalog.writeTransaction(context.Background(), "repository-merge", func(tx *sql.Tx) error { return setMeta(tx, "repository_merge_version", repositoryMergeVersion) })
	}
	if command == "add-this-mac" {
		return runAddThisMacCLI(config, args, os.Stdin, os.Stdout)
	}
	if err := config.EnsureDirs(); err != nil {
		return err
	}
	catalog, err := OpenCatalog(config.CatalogPath)
	if err != nil {
		return err
	}
	defer catalog.Close()
	catalog.RepositoryAliases = config.RepositoryAliases
	catalog.RepositorySeparate = config.RepositorySeparate
	catalog.setCaptureRoot(config.CaptureRoot)
	switch command {
	case "serve":
		openBrowser := false
		for _, arg := range args {
			if arg == "--open" {
				openBrowser = true
			}
		}
		if openBrowser {
			display := config.Host
			if strings.Contains(display, ":") {
				display = "[" + display + "]"
			}
			target := fmt.Sprintf("http://%s:%d/?token=%s", display, config.Port, urlQueryEscape(config.APIToken))
			go func() { _ = exec.Command("open", target).Run() }()
		}
		server := NewServer(config, catalog)
		// Only a service that got its port serves the library: then a release
		// (see releasedMarkerName) ends.
		server.life.listening = func() { _ = os.Remove(releasedMarker(config.CatalogPath)) }
		return server.Serve()
	case "ingest":
		results := []IngestResult{}
		for _, source := range config.Sources {
			if !source.Enabled {
				continue
			}
			adapter, err := MakeAdapter(source)
			if err != nil {
				return err
			}
			results = append(results, catalog.Ingest(adapter, nil))
		}
		links, err := catalog.ReconcileIdentities()
		if err != nil {
			return err
		}
		if err := catalog.refreshMainIntegrations(context.Background()); err != nil {
			return err
		}
		if err := catalog.refreshAllLibrary(context.Background()); err != nil {
			return err
		}
		return printJSON(map[string]any{"sources": results, "identity_links_added": links})
	case "repair-existing":
		if len(args) != 1 {
			return fmt.Errorf("usage: pharos repair-existing SOURCE")
		}
		var source *SourceConfig
		for index := range config.Sources {
			if config.Sources[index].Name == args[0] {
				source = &config.Sources[index]
				break
			}
		}
		if source == nil {
			return fmt.Errorf("unknown source: %s", args[0])
		}
		adapter, err := MakeAdapter(*source)
		if err != nil {
			return err
		}
		result := catalog.IngestExisting(adapter, nil)
		links, linkErr := catalog.ReconcileIdentities()
		if linkErr != nil {
			return linkErr
		}
		if result.Error != nil {
			return fmt.Errorf("repair failed: %v", result.Error)
		}
		if err := catalog.refreshAllLibrary(context.Background()); err != nil {
			return err
		}
		return printJSON(map[string]any{"source": result, "identity_links_added": links})
	case "build-tools":
		built, err := catalog.BackfillToolLedger(context.Background(), func(done, total int) {
			if done%500 == 0 || done == total {
				fmt.Fprintf(os.Stderr, "built tool ledger for %d/%d conversations\n", done, total)
			}
		})
		if err != nil {
			return fmt.Errorf("build tool ledger after %d conversations: %w", built, err)
		}
		if err := catalog.ensureToolRollup(context.Background()); err != nil {
			return err
		}
		status, err := catalog.ToolLedgerStatus(context.Background())
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"conversations_built": built, "status": status})
	case "refine-usage":
		refined, err := catalog.RefineUsageTimeline(func(done, total int) {
			if done%100 == 0 || done == total {
				fmt.Fprintf(os.Stderr, "refined %d/%d conversations\n", done, total)
			}
		})
		if err != nil {
			return fmt.Errorf("refine usage after %d conversations: %w", refined, err)
		}
		return printJSON(map[string]any{"conversations_refined": refined})
	case "repair-usage-attribution":
		repaired, err := catalog.RepairUsageAttribution(context.Background(), func(done, total int) {
			if done%100 == 0 || done == total {
				fmt.Fprintf(os.Stderr, "repaired usage for %d/%d workspaces\n", done, total)
			}
		})
		if err != nil {
			return fmt.Errorf("repair usage attribution after %d workspaces: %w", repaired, err)
		}
		return printJSON(map[string]any{"workspaces_repaired": repaired})
	case "upgrade":
		return runUpgradeCLI(catalog, args)
	case "pricing":
		return runPricingCLI(catalog, args)
	case "search":
		return runSearchCLI(catalog, args)
	case "tl1":
		return runTL1CLI(catalog, args)
	case "findings":
		return runFindingsCLI(catalog, args)
	case "health":
		value := catalog.Health()
		value["storage"] = storage(config)
		value["drive"] = driveReport(config, collectDrive(context.Background(), config))
		return printJSON(value)
	case "probe":
		return runProbeCLI(config, catalog, args)
	case "index":
		return runIndexCLI(config, catalog, args)
	case "upcoming", "protect", "preserve", "reclaim", "reconcile", "tick", "worker":
		return fmt.Errorf("%s is part of mothballed TL1 reclamation; see docs/reclamation/README.md", command)
	case "enrich-github":
		return fmt.Errorf("%s is not enabled in the Go service", command)
	default:
		return usageError()
	}
}

func usageError() error {
	return fmt.Errorf("usage: pharos [--config PATH] {init,init-library,add-this-mac,serve,capture,index,backup,ingest,repositories,repair-existing,refine-usage,repair-usage-attribution,upgrade,build-tools,pricing,search,tl1,findings,health,doctor,dev-ui,mcp,probe,volume-id}")
}
func urlQueryEscape(value string) string {
	replacer := strings.NewReplacer("%", "%25", " ", "%20", "+", "%2B", "?", "%3F", "&", "%26", "=", "%3D")
	return replacer.Replace(value)
}
func printJSON(value any) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(payload))
	return nil
}

func runSearchCLI(catalog *Catalog, args []string) error {
	query := ""
	parseArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		query = args[0]
		parseArgs = args[1:]
	}
	flags := flag.NewFlagSet("search", flag.ContinueOnError)
	repository := flags.String("repository", "", "")
	source := flags.String("source", "", "")
	file := flags.String("file", "", "")
	owner := flags.String("owner", "", "")
	provider := flags.String("provider", "", "")
	model := flags.String("model", "", "")
	from := flags.String("from", "", "")
	to := flags.String("to", "", "")
	flavor := flags.String("flavor", "", "")
	version := flags.String("version", "", "")
	outcome := flags.String("outcome", "", "")
	errorType := flags.String("error", "", "")
	metric := flags.String("metric", "", "")
	limit := flags.Int("limit", 20, "")
	prText := flags.String("pr", "", "")
	minimumText := flags.String("minimum", "", "")
	maximumText := flags.String("maximum", "", "")
	if err := flags.Parse(parseArgs); err != nil {
		return err
	}
	if query == "" && flags.NArg() > 0 {
		query = flags.Arg(0)
	}
	options := SearchOptions{Query: query, Repository: *repository, Source: *source, File: *file, Owner: *owner, Provider: *provider, Model: *model, From: *from, To: *to, Flavor: *flavor, Version: *version, Outcome: *outcome, Error: *errorType, Metric: *metric, Limit: *limit}
	if *prText != "" {
		value, err := strconv.Atoi(*prText)
		if err != nil {
			return err
		}
		options.PR = &value
	}
	if *minimumText != "" {
		value, err := strconv.ParseFloat(*minimumText, 64)
		if err != nil {
			return err
		}
		options.Minimum = &value
	}
	if *maximumText != "" {
		value, err := strconv.ParseFloat(*maximumText, 64)
		if err != nil {
			return err
		}
		options.Maximum = &value
	}
	value, err := catalog.Search(options)
	if err != nil {
		return err
	}
	return printJSON(value)
}

func executablePath() string { path, _ := os.Executable(); path, _ = filepath.Abs(path); return path }

// runPricingCLI validates a pricing file (the embedded one by default) and
// reports which models in this catalog still lack a confirmed price, or prints
// the prompt that asks an agent to refresh the price history.
func runPricingCLI(catalog *Catalog, args []string) error {
	if len(args) == 1 && args[0] == "prompt" {
		fmt.Print(catalog.pricingStatus()["prompt"])
		return nil
	}
	if len(args) == 0 || args[0] != "check" || len(args) > 2 {
		return fmt.Errorf("usage: pharos pricing {check [FILE],prompt}")
	}
	data, err := pricingDocumentBytes()
	if len(args) == 2 {
		data, err = os.ReadFile(expandPath(args[1]))
	}
	if err != nil {
		return err
	}
	doc, problems := parsePricing(data)
	statuses := map[string]int{}
	for _, change := range doc.Changes {
		statuses[change.Status]++
	}
	health := catalog.pricingHealth()
	if err := printJSON(map[string]any{"valid": len(problems) == 0, "problems": problems, "changes_by_status": statuses,
		"aliases": len(doc.Aliases), "catalog": health}); err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("pricing file has %d problems", len(problems))
	}
	return nil
}

// runUpgradeCLI runs the library upgrade, or with --status or --preview only
// reports what it would do. The upgrade always asks GitHub, through the gh
// tool, which repositories were renamed or moved; --github is accepted and
// ignored so scripts written for 0.4.1 keep working.
func runUpgradeCLI(catalog *Catalog, args []string) error {
	mode := "run"
	for _, arg := range args {
		switch arg {
		case "--github":
		case "--status", "--preview":
			mode = arg
		default:
			return fmt.Errorf("usage: pharos upgrade [--status|--preview]")
		}
	}
	ctx := context.Background()
	switch mode {
	case "--status":
		value, err := catalog.UpgradeStatus(ctx)
		if err != nil {
			return err
		}
		return printJSON(value)
	case "--preview":
		groups, err := catalog.RepositoryMergePreview(ctx)
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"repository_merges": groups})
	}
	started, last := time.Now(), time.Now()
	err := catalog.RunUpgrade(ctx, func(step string, done, total int) {
		if total == 0 || done == total || time.Since(last) > 10*time.Second {
			last = time.Now()
			fmt.Fprintf(os.Stderr, "%s %s: %d/%d\n", time.Since(started).Round(time.Second), step, done, total)
		}
	})
	if err != nil {
		return err
	}
	value, err := catalog.UpgradeStatus(ctx)
	if err != nil {
		return err
	}
	value["elapsed_seconds"] = time.Since(started).Seconds()
	return printJSON(value)
}

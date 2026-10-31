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

	"github.com/gbdubs/pharos/pricing"
)

func Run(arguments []string) error {
	lease, err := acquireAppUpdateLease(runningExecutable(), pharosSupportDir())
	if err != nil {
		return err
	}
	if lease != nil {
		defer lease.Close()
	}
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
	if command == "app" {
		return runApplicationCLI(args)
	}
	if command == "mcp-app" {
		return runApplicationMCPCLI(args, os.Stdin, os.Stdout)
	}
	if command == "library-transfer" {
		return runLibraryTransferCLI(args)
	}
	if command == "library-recover" {
		return runLibraryRecoverCLI(args)
	}
	if command == "cloud-restore" {
		return runCloudRestoreCLI(args)
	}
	// Labeling never opens the catalog through OpenCatalog: prepare reads a
	// read-only snapshot, and plan and status read only a dataset.
	if command == "labeling" {
		return runLabelingCLI(configPath, args, os.Stdout, os.Stderr)
	}
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
		// --service is for Ask Pharos, which names the service it runs in.
		flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
		service := flags.String("service", "", "private endpoint file of the service to answer from")
		if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
			return fmt.Errorf("usage: pharos [--config PATH] mcp [--service FILE]")
		}
		return RunMCP(configPath, *service, os.Stdin, os.Stdout)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	if command == "serve" {
		flags := flag.NewFlagSet("serve", flag.ContinueOnError)
		dynamic := flags.Bool("dynamic", false, "allocate an available loopback port")
		endpoint := flags.String("endpoint", "", "publish runtime endpoint to this private file")
		application := flags.String("application", "", "application installation managing this service")
		open := flags.Bool("open", false, "open a browser")
		if err := flags.Parse(args); err != nil {
			return err
		}
		if *dynamic {
			config.Port = 0
			config.APIToken = randomToken()
		}
		config.RuntimeEndpoint, config.Application = *endpoint, *application
		args = nil
		if *open {
			args = []string{"--open"}
		}
	}
	if command == "doctor" {
		return runDoctor(config)
	}
	// dev-ui only talks to the running service, so it skips the guards below
	// and never opens the catalog.
	if command == "dev-ui" {
		return runDevUICLI(config, args)
	}
	if command == "history-backfill" {
		guard := config
		// Preserve the volume guard while allowing catalog-independent status,
		// resume and offline rebuild. Only plan/prepare require the live DB.
		if len(args) > 0 && args[0] != "plan" && args[0] != "prepare" {
			guard.CatalogPath = config.Path
		}
		if err := guard.checkLibrary(volumeIdentity); err != nil {
			return err
		}
		use, err := fileLease(libraryUseLock(config), false)
		if err != nil {
			return err
		}
		defer use.Close()
		if len(args) > 0 && args[0] == "status" {
			return runHistoryCLI(config, args)
		}
		return runCLIOperation(config, "history-backfill", OperationSpec{Kind: "capture.history-backfill", Variant: firstOf(args)},
			func(context.Context, *Op) error { return runHistoryCLI(config, args) })
	}
	if err := config.checkLibrary(volumeIdentity); err != nil {
		return err
	}
	if _, err := os.Stat(pendingDeliveryPath(config)); err == nil {
		return fmt.Errorf("a verified library move needs to finish cleanup; resume its transfer before opening or capturing this library")
	}
	if _, err := os.Stat(config.CatalogPath); err == nil {
		use, err := fileLease(libraryUseLock(config), false)
		if err != nil {
			return err
		}
		defer use.Close()
	}
	// Capturing and backing up only copy files; they never open the catalog
	// for writing.
	// Each holds a lease under the capture root while it writes (§6.5).
	if command == "sqlite-records" {
		if subcommand := firstOf(args); subcommand == "status" || subcommand == "verify" {
			return runSQLiteRecordsCLI(config, args)
		}
		return runCLIOperation(config, "sqlite-records", OperationSpec{Kind: "capture.cli", Variant: "sqlite-records"},
			func(context.Context, *Op) error { return runSQLiteRecordsCLI(config, args) })
	}
	if command == "capture" {
		return runCLIOperation(config, "capture", OperationSpec{Kind: "capture.cli"},
			func(ctx context.Context, _ *Op) error { return runCaptureCLIContext(ctx, config, args) })
	}
	if command == "cloud-backup" {
		return runCloudCLI(config, args)
	}
	if command == "backup" {
		return runCLIOperation(config, "backup", OperationSpec{Kind: "backup.local"},
			func(context.Context, *Op) error { return runBackupCLI(config, args) })
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
		return runLeasedCatalogCommand(config, "repositories", OperationSpec{Kind: "repositories.merge"}, func(_ context.Context, _ *Op, catalog *Catalog) error {
			return applyRepositoryMergesCLI(config, catalog)
		})
	}
	if command == "add-this-mac" {
		return runAddThisMacCLI(config, args, os.Stdin, os.Stdout)
	}
	if err := config.EnsureDirs(); err != nil {
		return err
	}
	if command == "serve" {
		catalog, err := OpenCatalog(config.CatalogPath)
		if err != nil {
			return err
		}
		defer catalog.Close()
		catalog.RepositoryAliases = config.RepositoryAliases
		catalog.RepositorySeparate = config.RepositorySeparate
		catalog.setCaptureRoot(config.CaptureRoot)
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
	}
	// Every other command opens the catalog under a lease (§6.5): taken
	// before the open, so its Initialize is seen, and released after the
	// close, so its checkpoint is too.
	return runLeasedCatalogCommand(config, command, cliCatalogOperation(command, args), func(ctx context.Context, op *Op, catalog *Catalog) error {
		catalog.RepositoryAliases = config.RepositoryAliases
		catalog.RepositorySeparate = config.RepositorySeparate
		catalog.setCaptureRoot(config.CaptureRoot)
		switch command {
		case "ingest":
			return runIngestCLI(ctx, config, catalog, op)
		case "repair-existing":
			return runRepairExistingCLI(config, catalog, args)
		case "build-tools":
			return runBuildToolsCLI(ctx, catalog, op)
		case "agent-events":
			return runAgentEventsCLI(ctx, catalog, args)
		case "refine-usage":
			refined, err := catalog.RefineUsageTimeline(func(done, total int) {
				op.progress("Refine usage", "", "conversation", float64(done), float64(total), total > 0)
				if done%100 == 0 || done == total {
					fmt.Fprintf(os.Stderr, "refined %d/%d conversations\n", done, total)
				}
			})
			if err != nil {
				return fmt.Errorf("refine usage after %d conversations: %w", refined, err)
			}
			return printJSON(map[string]any{"conversations_refined": refined})
		case "repair-usage-attribution":
			repaired, err := catalog.RepairUsageAttribution(ctx, func(done, total int) {
				op.progress("Repair usage attribution", "", "workspace", float64(done), float64(total), total > 0)
				if done%100 == 0 || done == total {
					fmt.Fprintf(os.Stderr, "repaired usage for %d/%d workspaces\n", done, total)
				}
			})
			if err != nil {
				return fmt.Errorf("repair usage attribution after %d workspaces: %w", repaired, err)
			}
			return printJSON(map[string]any{"workspaces_repaired": repaired})
		case "upgrade":
			return runUpgradeCLIContext(ctx, catalog, args)
		case "pricing":
			return runPricingCLI(catalog, args)
		case "search":
			return runSearchCLI(catalog, args)
		case "tl1":
			return runTL1CLI(catalog, args)
		case "findings":
			return runFindingsCLI(catalog, args)
		case "labels":
			return runLabelsCLI(ctx, config, catalog, op, args, os.Stdout, os.Stderr)
		case "health":
			value := catalog.Health()
			value["storage"] = storage(config)
			value["drive"] = driveReport(config, collectDrive(ctx, config))
			return printJSON(value)
		case "probe":
			return runProbeCLI(config, catalog, args)
		case "index":
			return runIndexCLIContext(ctx, config, catalog, args)
		case "upcoming", "protect", "preserve", "reclaim", "reconcile", "tick", "worker":
			return fmt.Errorf("%s is part of mothballed TL1 reclamation; see docs/reclamation/README.md", command)
		case "enrich-github":
			return fmt.Errorf("%s is not enabled in the Go service", command)
		default:
			return usageError()
		}
	})
}

// cliCatalogOperation is the operation a catalog command runs as; a read
// command runs none of its own (its lease covers the open, Initialize and
// close).
func cliCatalogOperation(command string, args []string) OperationSpec {
	switch command {
	case "ingest":
		return OperationSpec{Kind: "ingest.cli", Variant: "ingest"}
	case "repair-existing":
		return OperationSpec{Kind: "ingest.cli", Variant: "repair-existing"}
	case "build-tools":
		return OperationSpec{Kind: "tools.ledger.build"}
	case "agent-events":
		// status only reads; import (or anything else, which fails) writes.
		if len(args) == 0 || args[0] != "status" {
			return OperationSpec{Kind: "analysis.agent_events.import"}
		}
	case "refine-usage", "repair-usage-attribution":
		return OperationSpec{Kind: "catalog.repair", Variant: command}
	case "upgrade":
		if !slices.Contains(args, "--status") && !slices.Contains(args, "--preview") {
			return OperationSpec{Kind: "library.upgrade"}
		}
	case "findings":
		if slices.Contains(args, "--refresh") {
			return OperationSpec{Kind: "analysis.findings"}
		}
	case "index":
		return OperationSpec{Kind: "index.captures"}
	case "labels":
		return labelsCLIOperation(args)
	}
	return OperationSpec{}
}

// runIngestCLI: pharos ingest, every enabled source in place.
func runIngestCLI(ctx context.Context, config Config, catalog *Catalog, op *Op) error {
	results := []IngestResult{}
	for _, source := range config.Sources {
		if !source.Enabled {
			continue
		}
		adapter, err := MakeAdapter(source)
		if err != nil {
			return err
		}
		op.Step(source.Name, "", -1)
		results = append(results, catalog.Ingest(adapter, nil))
	}
	op.Step("Link identities and refresh the Search view", "", -1)
	links, err := catalog.ReconcileIdentities()
	if err != nil {
		return err
	}
	if err := catalog.refreshMainIntegrations(ctx); err != nil {
		return err
	}
	if err := catalog.refreshAllLibrary(ctx); err != nil {
		return err
	}
	return printJSON(map[string]any{"sources": results, "identity_links_added": links})
}

// runRepairExistingCLI: pharos repair-existing SOURCE.
func runRepairExistingCLI(config Config, catalog *Catalog, args []string) error {
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
}

// runBuildToolsCLI: pharos build-tools.
func runBuildToolsCLI(ctx context.Context, catalog *Catalog, op *Op) error {
	built, err := catalog.BackfillToolLedger(ctx, func(done, total int) {
		op.progress("Build the Tools ledger", "", "conversation", float64(done), float64(total), total > 0)
		if done%500 == 0 || done == total {
			fmt.Fprintf(os.Stderr, "built tool ledger for %d/%d conversations\n", done, total)
		}
	})
	if err != nil {
		return fmt.Errorf("build tool ledger after %d conversations: %w", built, err)
	}
	if err := catalog.ensureToolRollup(ctx); err != nil {
		return err
	}
	status, err := catalog.ToolLedgerStatus(ctx)
	if err != nil {
		return err
	}
	return printJSON(map[string]any{"conversations_built": built, "status": status})
}

// runAgentEventsCLI: pharos agent-events import FILE | status. The import
// loads predictions of the JAYHAWK study's model (agent_events.go) until the
// model runs inside Pharos.
func runAgentEventsCLI(ctx context.Context, catalog *Catalog, args []string) error {
	if len(args) == 2 && args[0] == "import" {
		result, err := catalog.ImportAgentEvents(ctx, args[1])
		if err != nil {
			return err
		}
		return printJSON(result)
	}
	if len(args) == 1 && args[0] == "status" {
		status := map[string]any{}
		var messages, events, atCutoff int
		var first, last, models sql.NullString
		if err := catalog.DB.QueryRowContext(ctx, `SELECT count(*), min(day), max(day), group_concat(DISTINCT model) FROM agent_event_messages`).Scan(&messages, &first, &last, &models); err != nil {
			return err
		}
		if err := catalog.DB.QueryRowContext(ctx, `SELECT count(*), COALESCE(sum(probability>=?),0) FROM agent_events`, agentEventThreshold).Scan(&events, &atCutoff); err != nil {
			return err
		}
		status["messages"], status["first_day"], status["last_day"], status["models"] = messages, first.String, last.String, models.String
		status["events_scored"], status["events_at_threshold"], status["threshold"] = events, atCutoff, agentEventThreshold
		return printJSON(status)
	}
	return fmt.Errorf("usage: pharos agent-events {import FILE | status}")
}

// applyRepositoryMergesCLI: pharos repositories --merge --apply.
func applyRepositoryMergesCLI(config Config, catalog *Catalog) error {
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

// firstOf is args' first element, or "".
func firstOf(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func usageError() error {
	return fmt.Errorf("usage: pharos [--config PATH] {app,mcp-app,library-transfer,library-recover,init,init-library,add-this-mac,serve,capture,index,history-backfill,sqlite-records,backup,cloud-backup,cloud-restore,ingest,repositories,repair-existing,refine-usage,repair-usage-attribution,upgrade,build-tools,agent-events,pricing,search,tl1,findings,labeling,labels,health,doctor,dev-ui,mcp,probe,volume-id}")

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
	effort := flags.String("effort", "", "Recorded agent effort level")
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
	options := SearchOptions{Query: query, Repository: *repository, Source: *source, File: *file, Owner: *owner, Provider: *provider, Model: *model, Effort: *effort, From: *from, To: *to, Flavor: *flavor, Version: *version, Outcome: *outcome, Error: *errorType, Metric: *metric, Limit: *limit}
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
	data := pricing.CostChanges
	if len(args) == 2 {
		file, err := os.ReadFile(expandPath(args[1]))
		if err != nil {
			return err
		}
		data = file
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
	return runUpgradeCLIContext(context.Background(), catalog, args)
}

func runUpgradeCLIContext(ctx context.Context, catalog *Catalog, args []string) error {
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

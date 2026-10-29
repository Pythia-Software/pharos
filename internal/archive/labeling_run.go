package archive

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Runner timing, overridable by tests. The attempt timeout is the default of
// --timeout; the stop grace is how long in-flight attempts may finish after
// a stop before they are terminated; the poll interval is how often the
// runner checks for a stop request.
var (
	labelingAttemptTimeout = 330 * time.Second
	labelingStopGrace      = 2 * time.Minute
	labelingPollInterval   = 250 * time.Millisecond
)

// errLabelingHalted reports a run that stopped leasing on its own: the
// ceiling was reached or usage could not be priced.
var errLabelingHalted = errors.New("labeling run halted")

type labelingRunOptions struct {
	Dir, Job           string
	Create, CreateOnly bool
	Spec               labelingJobSpec
	MaxUSD             float64
	Concurrency        int
	Timeout            time.Duration
	RetryInvalid       int
	AllowUnpriced      bool
	// MaxConsecutiveFailures halts the run after this many failed or
	// unknown outcomes in a row (default 3).
	MaxConsecutiveFailures int
	// AcceptServedModel records the operator's decision to accept results
	// served by this model instead of the requested one.
	AcceptServedModel string
	// RefusalRegistry is the library-wide refusal registry a new job uses
	// (default: labeling-refusals.jsonl beside the dataset directory).
	RefusalRegistry string
	// CLIPath runs this executable for the route instead of the one on
	// PATH; tests point it at a fake CLI.
	CLIPath string
	Now     func() time.Time
	Out     io.Writer
	// Signals delivers SIGINT/SIGTERM: the first stops leasing, the second
	// terminates in-flight attempts.
	Signals <-chan os.Signal
}

type labelingRunner struct {
	opts                         labelingRunOptions
	dir, cliPath, schema, runner string
	db                           *sql.DB
	job                          labelingJob
	book                         priceBook
	labels                       map[string]bool
	calibration                  labelingCalibration
	mu                           sync.Mutex
	halted                       string
	stopping                     bool
	hardStop                     chan struct{}
	hardOnce                     sync.Once
	started                      time.Time
	finished                     int
	cliVersion                   string
	consecutiveFailures          int
	consecutiveRefusals          int
	accepted                     map[string]bool
	loopError                    error
	registry                     string
	registrySeen                 int
	// env is added to each CLI attempt's environment (agy's private HOME).
	env []string
}

func (r *labelingRunner) now() time.Time {
	if r.opts.Now != nil {
		return r.opts.Now()
	}
	return time.Now()
}

func (r *labelingRunner) day() string { return r.now().UTC().Format("2006-01-02") }

func (r *labelingRunner) printf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.opts.Out, format, args...)
}

func (r *labelingRunner) halt(reason string) {
	r.mu.Lock()
	first := r.halted == ""
	if first {
		r.halted = reason
	}
	r.mu.Unlock()
	if first {
		r.db.Exec(`UPDATE jobs SET halted=? WHERE name=?`, reason, r.job.Name)
		r.printf("%s: halted: %s\n", r.job.Name, reason)
	}
}

// runLabelingJob runs (and with Create first creates) a job until no
// request is pending, a stop is requested, or it halts.
func runLabelingJob(ctx context.Context, opts labelingRunOptions) error {
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if !labelingJobName.MatchString(opts.Job) {
		return fmt.Errorf("job name %q: use letters, digits, '.', '_' and '-'", opts.Job)
	}
	if opts.Concurrency < 1 {
		opts.Concurrency = 3
	}
	if opts.Timeout <= 0 {
		opts.Timeout = labelingAttemptTimeout
	}
	if opts.MaxConsecutiveFailures < 1 {
		opts.MaxConsecutiveFailures = 3
	}
	// A stop requested from now on applies to this run, even while it is
	// still starting.
	startedAt := now()
	if !opts.CreateOnly && (!(opts.MaxUSD > 0) || math.IsInf(opts.MaxUSD, 0)) {
		return errors.New("--max-usd must be a positive, finite number of dollars: the job's lifetime spend ceiling")
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return err
	}
	// Preparation takes this lock exclusively, so it cannot replace a
	// dataset a runner is using; the job lock admits one runner per job.
	shared, err := fileLease(filepath.Join(dir, labelingLockFile), false)
	if err != nil {
		return fmt.Errorf("a labeling prepare is using %s", dir)
	}
	defer shared.Close()
	lock, err := fileLease(filepath.Join(dir, ".run-"+opts.Job+".lock"), true)
	if err != nil {
		return fmt.Errorf("another runner is already running job %s in %s", opts.Job, dir)
	}
	defer lock.Close()
	db, err := openLabelingLedger(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	if opts.Create {
		registry := opts.RefusalRegistry
		if registry == "" {
			registry = labelingDefaultRegistry(dir)
		}
		summary, err := createLabelingJob(ctx, db, opts.Job, opts.Spec, nil, "", registry, nil)
		if err != nil {
			return err
		}
		fmt.Fprintln(opts.Out, "Created job; its selection is frozen:")
		writeLabelingJSON(opts.Out, summary)
		if opts.CreateOnly {
			return nil
		}
	}
	job, err := loadLabelingJob(db, opts.Job)
	if err != nil {
		return err
	}
	r := &labelingRunner{opts: opts, dir: dir, db: db, job: job, labels: labelingLabels(), calibration: labelingCalibrations[job.Route],
		hardStop: make(chan struct{}), started: time.Now()}
	if r.book, err = embeddedPriceBook(ctx, r.now()); err != nil {
		return err
	}
	if labelingRequestEstimate(r.book, job.Model, r.day(), 1000, 1000) == nil && !opts.AllowUnpriced {
		return fmt.Errorf("the built-in price book has no price for %s on %s, so the run's spend could not be bounded; pass --allow-unpriced to run anyway", job.Model, r.day())
	}
	if r.schema, err = labelingSchemaJSON(); err != nil {
		return err
	}
	if err := verifyLabelingContract(db, job); err != nil {
		return err
	}
	// The job's refusal registry (recorded at creation; a job from before
	// it gets the default now), read once here so a broken one stops the
	// run before anything is sent.
	var registry sql.NullString
	var seen sql.NullInt64
	if err := db.QueryRow(`SELECT refusal_registry,registry_entries FROM jobs WHERE name=?`, job.Name).Scan(&registry, &seen); err != nil {
		return err
	}
	r.registry, r.registrySeen = registry.String, int(seen.Int64)
	if r.registry == "" {
		r.registry = labelingDefaultRegistry(dir)
		if _, err := db.Exec(`UPDATE jobs SET refusal_registry=? WHERE name=?`, r.registry, job.Name); err != nil {
			return err
		}
	}
	if _, err := r.refusalCheck(true); err != nil {
		return err
	}
	if err := r.prepareFiles(); err != nil {
		return err
	}
	labelingStartupHook()
	if err := r.acceptServedModel(opts.AcceptServedModel); err != nil {
		return err
	}
	if err := r.repriceLabelingUsage(opts.AcceptServedModel != ""); err != nil {
		return err
	}
	// Attempts finished before CLI warnings were recorded get them from
	// their saved stderr.
	if earlier, err := queryMaps(db, `SELECT id,dir FROM attempts WHERE job=? AND state='finished' AND cli_warnings IS NULL`, job.Name); err != nil {
		return err
	} else {
		for _, row := range earlier {
			if _, err := db.Exec(`UPDATE attempts SET cli_warnings=? WHERE id=?`, jsonText(labelingCLIWarnings(filepath.Join(firstString(row["dir"]), "stderr.log"))), row["id"]); err != nil {
				return err
			}
		}
	}
	settings := map[string]any{"max_usd": opts.MaxUSD, "concurrency": opts.Concurrency, "timeout_seconds": opts.Timeout.Seconds(),
		"retry_invalid": opts.RetryInvalid, "allow_unpriced": opts.AllowUnpriced, "max_consecutive_failures": opts.MaxConsecutiveFailures,
		"accept_served_model": opts.AcceptServedModel, "pid": os.Getpid(), "cli_path": r.cliPath, "cli_version": r.cliVersion}
	// The previous halt is cleared before recovery, so a halt recovery
	// raises is the one status shows.
	if _, err := db.Exec(`UPDATE jobs SET stop_requested=CASE WHEN stop_requested_at>=? THEN stop_requested ELSE 0 END,halted=NULL,run_started_at=?,run_settings=? WHERE name=?`,
		startedAt, now(), compactJSON(settings), job.Name); err != nil {
		return err
	}
	if err := r.recover(); err != nil {
		return err
	}
	// Unpriced usage (an unknown auxiliary model, unattributed usage: an
	// upper bound is only an estimate, not the model's price) blocks new
	// requests until an explicit decision: --allow-unpriced, recorded with
	// its time, covers what was recorded before it. A served model other
	// than the requested one is governed by its hold (--accept-served-model
	// or reconcile --requeue-held); partial usage of an exposed attempt is
	// covered by its estimate.
	if opts.AllowUnpriced {
		if _, err := db.Exec(`INSERT INTO decisions(job,kind,value,decided_at) VALUES(?,?,?,?)`, job.Name, "allow_unpriced", "", now()); err != nil {
			return err
		}
	}
	var unpriced int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage u JOIN attempts a ON a.id=u.attempt_id WHERE a.job=?
		AND (u.cost_status='unpriced' AND NOT (u.role='primary' AND u.model<>? AND u.model NOT LIKE 'unattributed:%') OR u.cost_status='partial' AND a.usage_missing=0)
		AND a.finished_at > COALESCE((SELECT MAX(decided_at) FROM decisions WHERE job=? AND kind='allow_unpriced'),'')`, job.Name, job.Model, job.Name).Scan(&unpriced); err != nil {
		return err
	}
	if unpriced > 0 {
		return fmt.Errorf("job %s holds %d usage records with no price of their own (upper-bound estimates at most, see status --json); decide explicitly: --allow-unpriced records that decision and continues", job.Name, unpriced)
	}
	// Held results wait for the operator's decision.
	held, err := queryMaps(db, `SELECT a.reported_models FROM requests q JOIN attempts a ON a.request_id=q.id AND a.number=q.attempts WHERE q.job=? AND q.state='held'`, job.Name)
	if err != nil {
		return err
	}
	if len(held) > 0 {
		models := map[string]bool{}
		for _, row := range held {
			var reported []string
			json.Unmarshal([]byte(firstString(row["reported_models"])), &reported)
			for _, model := range reported {
				if model != job.Model {
					models[model] = true
				}
			}
		}
		names := []string{}
		for model := range models {
			names = append(names, model)
		}
		sort.Strings(names)
		return fmt.Errorf("job %s has %d requests held because %s answered instead of %s; run with --accept-served-model MODEL to accept a model (its held results become usable), or re-queue them with labeling reconcile --requeue-held",
			job.Name, len(held), strings.Join(names, ", "), job.Model)
	}
	if err := r.loop(ctx); err != nil {
		return err
	}
	status, err := labelingJobStatus(db, job.Name, r.now())
	if err != nil {
		return err
	}
	fmt.Fprintln(opts.Out, labelingStatusLine(status))
	if r.halted != "" {
		return fmt.Errorf("%w: %s", errLabelingHalted, r.halted)
	}
	return nil
}

// prepareFiles resolves the CLI and its version (asked at every run start:
// CLIs update themselves; the job keeps the first), the private runner
// directory, and the content-addressed schema file.
func (r *labelingRunner) prepareFiles() error {
	r.cliPath = r.opts.CLIPath
	if r.cliPath == "" {
		path, err := exec.LookPath(r.job.Route)
		if err != nil {
			return fmt.Errorf("the %s CLI is not on PATH: %w", r.job.Route, err)
		}
		r.cliPath = path
	}
	r.cliVersion = labelingCLIVersion(r.cliPath)
	if r.job.CLIVersion == "" {
		r.job.CLIVersion = r.cliVersion
		if _, err := r.db.Exec(`UPDATE jobs SET cli_version=?,cli_path=? WHERE name=?`, r.job.CLIVersion, r.cliPath, r.job.Name); err != nil {
			return err
		}
	} else if r.job.CLIVersion != r.cliVersion {
		r.printf("%s: the %s CLI is now %q (the job began with %q); each attempt records its version\n", r.job.Name, r.job.Route, r.cliVersion, r.job.CLIVersion)
	}
	r.runner = filepath.Join(r.dir, "runner")
	if err := os.MkdirAll(r.runner, 0o700); err != nil {
		return err
	}
	if entries, err := os.ReadDir(r.runner); err != nil {
		return err
	} else if len(entries) > 0 {
		return fmt.Errorf("the runner directory %s is not empty (a CLI wrote files there); inspect and remove them before running", r.runner)
	}
	if r.job.Route == labelingRouteAgy {
		home, err := labelingAgyHome(r.dir, os.Getenv("HOME"))
		if err != nil {
			return err
		}
		if listed, err := labelingAgyMCPServers(r.cliPath, home); err != nil {
			return err
		} else if listed != "" {
			return fmt.Errorf("agy would still start MCP servers with HOME=%s; refusing to run:\n%s", home, listed)
		}
		r.env = []string{"HOME=" + home}
	}
	contracts := filepath.Join(r.dir, "contracts")
	if err := os.MkdirAll(contracts, 0o700); err != nil {
		return err
	}
	path := r.schemaPath()
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) != r.schema {
			return fmt.Errorf("%s differs from this build's schema", path)
		}
		return nil
	}
	return writeFileAtomic(path, []byte(r.schema), 0o600)
}

func (r *labelingRunner) schemaPath() string {
	return filepath.Join(r.dir, "contracts", "schema-"+hashBytes([]byte(r.schema))[:16]+".json")
}

// recover settles what an earlier runner left: leased or stopped requests
// return to pending; a submitted attempt is re-read from its saved stream,
// completed from a terminal result there (never re-sent) or else unknown.
func (r *labelingRunner) recover() error {
	if _, err := r.db.Exec(`UPDATE requests SET state='pending',updated_at=? WHERE job=? AND state IN ('leased','stopped','waiting_suspicion')`, now(), r.job.Name); err != nil {
		return err
	}
	rows, err := queryMaps(r.db, `SELECT a.id,a.request_id,a.number,a.dir,a.day,a.pid,a.process_start FROM attempts a JOIN requests r ON r.id=a.request_id
		WHERE a.job=? AND a.state='submitted' ORDER BY a.id`, r.job.Name)
	if err != nil {
		return err
	}
	for _, row := range rows {
		attempt := labelingAttemptInfo{id: firstString(row["id"]), request: firstString(row["request_id"]), number: int(integer(row["number"])),
			dir: firstString(row["dir"]), day: firstString(row["day"])}
		cleanup := []map[string]any{}
		// The CLI of a runner that died may still run; its output has no
		// reader any more. The group is signalled only while its leader is
		// still the process the attempt started (same pid and start time).
		if pid := int(integer(row["pid"])); pid > 0 && syscall.Kill(-pid, 0) == nil {
			if recorded := firstString(row["process_start"]); recorded != "" && labelingProcessStart(pid) == recorded {
				if syscall.Kill(-pid, syscall.SIGKILL) == nil {
					cleanup = append(cleanup, map[string]any{"signal": "SIGKILL", "reason": "orphaned by an earlier runner"})
				}
			} else {
				cleanup = append(cleanup, map[string]any{"reason": "possibly_orphaned", "pid": pid,
					"note": "a process group with the recorded id runs, but its leader is not the recorded CLI process; not signalled"})
			}
		}
		stream, err := replayLabelingStream(r.job.Route, filepath.Join(attempt.dir, "events.jsonl"))
		if err != nil {
			return err
		}
		if err := r.finalize(attempt, stream, &labelingProcess{Cleanup: cleanup}, true); err != nil {
			return err
		}
	}
	// A submitted request whose attempt row is missing cannot exist (one
	// transaction writes both), but would otherwise wait forever.
	_, err = r.db.Exec(`UPDATE requests SET state='unknown',note='submitted without an attempt record' WHERE job=? AND state='submitted'`, r.job.Name)
	return err
}

// refusalCheck reads the registry fresh (it is small). A registry that
// cannot be read, does not parse, or holds fewer entries than the job saw
// before is an error: the run stops rather than send refused material.
func (r *labelingRunner) refusalCheck(persist bool) (*labelingRefusalCheck, error) {
	r.mu.Lock()
	seen := r.registrySeen
	r.mu.Unlock()
	check, err := loadLabelingRefusalCheck(r.registry, &seen)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if seen > r.registrySeen {
		r.registrySeen = seen
	}
	r.mu.Unlock()
	// Persisted outside transactions only (one connection).
	if persist {
		r.db.Exec(`UPDATE jobs SET registry_entries=MAX(COALESCE(registry_entries,0),?) WHERE name=?`, seen, r.job.Name)
	}
	return check, nil
}

// labelingStartupHook runs while a runner starts (after the CLI version
// check); tests request a stop there.
var labelingStartupHook = func() {}

// labelingLeaseHook runs before each lease; tests make it fail.
var labelingLeaseHook = func() error { return nil }

// verifyLabelingContract refuses to resume a job whose frozen contract this
// build or dataset no longer produces: the schema, taxonomy and prompt
// template hashes stored at creation, and every unsent item's contract
// hash, those of requests held for a suspicion included (they return to
// pending when the run starts).
func verifyLabelingContract(db *sql.DB, job labelingJob) error {
	schema, err := labelingSchemaJSON()
	if err != nil {
		return err
	}
	var schemaHash, taxonomyHash, templateHash string
	if err := db.QueryRow(`SELECT schema_sha256,taxonomy_sha256,prompt_template_sha256 FROM jobs WHERE name=?`, job.Name).Scan(&schemaHash, &taxonomyHash, &templateHash); err != nil {
		return err
	}
	for _, check := range []struct{ name, stored, current string }{{"schema", schemaHash, hashBytes([]byte(schema))},
		{"taxonomy", taxonomyHash, hashBytes(labelingTaxonomy)}, {"prompt " + job.Prompt, templateHash, labelingPromptTemplateHash(job.Prompt)}} {
		if check.stored != check.current {
			return fmt.Errorf("job %s was created with a different %s (sha256 %.12s, this build has %.12s); its results would not be comparable. Run it with the build that created it, or create a new job",
				job.Name, check.name, check.stored, check.current)
		}
	}
	rows, err := db.Query(`SELECT i.item_id,i.contract_hash,u.sender_role,u.channel,u.context_prompt,u.context_excerpt,t.text FROM requests q
		JOIN request_items i ON i.request_id=q.id LEFT JOIN items t ON t.id=i.item_id LEFT JOIN units u ON u.id=t.unit_id
		WHERE q.job=? AND q.state IN ('pending','leased','stopped','waiting_suspicion') AND i.blocked IS NULL`, job.Name)
	if err != nil {
		return err
	}
	defer rows.Close()
	changed := []string{}
	for rows.Next() {
		var id, stored string
		var sender, channel, context, text sql.NullString
		var excerpt sql.NullBool
		if err := rows.Scan(&id, &stored, &sender, &channel, &context, &excerpt, &text); err != nil {
			return err
		}
		row := labelingPromptRow{ID: id, Sender: sender.String, Channel: channel.String, Context: context.String, Excerpt: excerpt.Bool, Text: text.String}
		if !text.Valid || labelingContractHash(job.Prompt, row) != stored {
			changed = append(changed, id)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(changed) > 0 {
		return fmt.Errorf("job %s: %d unsent items no longer match the contract frozen at creation (first: %s); the dataset changed under the job. Create a new job (import-results keeps unchanged results)",
			job.Name, len(changed), changed[0])
	}
	return nil
}

// acceptServedModel records the operator's acceptance of a served model,
// loads every accepted model, and releases held requests whose differing
// models are all accepted: their held items return to the outcome they
// had, keeping the served_model_differs issue.
func (r *labelingRunner) acceptServedModel(model string) error {
	if model != "" {
		if _, err := r.db.Exec(`INSERT OR IGNORE INTO accepted_models(job,model,accepted_at) VALUES(?,?,?)`, r.job.Name, model, now()); err != nil {
			return err
		}
	}
	rows, err := queryMaps(r.db, `SELECT model FROM accepted_models WHERE job=?`, r.job.Name)
	if err != nil {
		return err
	}
	r.accepted = map[string]bool{}
	for _, row := range rows {
		r.accepted[firstString(row["model"])] = true
	}
	held, err := queryMaps(r.db, `SELECT q.id,a.id attempt,a.reported_models FROM requests q JOIN attempts a ON a.request_id=q.id AND a.number=q.attempts
		WHERE q.job=? AND q.state='held'`, r.job.Name)
	if err != nil {
		return err
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	released := 0
	for _, row := range held {
		var reported []string
		json.Unmarshal([]byte(firstString(row["reported_models"])), &reported)
		accepted := true
		for _, served := range reported {
			accepted = accepted && (served == r.job.Model || r.accepted[served])
		}
		if !accepted {
			continue
		}
		if _, err := tx.Exec(`UPDATE item_results SET outcome=COALESCE(held_outcome,outcome),served_model=? WHERE attempt_id=? AND outcome='held'`,
			servedModelAccepted, row["attempt"]); err != nil {
			return err
		}
		var unusable int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM item_results WHERE attempt_id=? AND outcome NOT IN ('usable','usable_with_evidence_issues')`, row["attempt"]).Scan(&unusable); err != nil {
			return err
		}
		state := requestCompleted
		if unusable > 0 {
			state = requestInvalid
		}
		if _, err := tx.Exec(`UPDATE requests SET state=?,updated_at=?,note=? WHERE id=?`, state, now(), "released: served model accepted", row["id"]); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE attempts SET served_model=? WHERE id=?`, servedModelAccepted, row["attempt"]); err != nil {
			return err
		}
		// Invalid or missing items of the released attempt are retried as
		// if it had just finished, in the same transaction.
		retry := []string{}
		failing, err := queryMaps(tx, `SELECT item_id FROM item_results WHERE attempt_id=? AND outcome IN ('invalid','missing') ORDER BY item_id`, row["attempt"])
		if err != nil {
			return err
		}
		for _, item := range failing {
			var failures int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM item_results WHERE job=? AND item_id=? AND outcome IN ('invalid','missing')`, r.job.Name, item["item_id"]).Scan(&failures); err != nil {
				return err
			}
			if failures <= r.opts.RetryInvalid {
				retry = append(retry, firstString(item["item_id"]))
			}
		}
		if err := labelingRetryRequest(tx, r.job.Name, firstString(row["id"]), retry, r.calibration.outputItem); err != nil {
			return err
		}
		released++
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if released > 0 {
		r.printf("%s: released %d held requests: served model accepted\n", r.job.Name, released)
	}
	return nil
}

// labelingProcessStart identifies a running process by its start time, so a
// reused pid is not mistaken for it. Empty when it does not run.
func labelingProcessStart(pid int) string {
	output, err := exec.Command("ps", "-o", "lstart=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func (r *labelingRunner) stopRequested(ctx context.Context) bool {
	r.mu.Lock()
	stopping := r.stopping
	r.mu.Unlock()
	if stopping {
		return true
	}
	var flag int
	r.db.QueryRow(`SELECT stop_requested FROM jobs WHERE name=?`, r.job.Name).Scan(&flag)
	if ctx.Err() != nil || flag != 0 {
		r.mu.Lock()
		r.stopping = true
		r.mu.Unlock()
		return true
	}
	return false
}

func (r *labelingRunner) terminate() {
	r.hardOnce.Do(func() { close(r.hardStop) })
}

// loop leases requests while none of stop, halt or the ceiling applies,
// with at most Concurrency attempts in flight.
func (r *labelingRunner) loop(ctx context.Context) error {
	done := make(chan struct{}, r.opts.Concurrency)
	active := 0
	ticker := time.NewTicker(labelingPollInterval)
	defer ticker.Stop()
	var grace <-chan time.Time
	announced := false
	signals := r.opts.Signals
	for {
		stopping := r.stopRequested(ctx)
		if stopping && !announced {
			announced = true
			r.printf("%s: stopping: no new requests; %d in flight may finish (up to %s)\n", r.job.Name, active, labelingStopGrace)
			grace = time.After(labelingStopGrace)
		}
		r.mu.Lock()
		halted := r.halted != ""
		r.mu.Unlock()
		if !stopping && !halted && active < r.opts.Concurrency {
			request, ok, err := r.next()
			if err == nil && ok {
				var leased bool
				leased, err = r.lease(request)
				if err == nil && leased {
					active++
					go func() {
						if err := r.attempt(ctx, request); err != nil {
							r.halt("attempt " + request.id + ": " + err.Error())
						}
						done <- struct{}{}
					}()
				}
			}
			// A ledger error stops leasing; attempts in flight still finish
			// and are finalized before the error is returned.
			if err != nil {
				r.loopError = err
				r.halt("ledger error: " + err.Error())
				continue
			}
			if ok {
				continue
			}
			if active == 0 {
				return nil
			}
		}
		if active == 0 && (stopping || halted) {
			return r.loopError
		}
		select {
		case <-done:
			active--
		case <-ticker.C:
		case <-grace:
			grace = nil
			r.printf("%s: stop grace over; terminating %d attempts\n", r.job.Name, active)
			r.terminate()
		case <-signals:
			if stopping {
				r.terminate()
			} else {
				r.mu.Lock()
				r.stopping = true
				r.mu.Unlock()
			}
		}
	}
}

type labelingPendingRequest struct {
	id            string
	input, output float64
}

func (r *labelingRunner) next() (labelingPendingRequest, bool, error) {
	var request labelingPendingRequest
	err := r.db.QueryRow(`SELECT id,est_input_tokens,est_output_tokens FROM requests WHERE job=? AND state='pending' ORDER BY position LIMIT 1`, r.job.Name).
		Scan(&request.id, &request.input, &request.output)
	if errors.Is(err, sql.ErrNoRows) {
		return request, false, nil
	}
	return request, err == nil, err
}

// lease checks the ceiling, known spend plus the estimates of in-flight
// requests, unknown outcomes and this request, then leases it.
func (r *labelingRunner) lease(request labelingPendingRequest) (bool, error) {
	if err := labelingLeaseHook(); err != nil {
		return false, err
	}
	spend, err := computeLabelingSpend(r.db, r.job, r.book, r.day())
	if err != nil {
		return false, err
	}
	next := 0.0
	if cost := labelingRequestEstimate(r.book, r.job.Model, r.day(), request.input, request.output); cost != nil {
		next = *cost
	}
	if committed := spend.CommittedEstimate + next; committed > r.opts.MaxUSD {
		r.halt(fmt.Sprintf("job lifetime spend ceiling: known $%.4f + in flight ~$%.4f + %d attempts without usage (unknown, failed or unreported) ~$%.4f (counted even after a retry: they may have been charged) + next request ~$%.4f exceeds --max-usd %.4f; to continue, run again with a higher --max-usd",
			spend.Known, spend.InFlight, spend.UnknownAttempts, spend.UnknownExposure, next, r.opts.MaxUSD))
		return false, nil
	}
	result, err := r.db.Exec(`UPDATE requests SET state='leased',updated_at=? WHERE id=? AND state='pending'`, now(), request.id)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n == 1, nil
}

// labelingOpenReceipt creates an attempt's events.jsonl; tests substitute
// a failing writer.
var labelingOpenReceipt = func(path string) (labelingReceiptFile, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}

type labelingReceiptFile interface {
	labelingReceipt
	Close() error
}

type labelingAttemptInfo struct {
	id, request, dir, day string
	number                int
}

// labelingRequestItems are a request's items in prompt order.
func (r *labelingRunner) requestItems(request string) ([]string, map[string]string, error) {
	rows, err := queryMaps(r.db, `SELECT item_id,contract_hash FROM request_items WHERE request_id=? AND blocked IS NULL ORDER BY position`, request)
	if err != nil {
		return nil, nil, err
	}
	ids, hashes := []string{}, map[string]string{}
	for _, row := range rows {
		id := firstString(row["item_id"])
		ids = append(ids, id)
		hashes[id] = firstString(row["contract_hash"])
	}
	return ids, hashes, nil
}

func (r *labelingRunner) promptRows(ids []string) ([]labelingPromptRow, error) {
	rows := []labelingPromptRow{}
	for _, id := range ids {
		row := labelingPromptRow{ID: id}
		if err := r.db.QueryRow(`SELECT u.sender_role,u.channel,u.context_prompt,u.context_excerpt,i.text FROM items i JOIN units u ON u.id=i.unit_id WHERE i.id=?`, id).
			Scan(&row.Sender, &row.Channel, &row.Context, &row.Excerpt, &row.Text); err != nil {
			return nil, fmt.Errorf("item %s: %w", id, err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func randomUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// attempt sends one leased request: files first, then submitted in one
// transaction, then the CLI, then finalize.
func (r *labelingRunner) attempt(ctx context.Context, request labelingPendingRequest) error {
	if r.stopRequested(ctx) {
		_, err := r.db.Exec(`UPDATE requests SET state='stopped',updated_at=? WHERE id=? AND state='leased'`, now(), request.id)
		return err
	}
	// Items a refusal elsewhere now blocks leave the request before its
	// prompt is built; a request left empty is not sent.
	check, err := r.refusalCheck(true)
	if err != nil {
		return err
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	blocked, remaining, held, err := applyLabelingRefusals(tx, check, r.job.Name, request.id)
	if err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if blocked > 0 {
		r.printf("%s %s: %d items not sent: blocked by a refusal\n", r.job.Name, request.id, blocked)
	}
	if held {
		r.printf("%s %s: held: an item is a suspected refusal in another job\n", r.job.Name, request.id)
	}
	if remaining == 0 || held {
		return nil
	}
	ids, _, err := r.requestItems(request.id)
	if err != nil {
		return err
	}
	rows, err := r.promptRows(ids)
	if err != nil {
		return err
	}
	prompt, err := buildLabelingPrompt(r.job.Prompt, rows)
	if err != nil {
		return err
	}
	var number int
	if err := r.db.QueryRow(`SELECT attempts+1 FROM requests WHERE id=?`, request.id).Scan(&number); err != nil {
		return err
	}
	_, sequence, _ := strings.Cut(request.id, ":")
	attempt := labelingAttemptInfo{id: fmt.Sprintf("%s/%d", request.id, number), request: request.id, number: number, day: r.day(),
		dir: filepath.Join(r.dir, "attempts", r.job.Name, sequence, fmt.Sprint(number))}
	if err := os.MkdirAll(attempt.dir, 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(attempt.dir, "prompt.txt"), []byte(prompt), 0o600); err != nil {
		return err
	}
	session := randomUUID()
	answer := filepath.Join(attempt.dir, "answer.json")
	args, err := labelingCommand(r.job.Route, r.job.Model, r.job.Effort, r.schema, r.schemaPath(), answer, session)
	if err != nil {
		return err
	}
	// Antigravity takes the prompt as an argument, not on stdin.
	stdin := prompt
	if r.job.Route == labelingRouteAgy {
		args = append(args, "--print-timeout", fmt.Sprintf("%ds", int(max(r.opts.Timeout-30*time.Second, 10*time.Second).Seconds())), "-p", prompt)
		stdin = ""
	}
	promptHash := hashBytes([]byte(prompt))
	recorded := make([]string, len(args))
	for index, arg := range args {
		recorded[index] = arg
		switch {
		case arg == r.schema:
			recorded[index] = "<schema " + r.schemaPath() + ">"
		case arg == prompt:
			recorded[index] = "<prompt.txt sha256 " + promptHash + ">"
		}
	}
	command := map[string]any{"cli": r.cliPath, "cli_version": r.cliVersion, "argv": recorded, "stdin": "prompt.txt sha256 " + promptHash,
		"cwd": r.runner, "environment": "inherited from the runner, minus CLAUDECODE, CLAUDE_CODE_ENTRYPOINT and PHAROS_API_TOKEN; not recorded",
		"session_id": session}
	if err := durableJSON(filepath.Join(attempt.dir, "command.json"), command); err != nil {
		return err
	}
	events, err := labelingOpenReceipt(filepath.Join(attempt.dir, "events.jsonl"))
	if err != nil {
		return err
	}
	defer events.Close()
	stderr, err := os.OpenFile(filepath.Join(attempt.dir, "stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer stderr.Close()
	tx, err = r.db.Begin()
	if err != nil {
		return err
	}
	// A stop that arrived while the files were written still wins: the
	// request is checked and marked submitted in one transaction.
	var stopFlag int
	if err := tx.QueryRow(`SELECT stop_requested FROM jobs WHERE name=?`, r.job.Name).Scan(&stopFlag); err != nil {
		tx.Rollback()
		return err
	}
	r.mu.Lock()
	stopping := r.stopping
	r.mu.Unlock()
	if stopFlag != 0 || stopping {
		_, err := tx.Exec(`UPDATE requests SET state='stopped',updated_at=? WHERE id=? AND state='leased'`, now(), request.id)
		if err != nil {
			tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	// A refusal recorded while the prompt was built: the request goes back
	// to pending without its blocked items (or stays unsent if none is
	// left), in the same transaction that would have submitted it.
	check, err = r.refusalCheck(false)
	if err != nil {
		tx.Rollback()
		return err
	}
	if late, _, lateHeld, err := applyLabelingRefusals(tx, check, r.job.Name, request.id); err != nil {
		tx.Rollback()
		return err
	} else if lateHeld {
		return tx.Commit()
	} else if late > 0 {
		if _, err := tx.Exec(`UPDATE requests SET state='pending' WHERE id=? AND state='leased'`, request.id); err != nil {
			tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	if _, err := tx.Exec(`INSERT INTO attempts(id,request_id,number,job,state,submitted_at,day,dir,prompt_sha256,session_id,cli_version) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		attempt.id, request.id, number, r.job.Name, requestSubmitted, now(), attempt.day, attempt.dir, promptHash, session, r.cliVersion); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`UPDATE requests SET state='submitted',attempts=?,updated_at=? WHERE id=?`, number, now(), request.id); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	stream := newLabelingStream(r.job.Route)
	var mu sync.Mutex
	process := runLabelingProcess(r.cliPath, args, r.env, r.runner, stdin, events, stderr, stream, &mu, r.opts.Timeout, r.hardStop, func(pid int) {
		r.db.Exec(`UPDATE attempts SET pid=?,process_start=? WHERE id=?`, pid, nilIfEmpty(labelingProcessStart(pid)), attempt.id)
	})
	return r.finalize(attempt, stream, &process, false)
}

// labelingUsageRow is one model's usage in an attempt, priced.
type labelingUsageRow struct {
	incomplete          bool
	model, source, role string
	tokens              map[string]int64
	cost                usageCost
}

func labelingCostTokens(counts tokenCounts) map[string]int64 {
	tokens := map[string]int64{}
	for key, value := range counts {
		tokens[key] = int64(math.Round(value))
	}
	if _, ok := tokens["uncached_input_tokens"]; !ok {
		tokens["uncached_input_tokens"] = max(0, tokens["input_tokens"]-tokens["cache_read_input_tokens"]-tokens["cache_creation_input_tokens"])
	}
	return tokens
}

// labelingExactCost prices usage only under the exact model string the
// route reported: a priced model whose canonical name is that string. No
// family normalization or alias maps a snapshot or variant name onto
// another model's rates; such usage stays unpriced.
func labelingExactCost(book priceBook, model, day string, tokens map[string]int64) usageCost {
	interval := book.price(modelFamily(model), day)
	if interval == nil || interval.model != model {
		return usageCost{status: "unpriced"}
	}
	return book.cost(model, day, tokens)
}

// labelingPricingRule versions how usage is priced; a job priced under
// another rule is re-priced from its stored tokens at the next run start.
//
// pricing-v5: an upper bound is stored as an estimate beside an unpriced
// row (estimate_usd, estimate_basis), not as its cost.
// pricing-v4: usage that cannot be priced as itself is priced at the
// provider's highest rates as an upper-bound estimate (exposure).
// pricing-v3: as v2, and a served model's usage that cannot be priced as
// itself is an estimate at the requested model's rate (exposure).
// pricing-v2: the labeling model (the requested one, and any served model
// the route reported) is priced only under its exact name, and a served
// model nobody accepted not at all; auxiliary models (a CLI's own calls,
// such as Claude Code's session titles) through the standard price-book
// resolution (family and dated aliases), so a dated snapshot name prices
// as its model.
const labelingPricingRule = "pricing-v5"

const (
	usagePrimary   = "primary"
	usageAuxiliary = "auxiliary"
)

// labelingUsageCost prices one usage row under labelingPricingRule. A
// served model other than the requested one is priced as itself only when
// the operator accepted it and the price book has it. Usage that cannot be
// priced as itself (a held or re-queued served model, an auxiliary model
// the book cannot resolve, an unpriced requested model) is priced at a
// conservative upper bound (labelingUpperBoundCost): exposure, not known
// spend, never $0.
func labelingUsageCost(book priceBook, role, model, requested, day string, tokens map[string]int64, blocked, assumed bool) usageCost {
	if strings.HasPrefix(model, labelingUnattributed) {
		return labelingUpperBoundCost(book, strings.TrimPrefix(model, labelingUnattributed), day, tokens)
	}
	var cost usageCost
	switch {
	case blocked:
		cost = usageCost{status: "unpriced"}
	case role == usageAuxiliary:
		cost = book.cost(model, day, tokens)
	default:
		cost = labelingExactCost(book, model, day, tokens)
	}
	if cost.status == "unpriced" || cost.cost == nil {
		cost = labelingUpperBoundCost(book, model, day, tokens)
	}
	if assumed && cost.status == "priced" {
		cost.status = "assumed"
	}
	return cost
}

// Usage cost statuses for estimates: exposure, not known spend.
const (
	costUpperBound = "estimated_upper_bound"
)

// labelingStoredUsageCost is how a usage row stores its cost: an upper
// bound is not a price, so the row is unpriced and the bound is kept apart
// as an estimate (exposure).
type labelingStoredUsageCost struct {
	cost, estimate     any
	status             string
	pricedModel, basis any
}

func labelingStoredCost(cost usageCost) labelingStoredUsageCost {
	stored := labelingStoredUsageCost{status: cost.status, pricedModel: nilIfEmpty(cost.pricedModel)}
	if cost.cost == nil {
		return stored
	}
	if cost.status == costUpperBound {
		stored.status, stored.pricedModel, stored.estimate, stored.basis = "unpriced", nil, *cost.cost, cost.pricedModel
		return stored
	}
	stored.cost = *cost.cost
	return stored
}

// labelingProvider infers a model's provider from its name.
func labelingProvider(model string) string {
	name := strings.ToLower(model)
	switch {
	case strings.HasPrefix(name, "claude") || strings.HasPrefix(name, "haiku") || strings.HasPrefix(name, "sonnet") || strings.HasPrefix(name, "opus") || strings.HasPrefix(name, "fable"):
		return "anthropic"
	case strings.HasPrefix(name, "gpt") || strings.HasPrefix(name, "codex") || regexp.MustCompile(`^o\d`).MatchString(name):
		return "openai"
	case strings.HasPrefix(name, "gemini"):
		return "google"
	}
	return ""
}

// labelingUpperBoundCost prices tokens at the highest rate, per token
// category, of any model of the same provider in the price book on that
// day (a category no model prices takes the highest rate of any category).
// Unpriced when the provider is unknown or has no priced model.
func labelingUpperBoundCost(book priceBook, model, day string, tokens map[string]int64) usageCost {
	provider := labelingProvider(model)
	if provider == "" {
		return usageCost{status: "unpriced"}
	}
	var highest [5]float64
	found := false
	for _, intervals := range book.prices {
		for _, interval := range intervals {
			if day < interval.from || interval.to != "" && day >= interval.to || labelingProvider(interval.model) != provider {
				continue
			}
			for index, rate := range interval.rates {
				if rate != nil && *rate > highest[index] {
					highest[index] = *rate
					found = true
				}
			}
		}
	}
	if !found {
		return usageCost{status: "unpriced"}
	}
	top := 0.0
	for _, rate := range highest {
		top = max(top, rate)
	}
	oneHour := tokens["cache_creation_1h_input_tokens"]
	quantities := [5]int64{tokens["uncached_input_tokens"], tokens["cache_read_input_tokens"],
		max(tokens["cache_creation_input_tokens"]-oneHour, 0), oneHour, tokens["output_tokens"]}
	total := 0.0
	for index, quantity := range quantities {
		rate := highest[index]
		if rate == 0 {
			rate = top
		}
		total += float64(quantity) * rate / 1_000_000
	}
	return usageCost{cost: &total, status: costUpperBound, pricedModel: "upper bound: " + provider}
}

// labelingAttemptUsage prices every model an attempt's terminal event
// reports, auxiliary models included, each at its own rates on the
// attempt's day (labelingUsageCost). blocked names served models nobody
// accepted: their usage is recorded but never priced. Claude's modelUsage
// lacks cache-write TTLs: the model whose counts equal the result's usage
// takes that usage (with the TTL split); a single cache-writing model takes
// the result's one-hour share; when the result reports one-hour writes that
// cannot be assigned to one model, every other model's writes are priced at
// the one-hour (higher) rate and marked assumed. Codex reports no model:
// its usage is the requested model's.
func labelingAttemptUsage(stream *labelingStream, requested, day string, book priceBook, blocked map[string]bool) (rows []labelingUsageRow, unexplained bool) {
	rows = []labelingUsageRow{}
	primary := map[string]bool{requested: true}
	for _, model := range stream.Models {
		primary[model] = true
	}
	addCounts := func(model, source string, counts tokenCounts, assumed bool) {
		if counts == nil {
			return
		}
		role := usageAuxiliary
		if primary[model] {
			role = usagePrimary
		}
		tokens := labelingCostTokens(counts)
		// Usage without the input and output counts cannot be priced
		// (a lone total_tokens would price as $0): partial, and the
		// attempt keeps its estimated exposure.
		_, hasInput := counts["input_tokens"]
		_, hasOutput := counts["output_tokens"]
		if !hasInput || !hasOutput {
			rows = append(rows, labelingUsageRow{model: model, source: source + " " + labelingIncompleteSource, role: role, tokens: tokens,
				cost: usageCost{status: "partial"}, incomplete: true})
			return
		}
		rows = append(rows, labelingUsageRow{model: model, source: source, role: role, tokens: tokens,
			cost: labelingUsageCost(book, role, model, requested, day, tokens, blocked[model], assumed)})
	}
	top := normalizeTokenCounts(stream.usage)
	if stream.route == labelingRouteClaude && len(stream.modelUsage) > 0 {
		models := make([]string, 0, len(stream.modelUsage))
		writers := 0
		for model := range stream.modelUsage {
			models = append(models, model)
			if counts := normalizeTokenCounts(stream.modelUsage[model]); counts != nil && counts["cache_creation_input_tokens"] > 0 {
				writers++
			}
		}
		sort.Strings(models)
		matched := ""
		for _, model := range models {
			counts := normalizeTokenCounts(stream.modelUsage[model])
			if matched == "" && top != nil && counts != nil && counts["input_tokens"] == top["input_tokens"] && counts["output_tokens"] == top["output_tokens"] {
				matched = model
			}
		}
		oneHour := top["cache_creation_1h_input_tokens"]
		for _, model := range models {
			counts := normalizeTokenCounts(stream.modelUsage[model])
			switch {
			case model == matched:
				addCounts(model, "result usage", top, false)
			case counts == nil:
			case oneHour > 0 && counts["cache_creation_input_tokens"] > 0 && matched == "" && writers == 1:
				counts["cache_creation_1h_input_tokens"] = math.Min(oneHour, counts["cache_creation_input_tokens"])
				addCounts(model, "modelUsage with the result's 1h cache-write share", counts, false)
			case oneHour > 0 && counts["cache_creation_input_tokens"] > 0:
				counts["cache_creation_1h_input_tokens"] = counts["cache_creation_input_tokens"]
				addCounts(model, labelingUnknownTTLSource, counts, true)
			default:
				addCounts(model, "modelUsage", counts, false)
			}
		}
		// The result's usage is the labeling call's: what modelUsage
		// attributes to the labeling model must cover it. The rest is
		// recorded as unattributed usage, bounded at the provider's highest
		// rates (exposure), and the run halts.
		if top != nil {
			var input, output int64
			for _, row := range rows {
				if row.role == usagePrimary {
					input += row.tokens["input_tokens"]
					output += row.tokens["output_tokens"]
				}
			}
			missingIn := max(int64(top["input_tokens"])-input, 0)
			missingOut := max(int64(top["output_tokens"])-output, 0)
			if missingIn > 0 || missingOut > 0 {
				tokens := map[string]int64{"input_tokens": missingIn, "uncached_input_tokens": missingIn, "output_tokens": missingOut, "total_tokens": missingIn + missingOut}
				cost := labelingUpperBoundCost(book, requested, day, tokens)
				rows = append(rows, labelingUsageRow{model: labelingUnattributed + requested, source: "result usage modelUsage does not attribute to the labeling model",
					role: usagePrimary, tokens: tokens, cost: cost})
				return rows, true
			}
		}
		return rows, false
	}
	if top == nil {
		return rows, false
	}
	model, source := requested, "requested model (the route reports none)"
	if len(stream.Models) > 0 {
		model, source = stream.Models[0], "reported model"
	}
	addCounts(model, source, top, false)
	return rows, false
}

// labelingUnattributed prefixes the usage row of result usage that
// modelUsage does not attribute to the labeling model.
const labelingUnattributed = "unattributed:"

const labelingIncompleteSource = "(missing input or output counts)"

const labelingUnknownTTLSource = "modelUsage; cache writes of unknown TTL priced at the 1h rate"

// repriceLabelingUsage prices every usage row of a job again from its
// stored tokens under the current rule, when the job was priced under
// another one or an operator accepted a served model.
func (r *labelingRunner) repriceLabelingUsage(force bool) error {
	var rule sql.NullString
	if err := r.db.QueryRow(`SELECT pricing_rule FROM jobs WHERE name=?`, r.job.Name).Scan(&rule); err != nil {
		return err
	}
	if rule.String == labelingPricingRule && !force {
		return nil
	}
	rows, err := queryMaps(r.db, `SELECT u.attempt_id,u.model,u.model_source,u.tokens,u.day,a.reported_models FROM usage u JOIN attempts a ON a.id=u.attempt_id WHERE a.job=?`, r.job.Name)
	if err != nil {
		return err
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, row := range rows {
		model := firstString(row["model"])
		var reported []string
		json.Unmarshal([]byte(firstString(row["reported_models"])), &reported)
		role := usageAuxiliary
		if model == r.job.Model || containsString(reported, model) || strings.HasPrefix(model, labelingUnattributed) {
			role = usagePrimary
		}
		var tokens map[string]int64
		json.Unmarshal([]byte(firstString(row["tokens"])), &tokens)
		blocked := role == usagePrimary && model != r.job.Model && !r.accepted[model]
		cost := labelingUsageCost(r.book, role, model, r.job.Model, firstString(row["day"]), tokens, blocked, firstString(row["model_source"]) == labelingUnknownTTLSource)
		// Counts that were incomplete stay incomplete: re-pricing never
		// turns partial usage into priced usage.
		if strings.Contains(firstString(row["model_source"]), labelingIncompleteSource) {
			cost = usageCost{status: "partial"}
		}
		stored := labelingStoredCost(cost)
		if _, err := tx.Exec(`UPDATE usage SET role=?,cost_usd=?,cost_status=?,priced_model=?,estimate_usd=?,estimate_basis=? WHERE attempt_id=? AND model=?`, role,
			stored.cost, stored.status, stored.pricedModel, stored.estimate, stored.basis, row["attempt_id"], model); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE jobs SET pricing_rule=? WHERE name=?`, labelingPricingRule, r.job.Name); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if len(rows) > 0 && rule.String != labelingPricingRule {
		r.printf("%s: re-priced %d usage records under %s (was %s)\n", r.job.Name, len(rows), labelingPricingRule, defaultString(rule.String, "pricing-v1"))
	}
	return nil
}

// finalize settles one attempt from its stream: outcome, item results,
// usage and spend, the request's state, and a retry request for invalid
// or missing items while --retry-invalid allows, all in one transaction.
func (r *labelingRunner) finalize(attempt labelingAttemptInfo, stream *labelingStream, process *labelingProcess, recovered bool) error {
	ids, hashes, err := r.requestItems(attempt.request)
	if err != nil {
		return err
	}
	sources, err := loadLabelingItemSources(context.Background(), r.db, ids)
	if err != nil {
		return err
	}
	ordered := make([]labelingItemSource, len(ids))
	for index, id := range ids {
		ordered[index] = sources[id]
	}
	all := func(outcome, issue string) []labelingItemResult {
		results := []labelingItemResult{}
		for _, id := range ids {
			results = append(results, labelingItemResult{ItemID: id, Outcome: outcome, Labels: []string{}, Uncertain: []string{},
				Evidence: []labelingEvidence{}, Issues: []string{issue}})
		}
		return results
	}
	var outcome, errorText string
	var results []labelingItemResult
	problems := []string{}
	notSent := false
	answer := ""
	if r.job.Route == labelingRouteCodex {
		answer = filepath.Join(attempt.dir, "answer.json")
	}
	validate := func(output json.RawMessage) {
		var requestProblems []string
		results, requestProblems = validateLabelingOutput(output, ordered, r.labels)
		problems = append(problems, requestProblems...)
		outcome = requestCompleted
		for _, result := range results {
			if result.Outcome != itemUsable && result.Outcome != itemEvidenceIssues {
				outcome = requestInvalid
			}
		}
		if len(requestProblems) > 0 {
			outcome = requestInvalid
		}
	}
	// Only a CLI that never started is known not to have been sent. Once it
	// runs it may reach the provider before printing anything.
	if stream.Terminal == "" && !stream.Started && process.StartError == "" {
		problems = append(problems, "no_stream_events")
	}
	// A refusal of a request of several items cannot be attributed to one
	// of them: each is marked unresolved, and none is re-sent by default.
	refuse := func(reason string) {
		outcome, results = requestRefused, all(itemRefused, "refused: "+reason)
		if len(ids) > 1 {
			for index := range results {
				results[index].Issues = append(results[index].Issues, "refused_copacked_unresolved")
			}
		}
	}
	answerOutput := json.RawMessage(nil)
	if answer != "" {
		answerOutput = stream.output(answer)
	}
	switch {
	case process.StartError != "":
		outcome, errorText, notSent = requestFailed, "not_sent: the CLI did not start: "+process.StartError, true
	case process.ReceiptError != "":
		// Without a durable receipt the result cannot be audited or
		// recovered: the attempt stays uncertain and the run halts.
		outcome, errorText = requestUnknown, "the saved stream is not durable: "+process.ReceiptError
	case stream.Refusal != "":
		// A structured refusal stands even when no terminal event followed.
		refuse(stream.Refusal)
	case len(stream.Tools) > 0:
		// Any tool use breaks the annotation-only contract, whatever else
		// was saved; it is not re-sent automatically.
		outcome, results = requestInvalid, all(itemInvalid, "tool_use")
		problems = append(problems, "tool_use")
	case stream.Terminal == "" && answerOutput != nil:
		// Codex wrote its last message, but the turn's end (and usage)
		// never arrived: the saved answer is the result; exposure stays.
		problems = append(problems, "answer_without_terminal")
		validate(answerOutput)
	case stream.Terminal == "":
		outcome = requestUnknown
		switch {
		case recovered:
			errorText = "the runner ended before the attempt did, and the saved stream has no terminal event"
		case process.TimedOut:
			errorText = "timed out without a terminal event"
		case process.Stopped:
			errorText = "terminated by a stop before a terminal event"
		default:
			errorText = fmt.Sprintf("the CLI exited (code %v) without a terminal event", exitCode(process))
		}
	case stream.Failed && labelingRefusalError.MatchString(stream.errorCode) && !labelingNetworkError.MatchString(stream.errorCode):
		refuse("provider error: " + firstRunes(stream.Error, 200))
	case stream.Failed:
		outcome, errorText = requestFailed, stream.Error
	default:
		output := stream.output(answer)
		switch {
		case output == nil && labelingOpensWithRefusal(stream.text):
			refuse("reply instead of structured output")
		case output == nil && refusalText.MatchString(stream.text):
			// Refusal words inside other text (a cut-off answer quoting
			// "I can't ...") are only a suspicion: kept apart, re-sent only
			// on the operator's request.
			outcome, results = requestRefusalSuspected, all(itemRefusalSuspected, "refusal_suspected")
			problems = append(problems, "refusal_suspected")
		case output == nil:
			outcome, results = requestInvalid, all(itemInvalid, "no_structured_output")
			problems = append(problems, "no_structured_output")
		default:
			validate(output)
		}
	}
	// The served model must be exactly the requested one, or one the
	// operator accepted. Codex names none, so its results are usable but
	// unverified.
	// Antigravity may report the model id it was given (model and effort
	// joined): that is the requested model.
	if cliModel := labelingCLIModel(r.job.Route, r.job.Model, r.job.Effort); cliModel != r.job.Model {
		for index, model := range stream.Models {
			if model == cliModel {
				stream.Models[index] = r.job.Model
			}
		}
	}
	served := servedModelUnverified
	if len(stream.Models) > 0 {
		served = servedModelVerified
	}
	differs, unaccepted := []string{}, map[string]bool{}
	for _, model := range stream.Models {
		if model != r.job.Model {
			differs = append(differs, model)
			if !r.accepted[model] {
				unaccepted[model] = true
			}
		}
	}
	if len(differs) > 0 {
		problems = append(problems, servedModelDiffers+":"+strings.Join(differs, ","))
		served = servedModelAccepted
		for index := range results {
			results[index].Issues = appendUnique(results[index].Issues, servedModelDiffers)
		}
	}
	if len(unaccepted) > 0 {
		served = servedModelDiffers
		if outcome == requestCompleted || outcome == requestInvalid {
			outcome = requestHeld
			for index := range results {
				results[index].HeldOutcome = results[index].Outcome
				if results[index].Outcome == itemUsable || results[index].Outcome == itemEvidenceIssues {
					results[index].Outcome = itemHeld
				}
			}
		}
	}
	usage, unexplained := labelingAttemptUsage(stream, r.job.Model, attempt.day, r.book, unaccepted)
	cliWarnings := labelingCLIWarnings(filepath.Join(attempt.dir, "stderr.log"))
	// An attempt that may have reached the provider but reported no usage
	// keeps its estimated exposure in spend.
	usageMissing := !notSent && len(usage) == 0
	for _, row := range usage {
		usageMissing = usageMissing || row.incomplete
	}
	// Usage reported for the call but not attributed to the labeling model
	// is unexplained: it is bounded as exposure and the run halts.
	if unexplained && !notSent {
		problems = append(problems, "usage_unexplained")
	}
	// A successful terminal event without readable usage halts the run:
	// spend can no longer be known.
	resultWithoutUsage := usageMissing && stream.Terminal != "" && !stream.Failed
	if resultWithoutUsage {
		problems = append(problems, "usage_missing")
	}
	// A refusal of a request of one item is pinned to that item: its content
	// goes into the refusal registry, durably, before the ledger records it.
	if outcome == requestRefused && len(ids) == 1 {
		rows, err := queryMaps(r.db, `SELECT u.message_sha256||':'||i.target_hash key FROM items i JOIN units u ON u.id=i.unit_id WHERE i.id=?`, ids[0])
		if err != nil || len(rows) == 0 {
			return errors.Join(err, fmt.Errorf("item %s is not in the dataset", ids[0]))
		}
		manifest, _ := labelingManifestHash(r.dir)
		entry := labelingRefusalEntry{Key: firstString(rows[0]["key"]), Dataset: r.dir, Manifest: manifest, Job: r.job.Name, Item: ids[0],
			Attempt: attempt.id, RecordedAt: now()}
		if err := appendLabelingRegistry(r.registry, []labelingRefusalEntry{entry}); err != nil {
			return err
		}
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exit any
	if process.ExitCode != nil {
		exit = *process.ExitCode
	}
	var cliCost any
	if stream.cliCost != nil {
		cliCost = *stream.cliCost
	}
	cleanup := process.Cleanup
	if cleanup == nil {
		cleanup = []map[string]any{}
	}
	toolEvents := stream.Tools
	if toolEvents == nil {
		toolEvents = []map[string]any{}
	}
	if _, err := tx.Exec(`UPDATE attempts SET state='finished',outcome=?,finished_at=?,session_id=COALESCE(?,session_id),reported_models=?,exit_code=?,terminal_event=?,
		cleanup=?,tool_events=?,refusal=?,error=?,cli_cost_usd=?,duration_ms=?,problems=?,recovered=?,not_sent=?,served_model=?,receipt_error=?,usage_missing=?,cli_warnings=? WHERE id=?`,
		outcome, now(), nilIfEmpty(stream.SessionID), jsonText(stream.Models), exit, nilIfEmpty(stream.Terminal), jsonText(cleanup), jsonText(toolEvents),
		nilIfEmpty(stream.Refusal), nilIfEmpty(errorText), cliCost, process.Duration.Milliseconds(), jsonText(problems), recovered, notSent, served,
		nilIfEmpty(process.ReceiptError), usageMissing, jsonText(cliWarnings), attempt.id); err != nil {
		return err
	}
	unpriced := []string{}
	for _, row := range usage {
		// New usage without a price of its own halts, except a served model
		// the operator accepted knowing it is only bounded.
		if row.cost.status == "partial" || row.cost.status == "unpriced" || row.cost.status == costUpperBound && !r.accepted[row.model] && !strings.HasPrefix(row.model, labelingUnattributed) {
			unpriced = append(unpriced, row.model+" ("+row.cost.status+")")
		}
		stored := labelingStoredCost(row.cost)
		if _, err := tx.Exec(`INSERT INTO usage(attempt_id,model,model_source,tokens,cost_usd,cost_status,priced_model,day,role,estimate_usd,estimate_basis) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			attempt.id, row.model, row.source, jsonText(row.tokens), stored.cost, stored.status, stored.pricedModel, attempt.day, row.role, stored.estimate, stored.basis); err != nil {
			return err
		}
	}
	retry := []string{}
	for _, result := range results {
		if err := insertLabelingItemResult(tx, attempt.id, r.job.Name, attempt.request, result, hashes[result.ItemID], "", served); err != nil {
			return err
		}
		if outcome != requestHeld && !containsString(problems, "tool_use") && (result.Outcome == itemInvalid || result.Outcome == itemMissing) {
			var failures int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM item_results WHERE job=? AND item_id=? AND outcome IN ('invalid','missing')`, r.job.Name, result.ItemID).
				Scan(&failures); err != nil {
				return err
			}
			if failures <= r.opts.RetryInvalid {
				retry = append(retry, result.ItemID)
			}
		}
	}
	if _, err := tx.Exec(`UPDATE requests SET state=?,updated_at=? WHERE id=?`, outcome, now(), attempt.request); err != nil {
		return err
	}
	if err := labelingRetryRequest(tx, r.job.Name, attempt.request, retry, r.calibration.outputItem); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	r.mu.Lock()
	r.finished++
	r.mu.Unlock()
	if process.ReceiptError != "" {
		r.halt("attempt " + attempt.id + ": its saved stream is not durable (" + process.ReceiptError + "); it stays unknown; fix the storage and run again")
	}
	if outcome == requestHeld {
		names := []string{}
		for model := range unaccepted {
			names = append(names, model)
		}
		sort.Strings(names)
		r.halt(fmt.Sprintf("attempt %s was answered by %s, not the requested %s; its results are held (not usable). Accept that model with --accept-served-model, or re-queue with reconcile --requeue-held",
			attempt.id, strings.Join(names, ", "), r.job.Model))
	}
	if len(unpriced) > 0 && !r.opts.AllowUnpriced {
		r.halt("usage the price book cannot price: " + strings.Join(unpriced, ", ") + " (attempt " + attempt.id + "); pass --allow-unpriced to continue")
	}
	if unexplained {
		r.halt("attempt " + attempt.id + " reported usage that modelUsage does not attribute to the labeling model; its cost is estimated, not known. Review it before running again")
	}
	if resultWithoutUsage {
		r.halt("attempt " + attempt.id + " ended without usage the runner could read; its cost is estimated, not known. Review it (reconcile lists it) before running again")
	}
	if outcome == requestFailed && labelingAccountError.MatchString(errorText+" "+strings.Join(stream.Warnings, " ")) {
		r.halt("attempt " + attempt.id + " failed with an authentication, rate-limit or quota error: " + firstRunes(errorText, 300))
	}
	if outcome == requestFailed && labelingNetworkError.MatchString(errorText+" "+strings.Join(stream.Warnings, " ")) {
		r.halt("attempt " + attempt.id + " failed with a connection error: " + firstRunes(errorText, 300) + "; check the network before running again")
	}
	if !recovered {
		r.mu.Lock()
		if outcome == requestFailed || outcome == requestUnknown {
			r.consecutiveFailures++
		} else {
			r.consecutiveFailures = 0
		}
		// Refusals in a row point at the route or the prompt, not the
		// items: they halt too, before a run refuses its way through a job.
		if outcome == requestRefused {
			r.consecutiveRefusals++
		} else {
			r.consecutiveRefusals = 0
		}
		failures, refusals := r.consecutiveFailures, r.consecutiveRefusals
		r.mu.Unlock()
		if failures >= r.opts.MaxConsecutiveFailures {
			r.halt(fmt.Sprintf("%d consecutive attempts failed or ended unknown (last: %s); check the route (login, flags, quota) before running again",
				failures, firstRunes(errorText, 300)))
		}
		if refusals >= r.opts.MaxConsecutiveFailures {
			r.halt(fmt.Sprintf("%d consecutive requests were refused; review them (reconcile lists them) before running again", refusals))
		}
	}
	usable := 0
	for _, result := range results {
		if result.Outcome == itemUsable || result.Outcome == itemEvidenceIssues {
			usable++
		}
	}
	r.progress(attempt, outcome, usable, len(ids), len(retry))
	return nil
}

// labelingCLIWarning matches a tagged CLI diagnostic line on stderr, such
// as "[claude-code:unrecognized_model] {...}".
var labelingCLIWarning = regexp.MustCompile(`^\[[a-z0-9_-]+:[a-z0-9_.-]+\].*`)

// labelingCLIWarnings returns an attempt's distinct tagged stderr lines,
// each cut to 300 characters, at most 20.
func labelingCLIWarnings(path string) []string {
	warnings := []string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return warnings
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if labelingCLIWarning.MatchString(line) {
			line = firstRunes(line, 300)
			if !containsString(warnings, line) && len(warnings) < 20 {
				warnings = append(warnings, line)
			}
		}
	}
	return warnings
}

func exitCode(process *labelingProcess) any {
	if process.ExitCode == nil {
		return "unknown"
	}
	return *process.ExitCode
}

// progress prints one line per finished request: state counts, spend,
// unknown exposure, rate and ETA.
func (r *labelingRunner) progress(attempt labelingAttemptInfo, outcome string, usable, items, retried int) {
	states := map[string]int{}
	rows, _ := queryMaps(r.db, `SELECT state,COUNT(*) n FROM requests WHERE job=? GROUP BY state`, r.job.Name)
	parts := []string{}
	for _, row := range rows {
		states[firstString(row["state"])] = int(integer(row["n"]))
	}
	for _, state := range labelingRequestStates {
		if states[state] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", state, states[state]))
		}
	}
	spend, _ := computeLabelingSpend(r.db, r.job, r.book, r.day())
	line := fmt.Sprintf("%s %s: %s, %d/%d items usable", r.job.Name, attempt.id, outcome, usable, items)
	if retried > 0 {
		line += fmt.Sprintf(", %d re-queued", retried)
	}
	line += fmt.Sprintf(" | %s | known $%.4f, unknown-outcome ~$%.4f", strings.Join(parts, ", "), spend.Known, spend.UnknownExposure)
	r.mu.Lock()
	finished := r.finished
	r.mu.Unlock()
	if elapsed := time.Since(r.started); finished > 0 && elapsed > 0 {
		rate := float64(finished) / elapsed.Minutes()
		remaining := states[requestPending] + states[requestLeased] + states[requestSubmitted]
		line += fmt.Sprintf(" | %.1f req/min, ETA %s", rate, (time.Duration(float64(remaining) / rate * float64(time.Minute))).Round(time.Second))
	}
	r.printf("%s\n", line)
}

// requestLabelingStop asks a job's runner to stop: it leases nothing more
// and lets in-flight attempts finish.
func requestLabelingStop(dir, name string) error {
	shared, err := fileLease(filepath.Join(dir, labelingLockFile), false)
	if err != nil {
		return fmt.Errorf("a labeling prepare is using %s", dir)
	}
	defer shared.Close()
	if err := requireLabelingLedger(dir); err != nil {
		return err
	}
	db, err := openLabelingLedger(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.Exec(`UPDATE jobs SET stop_requested=1,stop_requested_at=? WHERE name=?`, now(), name)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("no job %q in %s", name, dir)
	}
	return nil
}

// requireLabelingLedger refuses a dataset without run tables, so commands
// that only act on jobs never create them (which would make the dataset
// impossible to prepare --replace).
func requireLabelingLedger(dir string) error {
	db, err := openLabelingDataset(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	if !labelingHasLedger(db) {
		return fmt.Errorf("no labeling jobs in %s", dir)
	}
	return nil
}

// withLabelingJobLock runs fn while no runner holds the job. Unless
// create, the dataset must already hold run tables.
func withLabelingJobLock(dir, name string, create bool, fn func(db *sql.DB) error) error {
	if !labelingJobName.MatchString(name) {
		return fmt.Errorf("job name %q: use letters, digits, '.', '_' and '-'", name)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	shared, err := fileLease(filepath.Join(dir, labelingLockFile), false)
	if err != nil {
		return fmt.Errorf("a labeling prepare is using %s", dir)
	}
	defer shared.Close()
	lock, err := fileLease(filepath.Join(dir, ".run-"+name+".lock"), true)
	if err != nil {
		return fmt.Errorf("a runner is running job %s; stop it first", name)
	}
	defer lock.Close()
	if !create {
		if err := requireLabelingLedger(dir); err != nil {
			return err
		}
	}
	db, err := openLabelingLedger(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	return fn(db)
}

func labelingStatusJSON(dir, name string, asJSON bool, w io.Writer) error {
	db, err := openLabelingDataset(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	if !labelingHasLedger(db) {
		return fmt.Errorf("no labeling jobs in %s", dir)
	}
	status, err := labelingJobStatus(db, name, time.Now())
	if err != nil {
		return err
	}
	if asJSON {
		return writeLabelingJSON(w, status)
	}
	_, err = fmt.Fprintln(w, labelingStatusLine(status))
	return err
}

// labelingAttemptRecord reads one attempt row for status and tests.
func labelingAttemptRecord(db *sql.DB, id string) (map[string]any, error) {
	rows, err := queryMaps(db, `SELECT * FROM attempts WHERE id=?`, id)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	for _, key := range []string{"cleanup", "tool_events", "problems", "reported_models"} {
		var value any
		json.Unmarshal([]byte(firstString(rows[0][key])), &value)
		rows[0][key] = value
	}
	return rows[0], nil
}

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
		summary, err := createLabelingJob(ctx, db, opts.Job, opts.Spec, nil, "")
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
	if err := r.prepareFiles(); err != nil {
		return err
	}
	if err := r.recover(); err != nil {
		return err
	}
	// Usage an earlier run could not price keeps blocking new requests
	// until the operator allows it.
	if !opts.AllowUnpriced {
		var unpriced int
		if err := db.QueryRow(`SELECT COUNT(*) FROM usage u JOIN attempts a ON a.id=u.attempt_id WHERE a.job=? AND u.cost_status IN ('partial','unpriced')`,
			job.Name).Scan(&unpriced); err != nil {
			return err
		}
		if unpriced > 0 {
			return fmt.Errorf("job %s holds %d usage records the price book cannot price (see status --json); pass --allow-unpriced to continue", job.Name, unpriced)
		}
	}
	settings := map[string]any{"max_usd": opts.MaxUSD, "concurrency": opts.Concurrency, "timeout_seconds": opts.Timeout.Seconds(),
		"retry_invalid": opts.RetryInvalid, "allow_unpriced": opts.AllowUnpriced, "max_consecutive_failures": opts.MaxConsecutiveFailures,
		"pid": os.Getpid(), "cli_path": r.cliPath, "cli_version": r.cliVersion}
	if _, err := db.Exec(`UPDATE jobs SET stop_requested=0,halted=NULL,run_started_at=?,run_settings=? WHERE name=?`, now(), compactJSON(settings), job.Name); err != nil {
		return err
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
	if _, err := r.db.Exec(`UPDATE requests SET state='pending',updated_at=? WHERE job=? AND state IN ('leased','stopped')`, now(), r.job.Name); err != nil {
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
			if err != nil {
				return err
			}
			if ok {
				leased, err := r.lease(request)
				if err != nil {
					return err
				}
				if leased {
					active++
					go func() {
						if err := r.attempt(ctx, request); err != nil {
							r.halt("attempt " + request.id + ": " + err.Error())
						}
						done <- struct{}{}
					}()
				}
				continue
			}
			if active == 0 {
				return nil
			}
		}
		if active == 0 && (stopping || halted) {
			return nil
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
	spend, err := computeLabelingSpend(r.db, r.job, r.book, r.day())
	if err != nil {
		return false, err
	}
	next := 0.0
	if cost := labelingRequestEstimate(r.book, r.job.Model, r.day(), request.input, request.output); cost != nil {
		next = *cost
	}
	if committed := spend.CommittedEstimate + next; committed > r.opts.MaxUSD {
		r.halt(fmt.Sprintf("job lifetime spend ceiling: known $%.4f + in flight ~$%.4f + %d unknown or failed attempts without usage ~$%.4f (counted even after a retry: they may have been charged) + next request ~$%.4f exceeds --max-usd %.4f; to continue, run again with a higher --max-usd",
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
	rows, err := queryMaps(r.db, `SELECT item_id,contract_hash FROM request_items WHERE request_id=? ORDER BY position`, request)
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
	recorded := make([]string, len(args))
	for index, arg := range args {
		recorded[index] = arg
		if arg == r.schema {
			recorded[index] = "<schema " + r.schemaPath() + ">"
		}
	}
	promptHash := hashBytes([]byte(prompt))
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
	tx, err := r.db.Begin()
	if err != nil {
		return err
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
	process := runLabelingProcess(r.cliPath, args, r.runner, prompt, events, stderr, stream, &mu, r.opts.Timeout, r.hardStop, func(pid int) {
		r.db.Exec(`UPDATE attempts SET pid=?,process_start=? WHERE id=?`, pid, nilIfEmpty(labelingProcessStart(pid)), attempt.id)
	})
	return r.finalize(attempt, stream, &process, false)
}

// labelingUsageRow is one model's usage in an attempt, priced.
type labelingUsageRow struct {
	model, source string
	tokens        map[string]int64
	cost          usageCost
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

// labelingAttemptUsage prices every model an attempt's terminal event
// reports, auxiliary models included, each at its own rates on the
// attempt's day. Claude's modelUsage lacks cache-write TTLs, so the model
// whose counts equal the result's usage takes that usage (with the TTL
// split). Codex reports no model: its usage is the requested model's.
func labelingAttemptUsage(stream *labelingStream, requested, day string, book priceBook) []labelingUsageRow {
	rows := []labelingUsageRow{}
	add := func(model, source string, usage map[string]any) {
		counts := normalizeTokenCounts(usage)
		if counts == nil {
			return
		}
		tokens := labelingCostTokens(counts)
		rows = append(rows, labelingUsageRow{model: model, source: source, tokens: tokens, cost: book.cost(model, day, tokens)})
	}
	top := normalizeTokenCounts(stream.usage)
	if stream.route == labelingRouteClaude && len(stream.modelUsage) > 0 {
		models := make([]string, 0, len(stream.modelUsage))
		for model := range stream.modelUsage {
			models = append(models, model)
		}
		sort.Strings(models)
		matched := ""
		for _, model := range models {
			counts := normalizeTokenCounts(stream.modelUsage[model])
			if matched == "" && top != nil && counts != nil && counts["input_tokens"] == top["input_tokens"] && counts["output_tokens"] == top["output_tokens"] {
				matched = model
			}
		}
		// Without an exact match, the requested model's cache writes take
		// the result's one-hour share (at most its own writes), so the TTL
		// split is not lost to the cheaper 5-minute rate.
		oneHour := top["cache_creation_1h_input_tokens"]
		for _, model := range models {
			usage := stream.modelUsage[model]
			switch {
			case model == matched:
				add(model, "result usage", stream.usage)
			case matched == "" && model == requested && oneHour > 0:
				counts := normalizeTokenCounts(usage)
				if counts == nil {
					continue
				}
				counts["cache_creation_1h_input_tokens"] = math.Min(oneHour, counts["cache_creation_input_tokens"])
				tokens := labelingCostTokens(counts)
				rows = append(rows, labelingUsageRow{model: model, source: "modelUsage with the result's 1h cache-write share", tokens: tokens, cost: book.cost(model, day, tokens)})
			default:
				add(model, "modelUsage", usage)
			}
		}
		return rows
	}
	if stream.usage == nil {
		return rows
	}
	model, source := requested, "requested model (the route reports none)"
	if len(stream.Models) > 0 {
		model, source = stream.Models[0], "reported model"
	}
	add(model, source, stream.usage)
	return rows
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
	exitedAlone := !recovered && !process.TimedOut && !process.Stopped
	switch {
	case process.StartError != "":
		outcome, errorText, notSent = requestFailed, "the CLI did not start: "+process.StartError, true
	case process.ReceiptError != "":
		// Without a durable receipt the result cannot be audited or
		// recovered: the attempt stays uncertain and the run halts.
		outcome, errorText = requestUnknown, "the saved stream is not durable: "+process.ReceiptError
	case stream.Terminal == "" && !stream.Started && exitedAlone:
		// No session event at all: the CLI stopped before sending anything.
		outcome, errorText, notSent = requestFailed, fmt.Sprintf("not_sent: the CLI exited (code %v) before starting a session", exitCode(process)), true
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
	case stream.Refusal != "":
		outcome, results = requestRefused, all(itemRefused, "refused: "+stream.Refusal)
	case stream.Failed:
		outcome, errorText = requestFailed, stream.Error
	case len(stream.Tools) > 0:
		outcome, results = requestInvalid, all(itemInvalid, "tool_use")
		problems = append(problems, "tool_use")
	default:
		answer := ""
		if r.job.Route == labelingRouteCodex {
			answer = filepath.Join(attempt.dir, "answer.json")
		}
		output := stream.output(answer)
		switch {
		case output == nil && refusalText.MatchString(stream.text):
			outcome, results = requestRefused, all(itemRefused, "refused: text instead of structured output")
		case output == nil:
			outcome, results = requestInvalid, all(itemInvalid, "no_structured_output")
			problems = append(problems, "no_structured_output")
		default:
			var requestProblems []string
			results, requestProblems = validateLabelingOutput(output, ordered, r.labels)
			problems = append(problems, requestProblems...)
			outcome = requestCompleted
			for _, result := range results {
				if result.Outcome != itemUsable && result.Outcome != itemEvidenceIssues {
					outcome = requestInvalid
				}
			}
			if len(problems) > 0 {
				outcome = requestInvalid
			}
		}
	}
	usage := labelingAttemptUsage(stream, r.job.Model, attempt.day, r.book)
	// The served model must be exactly the requested one. Codex names none,
	// so its results are usable but unverified.
	served := servedModelUnverified
	if r.job.Route == labelingRouteClaude && len(stream.Models) > 0 {
		served = servedModelVerified
	}
	differs := []string{}
	for _, model := range stream.Models {
		if model != r.job.Model {
			differs = append(differs, model)
		}
	}
	if len(differs) > 0 {
		served = servedModelDiffers
		problems = append(problems, servedModelDiffers+":"+strings.Join(differs, ","))
		if outcome == requestCompleted || outcome == requestInvalid {
			outcome = requestHeld
			for index := range results {
				if results[index].Outcome == itemUsable || results[index].Outcome == itemEvidenceIssues {
					results[index].Outcome = itemHeld
				}
				results[index].Issues = appendUnique(results[index].Issues, servedModelDiffers)
			}
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
		cleanup=?,tool_events=?,refusal=?,error=?,cli_cost_usd=?,duration_ms=?,problems=?,recovered=?,not_sent=?,served_model=?,receipt_error=? WHERE id=?`,
		outcome, now(), nilIfEmpty(stream.SessionID), jsonText(stream.Models), exit, nilIfEmpty(stream.Terminal), jsonText(cleanup), jsonText(toolEvents),
		nilIfEmpty(stream.Refusal), nilIfEmpty(errorText), cliCost, process.Duration.Milliseconds(), jsonText(problems), recovered, notSent, served,
		nilIfEmpty(process.ReceiptError), attempt.id); err != nil {
		return err
	}
	unpriced := []string{}
	for _, row := range usage {
		var cost any
		if row.cost.cost != nil {
			cost = *row.cost.cost
		}
		if row.cost.status == "partial" || row.cost.status == "unpriced" {
			unpriced = append(unpriced, row.model+" ("+row.cost.status+")")
		}
		if _, err := tx.Exec(`INSERT INTO usage(attempt_id,model,model_source,tokens,cost_usd,cost_status,priced_model,day) VALUES(?,?,?,?,?,?,?,?)`,
			attempt.id, row.model, row.source, jsonText(row.tokens), cost, row.cost.status, nilIfEmpty(row.cost.pricedModel), attempt.day); err != nil {
			return err
		}
	}
	retry := []string{}
	for _, result := range results {
		if err := insertLabelingItemResult(tx, attempt.id, r.job.Name, attempt.request, result, hashes[result.ItemID], "", served); err != nil {
			return err
		}
		if outcome != requestHeld && (result.Outcome == itemInvalid || result.Outcome == itemMissing) {
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
	if len(unpriced) > 0 && !r.opts.AllowUnpriced {
		r.halt("usage the price book cannot price: " + strings.Join(unpriced, ", ") + " (attempt " + attempt.id + "); pass --allow-unpriced to continue")
	}
	if process.ReceiptError != "" {
		r.halt("attempt " + attempt.id + ": its saved stream is not durable (" + process.ReceiptError + "); it stays unknown; fix the storage and run again")
	}
	if outcome == requestHeld {
		r.halt(fmt.Sprintf("attempt %s was answered by %s, not the requested %s; its results are held (not usable). Decide whether to accept that model before running again",
			attempt.id, strings.Join(differs, ", "), r.job.Model))
	}
	if outcome == requestFailed && labelingAccountError.MatchString(errorText+" "+strings.Join(stream.Warnings, " ")) {
		r.halt("attempt " + attempt.id + " failed with an authentication, rate-limit or quota error: " + firstRunes(errorText, 300))
	}
	if !recovered {
		r.mu.Lock()
		if outcome == requestFailed || outcome == requestUnknown {
			r.consecutiveFailures++
		} else {
			r.consecutiveFailures = 0
		}
		failures := r.consecutiveFailures
		r.mu.Unlock()
		if failures >= r.opts.MaxConsecutiveFailures {
			r.halt(fmt.Sprintf("%d consecutive attempts failed or ended unknown (last: %s); check the route (login, flags, quota) before running again",
				failures, firstRunes(errorText, 300)))
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
	if err := requireLabelingLedger(dir); err != nil {
		return err
	}
	db, err := openLabelingLedger(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.Exec(`UPDATE jobs SET stop_requested=1 WHERE name=?`, name)
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

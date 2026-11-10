package archive

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Settings → Logs collects what someone needs when a page stays blank or
// Pharos stops: errors the page saw, slow and failed requests, the service's
// and app's output by run, failed background work, and macOS crash reports.
// Each source reports whether it holds something recent worth reading, and
// /api/logs/report gathers those into one text to hand an agent.
//
// Logs live on this Mac, under <support>/logs: app.log (the app's output,
// written by the Swift app), and one directory per library holding its
// service.log (written by the broker, so it keeps the panics the service
// itself cannot) and the page and request journals (written by the service).

// diagnosticLogDirEnv tells a library's service where its logs go.
const diagnosticLogDirEnv = "PHAROS_LOG_DIR"

var (
	// slowRequest is how long a request runs before it is logged.
	slowRequest = 3 * time.Second
	// stalledRequest is how long a page read runs before it is an error.
	stalledRequest = 30 * time.Second
	// diagnosticRecent is how far back an entry marks its source for attention.
	diagnosticRecent = 24 * time.Hour
	// crashRecent is how far back crash reports are listed and flagged.
	crashRecent = 7 * 24 * time.Hour
	// crashReportDir is where macOS writes this user's crash reports.
	crashReportDir = func() string { return filepath.Join(userHome(), "Library", "Logs", "DiagnosticReports") }
	// diagnosticJournalBytes is when a journal file is rewritten from memory.
	diagnosticJournalBytes int64 = 512 << 10
	// diagnosticLogBytes is when the broker starts a new service.log, and
	// how much of each log a page reads.
	diagnosticLogBytes  int64 = 4 << 20
	diagnosticTailBytes int64 = 1 << 20
	// crashReportBytes is the most of a crash report read for its summary.
	crashReportBytes int64 = 32 << 20
)

var diagnosticJournalLimit = 300

func diagnosticLogsRoot() string { return filepath.Join(pharosSupportDir(), "logs") }

// libraryLogDir is the broker's log directory for library id.
func libraryLogDir(id string) string { return filepath.Join(diagnosticLogsRoot(), "library-"+id) }

// serviceLogDir is this service's: the broker's choice, or, for a service
// started on its own, one per catalog.
func serviceLogDir(config Config) string {
	if dir := os.Getenv(diagnosticLogDirEnv); dir != "" {
		return dir
	}
	sum := sha256.Sum256([]byte(canonical(config.CatalogPath)))
	return filepath.Join(diagnosticLogsRoot(), "catalog-"+hex.EncodeToString(sum[:6]))
}

type diagnosticEvent struct {
	// ID is the event's identity, kept when a repeat folds into it.
	ID      string `json:"id"`
	At      string `json:"at"`
	Level   string `json:"level"` // error, warning or info
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	// Page is the page the event happened on; Method and Path the request.
	Page     string `json:"page,omitempty"`
	Method   string `json:"method,omitempty"`
	Path     string `json:"path,omitempty"`
	Status   int    `json:"status,omitempty"`
	Duration int64  `json:"duration_ms,omitempty"`
	// Count is how many times it happened in a row (1 when omitted).
	Count int `json:"count,omitempty"`
}

// diagnosticJournal keeps the latest events in memory and appends each new
// one to a JSON-lines file, so they outlast a restart.
type diagnosticJournal struct {
	mu     sync.Mutex
	path   string
	loaded bool
	events []diagnosticEvent
}

func (j *diagnosticJournal) loadLocked() {
	if j.loaded {
		return
	}
	j.loaded = true
	if j.path == "" {
		return
	}
	data, err := readTail(j.path, diagnosticJournalBytes)
	if err != nil {
		return
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		var event diagnosticEvent
		if json.Unmarshal(line, &event) == nil && event.At != "" {
			if event.ID == "" {
				sum := sha256.Sum256(line)
				event.ID = hex.EncodeToString(sum[:8])
			}
			j.events = append(j.events, event)
		}
	}
	if len(j.events) > diagnosticJournalLimit {
		j.events = j.events[len(j.events)-diagnosticJournalLimit:]
	}
}

// add records event, folding it into the previous one when it repeats
// within a minute (a failing render can report on every frame).
func (j *diagnosticJournal) add(event diagnosticEvent) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.loadLocked()
	if n := len(j.events); n > 0 {
		last := &j.events[n-1]
		if last.Kind == event.Kind && last.Message == event.Message && last.Path == event.Path && last.Page == event.Page && withinMinute(last.At, event.At) {
			last.Count = max(last.Count, 1) + 1
			last.At = event.At
			return
		}
	}
	event.ID = newDiagnosticID()
	j.events = append(j.events, event)
	if len(j.events) > diagnosticJournalLimit {
		j.events = slices.Clone(j.events[len(j.events)-diagnosticJournalLimit:])
	}
	if j.path == "" {
		return
	}
	if info, err := os.Stat(j.path); err == nil && info.Size() > diagnosticJournalBytes {
		j.rewriteLocked()
		return
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(j.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	line, _ := json.Marshal(event)
	_, _ = file.Write(append(line, '\n'))
	_ = file.Close()
}

func (j *diagnosticJournal) rewriteLocked() {
	var buffer bytes.Buffer
	for _, event := range j.events {
		line, _ := json.Marshal(event)
		buffer.Write(append(line, '\n'))
	}
	temporary := j.path + ".tmp"
	if os.WriteFile(temporary, buffer.Bytes(), 0o600) == nil {
		_ = os.Rename(temporary, j.path)
	}
}

// list returns the events, newest first.
func (j *diagnosticJournal) list() []diagnosticEvent {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.loadLocked()
	events := slices.Clone(j.events)
	slices.Reverse(events)
	return events
}

func newDiagnosticID() string {
	var id [8]byte
	_, _ = rand.Read(id[:])
	return hex.EncodeToString(id[:])
}

func withinMinute(previous, next string) bool {
	a, errA := time.Parse(timeLayout, previous)
	b, errB := time.Parse(timeLayout, next)
	return errA == nil && errB == nil && b.Sub(a) < time.Minute
}

// serviceDiagnostics is a service's page and request journals and the
// requests it is answering now.
type serviceDiagnostics struct {
	once     sync.Once
	dir      string
	page     diagnosticJournal
	requests diagnosticJournal
	mu       sync.Mutex
	next     uint64
	running  map[uint64]*requestTrace
	crashes  crashCache
	// attention is the menu bar's cached answer, at most attentionAge old.
	attentionMu sync.Mutex
	attentionAt time.Time
	attention   map[string]string
}

const attentionAge = 30 * time.Second

func (s *Server) diagnosticsState() *serviceDiagnostics {
	d := &s.diagnostics
	d.once.Do(func() {
		d.dir = serviceLogDir(s.Config())
		d.page.path = filepath.Join(d.dir, "page-errors.jsonl")
		d.requests.path = filepath.Join(d.dir, "requests.jsonl")
		d.running = map[uint64]*requestTrace{}
	})
	return d
}

// requestTrace times one request and keeps its status, and the start of an
// error response's body, for the request journal.
type requestTrace struct {
	http.ResponseWriter
	id     uint64
	method string
	// read is a request a page waits on to show anything (diagnosticRead).
	read    bool
	path    string
	started time.Time
	status  int
	// stream is read by runningRequests from other requests.
	stream atomic.Bool
	body   []byte
	panic  string
	// timing is where the request's time went (request_timing.go); ctx
	// carries it to the request's writes, statements and derived values.
	timing *requestTiming
	ctx    context.Context
}

func (t *requestTrace) WriteHeader(status int) {
	if t.status == 0 {
		t.status = status
		t.stream.Store(strings.HasPrefix(t.Header().Get("Content-Type"), "text/event-stream"))
		if t.timing != nil {
			t.timing.stream.Store(t.stream.Load())
		}
	}
	t.ResponseWriter.WriteHeader(status)
}

func (t *requestTrace) Write(data []byte) (int, error) {
	if t.status == 0 {
		t.WriteHeader(http.StatusOK)
	}
	if t.status >= 500 && len(t.body) < 600 {
		t.body = append(t.body, data[:min(len(data), 600-len(t.body))]...)
	}
	return t.ResponseWriter.Write(data)
}

func (t *requestTrace) Flush() {
	if flusher, ok := t.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (t *requestTrace) Unwrap() http.ResponseWriter { return t.ResponseWriter }

// traceRequest starts timing r on the goroutine that answers it. The log
// pages' own reads are not traced.
func (s *Server) traceRequest(w http.ResponseWriter, r *http.Request) (*requestTrace, func()) {
	if strings.HasPrefix(r.URL.Path, "/api/logs/") {
		return nil, func() {}
	}
	d := s.diagnosticsState()
	path := r.URL.Path
	if r.URL.RawQuery != "" {
		path += "?" + truncateText(r.URL.RawQuery, 300)
	}
	trace := &requestTrace{ResponseWriter: w, method: r.Method, path: path, read: diagnosticRead(r), started: time.Now()}
	// The line in the service's log names the route alone: a query string
	// can hold what someone searched for.
	trace.ctx, trace.timing = beginRequestTiming(r.Context(), s.Catalog, r.Method+" "+r.URL.Path, interactiveRead(r) || r.URL.Path == mcpRPCPath)
	d.mu.Lock()
	d.next++
	trace.id = d.next
	d.running[trace.id] = trace
	d.mu.Unlock()
	return trace, func() {
		d.mu.Lock()
		delete(d.running, trace.id)
		d.mu.Unlock()
		trace.timing.finish()
		elapsed := time.Since(trace.started)
		trace.timing.logSlowRequest(elapsed, trace.status, r.Context().Err() != nil)
		if event, ok := requestEvent(trace, elapsed); ok {
			if event.Kind == "slow" {
				event.Detail = trace.timing.breakdown(elapsed)
			}
			d.requests.add(event)
		}
	}
}

// diagnosticRead reports whether a page waits on r to show anything: a GET,
// or a POST that only reads, such as Query Table's queries and text search
// (interactiveRead). page-errors.js's pageRequestIsRead mirrors it.
func diagnosticRead(r *http.Request) bool {
	return r.Method == http.MethodGet || r.Method == http.MethodHead || interactiveRead(r)
}

// requestEvent is what the journal records for a finished request, if
// anything: a panic, an error status, or a slow answer. A slow page read
// can leave a page blank; a slow action (a sync, an export) is expected.
func requestEvent(t *requestTrace, elapsed time.Duration) (diagnosticEvent, bool) {
	event := diagnosticEvent{At: formatTime(t.started.Add(elapsed)), Method: t.method, Path: t.path, Status: t.status, Duration: elapsed.Milliseconds()}
	read := t.read
	switch {
	case t.panic != "":
		event.Level, event.Kind, event.Message = "error", "panic", fmt.Sprintf("%s %s stopped on an internal error", t.method, t.path)
		event.Detail = t.panic
	case t.status >= 500:
		event.Level, event.Kind, event.Message = "error", "failed", fmt.Sprintf("%s %s returned %d", t.method, t.path, t.status)
		event.Detail = strings.TrimSpace(strings.ToValidUTF8(string(t.body), ""))
	case t.stream.Load() || elapsed < slowRequest:
		return event, false
	case read && elapsed >= stalledRequest:
		event.Level, event.Kind, event.Message = "error", "slow", fmt.Sprintf("%s %s took %s", t.method, t.path, elapsedText(elapsed))
	case read:
		event.Level, event.Kind, event.Message = "warning", "slow", fmt.Sprintf("%s %s took %s", t.method, t.path, elapsedText(elapsed))
	default:
		event.Level, event.Kind, event.Message = "info", "slow", fmt.Sprintf("%s %s took %s", t.method, t.path, elapsedText(elapsed))
	}
	return event, true
}

func elapsedText(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1f s", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// runningRequests lists the requests answering for at least a second,
// longest first; streams are open by design.
func (s *Server) runningRequests(at time.Time) []map[string]any {
	d := s.diagnosticsState()
	d.mu.Lock()
	defer d.mu.Unlock()
	traces := []*requestTrace{}
	for _, trace := range d.running {
		if !trace.stream.Load() && at.Sub(trace.started) >= time.Second {
			traces = append(traces, trace)
		}
	}
	slices.SortFunc(traces, func(a, b *requestTrace) int { return a.started.Compare(b.started) })
	rows := []map[string]any{}
	for _, trace := range traces {
		rows = append(rows, map[string]any{"method": trace.method, "path": trace.path, "read": trace.read, "started_at": formatTime(trace.started), "duration_ms": at.Sub(trace.started).Milliseconds()})
	}
	return rows
}

// pageEvent kinds the page may report, and their levels.
var pageEventLevels = map[string]string{"error": "error", "rejection": "error", "render": "error", "webkit": "error", "resource": "warning", "request": "warning", "stalled": "warning"}

// recordPageEvents answers POST /api/logs/page: up to 20 events.
func (s *Server) recordPageEvents(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Events []struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
			Detail  string `json:"detail"`
			Page    string `json:"page"`
			Path    string `json:"path"`
			Status  int    `json:"status"`
			Elapsed int64  `json:"duration_ms"`
		} `json:"events"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&body); err != nil {
		writeError(w, fmt.Errorf("expected {\"events\": [...]}: %w", err), http.StatusBadRequest)
		return
	}
	d := s.diagnosticsState()
	recorded := 0
	for _, event := range body.Events[:min(len(body.Events), 20)] {
		level, ok := pageEventLevels[event.Kind]
		if !ok || strings.TrimSpace(event.Message) == "" {
			continue
		}
		d.page.add(diagnosticEvent{At: formatTime(time.Now()), Level: level, Kind: event.Kind, Message: truncateText(event.Message, 2000),
			Detail: truncateText(event.Detail, 8000), Page: truncateText(event.Page, 500), Path: truncateText(event.Path, 500), Status: event.Status, Duration: max(event.Elapsed, 0)})
		recorded++
	}
	writeJSON(w, map[string]any{"recorded": recorded}, http.StatusOK)
}

func truncateText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "")
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// readTail reads at most limit bytes from the end of path, from a line start.
func readTail(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	offset := max(info.Size()-limit, 0)
	data := make([]byte, info.Size()-offset)
	if _, err := file.ReadAt(data, offset); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if offset > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	return data, nil
}

// Output logs mark each run with a line the reader splits on. The broker
// writes a service's; the Swift app writes its own start line.
var (
	logRunStart = regexp.MustCompile(`^=== Pharos (service|app) started (\S+)(.*) ===$`)
	logRunEnd   = regexp.MustCompile(`^=== Pharos service exited (\S+) \(pid \d+\): (.*) ===$`)
	// logProblem marks lines worth counting; logCrash, those that end a Go
	// process (a panic, a runtime fault, a memory fault exit).
	logProblem = regexp.MustCompile(`(?i)\b(error|errors|failed|failure|panic|fatal|could not|cannot)\b`)
	logCrash   = regexp.MustCompile(`^(panic: |fatal error: |unexpected fault address|SIGSEGV|SIGBUS)`)
)

type outputRun struct {
	Started  string   `json:"started_at,omitempty"`
	Ended    string   `json:"ended_at,omitempty"`
	Exit     string   `json:"exit,omitempty"`
	Running  bool     `json:"running"`
	Crashed  bool     `json:"crashed"`
	Problems int      `json:"problems"`
	Crash    string   `json:"crash,omitempty"`
	Lines    []string `json:"lines"`
	Omitted  int      `json:"omitted,omitempty"`
}

// outputRunLines is how many lines of each run a page shows.
const outputRunLines = 400

// parseOutputRuns splits a log into its runs, newest first. The last run is
// the current one when current is true; any earlier run without an exit
// line ended with the process that wrote it (the broker stopped too).
func parseOutputRuns(data []byte, current bool) []outputRun {
	runs := []outputRun{}
	var run *outputRun
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := strings.TrimRight(strings.ToValidUTF8(scanner.Text(), "�"), "\r")
		if match := logRunStart.FindStringSubmatch(line); match != nil {
			// A log that moved to its previous file starts again with the
			// same line; the run continues.
			if run != nil && run.Started == match[2] && run.Ended == "" {
				continue
			}
			runs = append(runs, outputRun{Started: match[2], Lines: []string{}})
			run = &runs[len(runs)-1]
			continue
		}
		if run == nil {
			runs = append(runs, outputRun{Lines: []string{}})
			run = &runs[len(runs)-1]
		}
		if match := logRunEnd.FindStringSubmatch(line); match != nil {
			run.Ended, run.Exit = match[1], match[2]
			continue
		}
		if logCrash.MatchString(line) && run.Crash == "" {
			run.Crash = line
		}
		if logProblem.MatchString(line) {
			run.Problems++
		}
		run.Lines = append(run.Lines, line)
	}
	for i := range runs {
		run := &runs[i]
		last := i == len(runs)-1
		run.Running = last && current && run.Ended == ""
		cleanExit := run.Exit == "" || run.Exit == "exit status 0" || run.Exit == "signal: terminated" || run.Exit == "signal: interrupt"
		run.Crashed = run.Crash != "" || !cleanExit
		if run.Crash == "" && !cleanExit {
			run.Crash = "Exited: " + run.Exit
		}
		if len(run.Lines) > outputRunLines {
			run.Omitted = len(run.Lines) - outputRunLines
			run.Lines = run.Lines[run.Omitted:]
		}
	}
	slices.Reverse(runs)
	return runs
}

// readOutputLog reads the end of path, after the end of its previous file.
func readOutputLog(path string) []byte {
	current, _ := readTail(path, diagnosticTailBytes)
	if rest := diagnosticTailBytes - int64(len(current)); rest > 4096 {
		if previous, err := readTail(previousLogPath(path), rest); err == nil {
			return append(previous, current...)
		}
	}
	return current
}

func previousLogPath(path string) string {
	return strings.TrimSuffix(path, ".log") + ".previous.log"
}

// runLog is a run's output file. Past diagnosticLogBytes it moves to its
// previous file and starts again with the run's start line, which
// parseOutputRuns joins to the run it continues, so a noisy run stays
// bounded too. macos/LibraryVolume.swift's RunLogFile does the same for
// app.log.
type runLog struct {
	mu    sync.Mutex
	path  string
	start string
	file  *os.File
	size  int64
}

// openServiceLog starts a run in library id's service.log. It returns nil
// when the log cannot be written; the service still runs.
func openServiceLog(id string) (*runLog, string) {
	dir := libraryLogDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, dir
	}
	log := &runLog{path: filepath.Join(dir, "service.log"), start: fmt.Sprintf("=== Pharos service started %s ===\n", formatTime(time.Now()))}
	if !log.openLocked() {
		return nil, dir
	}
	return log, dir
}

func (l *runLog) openLocked() bool {
	if info, err := os.Stat(l.path); err == nil && info.Size() > diagnosticLogBytes {
		_ = os.Rename(l.path, previousLogPath(l.path))
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	l.file, l.size = file, 0
	if info, err := file.Stat(); err == nil {
		l.size = info.Size()
	}
	// It could not be moved aside (the folder is read-only): start it over
	// rather than grow it, or reopen it on every write.
	if l.size > diagnosticLogBytes && file.Truncate(0) == nil {
		l.size = 0
	}
	n, _ := io.WriteString(file, l.start)
	l.size += int64(n)
	return true
}

func (l *runLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return len(data), nil
	}
	n, err := l.file.Write(data)
	l.size += int64(n)
	if l.size > diagnosticLogBytes {
		_ = l.file.Close()
		l.file = nil
		l.openLocked()
	}
	return n, err
}

// closeServiceLog ends the run with how the process exited.
func closeServiceLog(log *runLog, pid int, err error) {
	if log == nil {
		return
	}
	exit := "exit status 0"
	if err != nil {
		exit = err.Error()
	}
	fmt.Fprintf(log, "=== Pharos service exited %s (pid %d): %s ===\n", formatTime(time.Now()), pid, exit)
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.file != nil {
		_ = log.file.Close()
		log.file = nil
	}
}

// quietWriter passes writes on and never fails: the app may have closed the
// broker's stderr, and that must not stop a service's log file.
type quietWriter struct{ io.Writer }

func (q quietWriter) Write(data []byte) (int, error) {
	_, _ = q.Writer.Write(data)
	return len(data), nil
}

// A crash report macOS wrote for a Pharos process, or for a WebKit process
// the app started.
type crashReport struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	At       string   `json:"at"`
	Process  string   `json:"process"`
	Kind     string   `json:"kind"` // crash or resource
	Summary  string   `json:"summary"`
	Frames   []string `json:"frames,omitempty"`
	modified time.Time
}

type crashCache struct {
	mu      sync.Mutex
	at      time.Time
	reports []crashReport
}

// pharosCrashNames are the processes whose reports are listed by name.
var pharosCrashNames = regexp.MustCompile(`^(?i)(pharos|pharos-labeler-ort)[-_.]`)

// webKitCrash is a WebKit helper's report; it is Pharos's when the app
// started it.
var (
	webKitCrash     = regexp.MustCompile(`(?i)com\.apple\.WebKit\.`)
	pharosCoalition = regexp.MustCompile(`"(coalitionName|responsibleProc)"\s*:\s*"(local\.pharos|Pharos)"`)
)

// crashReports lists the recent reports, newest first, reading the folder
// at most once a minute.
func (s *Server) crashReports(at time.Time) []crashReport {
	cache := &s.diagnosticsState().crashes
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.reports != nil && at.Sub(cache.at) < time.Minute {
		return cache.reports
	}
	cache.at, cache.reports = at, scanCrashReports(crashReportDir(), at)
	return cache.reports
}

func scanCrashReports(dir string, at time.Time) []crashReport {
	reports := []crashReport{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return reports
	}
	for _, entry := range entries {
		name := entry.Name()
		extension := filepath.Ext(name)
		if entry.IsDir() || (extension != ".ips" && extension != ".crash" && extension != ".diag") {
			continue
		}
		pharos, webKit := pharosCrashNames.MatchString(name), webKitCrash.MatchString(name)
		if !pharos && !webKit {
			continue
		}
		info, err := entry.Info()
		if err != nil || at.Sub(info.ModTime()) > crashRecent {
			continue
		}
		path := filepath.Join(dir, name)
		head, err := readHead(path, 256<<10)
		if err != nil || (webKit && !pharosCoalition.Match(head)) {
			continue
		}
		// The summary needs the whole report: its body is one JSON document,
		// and a process with many threads writes megabytes of it.
		if info.Size() > int64(len(head)) {
			if whole, err := readHead(path, crashReportBytes); err == nil {
				head = whole
			}
		}
		report := summarizeCrashReport(name, head)
		report.Path, report.modified, report.At = path, info.ModTime(), formatTime(info.ModTime())
		reports = append(reports, report)
	}
	slices.SortFunc(reports, func(a, b crashReport) int { return b.modified.Compare(a.modified) })
	return reports
}

func readHead(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, limit))
}

// summarizeCrashReport reads an .ips report's two JSON documents (a header
// line, then the body) for its exception and the crashed thread's frames.
// A .diag (resource) report or older text report keeps its first lines.
func summarizeCrashReport(name string, data []byte) crashReport {
	report := crashReport{Name: name, Kind: "crash", Process: strings.SplitN(strings.TrimSuffix(name, filepath.Ext(name)), "-20", 2)[0]}
	if filepath.Ext(name) == ".diag" || strings.Contains(name, "_resource") {
		report.Kind = "resource"
	}
	header, body, _ := bytes.Cut(data, []byte("\n"))
	var top struct {
		App     string `json:"app_name"`
		BugType string `json:"bug_type"`
	}
	var detail struct {
		ProcName  string `json:"procName"`
		Exception struct {
			Type    string `json:"type"`
			Signal  string `json:"signal"`
			Subtype string `json:"subtype"`
		} `json:"exception"`
		Termination struct {
			Indicator string `json:"indicator"`
		} `json:"termination"`
		ASI            map[string][]string `json:"asi"`
		FaultingThread int                 `json:"faultingThread"`
		Threads        []struct {
			Frames []struct {
				ImageIndex int    `json:"imageIndex"`
				Symbol     string `json:"symbol"`
			} `json:"frames"`
		} `json:"threads"`
		UsedImages []struct {
			Name string `json:"name"`
		} `json:"usedImages"`
	}
	if json.Unmarshal(header, &top) == nil && json.Unmarshal(body, &detail) == nil {
		report.Process = defaultString(detail.ProcName, defaultString(top.App, report.Process))
		parts := []string{}
		for _, part := range []string{detail.Exception.Type, detail.Exception.Signal, detail.Exception.Subtype, detail.Termination.Indicator} {
			if part != "" && !slices.Contains(parts, part) {
				parts = append(parts, part)
			}
		}
		for _, messages := range detail.ASI {
			parts = append(parts, messages...)
		}
		report.Summary = strings.Join(parts, " · ")
		if detail.FaultingThread >= 0 && detail.FaultingThread < len(detail.Threads) {
			for _, frame := range detail.Threads[detail.FaultingThread].Frames[:min(len(detail.Threads[detail.FaultingThread].Frames), 12)] {
				image := ""
				if frame.ImageIndex >= 0 && frame.ImageIndex < len(detail.UsedImages) {
					image = detail.UsedImages[frame.ImageIndex].Name
				}
				report.Frames = append(report.Frames, strings.TrimSpace(image+" "+defaultString(frame.Symbol, "???")))
			}
		}
		return report
	}
	lines := strings.Split(strings.ToValidUTF8(string(data[:min(len(data), 4096)]), ""), "\n")
	report.Summary = strings.TrimSpace(strings.Join(lines[:min(len(lines), 6)], " "))
	return report
}

// problemOperations are this Mac's failed and interrupted operations since
// since, newest first.
func (c *Catalog) problemOperations(ctx context.Context, since time.Time, limit int) ([]journalRow, error) {
	rows, err := c.reader().QueryContext(ctx, "SELECT "+journalColumns+" FROM operation_journal WHERE host_id=? AND started_at>=? AND state IN (?, ?) ORDER BY started_at DESC LIMIT ?",
		currentHost().ID, formatTime(since), opFailed, opInterrupted, limit)
	if err != nil {
		return nil, err
	}
	return scanJournalRows(rows)
}

// The sources, in the order the page lists them.
var diagnosticSources = []struct{ ID, Title string }{
	{"page", "Page errors"}, {"requests", "Slow & failed requests"}, {"service", "Service output"},
	{"app", "App output"}, {"background", "Background work"}, {"crashes", "Crash reports"},
}

type sourceSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Attention string `json:"attention,omitempty"` // error or warning
	Count     int    `json:"count"`
	Latest    string `json:"latest_at,omitempty"`
	Headline  string `json:"headline,omitempty"`
	// Files are the logs on disk an agent on this Mac can read in full.
	Files []string `json:"files,omitempty"`
}

// diagnosticDocument is one source's entries, and its summary.
type diagnosticDocument struct {
	Summary sourceSummary     `json:"summary"`
	Events  []diagnosticEvent `json:"events,omitempty"`
	Running []map[string]any  `json:"running,omitempty"`
	Runs    []outputRun       `json:"runs,omitempty"`
	Work    []map[string]any  `json:"operations,omitempty"`
	Crashes []crashReport     `json:"reports,omitempty"`
}

func (s *Server) diagnosticSource(ctx context.Context, id string, at time.Time) (diagnosticDocument, error) {
	d := s.diagnosticsState()
	title := ""
	for _, source := range diagnosticSources {
		if source.ID == id {
			title = source.Title
		}
	}
	if title == "" {
		return diagnosticDocument{}, fmt.Errorf("unknown log source %q", id)
	}
	doc := diagnosticDocument{Summary: sourceSummary{ID: id, Title: title}}
	summary := &doc.Summary
	switch id {
	case "page", "requests":
		journal := &d.page
		if id == "requests" {
			journal = &d.requests
			doc.Running = s.runningRequests(at)
		}
		doc.Events = journal.list()
		summary.Files = []string{journal.path}
		summarizeEvents(summary, doc.Events, at)
		// Actions stay listed; only a read that has not answered can leave
		// a page blank (as requestEvent judges finished requests).
		for _, running := range doc.Running {
			elapsed := time.Duration(running["duration_ms"].(int64)) * time.Millisecond
			if running["read"] == true && elapsed >= slowRequest {
				level := map[bool]string{true: "error", false: "warning"}[elapsed >= stalledRequest]
				raiseAttention(summary, level)
				summary.Headline = fmt.Sprintf("%s %s has been running for %s", running["method"], running["path"], elapsedText(elapsed))
			}
		}
	case "service", "app":
		path := filepath.Join(d.dir, "service.log")
		if id == "app" {
			path = filepath.Join(diagnosticLogsRoot(), "app.log")
		}
		summary.Files = []string{path}
		if _, err := os.Stat(previousLogPath(path)); err == nil {
			summary.Files = append(summary.Files, previousLogPath(path))
		}
		doc.Runs = parseOutputRuns(readOutputLog(path), true)
		for i, run := range doc.Runs {
			// A run is recent by when it ended: a crash after a long run is
			// news. One without an exit line ended before the next began.
			last := run.Ended
			if last == "" && i > 0 {
				last = doc.Runs[i-1].Started
			}
			ended, _ := time.Parse(timeLayout, defaultString(last, run.Started))
			recent := run.Running || (!ended.IsZero() && at.Sub(ended) < diagnosticRecent)
			summary.Count += run.Problems
			if summary.Latest == "" {
				summary.Latest = run.Started
			}
			if !recent {
				continue
			}
			if run.Crashed && id == "service" {
				raiseAttention(summary, "error")
				if summary.Headline == "" {
					summary.Headline = "A run ended in a crash: " + run.Crash
				}
			} else if run.Problems > 0 {
				raiseAttention(summary, "warning")
				if summary.Headline == "" {
					summary.Headline = fmt.Sprintf("%d lines mention an error", run.Problems)
				}
			}
		}
		if len(doc.Runs) == 0 {
			summary.Headline = "No output recorded yet."
			if id == "service" && os.Getenv(diagnosticLogDirEnv) == "" {
				summary.Headline = "This service was started on its own; only the Pharos app records its output."
			}
		}
	case "background":
		rows, err := s.Catalog.problemOperations(ctx, at.Add(-crashRecent), 50)
		if err != nil {
			return doc, err
		}
		doc.Work = []map[string]any{}
		for _, row := range rows {
			label := row.Kind
			if kind, ok := lookupOperationKind(row.Kind); ok {
				label = kind.Label
			}
			message := defaultString(row.Error, reasonSentence(row.Reason))
			doc.Work = append(doc.Work, map[string]any{"id": row.ID, "kind": row.Kind, "label": label, "state": row.State, "reason": reasonSentence(row.Reason),
				"error": row.Error, "step": row.StepName, "started_at": row.StartedAt, "ended_at": row.EndedAt, "message": message})
			started, _ := time.Parse(timeLayout, row.StartedAt)
			summary.Count++
			if summary.Latest == "" {
				summary.Latest = row.StartedAt
			}
			if at.Sub(started) < diagnosticRecent {
				level := "warning"
				if row.State == opFailed || row.Reason == "panic" {
					level = "error"
				}
				raiseAttention(summary, level)
				if summary.Headline == "" {
					summary.Headline = label + ": " + message
				}
			}
		}
	case "crashes":
		doc.Crashes = s.crashReports(at)
		summary.Files = []string{crashReportDir()}
		summary.Count = len(doc.Crashes)
		for _, report := range doc.Crashes {
			if summary.Latest == "" {
				summary.Latest = report.At
				summary.Headline = report.Process + ": " + defaultString(report.Summary, report.Name)
			}
			raiseAttention(summary, map[string]string{"crash": "error", "resource": "warning"}[report.Kind])
		}
	}
	return doc, nil
}

func summarizeEvents(summary *sourceSummary, events []diagnosticEvent, at time.Time) {
	for _, event := range events {
		summary.Count += max(event.Count, 1)
		if summary.Latest == "" {
			summary.Latest = event.At
		}
		when, _ := time.Parse(timeLayout, event.At)
		if event.Level == "info" || at.Sub(when) >= diagnosticRecent {
			continue
		}
		if raiseAttention(summary, event.Level) || summary.Headline == "" {
			summary.Headline = event.Message
		}
	}
}

// raiseAttention sets the summary's attention to level when that is more
// severe, and reports whether it did.
func raiseAttention(summary *sourceSummary, level string) bool {
	rank := map[string]int{"": 0, "warning": 1, "error": 2}
	if rank[level] > rank[summary.Attention] {
		summary.Attention = level
		return true
	}
	return false
}

func (s *Server) diagnosticSummaries(ctx context.Context, at time.Time) []sourceSummary {
	summaries := []sourceSummary{}
	for _, source := range diagnosticSources {
		doc, err := s.diagnosticSource(ctx, source.ID, at)
		if err != nil {
			doc.Summary = sourceSummary{ID: source.ID, Title: source.Title, Attention: "warning", Headline: "Could not read: " + err.Error()}
		}
		summaries = append(summaries, doc.Summary)
	}
	return summaries
}

// diagnosticReport is the text the page copies for an agent: every source
// that needs attention, with its recent entries and where its full logs are.
func (s *Server) diagnosticReport(ctx context.Context, at time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Pharos diagnostic report\n\nGenerated %s on %s/%s. Executable: %s\n", formatTime(at), runtime.GOOS, runtime.GOARCH, executablePath())
	fmt.Fprintf(&b, "Logs folder: %s\n", diagnosticLogsRoot())
	b.WriteString("\nThe full logs are plain files on this Mac; read them for more context than this summary holds.\n")
	listed := false
	for _, source := range diagnosticSources {
		doc, err := s.diagnosticSource(ctx, source.ID, at)
		if err != nil || doc.Summary.Attention == "" {
			continue
		}
		listed = true
		summary := doc.Summary
		fmt.Fprintf(&b, "\n## %s (%s)\n\n%s\n", summary.Title, summary.Attention, summary.Headline)
		for _, file := range summary.Files {
			fmt.Fprintf(&b, "- File: %s\n", file)
		}
		for _, running := range doc.Running {
			fmt.Fprintf(&b, "- Still running: %s %s for %d ms\n", running["method"], running["path"], running["duration_ms"])
		}
		// Limit what is worth reading, not what was recorded: newer
		// informational entries must not crowd out the error behind the mark.
		events := slices.DeleteFunc(slices.Clone(doc.Events), func(event diagnosticEvent) bool { return event.Level == "info" })
		for _, event := range events[:min(len(events), 15)] {
			fmt.Fprintf(&b, "- %s [%s/%s] %s", event.At, event.Level, event.Kind, event.Message)
			if event.Count > 1 {
				fmt.Fprintf(&b, " (×%d)", event.Count)
			}
			if event.Page != "" {
				fmt.Fprintf(&b, " on %s", event.Page)
			}
			b.WriteString("\n")
			if event.Detail != "" {
				fmt.Fprintf(&b, "\n```\n%s\n```\n\n", truncateText(event.Detail, 3000))
			}
		}
		runs := slices.DeleteFunc(slices.Clone(doc.Runs), func(run outputRun) bool { return !run.Crashed && run.Problems == 0 })
		for _, run := range runs[:min(len(runs), 3)] {
			state := "running"
			if !run.Running {
				state = defaultString(run.Exit, "ended without an exit line")
			}
			fmt.Fprintf(&b, "\n### Run started %s (%s)\n", defaultString(run.Started, "earlier"), state)
			if run.Crash != "" {
				fmt.Fprintf(&b, "Crash: %s\n", run.Crash)
			}
			lines := run.Lines[max(len(run.Lines)-80, 0):]
			fmt.Fprintf(&b, "\n```\n%s\n```\n", strings.Join(lines, "\n"))
		}
		for _, work := range doc.Work[:min(len(doc.Work), 10)] {
			fmt.Fprintf(&b, "- %s %s (%s): %s", work["started_at"], work["label"], work["state"], work["message"])
			if step, _ := work["step"].(string); step != "" {
				fmt.Fprintf(&b, " during %q", step)
			}
			b.WriteString("\n")
		}
		for _, report := range doc.Crashes[:min(len(doc.Crashes), 5)] {
			fmt.Fprintf(&b, "- %s %s (%s): %s\n  File: %s\n", report.At, report.Process, report.Kind, report.Summary, report.Path)
			for _, frame := range report.Frames {
				fmt.Fprintf(&b, "    %s\n", frame)
			}
		}
	}
	if !listed {
		b.WriteString("\nNo source has recent errors, slow requests or crashes.\n")
	}
	return b.String()
}

// logsHTTP answers /api/logs/*.
func (s *Server) logsHTTP(w http.ResponseWriter, r *http.Request) {
	at := time.Now()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/logs/page":
		s.recordPageEvents(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/logs/summary":
		writeJSON(w, map[string]any{"generated_at": formatTime(at), "folder": diagnosticLogsRoot(), "sources": s.diagnosticSummaries(r.Context(), at)}, http.StatusOK)
	case r.Method == http.MethodGet && r.URL.Path == "/api/logs/source":
		doc, err := s.diagnosticSource(r.Context(), r.URL.Query().Get("id"), at)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, doc, http.StatusOK)
	case r.Method == http.MethodGet && r.URL.Path == "/api/logs/report":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, s.diagnosticReport(r.Context(), at))
	case r.Method == http.MethodGet && r.URL.Path == "/api/logs/crash":
		// Only a listed report can be read, by name.
		name := r.URL.Query().Get("name")
		for _, report := range s.crashReports(at) {
			if report.Name == name {
				data, err := readHead(report.Path, 2<<20)
				if err != nil {
					writeError(w, err, http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				_, _ = w.Write(data)
				return
			}
		}
		writeError(w, fmt.Errorf("no recent crash report named %q", name), http.StatusNotFound)
	default:
		writeJSON(w, map[string]any{"error": "not found"}, http.StatusNotFound)
	}
}

// logsAttention is the menu bar's view: the sources that need attention.
func (s *Server) logsAttention(ctx context.Context) map[string]string {
	d := s.diagnosticsState()
	d.attentionMu.Lock()
	defer d.attentionMu.Unlock()
	if d.attention != nil && time.Since(d.attentionAt) < attentionAge {
		return d.attention
	}
	attention := map[string]string{}
	for _, summary := range s.diagnosticSummaries(ctx, time.Now()) {
		if summary.Attention != "" {
			attention[summary.ID] = summary.Attention
		}
	}
	d.attentionAt, d.attention = time.Now(), attention
	return attention
}

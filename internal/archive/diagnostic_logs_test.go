package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticJournalFoldsRepeatsAndOutlastsARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page-errors.jsonl")
	journal := &diagnosticJournal{path: path}
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	add := func(offset time.Duration, message string) {
		journal.add(diagnosticEvent{At: formatTime(at.Add(offset)), Level: "error", Kind: "error", Message: message})
	}
	add(0, "first")
	add(time.Second, "boom")
	add(2*time.Second, "boom")
	add(2*time.Minute, "boom")  // a minute later is a new event
	add(2*time.Minute, "other") // same instant, distinct event
	events := journal.list()
	if len(events) != 4 || events[1].Message != "boom" || events[2].Count != 2 || events[3].Message != "first" {
		t.Fatalf("events = %+v, want newest first with the repeat folded", events)
	}
	ids := map[string]bool{}
	for _, event := range events {
		ids[event.ID] = true
	}
	if len(ids) != 4 || ids[""] {
		t.Errorf("events share or lack identities: %+v", events)
	}
	reopened := (&diagnosticJournal{path: path}).list()
	// The file keeps each new event once; a fold changes only memory.
	if len(reopened) != 4 || reopened[2].Message != "boom" || reopened[3].Message != "first" {
		t.Fatalf("after a restart = %+v", reopened)
	}
	// A folded event keeps the identity a page's disclosure uses.
	if reopened[2].ID != events[2].ID || reopened[0].ID != events[0].ID {
		t.Errorf("identities changed across a restart: %+v, then %+v", events, reopened)
	}
}

func TestDiagnosticJournalStaysBounded(t *testing.T) {
	override(t, &diagnosticJournalBytes, 2048)
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	journal := &diagnosticJournal{path: path}
	for i := range diagnosticJournalLimit + 50 {
		journal.add(diagnosticEvent{At: formatTime(time.Unix(int64(i)*120, 0)), Level: "warning", Kind: "slow", Message: fmt.Sprint("request ", i)})
	}
	if events := journal.list(); len(events) != diagnosticJournalLimit || events[0].Message != fmt.Sprint("request ", diagnosticJournalLimit+49) {
		t.Fatalf("kept %d events, newest %q", len(events), events[0].Message)
	}
	if info, err := os.Stat(path); err != nil || info.Size() > int64(diagnosticJournalLimit)*400 {
		t.Fatalf("journal file = %v, %v; want it rewritten from memory", info, err)
	}
}

func TestRequestEventsKeepPanicsErrorsAndSlowReads(t *testing.T) {
	started := time.Now()
	trace := func(method string, status int) *requestTrace {
		return &requestTrace{method: method, path: "/api/x", read: method == "GET", started: started, status: status}
	}
	query := &requestTrace{method: "POST", path: "/api/query/usage/rows", read: true, started: started, status: 200}
	if _, logged := requestEvent(trace("GET", 200), time.Second); logged {
		t.Error("a quick answer was logged")
	}
	cases := []struct {
		trace   *requestTrace
		elapsed time.Duration
		level   string
		kind    string
	}{
		{trace("GET", 200), 4 * time.Second, "warning", "slow"},
		{trace("GET", 200), time.Minute, "error", "slow"},
		{trace("POST", 200), time.Minute, "info", "slow"},
		{query, 4 * time.Second, "warning", "slow"}, // Query Table reads with POST
		{trace("GET", 503), time.Millisecond, "error", "failed"},
		{&requestTrace{method: "GET", path: "/api/x", started: started, status: 500, panic: "boom\n\ngoroutine 1"}, time.Millisecond, "error", "panic"},
	}
	for _, c := range cases {
		event, logged := requestEvent(c.trace, c.elapsed)
		if !logged || event.Level != c.level || event.Kind != c.kind {
			t.Errorf("%s %d after %s = %+v, %v; want %s %s", c.trace.method, c.trace.status, c.elapsed, event, logged, c.level, c.kind)
		}
	}
	stream := trace("GET", 200)
	stream.stream.Store(true)
	if _, logged := requestEvent(stream, time.Hour); logged {
		t.Error("an event stream, open by design, was logged as slow")
	}
}

func TestServiceLogRunsMarkCrashes(t *testing.T) {
	t.Setenv("PHAROS_SUPPORT_DIR", t.TempDir())
	write := func(output string, err error) {
		file, _ := openServiceLog("lib")
		if file == nil {
			t.Fatal("could not open the service log")
		}
		fmt.Fprint(file, output)
		closeServiceLog(file, 42, err)
	}
	write("listening\n", nil)
	write("listening\npanic: runtime error: index out of range\n\ngoroutine 1 [running]:\n", errors.New("exit status 2"))
	write("listening\nsync failed: disk full\n", errors.New("signal: terminated"))
	current, _ := openServiceLog("lib")
	fmt.Fprint(current, "listening\n")
	runs := parseOutputRuns(readOutputLog(filepath.Join(libraryLogDir("lib"), "service.log")), true)
	if len(runs) != 4 {
		t.Fatalf("runs = %+v", runs)
	}
	if !runs[0].Running || runs[0].Crashed {
		t.Errorf("current run = %+v, want running", runs[0])
	}
	if runs[1].Crashed || runs[1].Problems != 1 || runs[1].Exit != "signal: terminated" {
		t.Errorf("stopped run = %+v, want a clean stop with one problem line", runs[1])
	}
	if !runs[2].Crashed || runs[2].Crash != "panic: runtime error: index out of range" || runs[2].Exit != "exit status 2" {
		t.Errorf("panicked run = %+v", runs[2])
	}
	if runs[3].Crashed || runs[3].Exit != "exit status 0" {
		t.Errorf("first run = %+v", runs[3])
	}
	// A run the broker never closed ended with the broker itself.
	unclosed := parseOutputRuns([]byte("=== Pharos service started 2026-11-02T10:00:00.000Z ===\nfatal error: concurrent map writes\n=== Pharos service started 2026-11-02T10:05:00.000Z ===\nok\n"), true)
	if !unclosed[1].Crashed || unclosed[1].Crash != "fatal error: concurrent map writes" || unclosed[1].Running {
		t.Errorf("unclosed run = %+v", unclosed[1])
	}
}

// The broker's copy keeps what the service's runtime writes as it dies,
// which the service's own stderr capture cannot.
func TestServiceLogKeepsAChildsPanic(t *testing.T) {
	t.Setenv("PHAROS_SUPPORT_DIR", t.TempDir())
	file, _ := openServiceLog("lib")
	cmd := exec.Command("/bin/sh", "-c", "echo 'panic: boom' >&2; exit 2")
	cmd.Stderr = quietWriter{file}
	err := cmd.Run()
	closeServiceLog(file, 1, err)
	runs := parseOutputRuns(readOutputLog(filepath.Join(libraryLogDir("lib"), "service.log")), false)
	if len(runs) != 1 || !runs[0].Crashed || runs[0].Crash != "panic: boom" {
		t.Fatalf("runs = %+v", runs)
	}
}

// A noisy run moves to the previous file as it writes, and still reads as
// one run whose crash is kept.
func TestServiceLogRotatesWhileARunWrites(t *testing.T) {
	t.Setenv("PHAROS_SUPPORT_DIR", t.TempDir())
	override(t, &diagnosticLogBytes, 4096)
	override(t, &diagnosticTailBytes, 64<<10)
	log, _ := openServiceLog("lib")
	for i := range 400 {
		fmt.Fprintf(log, "line %d: still working\n", i)
	}
	fmt.Fprint(log, "panic: boom\n")
	closeServiceLog(log, 7, errors.New("exit status 2"))
	path := filepath.Join(libraryLogDir("lib"), "service.log")
	for _, file := range []string{path, previousLogPath(path)} {
		if info, err := os.Stat(file); err != nil || info.Size() > 4096+200 {
			t.Fatalf("%s = %v, %v; want it bounded while the run writes", file, info, err)
		}
	}
	runs := parseOutputRuns(readOutputLog(path), false)
	if len(runs) != 1 || !runs[0].Crashed || runs[0].Crash != "panic: boom" || runs[0].Exit != "exit status 2" {
		t.Fatalf("runs = %+v, want the continued run joined with its crash", runs)
	}
}

// A log that cannot be moved aside starts over instead of growing.
func TestServiceLogStaysBoundedInAReadOnlyFolder(t *testing.T) {
	t.Setenv("PHAROS_SUPPORT_DIR", t.TempDir())
	override(t, &diagnosticLogBytes, 4096)
	dir := libraryLogDir("lib")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "service.log")
	if err := os.WriteFile(path, make([]byte, 10000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	log, _ := openServiceLog("lib")
	for i := range 400 {
		fmt.Fprintf(log, "line %d: still working\n", i)
	}
	closeServiceLog(log, 1, nil)
	if info, err := os.Stat(path); err != nil || info.Size() > 4096+200 {
		t.Fatalf("service.log = %v, %v; want it bounded", info, err)
	}
}

func TestCrashReportsListPharosAndItsWebProcesses(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	report := func(name, body string, age time.Duration) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(path, now.Add(-age), now.Add(-age))
	}
	ips := func(process, coalition string) string {
		header, _ := json.Marshal(map[string]any{"app_name": process, "bug_type": "309"})
		body, _ := json.Marshal(map[string]any{"procName": process, "coalitionName": coalition,
			"exception": map[string]any{"type": "EXC_BAD_ACCESS", "signal": "SIGSEGV"}, "faultingThread": 1,
			"usedImages": []any{map[string]any{"name": "pharos"}},
			"threads":    []any{map[string]any{"frames": []any{}}, map[string]any{"frames": []any{map[string]any{"imageIndex": 0, "symbol": "runtime.sigpanic"}}}}})
		return string(header) + "\n" + string(body)
	}
	report("pharos-2026-11-02-100000.ips", ips("pharos", "local.pharos"), time.Hour)
	report("com.apple.WebKit.WebContent-2026-11-02-100000.ips", ips("com.apple.WebKit.WebContent", "local.pharos"), 2*time.Hour)
	report("com.apple.WebKit.WebContent-2026-11-02-090000.ips", ips("com.apple.WebKit.WebContent", "com.apple.Safari"), time.Hour)
	report("pharos_2026-11-02-080000_Mac.cpu_resource.diag", "Command: pharos\nEvent: cpu usage\n", 3*time.Hour)
	report("Pharos-2026-10-01-100000.ips", ips("Pharos", "local.pharos"), 30*24*time.Hour)
	report("Safari-2026-11-02-100000.ips", ips("Safari", "com.apple.Safari"), time.Hour)
	// A report larger than the head read to filter it is summarized whole.
	large := strings.Replace(ips("pharos-labeler-ort", "local.pharos"), `"threads":[`, `"threads":[{"frames":[],"padding":"`+strings.Repeat("x", 400<<10)+`"},`, 1)
	large = strings.Replace(large, `"faultingThread":1`, `"faultingThread":2`, 1)
	report("pharos-labeler-ort-2026-11-02-110000.ips", large, 30*time.Minute)
	reports := scanCrashReports(dir, now)
	if len(reports) != 4 || reports[0].Summary != "EXC_BAD_ACCESS · SIGSEGV" || len(reports[0].Frames) != 1 {
		t.Fatalf("large report = %+v", reports[0])
	}
	reports = reports[1:]
	if len(reports) != 3 {
		t.Fatalf("reports = %+v, want Pharos's, its web process's and its resource report", reports)
	}
	if reports[0].Process != "pharos" || reports[0].Summary != "EXC_BAD_ACCESS · SIGSEGV" || len(reports[0].Frames) != 1 || reports[0].Frames[0] != "pharos runtime.sigpanic" {
		t.Errorf("crash = %+v", reports[0])
	}
	if reports[1].Process != "com.apple.WebKit.WebContent" || reports[2].Kind != "resource" {
		t.Errorf("reports = %+v", reports)
	}
}

func TestLogsAPI(t *testing.T) {
	catalog, config := testCatalog(t)
	t.Setenv(diagnosticLogDirEnv, t.TempDir())
	crashes := t.TempDir()
	override(t, &crashReportDir, func() string { return crashes })
	server := testServer(t, config, catalog)
	call := func(method, path string, body any) *httpResponse {
		response := apiCall(t, server, config.APIToken, method, path, body)
		return &httpResponse{t, response.Code, response.Body.String()}
	}
	summary := func() map[string]sourceSummary {
		var document struct{ Sources []sourceSummary }
		call("GET", "/api/logs/summary", nil).decode(&document)
		sources := map[string]sourceSummary{}
		for _, source := range document.Sources {
			sources[source.ID] = source
		}
		return sources
	}
	if quiet := summary(); quiet["page"].Attention != "" || quiet["background"].Attention != "" || len(quiet) != len(diagnosticSources) {
		t.Fatalf("a quiet library's summary = %+v", quiet)
	}
	if text := call("GET", "/api/logs/report", nil).body; !strings.Contains(text, "No source has recent errors") {
		t.Errorf("quiet report = %q", text)
	}

	recorded := call("POST", "/api/logs/page", map[string]any{"events": []any{
		map[string]any{"kind": "error", "message": "TypeError: x is undefined", "detail": "at render (index.js:1)", "page": "/analyze"},
		map[string]any{"kind": "made-up", "message": "ignored"},
	}})
	if recorded.status != http.StatusOK || !strings.Contains(recorded.body, `"recorded":1`) {
		t.Fatalf("POST /api/logs/page = %d %s", recorded.status, recorded.body)
	}
	execTx(t, catalog.DB, fmt.Sprintf(`INSERT INTO operation_journal(id,root_id,kind,trigger,host_id,process,pid,instance,state,error,started_at,updated_at,ended_at)
		VALUES('f','f','index.changes.automatic','schedule',%s,'service',1,'i','failed','database is locked','%s','%s','%s')`,
		sqlQuote(currentHost().ID), formatTime(time.Now().Add(-time.Hour)), formatTime(time.Now()), formatTime(time.Now())))

	sources := summary()
	if sources["page"].Attention != "error" || sources["page"].Headline != "TypeError: x is undefined" {
		t.Errorf("page = %+v", sources["page"])
	}
	if sources["background"].Attention != "error" || sources["background"].Headline != "Index changes: database is locked" {
		t.Errorf("background = %+v", sources["background"])
	}
	var page diagnosticDocument
	call("GET", "/api/logs/source?id=page", nil).decode(&page)
	if len(page.Events) != 1 || page.Events[0].Page != "/analyze" || page.Events[0].Detail != "at render (index.js:1)" {
		t.Errorf("page events = %+v", page.Events)
	}
	report := call("GET", "/api/logs/report", nil).body
	for _, want := range []string{"## Page errors (error)", "TypeError: x is undefined", "at render (index.js:1)", "## Background work (error)", "database is locked", "page-errors.jsonl"} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if response := call("GET", "/api/logs/source?id=nope", nil); response.status != http.StatusBadRequest {
		t.Errorf("unknown source = %d", response.status)
	}
	if response := call("GET", "/api/logs/crash?name=../../etc/passwd", nil); response.status != http.StatusNotFound {
		t.Errorf("an unlisted crash report = %d, want 404", response.status)
	}
	// The menu bar asks only when its Logs item is shown.
	if err := catalog.SetUIPreferences(t.Context(), map[string]any{"set": map[string]any{menuBarPreferenceKey: map[string]any{"logs": true}}}); err != nil {
		t.Fatal(err)
	}
	status, err := server.menuBarStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if logs := status["logs"].(map[string]string); logs["page"] != "error" || logs["background"] != "error" {
		t.Errorf("menu bar logs = %+v", logs)
	}
}

// A crash after a long run is recent by when it ended, and the copied report
// keeps the evidence behind each mark ahead of newer, uneventful entries.
func TestLogsReportRecentCrashesAndTheirEvidence(t *testing.T) {
	catalog, config := testCatalog(t)
	dir := t.TempDir()
	t.Setenv(diagnosticLogDirEnv, dir)
	override(t, &crashReportDir, func() string { return t.TempDir() })
	server := testServer(t, config, catalog)
	now := time.Now()
	stamp := func(ago time.Duration) string { return formatTime(now.Add(-ago)) }
	var log strings.Builder
	fmt.Fprintf(&log, "=== Pharos service started %s ===\npanic: lost in a long run\n=== Pharos service exited %s (pid 1): exit status 2 ===\n", stamp(72*time.Hour), stamp(time.Minute))
	for i := range 3 {
		fmt.Fprintf(&log, "=== Pharos service started %s ===\nok\n=== Pharos service exited %s (pid 2): signal: terminated ===\n", stamp(time.Duration(50-i)*time.Second), stamp(time.Duration(40-i)*time.Second))
	}
	if err := os.WriteFile(filepath.Join(dir, "service.log"), []byte(log.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	d := server.diagnosticsState()
	d.page.add(diagnosticEvent{At: stamp(time.Hour), Level: "error", Kind: "render", Message: "render failed", Detail: "stack of the render failure"})
	for i := range 16 {
		d.requests.add(diagnosticEvent{At: stamp(time.Duration(30-i) * time.Minute), Level: "info", Kind: "slow", Message: fmt.Sprint("POST /api/sync ", i)})
	}
	d.requests.add(diagnosticEvent{At: stamp(2 * time.Hour), Level: "error", Kind: "panic", Message: "GET /api/x stopped", Detail: "stack of the panic"})
	d.requests.events = append(d.requests.events[len(d.requests.events)-1:], d.requests.events[:len(d.requests.events)-1]...) // the panic is the oldest

	doc, err := server.diagnosticSource(t.Context(), "service", now)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Summary.Attention != "error" || !strings.Contains(doc.Summary.Headline, "panic: lost in a long run") {
		t.Errorf("service = %+v; a crash a minute ago after three days' running is recent", doc.Summary)
	}
	report := server.diagnosticReport(t.Context(), now)
	for _, want := range []string{"panic: lost in a long run", "stack of the panic", "stack of the render failure"} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
}

func TestRequestTraceListsSlowRequestsAndRunningOnes(t *testing.T) {
	catalog, config := testCatalog(t)
	t.Setenv(diagnosticLogDirEnv, t.TempDir())
	override(t, &slowRequest, time.Duration(0))
	server := testServer(t, config, catalog)
	apiCall(t, server, config.APIToken, "GET", "/api/menu-bar/status", nil)
	var requests diagnosticDocument
	response := apiCall(t, server, config.APIToken, "GET", "/api/logs/source?id=requests", nil)
	if err := json.Unmarshal(response.Body.Bytes(), &requests); err != nil {
		t.Fatal(err)
	}
	if len(requests.Events) != 1 || requests.Events[0].Path != "/api/menu-bar/status" || requests.Events[0].Kind != "slow" {
		t.Fatalf("requests = %+v; the log's own reads are not traced", requests.Events)
	}
	// Query Table reads with POST; the page waits on it as on a GET.
	read, readDone := server.traceRequest(httptest.NewRecorder(), mustRequest(t, "POST", "/api/query/usage/rows"))
	read.started = time.Now().Add(-time.Minute)
	action, actionDone := server.traceRequest(httptest.NewRecorder(), mustRequest(t, "POST", "/api/ask/send"))
	action.started = time.Now().Add(-2 * time.Minute)
	// Another request reads the running list while these answer (go test -race).
	written := make(chan struct{})
	go func() { action.WriteHeader(http.StatusOK); close(written) }()
	running := server.runningRequests(time.Now())
	<-written
	if len(running) != 2 || running[0]["path"] != "/api/ask/send" || running[1]["path"] != "/api/query/usage/rows" {
		t.Errorf("running = %+v", running)
	}
	doc, err := server.diagnosticSource(t.Context(), "requests", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if doc.Summary.Attention != "error" || !strings.HasPrefix(doc.Summary.Headline, "POST /api/query/usage/rows") {
		t.Errorf("summary = %+v; a long action is listed but only a stalled read is an alert", doc.Summary)
	}
	readDone()
	// The finished read is now a slow-read event; the action still running
	// is listed but never the alert.
	if doc, _ := server.diagnosticSource(t.Context(), "requests", time.Now()); strings.Contains(doc.Summary.Headline, "/api/ask/send") || len(doc.Running) != 1 {
		t.Errorf("a minute-long action marked requests: %+v", doc)
	}
	actionDone()
}

type httpResponse struct {
	t      *testing.T
	status int
	body   string
}

func (r *httpResponse) decode(value any) {
	r.t.Helper()
	if r.status != http.StatusOK {
		r.t.Fatalf("HTTP %d: %s", r.status, r.body)
	}
	if err := json.Unmarshal([]byte(r.body), value); err != nil {
		r.t.Fatal(err)
	}
}

func mustRequest(t *testing.T, method, target string) *http.Request {
	request, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

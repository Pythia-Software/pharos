package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type applicationEndpoint struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	PID      int    `json:"pid"`
	Lifetime string `json:"lifetime"`
	Catalog  string `json:"catalog,omitempty"`
}

type applicationChild struct {
	endpoint applicationEndpoint
	cmd      *exec.Cmd
	done     chan struct{}
	started  time.Time
}

type applicationManager struct {
	mu          sync.Mutex
	registry    installationRegistry
	id          string
	token       string
	generation  uint64
	children    map[string]*applicationChild
	ctx         context.Context
	operationMu sync.Mutex
	jobMu       sync.Mutex
	pickerMu    sync.Mutex
	job         map[string]any
	client      *http.Client
	busy        bool
	released    bool
	suspended   map[string]bool
	missing     map[string]bool
	// operations is the broker's registry (operation_broker.go), readable
	// without mu.
	operations *brokerOperations
	// starts counts each library's service starts, so a later one is a
	// respawn; startTrigger overrides the trigger of the next.
	starts map[string]int
	// leaseDirCache maps a library configuration to its lease directories.
	leaseDirCache map[string][]string
	// canonicalCache maps a library configuration to its canonical path,
	// resolved once, so the status document never touches the filesystem
	// to match a lease's libraries.
	canonicalCache map[string]string
	startTrigger   string
}

func applicationRuntimePath(id string) string {
	return filepath.Join(filepath.Dir(installationPath(id)), "runtime.json")
}

func newApplicationManager(ctx context.Context, application string) (*applicationManager, error) {
	r, err := loadInstallation(application)
	if err != nil {
		return nil, err
	}
	// Earlier managed releases persisted an empty selection on eject. Recover
	// that installation's explicit integration choice once at startup.
	if r.Selected == "" && r.library(r.MCP) != nil {
		r.Selected = r.MCP
	}
	m := &applicationManager{registry: r, id: installationID(application), token: randomToken(), children: map[string]*applicationChild{}, suspended: map[string]bool{}, missing: map[string]bool{}, ctx: ctx,
		client: &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{Proxy: nil}}, starts: map[string]int{}, leaseDirCache: map[string][]string{}, canonicalCache: map[string]string{}}
	m.operations = newBrokerOperations(brokerOperationsPath(m.id))
	m.publishLocked()
	return m, nil
}

func runApplicationCLI(args []string) error {
	f := flag.NewFlagSet("app", flag.ContinueOnError)
	application := f.String("application", "", "installed application path")
	endpoint := f.String("endpoint", "", "private endpoint file for the native wrapper")
	initial := f.String("initial-config", "", "existing library to register without moving its data")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *application == "" {
		return fmt.Errorf("usage: pharos app --application APP [--endpoint FILE]")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	m, err := newApplicationManager(ctx, *application)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(installationPath(m.id)), 0o700); err != nil {
		return err
	}
	owner, err := fileLease(filepath.Join(filepath.Dir(installationPath(m.id)), ".application.lock"), true)
	if err != nil {
		return fmt.Errorf("this copy of Pharos is already open: %w", err)
	}
	defer owner.Close()
	if *initial != "" && len(m.registry.Libraries) == 0 {
		config, err := LoadConfig(*initial)
		if err == nil {
			var id string
			id, err = m.registry.register(config, "My Library")
			if err == nil {
				m.registry.Selected = id
			}
		}
		if err != nil {
			message := fmt.Sprintf("Could not open the previous library: %v. Choose an available library or upgrade Pharos, then try again.", err)
			m.reportJob("paused", message)
			fmt.Fprintln(os.Stderr, message)
		}
	}
	if err = m.registry.save(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	ep := applicationEndpoint{URL: "http://" + listener.Addr().String(), Token: m.token, PID: os.Getpid(), Lifetime: randomToken()}
	if err = durableJSON(applicationRuntimePath(m.id), ep); err != nil {
		return err
	}
	defer os.Remove(applicationRuntimePath(m.id))
	if *endpoint != "" {
		if err = durableJSON(*endpoint, ep); err != nil {
			return err
		}
		defer os.Remove(*endpoint)
	}
	server := &http.Server{Handler: m, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, c := context.WithTimeout(context.Background(), stopTimeout)
		defer c()
		_ = server.Shutdown(shutdown)
	}()
	if pid, _ := strconv.Atoi(os.Getenv(parentEnv)); pid > 0 {
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if syscall.Kill(pid, 0) != nil {
						cancel()
						return
					}
				}
			}
		}()
	}
	m.operations.start(ctx)
	go m.maintain()
	err = server.Serve(listener)
	// Operations use this context and must reach their cancellation boundary
	// before the broker exits (handlers can have returned an async receipt).
	cancel()
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	m.mu.Lock()
	for id := range m.children {
		m.stopChildLocked(id)
	}
	m.mu.Unlock()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (m *applicationManager) childLocked(id string) (*applicationChild, error) {
	if m.suspended[id] {
		return nil, fmt.Errorf("this library has been released; reconnect explicitly or reattach its drive")
	}
	if m.busy || m.released {
		return nil, fmt.Errorf("Pharos is finishing a library operation; wait for it to complete")
	}
	if c := m.children[id]; c != nil {
		select {
		case <-c.done:
			delete(m.children, id)
		default:
			return c, nil
		}
	}
	l := m.registry.library(id)
	if l == nil {
		return nil, fmt.Errorf("choose a library")
	}
	config, err := LoadConfig(l.resolve())
	if err != nil {
		return nil, err
	}
	if err = config.checkLibrary(libraryVolumeIdentity); err != nil {
		return nil, err
	}
	d, err := readLibraryDescriptor(config)
	if err != nil {
		return nil, err
	}
	if d.ID != l.ID || d.Instance != l.Instance {
		return nil, fmt.Errorf("the selected library was restored or replaced; recover or register it explicitly")
	}
	trigger := m.startTrigger
	if trigger == "" {
		trigger = triggerStartup
		if m.starts[id] > 0 {
			trigger = triggerRespawn
		}
	}
	m.starts[id]++
	start := m.operations.begin(OperationSpec{Kind: "service.start", Trigger: trigger}, id)
	child, err := m.startChildLocked(id, l, config, start)
	start.Finish(err)
	m.publishLocked()
	return child, err
}

// startChildLocked starts library l's service and waits for it to answer.
func (m *applicationManager) startChildLocked(id string, l *registeredLibrary, config Config, start *Op) (*applicationChild, error) {
	start.Step("Start the library's service", "", -1)
	path := filepath.Join(filepath.Dir(installationPath(m.id)), "service-"+l.ID+".json")
	_ = os.Remove(path)
	cmd := exec.Command(runningExecutable(), "--config", config.Path, "serve", "--dynamic", "--endpoint", path, "--application", m.registry.Application)
	cmd.Env = append(os.Environ(), parentEnv+"="+strconv.Itoa(os.Getpid()))
	cmd.Stderr = os.Stderr
	c := &applicationChild{cmd: cmd, done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { _ = cmd.Wait(); close(c.done) }()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(2 * time.Minute)
	defer timeout.Stop()
	for {
		select {
		case <-m.ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM)
			return nil, m.ctx.Err()
		case <-c.done:
			return nil, fmt.Errorf("Pharos could not open %s; inspect the service diagnostic", l.Name)
		case <-timeout.C:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			return nil, fmt.Errorf("opening %s took too long", l.Name)
		case <-ticker.C:
			b, e := os.ReadFile(path)
			if e != nil {
				continue
			}
			if json.Unmarshal(b, &c.endpoint) != nil || c.endpoint.PID != cmd.Process.Pid || c.endpoint.Catalog != config.CatalogPath {
				continue
			}
			request, _ := http.NewRequestWithContext(m.ctx, "GET", c.endpoint.URL+"/api/ready", nil)
			request.Header.Set("Authorization", "Bearer "+c.endpoint.Token)
			response, e := m.client.Do(request)
			if e != nil {
				continue
			}
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				c.started = time.Now()
				m.children[id] = c
				return c, nil
			}
		}
	}
}

// childIDsLocked lists the libraries whose services are running.
func (m *applicationManager) childIDsLocked() []string {
	ids := []string{}
	for id := range m.children {
		ids = append(ids, id)
	}
	return ids
}

func validLibraryName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || len(name) > 160 {
		return fmt.Errorf("choose a library name without path separators")
	}
	return nil
}

// selectLocked opens id in Pharos (and so for MCP), and remembers when the
// library it replaces was last open.
func (m *applicationManager) selectLocked(id string) {
	if previous := m.registry.library(m.registry.Selected); previous != nil && previous.ID != id {
		previous.LastOpened = now()
	}
	m.registry.Selected = id
	m.generation++
}

// applicationSummaryInterval is how often the app asks each open library's
// service how many conversations it holds, to show once the library closes.
var applicationSummaryInterval = time.Minute

// rememberSummaries records each running library's conversation count, and
// that the selected one is open now. It saves only a changed count.
func (m *applicationManager) rememberSummaries() {
	m.mu.Lock()
	children := map[string]*applicationChild{}
	for id, c := range m.children {
		if !m.busy && m.runningLocked(id) {
			children[id] = c
		}
	}
	m.mu.Unlock()
	for id, c := range children {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		request, _ := http.NewRequestWithContext(ctx, "GET", c.endpoint.URL+"/api/library/summary", nil)
		request.Header.Set("Authorization", "Bearer "+c.endpoint.Token)
		var summary struct {
			Conversations *int64 `json:"conversations"`
		}
		response, err := m.client.Do(request)
		if err == nil {
			if response.StatusCode == http.StatusOK {
				err = json.NewDecoder(response.Body).Decode(&summary)
			}
			response.Body.Close()
		}
		cancel()
		if err != nil || summary.Conversations == nil {
			continue
		}
		m.mu.Lock()
		if l := m.registry.library(id); l != nil && m.children[id] == c {
			changed := l.Conversations == nil || *l.Conversations != *summary.Conversations
			l.Conversations = summary.Conversations
			if id == m.registry.Selected {
				l.LastOpened = now()
			}
			if changed {
				_ = m.registry.save()
			}
		}
		m.mu.Unlock()
	}
}

// runningLocked reports whether id's service is up, without starting it.
func (m *applicationManager) runningLocked(id string) bool {
	c := m.children[id]
	if c == nil {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (m *applicationManager) stopChildLocked(id string) {
	c := m.children[id]
	if c == nil {
		return
	}
	_ = c.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-c.done:
		delete(m.children, id)
	case <-time.After(stopTimeout):
	}
	m.publishLocked()
}

func (m *applicationManager) authorized(r *http.Request) bool {
	if secureEqual(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), m.token) {
		return true
	}
	cookie, err := r.Cookie("pharos_app_" + m.id)
	return err == nil && secureEqual(cookie.Value, m.token)
}

func (m *applicationManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/" && secureEqual(r.URL.Query().Get("token"), m.token) {
		http.SetCookie(w, &http.Cookie{Name: "pharos_app_" + m.id, Value: m.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		target := "/app"
		m.mu.Lock()
		if _, err := m.childLocked(m.registry.Selected); err == nil {
			target = "/"
		}
		m.mu.Unlock()
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if !m.authorized(r) {
		writeError(w, fmt.Errorf("open Pharos to connect to this application"), http.StatusUnauthorized)
		return
	}
	// The broker answers for itself; a request scoped to a library (the
	// app's window asking whether its page can load) goes on to that
	// library's service, below.
	if r.URL.Path == "/api/ready" && r.Header.Get("X-Pharos-Library") == "" {
		writeJSON(w, map[string]any{"ready": true}, http.StatusOK)
		return
	}
	// Never takes m.mu, which create and child starts hold for minutes.
	if r.URL.Path == "/api/app/operations" && r.Method == http.MethodGet {
		writeJSON(w, m.operations.document(r.Context(), r.URL.Query().Get("libraries") != "0"), http.StatusOK)
		return
	}
	if r.URL.Path == "/api/app/status" && r.Method == http.MethodGet {
		m.operations.serveStatus(w, r)
		return
	}
	if r.URL.Path == "/api/app/status/stream" && r.Method == http.MethodGet {
		m.operations.serveStatusStream(w, r)
		return
	}
	if r.Method == "POST" && !sameOrigin(r) {
		writeError(w, fmt.Errorf("request origin does not match this app"), http.StatusForbidden)
		return
	}
	if r.URL.Path == "/api/release" && r.Method == "POST" {
		if !m.operationMu.TryLock() {
			writeError(w, fmt.Errorf("a library operation is still running"), 503)
			return
		}
		defer m.operationMu.Unlock()
		m.mu.Lock()
		release := m.operations.begin(OperationSpec{Kind: "library.release", Trigger: triggerUser}, m.childIDsLocked()...)
		defer func() { release.Finish(nil) }()
		m.released = true
		m.generation++
		for id := range m.children {
			m.stopChildLocked(id)
		}
		remaining := len(m.children)
		m.mu.Unlock()
		if remaining > 0 {
			writeError(w, fmt.Errorf("libraries are still closing"), 503)
			return
		}
		writeJSON(w, map[string]any{"released": true}, 200)
		return
	}
	if r.URL.Path == "/api/app/mcp" {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var request map[string]any
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request) != nil {
			writeError(w, fmt.Errorf("invalid MCP request"), 400)
			return
		}
		answer, err := m.mcp(r.Context(), request)
		if err != nil {
			writeJSON(w, mcpToolError(request, err.Error()), 200)
			return
		}
		writeJSON(w, answer, 200)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/app/") {
		m.api(w, r)
		return
	}
	if r.URL.Path == "/app" || r.URL.Path == "/app/" {
		// Setup and the chooser are for when no library is open. Once one
		// is, its Settings → Libraries holds every library control. A child
		// start holds m.mu for minutes; the page doesn't wait for it.
		open := false
		if m.mu.TryLock() {
			open = !m.busy && m.runningLocked(m.registry.Selected)
			m.mu.Unlock()
		}
		if open {
			http.Redirect(w, r, "/settings/libraries", http.StatusSeeOther)
			return
		}
		writeHTML(w, applicationHTML())
		return
	}
	// The startup shell needs its styles even with no selected library. Static
	// assets never open a catalog or start a library service.
	if asset, ok := uiAssets[r.URL.Path]; ok && r.Method == http.MethodGet {
		writeEmbeddedAsset(w, asset.name, asset.contentType)
		return
	}
	m.mu.Lock()
	id := m.registry.Selected
	generation := m.generation
	if scope := r.Header.Get("X-Pharos-Library"); scope != "" && scope != id {
		m.mu.Unlock()
		writeError(w, fmt.Errorf("the application switched libraries; reopen this page"), 409)
		return
	}
	c, err := m.childLocked(id)
	m.mu.Unlock()
	if err != nil {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, err, 503)
		} else {
			http.Redirect(w, r, "/app", http.StatusSeeOther)
		}
		return
	}
	target, _ := url.Parse(c.endpoint.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(req *http.Request) {
		original(req)
		req.Header.Set("Authorization", "Bearer "+c.endpoint.Token)
		req.Header.Set("Accept-Encoding", "identity")
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		m.mu.Lock()
		current := m.generation == generation && m.registry.Selected == id && m.children[id] == c
		m.mu.Unlock()
		if !current {
			return fmt.Errorf("the application changed libraries; reopen this page")
		}
		if !isUIPage(r.URL.Path) || !strings.Contains(response.Header.Get("Content-Type"), "text/html") {
			return nil
		}
		b, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return err
		}
		scope, _ := json.Marshal(id)
		sessionScope := sha256.Sum256([]byte(currentHost().ID + "\x00" + m.id))
		m.mu.Lock()
		library := *m.registry.library(id)
		m.mu.Unlock()
		session, _ := json.Marshal(map[string]string{"preferences": fmt.Sprintf("pharos-session:%x:", sessionScope[:16]), "storage": id + ":" + library.Instance + ":", "legacy": id + ":"})
		selection, _ := json.Marshal(map[string]string{"id": id, "name": library.Name, "config": library.Config})
		script := `<script>window.pharosManagedSession=` + string(session) + `;window.pharosManagedLibrary=` + string(selection) + `;</script>`
		html := strings.Replace(string(b), "<head>", "<head>"+script, 1)
		binding := `<script>(()=>{const f=window.fetch;window.fetch=(input,options={})=>{const u=new URL(input instanceof Request?input.url:input,location.href);if(u.origin===location.origin&&!u.pathname.startsWith('/api/app/')){const h=new Headers(input instanceof Request?input.headers:options.headers);h.set('X-Pharos-Library',` + string(scope) + `);options={...options,headers:h}}return f.call(window,input,options)};setInterval(()=>f.call(window,'/api/app/state').then(r=>r.json()).then(s=>{window.dispatchEvent(new CustomEvent('pharos:application-state',{detail:s}));if(!window.pharosLibrarySwitching&&(s.selected!==` + string(scope) + `||!s.libraries?.some(l=>l.id===` + string(scope) + `&&l.connected))){location.replace('/app')}}).catch(()=>{document.body.textContent='Pharos is offline. Reopen the application to reconnect.'}),3000)})();</script>`
		html = strings.Replace(html, "<head>", "<head>"+binding, 1)
		html = managedLibraryHTML(html)
		response.Body = io.NopCloser(strings.NewReader(html))
		response.ContentLength = int64(len(html))
		response.Header.Set("Content-Length", strconv.Itoa(len(html)))
		return nil
	}
	proxy.ServeHTTP(w, r)
}

func (m *applicationManager) mcp(ctx context.Context, request map[string]any) (map[string]any, error) {
	m.mu.Lock()
	// Agents search the library open in Pharos, whichever it is.
	open := m.registry.library(m.registry.Selected)
	if open == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("no library is open; open one in Pharos")
	}
	if m.suspended[open.ID] || m.released || m.busy {
		m.mu.Unlock()
		return nil, fmt.Errorf("%s is released or temporarily unavailable; reopen it in Pharos after its current operation finishes", open.Name)
	}
	c := m.children[open.ID]
	if c == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("%s is offline; open it in Pharos", open.Name)
	}
	select {
	case <-c.done:
		m.mu.Unlock()
		return nil, fmt.Errorf("%s is disconnected; reopen it in Pharos", open.Name)
	default:
	}
	generation := m.generation
	endpoint := c.endpoint
	m.mu.Unlock()
	b, _ := json.Marshal(request)
	post, err := http.NewRequestWithContext(ctx, "POST", endpoint.URL+"/api/mcp/rpc", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	post.Header.Set("Authorization", "Bearer "+endpoint.Token)
	post.Header.Set("Content-Type", "application/json")
	post.Header.Set(mcpCatalogHeader, endpoint.Catalog)
	response, err := m.client.Do(post)
	if err != nil {
		return nil, fmt.Errorf("the open library is offline: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("the open library is unavailable (%s)", response.Status)
	}
	var answer map[string]any
	if err = json.NewDecoder(response.Body).Decode(&answer); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation != m.generation || m.children[m.registry.Selected] != c {
		return nil, fmt.Errorf("Pharos changed libraries while this request ran; retry")
	}
	select {
	case <-c.done:
		return nil, fmt.Errorf("the library went offline while this request ran; retry after reconnecting")
	default:
	}
	return answer, nil
}

// Browser windows do not have the native WebKit bridge. Standard Additions
// provides the same directory picker on the Mac running this local service.
// Keep cancellation distinct from failure, and return paths as JSON so spaces,
// quotes and newlines survive without shell interpolation.
var applicationFolderPicker = func(ctx context.Context) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("the folder picker requires macOS; enter the folder path instead")
	}
	const script = `const app = Application.currentApplication();
app.includeStandardAdditions = true;
try {
  JSON.stringify(app.chooseFolder({withPrompt: "Choose a Pharos library or a parent folder for a new library."}).toString());
} catch (error) {
  if (error.errorNumber !== -128) throw error;
  JSON.stringify("");
}`
	output, err := exec.CommandContext(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", script).Output()
	if err != nil {
		return "", fmt.Errorf("could not open the folder picker; enter the folder path or try again: %w", err)
	}
	var path string
	if err = json.Unmarshal(output, &path); err != nil {
		return "", fmt.Errorf("could not read the selected folder: %w", err)
	}
	return path, nil
}

func (m *applicationManager) api(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/app/state" && r.Method == "GET" {
		m.mu.Lock()
		libraries := make([]map[string]any, 0, len(m.registry.Libraries))
		for i := range m.registry.Libraries {
			l := &m.registry.Libraries[i]
			_, err := l.load()
			connected := false
			if c := m.children[l.ID]; c != nil && err == nil {
				select {
				case <-c.done:
				default:
					connected = true
				}
			}
			entry := map[string]any{"id": l.ID, "name": l.Name, "config": l.Config, "available": err == nil, "connected": connected && m.registry.Selected == l.ID, "recovery_point": l.RecoveryPoint, "last_opened": l.LastOpened}
			if l.Conversations != nil {
				entry["conversations"] = *l.Conversations
			}
			if err != nil {
				entry["error"] = err.Error()
			}
			libraries = append(libraries, entry)
		}
		state := map[string]any{"libraries": libraries, "selected": m.registry.Selected, "generation": m.generation, "mcp": m.registry.Selected, "application": m.registry.Application, "installation": m.id, "delivery_source": m.registry.DeliverySource, "delivery_target": m.registry.DeliveryTarget, "busy": m.busy}
		state["local_library_parent"] = filepath.Join(pharosSupportDir(), "Libraries")
		m.mu.Unlock()
		m.jobMu.Lock()
		state["job"] = m.job
		m.jobMu.Unlock()
		writeJSON(w, state, 200)
		return
	}
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, err, 400)
		return
	}
	if r.URL.Path == "/api/app/choose-folder" {
		if !m.pickerMu.TryLock() {
			writeError(w, fmt.Errorf("a folder picker is already open"), http.StatusConflict)
			return
		}
		defer m.pickerMu.Unlock()
		path, err := applicationFolderPicker(r.Context())
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"path": path}, http.StatusOK)
		return
	}
	if r.URL.Path == "/api/app/transfer" || r.URL.Path == "/api/app/recover" {
		m.startOperation(w, r, body)
		return
	}
	if r.URL.Path == "/api/app/compact" {
		m.startCompaction(w, body)
		return
	}
	if r.URL.Path == "/api/app/release-volume" {
		if !m.operationMu.TryLock() {
			writeError(w, fmt.Errorf("Pharos is finishing a library operation; wait and eject again"), 503)
			return
		}
		defer m.operationMu.Unlock()
		m.mu.Lock()
		defer m.mu.Unlock()
		defer m.publishLocked()
		volume := firstString(body["volume"])
		release := m.operations.begin(OperationSpec{Kind: "library.release", Subject: volume, Trigger: triggerDevice}, m.childIDsLocked()...)
		var releaseErr error
		defer func() { release.Finish(releaseErr) }()
		if volume == "" {
			writeError(w, fmt.Errorf("missing volume identity"), 400)
			return
		}
		for i := range m.registry.Libraries {
			l := &m.registry.Libraries[i]
			if l.Volume == "" {
				// Registries saved by earlier builds used only the optional pin.
				if config, err := LoadConfig(l.resolve()); err == nil {
					l.rememberLocation(config)
				}
			}
			if strings.EqualFold(l.Volume, volume) {
				m.suspended[l.ID] = true
				m.stopChildLocked(l.ID)
				if m.children[l.ID] != nil {
					releaseErr = fmt.Errorf("library is still closing; retry eject shortly")
					writeError(w, releaseErr, 503)
					return
				}
			}
		}
		m.generation++
		// Eject suspends this lifetime only. Keep the selected library and the
		// delivery pair on disk for relaunch or a verified drive return.
		writeJSON(w, map[string]any{"released": true}, 200)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.publishLocked()
	if m.busy && (r.URL.Path == "/api/app/create" || r.URL.Path == "/api/app/open" || r.URL.Path == "/api/app/connect") {
		writeError(w, fmt.Errorf("wait for the current library operation to finish"), http.StatusConflict)
		return
	}
	var err error
	switch r.URL.Path {
	case "/api/app/create":
		name := strings.TrimSpace(firstString(body["name"]))
		if err = validLibraryName(name); err != nil {
			break
		}
		path := firstString(body["path"])
		location := firstString(body["location"])
		if location != "" && location != "mac" && location != "ssd" {
			err = fmt.Errorf("choose local or external library storage")
			break
		}
		if location == "ssd" && path == "" {
			err = fmt.Errorf("choose a library folder on your external drive")
			break
		}
		if path == "" {
			path = filepath.Join(pharosSupportDir(), "Libraries", name)
			if location == "" && strings.HasPrefix(m.registry.Application, "/Volumes/") {
				path = filepath.Join(filepath.Dir(m.registry.Application), "Libraries", name)
			}
			err = os.MkdirAll(filepath.Dir(path), 0o700)
		} else {
			path = expandPath(path)
			if !filepath.IsAbs(path) {
				err = fmt.Errorf("choose an absolute library location")
			} else if location != "ssd" && canonical(filepath.Dir(path)) == canonical(filepath.Join(pharosSupportDir(), "Libraries")) {
				// Editing the proposed default sends an explicit path on a fresh
				// install. Only this managed parent may be created implicitly.
				err = os.MkdirAll(filepath.Dir(path), 0o700)
			} else {
				_, err = os.Stat(filepath.Dir(path))
			}
		}
		if err != nil {
			break
		}
		if within(canonical(path), canonical(m.registry.Application)) {
			err = fmt.Errorf("choose a library folder outside the application bundle")
			break
		}
		create := m.operations.begin(OperationSpec{Kind: "library.create", Subject: name, Trigger: triggerUser})
		var config Config
		config, err = InitLibrary(path, libraryVolumeIdentity)
		create.Finish(err)
		if err != nil {
			break
		}
		d, e := readLibraryDescriptor(config)
		if e != nil {
			err = e
			break
		}
		d.Name = name
		if err = durableJSON(descriptorPath(config), d); err != nil {
			break
		}
		var id string
		id, err = m.registry.register(config, name)
		if err == nil {
			m.selectLocked(id)
		}
	case "/api/app/open":
		path := expandPath(firstString(body["path"]))
		if path == "" {
			err = fmt.Errorf("choose an existing library folder or configuration file")
			break
		}
		if info, e := os.Stat(path); e == nil && info.IsDir() {
			path = filepath.Join(path, libraryConfigName)
		}
		var config Config
		config, err = LoadConfig(path)
		if err != nil {
			break
		}
		var id string
		id, err = m.registry.register(config, firstString(body["name"]))
		if err == nil {
			m.selectLocked(id)
		}
	case "/api/app/connect":
		id := firstString(body["id"])
		delete(m.suspended, id)
		m.startTrigger = triggerUser
		_, err = m.childLocked(id)
		m.startTrigger = ""
		if err == nil {
			m.selectLocked(id)
		}
	case "/api/app/disconnect":
		id := firstString(body["id"])
		if m.busy {
			err = fmt.Errorf("wait for the current operation to finish")
			break
		}
		m.stopChildLocked(id)
		if m.children[id] != nil {
			err = fmt.Errorf("library is still closing; retry shortly")
			break
		}
		m.generation++
		m.suspended[id] = true
		if l := m.registry.library(id); l != nil {
			l.LastOpened = now()
		}
	case "/api/app/rename":
		l := m.registry.library(firstString(body["id"]))
		name := strings.TrimSpace(firstString(body["name"]))
		if l == nil {
			err = fmt.Errorf("choose a registered library")
			break
		}
		if err = validLibraryName(name); err != nil {
			break
		}
		l.Name = name
		// The name travels with the library when its folder is reachable;
		// otherwise only this Mac's list changes until it is renamed there.
		if config, e := l.load(); e == nil {
			if d, e := readLibraryDescriptor(config); e == nil && d.ID == l.ID {
				d.Name = name
				err = durableJSON(descriptorPath(config), d)
			}
		}
		m.generation++
	case "/api/app/delivery":
		a, b := firstString(body["source"]), firstString(body["target"])
		if a != "" && (a == b || m.registry.library(a) == nil || m.registry.library(b) == nil) {
			err = fmt.Errorf("choose two different registered libraries")
			break
		}
		if a != "" {
			var source Config
			source, err = m.registry.library(a).load()
			if err == nil {
				err = protectPendingEvidence(source)
			}
			if err != nil {
				break
			}
		}
		m.registry.DeliverySource, m.registry.DeliveryTarget = a, b
	default:
		writeError(w, fmt.Errorf("unknown application action"), 404)
		return
	}
	if err == nil {
		err = m.registry.save()
	}
	if err != nil {
		writeError(w, err, 400)
		return
	}
	writeJSON(w, map[string]any{"saved": true}, 200)
}

func runApplicationMCPCLI(args []string, input io.Reader, output io.Writer) error {
	f := flag.NewFlagSet("mcp-app", flag.ContinueOnError)
	application := f.String("application", "", "installed application path")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *application == "" {
		return fmt.Errorf("usage: pharos mcp-app --application APP")
	}
	// An agent can keep stdio open after its portable app or disk disappears.
	// Such a process must not remain as a helper on the Mac after a forced yank.
	if strings.HasPrefix(*application, "/Volumes/") {
		if _, err := os.Stat(*application); err == nil {
			go func() {
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for range ticker.C {
					if _, err := os.Stat(*application); os.IsNotExist(err) {
						os.Exit(0)
					}
				}
			}()
		}
	}
	client := &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{Proxy: nil}}
	return serveMCP(input, output, func(request map[string]any) map[string]any {
		method := firstString(request["method"])
		if method == "tools/list" {
			return mcpResult(request, map[string]any{"tools": mcpTools})
		}
		if method != "tools/call" {
			return handleMCP(nil, request)
		}
		answer, err := applicationRPC(client, *application, request)
		if err != nil {
			return mcpToolError(request, err.Error())
		}
		return answer
	}, nil)
}

func applicationRPC(client *http.Client, application string, request map[string]any) (map[string]any, error) {
	var endpoint applicationEndpoint
	b, err := os.ReadFile(applicationRuntimePath(installationID(application)))
	if err != nil {
		return nil, fmt.Errorf("Pharos is offline; open this copy of Pharos")
	}
	if err = json.Unmarshal(b, &endpoint); err != nil {
		return nil, err
	}
	if endpoint.PID <= 0 || syscall.Kill(endpoint.PID, 0) != nil {
		return nil, fmt.Errorf("Pharos is offline; open this copy of Pharos")
	}
	payload, _ := json.Marshal(request)
	post, err := http.NewRequest("POST", endpoint.URL+"/api/app/mcp", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	post.Header.Set("Authorization", "Bearer "+endpoint.Token)
	post.Header.Set("Content-Type", "application/json")
	response, err := client.Do(post)
	if err != nil {
		return nil, fmt.Errorf("Pharos is offline: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("Pharos application is unavailable (%s)", response.Status)
	}
	var answer map[string]any
	if err = json.NewDecoder(response.Body).Decode(&answer); err != nil {
		return nil, err
	}
	return answer, nil
}

// A registration made before the broker existed may still run `--config ...
// mcp`. Once that library is managed by this installed app, the legacy entry
// must obey the same assignment gate. An unbundled CLI remains explicit mode.
func managedApplicationForConfig(executable string, config Config) string {
	macOS := filepath.Dir(executable)
	contents := filepath.Dir(macOS)
	application := filepath.Dir(contents)
	if filepath.Base(macOS) != "MacOS" || filepath.Base(contents) != "Contents" || !strings.HasSuffix(application, ".app") {
		return ""
	}
	registry, err := loadInstallation(application)
	if err != nil {
		return ""
	}
	for _, l := range registry.Libraries {
		if canonical(l.Config) == canonical(config.Path) {
			return application
		}
	}
	return ""
}

func (m *applicationManager) maintain() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	lastDelivery, lastSummary := time.Time{}, time.Time{}
	wasPresent := false
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			for id := range m.suspended {
				if l := m.registry.library(id); l != nil {
					_, err := os.Stat(l.resolve())
					if err != nil {
						m.missing[id] = true
					} else if m.missing[id] {
						delete(m.suspended, id)
						delete(m.missing, id)
					}
				}
			}
			selected := m.registry.Selected
			if selected != "" {
				_, _ = m.childLocked(selected)
			}
			m.publishLocked()
			source, target := m.registry.DeliverySource, m.registry.DeliveryTarget
			m.mu.Unlock()
			if time.Since(lastSummary) >= applicationSummaryInterval {
				m.rememberSummaries()
				lastSummary = time.Now()
			}
			if source != "" && target != "" {
				m.mu.Lock()
				l := m.registry.library(target)
				present := false
				if l != nil {
					_, err := os.Stat(l.resolve())
					present = err == nil
				}
				m.mu.Unlock()
				if time.Since(lastDelivery) >= applicationDeliveryInterval || (present && !wasPresent) {
					m.deliveryCycle(source, target)
					lastDelivery = time.Now()
				}
				wasPresent = present
			}
		}
	}
}

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
	return &applicationManager{registry: r, id: installationID(application), token: randomToken(), children: map[string]*applicationChild{}, suspended: map[string]bool{}, missing: map[string]bool{}, ctx: ctx,
		client: &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{Proxy: nil}}}, nil
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
	path := filepath.Join(filepath.Dir(installationPath(m.id)), "service-"+l.ID+".json")
	_ = os.Remove(path)
	cmd := exec.Command(runningExecutable(), "--config", config.Path, "serve", "--dynamic", "--endpoint", path, "--application", m.registry.Application)
	cmd.Env = append(os.Environ(), parentEnv+"="+strconv.Itoa(os.Getpid()))
	cmd.Stderr = os.Stderr
	c := &applicationChild{cmd: cmd, done: make(chan struct{})}
	if err = cmd.Start(); err != nil {
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
				m.children[id] = c
				return c, nil
			}
		}
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
	if r.URL.Path == "/api/ready" {
		writeJSON(w, map[string]any{"ready": true}, http.StatusOK)
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
		if !strings.Contains(response.Header.Get("Content-Type"), "text/html") {
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
		instance := m.registry.library(id).Instance
		m.mu.Unlock()
		session, _ := json.Marshal(map[string]string{"preferences": fmt.Sprintf("pharos-session:%x:", sessionScope[:16]), "storage": id + ":" + instance + ":", "legacy": id + ":"})
		script := `<script>window.pharosManagedSession=` + string(session) + `;</script>`
		html := strings.Replace(string(b), "<head>", "<head>"+script, 1)
		binding := `<script>(()=>{const f=window.fetch;window.fetch=(input,options={})=>{const u=new URL(input instanceof Request?input.url:input,location.href);if(u.origin===location.origin&&!u.pathname.startsWith('/api/app/')){const h=new Headers(input instanceof Request?input.headers:options.headers);h.set('X-Pharos-Library',` + string(scope) + `);options={...options,headers:h}}return f.call(window,input,options)};setInterval(()=>f.call(window,'/api/app/state').then(r=>r.json()).then(s=>{if(s.selected!==` + string(scope) + `||!s.libraries?.some(l=>l.id===` + string(scope) + `&&l.connected)){location.replace('/app')}}).catch(()=>{document.body.textContent='Pharos is offline. Reopen the application to reconnect.'}),3000)})();</script>`
		html = strings.Replace(html, "<head>", "<head>"+binding, 1)
		html = strings.Replace(html, `<div class="bar">`, `<div class="bar"><a href="/app" style="color:inherit;font-size:12px;flex:none" title="Choose a library and MCP settings">Libraries</a>`, 1)
		response.Body = io.NopCloser(strings.NewReader(html))
		response.ContentLength = int64(len(html))
		response.Header.Set("Content-Length", strconv.Itoa(len(html)))
		return nil
	}
	proxy.ServeHTTP(w, r)
}

func (m *applicationManager) mcp(ctx context.Context, request map[string]any) (map[string]any, error) {
	m.mu.Lock()
	assigned := m.registry.library(m.registry.MCP)
	if assigned == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("open a library in Pharos to assign MCP")
	}
	if m.suspended[assigned.ID] || m.released || m.busy {
		m.mu.Unlock()
		return nil, fmt.Errorf("the assigned MCP library is released or temporarily unavailable; reconnect it in Pharos after its current operation finishes")
	}
	if m.registry.Selected != assigned.ID {
		name := assigned.Name
		active := m.registry.library(m.registry.Selected)
		child := m.children[m.registry.Selected]
		connected := active != nil && child != nil && !m.suspended[m.registry.Selected] && !m.released
		if connected {
			select {
			case <-child.done:
				connected = false
			default:
			}
		}
		if !connected {
			m.mu.Unlock()
			return nil, fmt.Errorf("no library is connected. MCP is attached to %s; connect to it in Pharos", name)
		}
		m.mu.Unlock()
		return nil, fmt.Errorf("Pharos is working with %s right now. MCP is attached to %s. Connect to %s or explicitly change its assignment in MCP settings", active.Name, name, name)
	}
	c := m.children[assigned.ID]
	if c == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("the assigned MCP library is offline; connect to it in Pharos")
	}
	select {
	case <-c.done:
		m.mu.Unlock()
		return nil, fmt.Errorf("the assigned MCP library is disconnected; reopen it in Pharos")
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
		return nil, fmt.Errorf("the assigned MCP library is offline: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("the assigned MCP library is unavailable (%s)", response.Status)
	}
	var answer map[string]any
	if err = json.NewDecoder(response.Body).Decode(&answer); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation != m.generation || m.children[m.registry.MCP] != c {
		return nil, fmt.Errorf("Pharos changed its library connection while this request ran; retry after connecting to the assigned MCP library")
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
			entry := map[string]any{"id": l.ID, "name": l.Name, "config": l.Config, "available": err == nil, "connected": connected && m.registry.Selected == l.ID, "recovery_point": l.RecoveryPoint}
			if err != nil {
				entry["error"] = err.Error()
			}
			libraries = append(libraries, entry)
		}
		state := map[string]any{"libraries": libraries, "selected": m.registry.Selected, "generation": m.generation, "mcp": m.registry.MCP, "application": m.registry.Application, "installation": m.id, "delivery_source": m.registry.DeliverySource, "delivery_target": m.registry.DeliveryTarget, "busy": m.busy}
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
	if r.URL.Path == "/api/app/release-volume" {
		if !m.operationMu.TryLock() {
			writeError(w, fmt.Errorf("Pharos is finishing a library operation; wait and eject again"), 503)
			return
		}
		defer m.operationMu.Unlock()
		m.mu.Lock()
		defer m.mu.Unlock()
		volume := firstString(body["volume"])
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
					writeError(w, fmt.Errorf("library is still closing; retry eject shortly"), 503)
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
	var err error
	switch r.URL.Path {
	case "/api/app/create":
		name := strings.TrimSpace(firstString(body["name"]))
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || len(name) > 160 {
			err = fmt.Errorf("choose a library name without path separators")
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
		var config Config
		config, err = InitLibrary(path, libraryVolumeIdentity)
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
			m.registry.Selected = id
			m.generation++
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
			m.registry.Selected = id
			m.generation++
		}
	case "/api/app/connect":
		id := firstString(body["id"])
		delete(m.suspended, id)
		_, err = m.childLocked(id)
		if err == nil {
			m.registry.Selected = id
			m.generation++
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
	case "/api/app/mcp-library":
		id := firstString(body["id"])
		if m.registry.library(id) == nil {
			err = fmt.Errorf("choose a registered library")
			break
		}
		m.registry.MCP = id
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
	lastDelivery := time.Time{}
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
			source, target := m.registry.DeliverySource, m.registry.DeliveryTarget
			m.mu.Unlock()
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

package archive

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// An agent client keeps its MCP server process for the whole session, often
// days. So the server never holds the catalog between requests, which would
// keep a release (see mcpRPC) from closing it, and never fails for good. Each
// tools/list and tools/call reloads the configuration, then goes to the running
// Pharos service for it, whose open catalog and warm Library cache answer and
// record the call, or, when no service answers, opens the catalog for that one
// request. While the library is unavailable, initialize and tools/list still
// answer and tool calls fail with a "not connected" error.
type mcpServer struct {
	configPath string // --config; "" resolves as LoadConfig does
	identity   func(string) string
	client     *http.Client
	verified   string // library directory last checked against volume_id
	// answer is handleMCP and restart restartMCP; tests replace them.
	answer  func(*Catalog, map[string]any) map[string]any
	restart func(pending []byte) error
	faulted bool // a direct call hit a memory fault (see direct)
}

func newMCPServer(configPath string) *mcpServer {
	// No proxy: the request carries the API token.
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, IdleConnTimeout: 30 * time.Second}
	executable := runningExecutable()
	return &mcpServer{configPath: configPath, identity: volumeIdentity,
		client: &http.Client{Transport: transport, Timeout: 2 * time.Minute}, answer: handleMCP,
		restart: func(pending []byte) error { return restartMCP(executable, pending) }}
}

// RunMCP serves MCP over stdio for the library or install that configPath
// names or, without one, that LoadConfig finds.
func RunMCP(configPath string, input io.Reader, output io.Writer) error {
	quickHostDetection = true
	return newMCPServer(configPath).run(input, output)
}

// mcpPendingEnv hands input read ahead but not yet answered to the process
// that restartMCP replaces this one with.
const mcpPendingEnv = "PHAROS_MCP_PENDING"

func (m *mcpServer) run(input io.Reader, output io.Writer) error {
	if _, err := m.config(); err != nil {
		fmt.Fprintf(os.Stderr, "Pharos MCP: %v; tool calls fail until it is available\n", err)
	}
	if pending, ok := os.LookupEnv(mcpPendingEnv); ok {
		os.Unsetenv(mcpPendingEnv)
		data, _ := base64.StdEncoding.DecodeString(pending)
		input = io.MultiReader(bytes.NewReader(data), input)
	}
	return serveMCP(input, output, m.handle, m.afterAnswer)
}

// afterAnswer restarts the process once the answer to a faulted call is out.
func (m *mcpServer) afterAnswer(pending []byte) {
	if !m.faulted {
		return
	}
	err := m.restart(pending)
	fmt.Fprintf(os.Stderr, "Pharos MCP: could not restart after a memory fault, so calls on the same catalog may fault again: %v\n", err)
	m.faulted = false
}

// restartMCP replaces this process with a fresh run of executable, this
// process's own. The PID and stdio stay, so the agent client stays connected.
// It returns only on failure.
func restartMCP(executable string, pending []byte) error {
	fmt.Fprintln(os.Stderr, "Pharos MCP: restarting after a memory fault while reading the catalog")
	return syscall.Exec(executable, os.Args, append(os.Environ(), mcpPendingEnv+"="+base64.StdEncoding.EncodeToString(pending)))
}

func (m *mcpServer) handle(request map[string]any) map[string]any {
	method := firstString(request["method"])
	if method != "tools/list" && method != "tools/call" {
		return handleMCP(nil, request)
	}
	response, err := m.dispatch(request)
	switch {
	case err == nil:
		return response
	case method == "tools/list":
		// Clients keep the tools listed when they connect, so list them while
		// the library is away too; calls then say why they fail.
		return mcpResult(request, map[string]any{"tools": mcpTools})
	default:
		return mcpToolError(request, err.Error())
	}
}

func (m *mcpServer) dispatch(request map[string]any) (map[string]any, error) {
	config, err := m.config()
	if err != nil {
		return nil, err
	}
	if response, forwarded, err := m.forward(config, request); forwarded {
		return response, err
	}
	return m.direct(config, request)
}

func (m *mcpServer) config() (Config, error) {
	config, err := loadConfig(m.configPath, false)
	if err != nil {
		m.verified = ""
		if _, statErr := os.Stat(config.Path); errors.Is(statErr, os.ErrNotExist) {
			return config, libraryNotConnected(config.Path)
		}
	}
	return config, err
}

// libraryNotConnected explains a missing library file. A drive that is not
// mounted leaves no /Volumes/NAME behind.
func libraryNotConnected(path string) error {
	reason := path + " is missing"
	if rest, ok := strings.CutPrefix(path, "/Volumes/"); ok {
		mount := "/Volumes/" + strings.SplitN(rest, "/", 2)[0]
		if _, err := os.Stat(mount); errors.Is(err, os.ErrNotExist) {
			reason = mount + " is not mounted"
		}
	}
	return fmt.Errorf("Pharos library is not connected: %s", reason)
}

// mcpCatalogHeader names the catalog a forwarded request is for, so that a
// service with another catalog open on the same port declines it.
const mcpCatalogHeader = "X-Pharos-Catalog"

// forward sends request to the running service for config. It reports false
// only when nothing ran there, so the caller may open the catalog itself.
func (m *mcpServer) forward(config Config, request map[string]any) (map[string]any, bool, error) {
	if config.Host != "127.0.0.1" && config.Host != "::1" && config.Host != "localhost" {
		return nil, false, nil
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, true, err
	}
	post, err := http.NewRequest(http.MethodPost, "http://"+net.JoinHostPort(config.Host, strconv.Itoa(config.Port))+"/api/mcp/rpc", bytes.NewReader(body))
	if err != nil {
		return nil, false, nil
	}
	post.Header.Set("Authorization", "Bearer "+config.APIToken)
	post.Header.Set("Content-Type", "application/json")
	post.Header.Set(mcpCatalogHeader, config.CatalogPath)
	answer, err := m.client.Do(post)
	if err != nil {
		var dial *net.OpError
		if errors.As(err, &dial) && dial.Op == "dial" {
			return nil, false, nil
		}
		// The service may have run the call. Rerunning it here could also open
		// a catalog the service just released for an eject.
		return nil, true, fmt.Errorf("the Pharos service did not answer: %w", err)
	}
	defer answer.Body.Close()
	switch answer.StatusCode {
	case http.StatusOK:
		var response map[string]any
		decoder := json.NewDecoder(answer.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&response); err != nil {
			return nil, true, fmt.Errorf("the Pharos service sent an unreadable answer: %w", err)
		}
		return response, true, nil
	case http.StatusNoContent:
		return nil, true, nil
	case http.StatusServiceUnavailable:
		return nil, true, errLibraryReleased
	case http.StatusUnauthorized, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusConflict:
		// Another library's service, or a build without forwarding.
		_, _ = io.Copy(io.Discard, answer.Body)
		return nil, false, nil
	}
	return nil, true, fmt.Errorf("the Pharos service answered %s", answer.Status)
}

// direct opens the catalog for this request alone.
//
// A drive yanked mid-call faults on SQLite's mapped index (see exitOnFault).
// The call then fails, and the process restarts after answering (see
// afterAnswer): SQLite's state for the catalog is unusable, and it would be
// reused for the same file (same device and inode) once the drive is back.
// So after a fault nothing touches the catalog again, not even to close it.
func (m *mcpServer) direct(config Config, request map[string]any) (response map[string]any, err error) {
	if err := m.checkLibrary(config); err != nil {
		return nil, err
	}
	if releasedRecently(config.CatalogPath) {
		return nil, errLibraryReleased
	}
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	defer func() {
		if value := recover(); value != nil {
			if _, fault := value.(interface{ Addr() uintptr }); !fault {
				panic(value)
			}
			m.faulted, m.verified = true, ""
			response, err = nil, libraryFault(config.CatalogPath, value)
		}
	}()
	catalog, err := OpenCatalogForQuery(config.CatalogPath)
	if errors.Is(err, os.ErrNotExist) {
		if config.Library {
			m.verified = ""
			return nil, libraryNotConnected(config.CatalogPath)
		}
		// Only a library refuses to start a missing catalog (see checkLibrary).
		if err = config.EnsureDirs(); err == nil {
			catalog, err = OpenCatalog(config.CatalogPath)
		}
	}
	if err != nil {
		return nil, err
	}
	response = m.answer(catalog, request)
	catalog.closeQuery()
	return response, nil
}

func libraryFault(catalogPath string, value any) error {
	dir := filepath.Dir(catalogPath)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("Pharos library disconnected: its drive went away during this call (%s is gone); calls succeed again once it is back", dir)
	}
	return fmt.Errorf("Pharos library disconnected: reading %s failed with a memory fault (%v); try again", catalogPath, value)
}

var errLibraryReleased = errors.New("Pharos library is not connected: the Pharos service has released it so that its drive can be ejected")

// A release (before an eject) leaves releasedMarkerName beside the catalog.
// For releaseQuiet, or until the next serve removes it, the direct path does
// not reopen the catalog: a call between the service exiting and the drive
// unmounting would make the approved eject fail.
const (
	releasedMarkerName = ".released"
	releaseQuiet       = 30 * time.Second
)

func releasedMarker(catalogPath string) string {
	return filepath.Join(filepath.Dir(catalogPath), releasedMarkerName)
}

func releasedRecently(catalogPath string) bool {
	info, err := os.Stat(releasedMarker(catalogPath))
	if err != nil {
		return false
	}
	age := time.Since(info.ModTime())
	return age > -releaseQuiet && age < releaseQuiet
}

// checkLibrary runs the library guards before a direct open. Checking the
// volume shells out to diskutil (~0.1 s), so it is skipped while the library
// directory is the same one on the same device as when it last passed.
func (m *mcpServer) checkLibrary(config Config) error {
	identity := m.identity
	key := ""
	if info, err := os.Stat(filepath.Dir(config.Path)); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			key = fmt.Sprint(config.Path, "|", config.VolumeID, "|", stat.Dev, "|", stat.Ino)
		}
	}
	if key != "" && key == m.verified {
		identity = func(string) string { return config.VolumeID }
	}
	m.verified = ""
	if err := config.checkLibrary(identity); err != nil {
		return err
	}
	m.verified = key
	return nil
}

// mcpRPC answers an MCP request that an agent's MCP server process forwarded,
// from this service's catalog, recording history as that process would.
func (s *Server) mcpRPC(w http.ResponseWriter, r *http.Request, request map[string]any) {
	catalog := s.Catalog
	if catalog == nil {
		writeJSON(w, map[string]any{"error": "the library is not open"}, http.StatusServiceUnavailable)
		return
	}
	if path := r.Header.Get(mcpCatalogHeader); path != "" && !sameFile(path, catalog.Path) {
		writeJSON(w, map[string]any{"error": "this service has a different catalog open"}, http.StatusConflict)
		return
	}
	response := handleMCP(catalog, request)
	if response == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, response, http.StatusOK)
}

func sameFile(left, right string) bool {
	a, err := os.Stat(left)
	if err != nil {
		return false
	}
	b, err := os.Stat(right)
	return err == nil && os.SameFile(a, b)
}

// pharosSupportDir holds per-Mac state that must not live on the library
// drive, such as host.json. PHAROS_SUPPORT_DIR overrides it.
func pharosSupportDir() string {
	if dir := os.Getenv("PHAROS_SUPPORT_DIR"); dir != "" {
		if absolute, err := filepath.Abs(expandPath(dir)); err == nil {
			return absolute
		}
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "Pharos")
}

// shellQuote quotes value for a POSIX shell, unless it needs no quoting.
func shellQuote(value string) string {
	if value != "" && strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-./:=@%+,") == "" {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

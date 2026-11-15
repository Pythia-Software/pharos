package archive

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/gbdubs/pharos/internal/platform/filelock"
)

// capture-push captures this machine's agent transcripts and uploads them
// to a bucket a Pharos library imports from (docs/capture-push.md). It needs
// no library, catalog or configuration beyond its own two files, and runs
// wherever Pharos builds.
//
// Nothing in the bucket is overwritten. A member is cut into chunks named
// by their content; an export, written after its chunks, lists them under
// this pusher's own ID. Two writers therefore never put different bytes at
// one key, and an interrupted or repeated push is harmless.

const pushContract = "pharos-push/1"

var (
	// pushMinInterval separates the starts of two background passes.
	pushMinInterval = 30 * time.Second
	// pushWatchPoll is how often --watch looks at the transcript folders.
	pushWatchPoll = 2 * time.Second
	// After pushRefusalLimit passes in a row that the destination refused,
	// background passes run once per pushRefusalBackoff.
	pushRefusalLimit   = 5
	pushRefusalBackoff = 24 * time.Hour
	pushNow            = time.Now
	pushSpawn          = spawnPushWorker
	// pushVersion is set when a release is linked.
	pushVersion = ""
)

var errPushRunning = errors.New("another push is running")

// Platforms: how capture-push recognizes the sandbox platform it runs on
// (docs/plugin-import-platforms.md). Published as
// contracts/plugins/platforms.json, which a test holds equal to this list.

type pushPlatformRecipe struct {
	// Name is the channel key; Label is how the channel and pool read.
	Name  string `json:"name"`
	Label string `json:"label"`
	// Markers are environment variables that are all set on the platform.
	Markers  []string             `json:"markers"`
	Identity pushPlatformIdentity `json:"identity"`
	// IdentityToken is the command that prints a signed identity token for
	// {audience}, for keyless credentials.
	IdentityToken []string `json:"identity_token,omitempty"`
	// Ownerless says what a task without an owner is: "refused" until the
	// platform's own answer has been checked.
	Ownerless string `json:"ownerless"`
	Checked   string `json:"checked"`
}

// pushPlatformIdentity names the environment variables that identify a task.
type pushPlatformIdentity struct {
	// Instance stays the same for as long as one sandbox's files are the
	// same files, across suspension, and differs between sandboxes started
	// from one image.
	Instance string `json:"instance"`
	Task     string `json:"task,omitempty"`
	TaskURL  string `json:"task_url,omitempty"`
	Owner    string `json:"owner,omitempty"`
	// EnvironmentSegment counts from the end of the task link's path to the
	// segment naming the environment.
	EnvironmentSegment int `json:"environment_segment,omitempty"`
}

var pushPlatforms = []pushPlatformRecipe{{
	Name: "niteshift", Label: "Niteshift", Markers: []string{"NITESHIFT_TASK_ID", "NITESHIFT_SANDBOX_ID"},
	Identity:      pushPlatformIdentity{Instance: "NITESHIFT_TASK_ID", Task: "NITESHIFT_TASK_ID", TaskURL: "NITESHIFT_TASK_URL", Owner: "NITESHIFT_USER_ID", EnvironmentSegment: 2},
	IdentityToken: []string{"ns", "oidc-token", "--audience", "{audience}"},
	Ownerless:     "refused", Checked: "2026-11-15",
}}

type pushPlatformsDocument struct {
	Contract  string               `json:"contract"`
	Platforms []pushPlatformRecipe `json:"platforms"`
}

func pushPlatformsContract() pushPlatformsDocument {
	return pushPlatformsDocument{Contract: "pharos-platforms/1", Platforms: pushPlatforms}
}

type pushPlatform struct {
	Name        string `json:"name"`
	Environment string `json:"environment,omitempty"`
	Task        string `json:"task,omitempty"`
	TaskURL     string `json:"task_url,omitempty"`
}

type pushInitiator struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

// pushIdentity is what one machine says about itself in everything it
// pushes, and the prefix that follows from it.
type pushIdentity struct {
	Platform  *pushPlatform
	Initiator *pushInitiator
	Owner     string
	Prefix    string
	Pool      string
	// instance distinguishes this machine from a copy of its files.
	instance string
}

var pushOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$`)

const pushOwnerShared = "shared"

// resolvePushIdentity reads the platform's markers and decides whose work
// this is. It never guesses a person: a prefix with {owner} and no owner is
// refused, and shared is used only when the machine is declared to run an
// automation.
func resolvePushIdentity(config pushConfig) (pushIdentity, error) {
	identity := pushIdentity{Prefix: config.Prefix, Pool: config.Pool}
	var recipe *pushPlatformRecipe
	for i := range pushPlatforms {
		if !slices.ContainsFunc(pushPlatforms[i].Markers, func(name string) bool { return os.Getenv(name) == "" }) {
			recipe = &pushPlatforms[i]
			break
		}
	}
	platformOwner := ""
	if recipe != nil {
		env := func(name string) string {
			if name == "" {
				return ""
			}
			return strings.TrimSpace(os.Getenv(name))
		}
		block := &pushPlatform{Name: recipe.Name, Task: truncateText(env(recipe.Identity.Task), 199)}
		if link, err := url.Parse(env(recipe.Identity.TaskURL)); err == nil && link.Scheme == "https" && link.Host != "" {
			block.TaskURL = link.String()
			segments := strings.Split(strings.Trim(link.Path, "/"), "/")
			if at := len(segments) - recipe.Identity.EnvironmentSegment; recipe.Identity.EnvironmentSegment > 0 && at >= 0 {
				block.Environment = truncateText(segments[at], 199)
			}
		}
		identity.Platform, platformOwner = block, env(recipe.Identity.Owner)
		identity.instance = "platform:" + recipe.Name + ":" + env(recipe.Identity.Instance)
		if identity.Pool == "" {
			identity.Pool = truncateText(strings.TrimSuffix(recipe.Label+" · "+block.Environment, " · "), 79)
		}
	}
	automation := firstString(config.Automation, os.Getenv("PHAROS_PUSH_AUTOMATION"))
	identity.Owner = firstString(config.Owner, os.Getenv("PHAROS_PUSH_OWNER"))
	switch {
	case automation != "":
		identity.Initiator = &pushInitiator{Kind: "automation", Name: truncateText(automation, 79)}
		identity.Owner = defaultString(identity.Owner, pushOwnerShared)
	case identity.Owner == "":
		identity.Owner = platformOwner
		fallthrough
	default:
		if identity.Owner != "" && identity.Owner != pushOwnerShared {
			identity.Initiator = &pushInitiator{Kind: "person"}
		}
	}
	valid := pushOwnerPattern.MatchString(identity.Owner)
	if strings.Contains(config.Prefix, "{owner}") {
		if !valid {
			where := "this machine does not say whose work it holds"
			if identity.Owner != "" {
				where = fmt.Sprintf("%q cannot name an owner", identity.Owner)
			} else if recipe != nil && recipe.Identity.Owner != "" {
				where = fmt.Sprintf("%s did not set %s for this task", recipe.Label, recipe.Identity.Owner)
			}
			return identity, fmt.Errorf(`the prefix %q files work by owner, and %s. Set PHAROS_PUSH_OWNER (or "owner" in the configuration) to the person's identifier, or PHAROS_PUSH_AUTOMATION to an automation's name for runs nobody started`, config.Prefix, where)
		}
		identity.Prefix = strings.ReplaceAll(config.Prefix, "{owner}", identity.Owner)
	} else if !valid {
		identity.Owner = ""
	}
	return identity, nil
}

// What is written to the bucket.

type pushExportDoc struct {
	Contract      string         `json:"contract"`
	Pusher        string         `json:"pusher"`
	PushedAt      string         `json:"pushed_at"`
	Previous      *string        `json:"previous"`
	Pool          string         `json:"pool,omitempty"`
	RetentionDays int            `json:"retention_days,omitempty"`
	Envelope      CaptureExport  `json:"envelope"`
	Chunking      string         `json:"chunking"`
	Members       []pushMember   `json:"members"`
	Platform      *pushPlatform  `json:"platform,omitempty"`
	Initiator     *pushInitiator `json:"initiator,omitempty"`
	Owner         string         `json:"owner,omitempty"`
}

type pushMember struct {
	Path   string         `json:"path"`
	Chunks []pushDocChunk `json:"chunks"`
}

// pushDocChunk names a chunk as the contract does: "sha256:<hex>".
type pushDocChunk struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type pushHelloDoc struct {
	Contract  string         `json:"contract"`
	Pusher    string         `json:"pusher"`
	At        string         `json:"at"`
	Version   string         `json:"version"`
	OS        string         `json:"os"`
	Label     string         `json:"label,omitempty"`
	Pool      string         `json:"pool,omitempty"`
	Sources   []string       `json:"sources"`
	Platform  *pushPlatform  `json:"platform,omitempty"`
	Initiator *pushInitiator `json:"initiator,omitempty"`
	Owner     string         `json:"owner,omitempty"`
}

func pushKey(prefix string, parts ...string) string {
	return strings.TrimPrefix(prefix+"/"+strings.Join(parts, "/"), "/")
}

func pushRunningVersion() string {
	if pushVersion != "" {
		return pushVersion
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

// State: <state>/state.json, beside the capture root it describes.

type pushState struct {
	Pusher string `json:"pusher"`
	// Machine is where this state was made. Found on another machine (an
	// image baked with one inside, a cloned volume) the state takes a new
	// pusher before it pushes.
	Machine string `json:"machine"`
	// Destination identifies the bucket and prefix the rest describes.
	Destination string                      `json:"destination"`
	HelloAt     string                      `json:"hello_at,omitempty"`
	Sources     map[string]*pushSourceState `json:"sources"`
	// Chunks maps each uploaded chunk to when it was uploaded, in Unix
	// seconds.
	Chunks   map[string]int64 `json:"chunks"`
	Refusals int              `json:"refusals,omitempty"`
	Last     *pushPassRecord  `json:"last,omitempty"`
}

type pushSourceState struct {
	// LastKey is the newest export's name below exports/<pusher>/<source>/.
	LastKey string `json:"last_key,omitempty"`
	// Content digests the transcripts of that export, metadata apart.
	Content string                   `json:"content,omitempty"`
	Files   map[string]pushFileState `json:"files,omitempty"`
}

// pushFileState remembers a member read as one exact file, so an unchanged
// file is not read again.
type pushFileState struct {
	Dev     uint64      `json:"dev"`
	Ino     uint64      `json:"ino"`
	Size    int64       `json:"size"`
	MTimeNS int64       `json:"mtime_ns"`
	SHA256  string      `json:"sha256"`
	Chunks  []pushChunk `json:"chunks"`
}

type pushPassRecord struct {
	StartedAt  string             `json:"started_at"`
	FinishedAt string             `json:"finished_at"`
	OK         bool               `json:"ok"`
	Error      string             `json:"error,omitempty"`
	Refused    bool               `json:"refused,omitempty"`
	Pusher     string             `json:"pusher,omitempty"`
	Prefix     string             `json:"prefix,omitempty"`
	Owner      string             `json:"owner,omitempty"`
	Platform   string             `json:"platform,omitempty"`
	Sources    []pushSourceResult `json:"sources"`
	Warnings   []string           `json:"warnings,omitempty"`
}

type pushSourceResult struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Export         string `json:"export,omitempty"`
	ID             string `json:"id,omitempty"`
	Unchanged      bool   `json:"unchanged,omitempty"`
	Files          int    `json:"files"`
	ChunksUploaded int    `json:"chunks_uploaded"`
	BytesUploaded  int64  `json:"bytes_uploaded"`
	Error          string `json:"error,omitempty"`
}

func loadPushState(dir string) (*pushState, error) {
	state := &pushState{}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err == nil {
		if err = json.Unmarshal(data, state); err != nil {
			return nil, fmt.Errorf("%s is damaged (%w); remove the folder %s to start again", filepath.Join(dir, "state.json"), err, dir)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if state.Sources == nil {
		state.Sources = map[string]*pushSourceState{}
	}
	if state.Chunks == nil {
		state.Chunks = map[string]int64{}
	}
	return state, nil
}

func (s *pushState) save(dir string) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "state.json"), data, 0o600)
}

func newPusherID() (string, error) {
	var raw [10]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "psh_" + hex.EncodeToString(raw[:]), nil
}

// pushMachine identifies the machine a state belongs to: the platform's
// instance where there is one, else the operating system's machine ID and
// the hostname, which between them differ for a container and a clone.
func pushMachine(identity pushIdentity) string {
	if identity.instance != "" {
		return stableID("push-machine", identity.instance)
	}
	id, _ := os.ReadFile(pushMachineIDFile)
	hostname, _ := os.Hostname()
	return stableID("push-machine", strings.TrimSpace(string(id)), hostname)
}

var pushMachineIDFile = filepath.Join(string(filepath.Separator)+"etc", "machine-id")

// Options and the folders they name.

type pushOptions struct {
	ConfigPath string
	StateDir   string
}

func (o pushOptions) resolved() pushOptions {
	home, _ := os.UserHomeDir()
	o.ConfigPath = expandPath(firstString(o.ConfigPath, os.Getenv("PHAROS_PUSH_CONFIG"), filepath.Join(home, ".pharos", "pharos-push.json")))
	o.StateDir = expandPath(firstString(o.StateDir, os.Getenv("PHAROS_PUSH_STATE"), filepath.Join(home, ".pharos", "push")))
	if absolute, err := filepath.Abs(o.StateDir); err == nil {
		o.StateDir = absolute
	}
	return o
}

func (o pushOptions) pendingPath() string { return filepath.Join(o.StateDir, "pending") }
func (o pushOptions) captureRoot() string { return filepath.Join(o.StateDir, "captures") }

// pushSources lists what to capture: the configuration's sources, else the
// usual folders of every kind that can travel as a capture export, where
// they exist. An agent app's own variable for its folder comes first.
func pushSources(config pushConfig) []SourceConfig {
	if len(config.Sources) > 0 {
		var sources []SourceConfig
		for _, source := range config.Sources {
			sources = append(sources, SourceConfig{Name: source.Name, Kind: source.Kind, Path: expandPath(source.Path), Enabled: true})
		}
		return sources
	}
	home, _ := os.UserHomeDir()
	moved := map[string]string{}
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		moved["claude"] = filepath.Join(dir, "projects")
	}
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		moved["codex"] = dir
	}
	var sources []SourceConfig
	for _, spec := range providerRegistry {
		if spec.Import == nil || !slices.Contains(spec.Import.Forms, importCaptureExport) {
			continue
		}
		candidates := []string{}
		if dir := moved[spec.Kind]; dir != "" {
			candidates = append(candidates, dir)
		}
		for _, root := range spec.Import.Roots {
			candidates = append(candidates, filepath.Join(home, filepath.FromSlash(root)))
		}
		seen := map[string]bool{}
		for _, candidate := range candidates {
			resolved, err := filepath.EvalSymlinks(candidate)
			if err != nil || seen[resolved] {
				continue
			}
			if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
				continue
			}
			seen[resolved] = true
			name := spec.Kind
			if len(seen) > 1 {
				name = fmt.Sprintf("%s-%d", spec.Kind, len(seen))
			}
			sources = append(sources, SourceConfig{Name: name, Kind: spec.Kind, Path: resolved, Enabled: true})
		}
	}
	return sources
}

// pushSignature changes when a transcript under a source is added, grows or
// is rewritten. --watch compares it between polls.
func pushSignature(sources []SourceConfig) string {
	digest := sha256.New()
	for _, source := range sources {
		_ = filepath.WalkDir(source.Path, func(file string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			name := strings.ToLower(entry.Name())
			if !strings.HasSuffix(name, ".jsonl") && !strings.HasSuffix(name, ".jsonl.zst") {
				return nil
			}
			if info, err := entry.Info(); err == nil {
				fmt.Fprintf(digest, "%s\x00%d\x00%d\n", file, info.Size(), info.ModTime().UnixNano())
			}
			return nil
		})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// pushSink receives a frozen capture's export and uploads it as chunks.
type pushSink struct {
	ctx       context.Context
	store     pushStore
	prefix    string
	state     *pushState
	retention time.Duration
	previous  map[string]pushFileState
	files     map[string]pushFileState
	members   map[string][]pushChunk
	metadata  map[string][]byte
	result    *pushSourceResult
}

func (s *pushSink) mkdir(string) error { return nil }

// fresh reports whether the bucket holds a chunk and will keep it: one
// uploaded more than half the bucket's lifecycle ago is sent again.
func (s *pushSink) fresh(sha string) bool {
	at, ok := s.state.Chunks[sha]
	return ok && (s.retention == 0 || pushNow().Sub(time.Unix(at, 0)) < s.retention/2)
}

func (s *pushSink) emit(chunk pushChunk, data []byte) error {
	if s.fresh(chunk.SHA256) {
		return nil
	}
	if err := s.store.put(s.ctx, pushKey(s.prefix, "chunks", chunk.SHA256), data); err != nil {
		return err
	}
	s.state.Chunks[chunk.SHA256] = pushNow().Unix()
	s.result.ChunksUploaded++
	s.result.BytesUploaded += chunk.Size
	return nil
}

// writeFile receives the capture's own records. They change on every pass,
// so they are held until the pass knows it has something to publish.
func (s *pushSink) writeFile(rel string, data []byte) error {
	if rel != captureExportEnvelope {
		s.metadata[rel] = slices.Clone(data)
	}
	return nil
}

// uploadMetadata cuts and uploads the held records.
func (s *pushSink) uploadMetadata() error {
	for rel, data := range s.metadata {
		chunker := newGearChunker(s.emit)
		if _, err := chunker.Write(data); err != nil {
			return err
		}
		if err := chunker.Close(); err != nil {
			return err
		}
		s.members[rel] = chunker.chunks
	}
	return nil
}

type pushMemberWriter struct {
	*gearChunker
	sink *pushSink
	rel  string
	st   exportStat
}

func (w *pushMemberWriter) Close() error {
	if err := w.gearChunker.Close(); err != nil {
		return err
	}
	w.sink.members[w.rel] = w.chunks
	w.sink.files[w.rel] = pushFileState{Dev: w.st.Dev, Ino: w.st.Ino, Size: w.st.Size, MTimeNS: w.st.MTimeNS, SHA256: w.sum, Chunks: w.chunks}
	return nil
}

func (s *pushSink) create(rel string, st exportStat) (io.WriteCloser, error) {
	return &pushMemberWriter{gearChunker: newGearChunker(s.emit), sink: s, rel: rel, st: st}, nil
}

func (s *pushSink) known(rel string, st exportStat) (string, bool) {
	entry, ok := s.previous[rel]
	if !ok || entry.Dev != st.Dev || entry.Ino != st.Ino || entry.Size != st.Size || entry.MTimeNS != st.MTimeNS {
		return "", false
	}
	for _, chunk := range entry.Chunks {
		if !s.fresh(chunk.SHA256) {
			return "", false
		}
	}
	s.members[rel], s.files[rel] = entry.Chunks, entry
	return entry.SHA256, true
}

// streamCaptureExport sends the capture of source under root/host to sink
// as ExportCapture would stage it, holding the host's capture lock.
func streamCaptureExport(ctx context.Context, root, host, source string, sink exportSink) (CaptureExport, error) {
	if !captureExportSupported {
		return CaptureExport{}, errCaptureExportUnsupported
	}
	for _, name := range []string{host, source} {
		if !validCaptureName(name) || exportSegmentProblem(name) != "" {
			return CaptureExport{}, fmt.Errorf("%q cannot name a captured host or source", name)
		}
	}
	rootPath, err := resolveExportDir(root)
	if err != nil {
		return CaptureExport{}, fmt.Errorf("capture root: %w", err)
	}
	rootDir, err := exportOpenRoot(rootPath)
	if err != nil {
		return CaptureExport{}, err
	}
	defer rootDir.Close()
	hostDir, err := rootDir.sub(host)
	if err != nil {
		return CaptureExport{}, fmt.Errorf("host %s has no capture: %w", host, err)
	}
	defer hostDir.Close()
	sourceDir, err := hostDir.sub(source)
	if err != nil {
		return CaptureExport{}, fmt.Errorf("source %s has no capture: %w", source, err)
	}
	defer sourceDir.Close()
	lock, err := freezeCaptureForExport(ctx, hostDir, filepath.Join(rootPath, host, captureLockName))
	if err != nil {
		return CaptureExport{}, err
	}
	defer releaseLockFile(lock)
	return stageCaptureExport(ctx, rootDir, hostDir, sourceDir, sink, host, source)
}

// pushRun is one pass's resolved inputs.
type pushRun struct {
	options  pushOptions
	config   pushConfig
	identity pushIdentity
	store    pushStore
	state    *pushState
}

// preparePush reads the configuration and state and settles who this pusher
// is. It changes nothing in the bucket.
func preparePush(options pushOptions) (*pushRun, error) {
	config, err := readPushConfig(options.ConfigPath)
	if err != nil {
		return nil, err
	}
	if config.Access != "write" {
		return nil, fmt.Errorf("%s gives read access; a machine that pushes needs the write configuration", options.ConfigPath)
	}
	identity, err := resolvePushIdentity(config)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(options.captureRoot(), 0o700); err != nil {
		return nil, err
	}
	state, err := loadPushState(options.StateDir)
	if err != nil {
		return nil, err
	}
	if machine := pushMachine(identity); state.Pusher == "" || state.Machine != machine {
		// A new root, or one copied here from another machine: a new pusher
		// with its own chain of exports. The chunks stay where they are.
		if state.Pusher, err = newPusherID(); err != nil {
			return nil, err
		}
		state.Machine, state.HelloAt, state.Sources, state.Refusals = machine, "", map[string]*pushSourceState{}, 0
	}
	d := config.Destination
	if destination := stableID("push-destination", d.Type, d.Bucket, d.Endpoint, d.Account, d.Container, d.URLTemplate, identity.Prefix); state.Destination != destination {
		// Nothing recorded about another bucket or prefix holds here.
		state.Destination, state.HelloAt, state.Sources, state.Chunks, state.Refusals = destination, "", map[string]*pushSourceState{}, map[string]int64{}, 0
	}
	store, err := newPushStore(config, filepath.Dir(options.ConfigPath))
	if err != nil {
		return nil, err
	}
	return &pushRun{options: options, config: config, identity: identity, store: store, state: state}, nil
}

// host is the captured host this pusher writes as: PHAROS_HOST_ID when set,
// else an ID that follows the pusher, so that a copied root is a new host.
func (r *pushRun) host() Host {
	label := firstString(os.Getenv("PHAROS_HOST_LABEL"))
	if label == "" {
		label, _ = os.Hostname()
	}
	name := os.Getenv("USER")
	if account, err := user.Current(); err == nil && account.Username != "" {
		name = account.Username
	}
	id := firstString(os.Getenv("PHAROS_HOST_ID"))
	if id == "" {
		id = stableID("host", "pusher", r.state.Pusher)
	}
	return Host{ID: id, Label: label, User: name}
}

func (r *pushRun) hello(ctx context.Context, sources []SourceConfig) error {
	kinds := []string{}
	for _, source := range sources {
		if !slices.Contains(kinds, source.Kind) {
			kinds = append(kinds, source.Kind)
		}
	}
	system := runtime.GOOS
	if !slices.Contains([]string{"linux", "windows", "darwin"}, system) {
		system = "other"
	}
	at := pushNow().UTC().Format(time.RFC3339)
	data, err := json.Marshal(pushHelloDoc{Contract: pushContract, Pusher: r.state.Pusher, At: at, Version: pushRunningVersion(), OS: system,
		Label: truncateText(r.host().Label, 199), Pool: r.identity.Pool, Sources: kinds,
		Platform: r.identity.Platform, Initiator: r.identity.Initiator, Owner: r.identity.Owner})
	if err != nil {
		return err
	}
	if err := r.store.put(ctx, pushKey(r.identity.Prefix, "hello", r.state.Pusher+".json"), data); err != nil {
		return err
	}
	r.state.HelloAt = at
	return nil
}

// pass captures and uploads once. The state it leaves is true of the bucket
// whether or not the pass succeeded.
func (r *pushRun) pass(ctx context.Context) pushPassRecord {
	record := pushPassRecord{StartedAt: pushNow().UTC().Format(time.RFC3339), Pusher: r.state.Pusher, Prefix: r.identity.Prefix,
		Owner: r.identity.Owner, Sources: []pushSourceResult{}}
	if r.identity.Platform != nil {
		record.Platform = r.identity.Platform.Name
	}
	fail := func(err error) pushPassRecord {
		record.Error, record.Refused = err.Error(), pushRefused(err)
		record.FinishedAt = pushNow().UTC().Format(time.RFC3339)
		return record
	}
	host := r.host()
	previousHost := currentHost
	currentHost = func() Host { return host }
	defer func() { currentHost = previousHost }()

	sources := pushSources(r.config)
	capture := defaultConfig(filepath.Join(r.options.StateDir, "archive.toml"))
	capture.CatalogPath = filepath.Join(r.options.StateDir, "catalog.sqlite3")
	capture.ArchiveRoot = filepath.Join(r.options.StateDir, "preserved")
	capture.StagingRoot = filepath.Join(r.options.StateDir, "staging")
	capture.CaptureRoot, capture.Sources = r.options.captureRoot(), sources
	summary, err := Capture(ctx, capture, sources, nil)
	if err != nil {
		return fail(fmt.Errorf("capture: %w", err))
	}
	for _, problem := range summary.Errors {
		record.Warnings = append(record.Warnings, fmt.Sprintf("%s: %s", problem.Path, problem.Error))
	}
	if r.state.HelloAt == "" {
		if err := r.hello(ctx, sources); err != nil {
			return fail(err)
		}
	}
	retention := time.Duration(r.config.RetentionDays) * 24 * time.Hour
	if retention > 0 {
		// The bucket has dropped these by now.
		expired := pushNow().Add(-retention).Unix()
		for sha, at := range r.state.Chunks {
			if at < expired {
				delete(r.state.Chunks, sha)
			}
		}
	}
	var failed error
	for _, source := range sources {
		if _, err := os.Stat(filepath.Join(r.options.captureRoot(), host.ID, source.Name, captureManifestName)); err != nil {
			continue
		}
		result, err := r.pushSource(ctx, host.ID, source, retention)
		if err != nil {
			result.Error = err.Error()
			failed = errors.Join(failed, fmt.Errorf("%s: %w", source.Name, err))
		}
		record.Sources = append(record.Sources, result)
		// Chunks uploaded so far stay known if the next source fails.
		if err := r.state.save(r.options.StateDir); err != nil {
			return fail(err)
		}
		if ctx.Err() != nil {
			break
		}
	}
	if failed != nil {
		return fail(failed)
	}
	record.OK, record.FinishedAt = true, pushNow().UTC().Format(time.RFC3339)
	return record
}

func (r *pushRun) pushSource(ctx context.Context, host string, source SourceConfig, retention time.Duration) (pushSourceResult, error) {
	result := pushSourceResult{Name: source.Name, Kind: source.Kind}
	known := r.state.Sources[source.Name]
	if known == nil {
		known = &pushSourceState{}
	}
	sink := &pushSink{ctx: ctx, store: r.store, prefix: r.identity.Prefix, state: r.state, retention: retention, previous: known.Files,
		files: map[string]pushFileState{}, members: map[string][]pushChunk{}, metadata: map[string][]byte{}, result: &result}
	envelope, err := streamCaptureExport(ctx, r.options.captureRoot(), host, source.Name, sink)
	if err != nil {
		return result, err
	}
	if envelope.Files == nil {
		envelope.Files = []CaptureExportFile{}
	}
	content := sha256.New()
	for _, file := range envelope.Files {
		if _, metadata := sink.metadata[file.Path]; !metadata {
			result.Files++
			fmt.Fprintf(content, "%s\x00%s\n", file.Path, file.SHA256)
		}
	}
	known.Files = sink.files
	r.state.Sources[source.Name] = known
	result.ID = envelope.ID
	// Only transcripts make a new export, and a source that has none yet
	// makes no first one.
	digest := hex.EncodeToString(content.Sum(nil))
	if digest == known.Content || known.LastKey == "" && result.Files == 0 {
		result.Unchanged = true
		return result, nil
	}
	if err := sink.uploadMetadata(); err != nil {
		return result, err
	}
	doc := pushExportDoc{Contract: pushContract, Pusher: r.state.Pusher, Pool: r.identity.Pool, RetentionDays: r.config.RetentionDays,
		Envelope: envelope, Chunking: pushChunking, Members: []pushMember{},
		Platform: r.identity.Platform, Initiator: r.identity.Initiator, Owner: r.identity.Owner}
	for _, file := range envelope.Files {
		member := pushMember{Path: file.Path, Chunks: []pushDocChunk{}}
		for _, chunk := range sink.members[file.Path] {
			member.Chunks = append(member.Chunks, pushDocChunk{SHA256: "sha256:" + chunk.SHA256, Size: chunk.Size})
		}
		doc.Members = append(doc.Members, member)
	}
	at := pushNow().UTC()
	doc.PushedAt = at.Format(time.RFC3339)
	if known.LastKey != "" {
		doc.Previous = &known.LastKey
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return result, err
	}
	name := at.Format("20060102T150405Z") + "-" + envelope.ID + ".json"
	key := pushKey(r.identity.Prefix, "exports", r.state.Pusher, source.Name, name)
	// Its chunks are all in the bucket by now, so a reader that sees this
	// export can assemble it.
	if err := r.store.put(ctx, key, data); err != nil {
		return result, err
	}
	known.LastKey, known.Content, result.Export = name, digest, key
	return result, nil
}

// assemblePushExport rebuilds, in the empty folder dir, the capture export
// a pushed export describes, fetching each chunk with get, and verifies it.
func assemblePushExport(ctx context.Context, get func(ctx context.Context, key string) ([]byte, error), prefix string, doc pushExportDoc, dir string) (CaptureExport, error) {
	if doc.Contract != pushContract || doc.Chunking != pushChunking {
		return CaptureExport{}, fmt.Errorf("a pushed export of contract %q and chunking %q cannot be read", doc.Contract, doc.Chunking)
	}
	if len(doc.Members) != len(doc.Envelope.Files) {
		return CaptureExport{}, errors.New("the pushed export's members do not match its envelope")
	}
	for i, member := range doc.Members {
		file := doc.Envelope.Files[i]
		if member.Path != file.Path || !fs.ValidPath(member.Path) || !strings.HasPrefix(member.Path, captureExportCaptures+"/") {
			return CaptureExport{}, fmt.Errorf("the pushed export's member %q does not match its envelope", member.Path)
		}
		target := filepath.Join(dir, filepath.FromSlash(member.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return CaptureExport{}, err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return CaptureExport{}, err
		}
		for _, chunk := range member.Chunks {
			sha := strings.TrimPrefix(chunk.SHA256, "sha256:")
			if !validExportHash(sha) {
				out.Close()
				return CaptureExport{}, fmt.Errorf("%s names a chunk that is not a SHA-256", member.Path)
			}
			data, err := get(ctx, pushKey(prefix, "chunks", sha))
			if err == nil {
				if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != sha || int64(len(data)) != chunk.Size {
					err = fmt.Errorf("chunk %s does not hold the bytes it is named for", sha)
				} else {
					_, err = out.Write(data)
				}
			}
			if err != nil {
				out.Close()
				return CaptureExport{}, err
			}
		}
		if err := out.Close(); err != nil {
			return CaptureExport{}, err
		}
	}
	envelope, err := doc.Envelope.canonical()
	if err != nil {
		return CaptureExport{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, captureExportEnvelope), append(envelope, '\n'), 0o600); err != nil {
		return CaptureExport{}, err
	}
	return VerifyCaptureExport(ctx, dir)
}

// Running passes: one at a time for a state folder, and the last request
// always gets a pass that began after it.

func lockPush(options pushOptions, wait filelock.WaitMode) (*os.File, error) {
	if err := os.MkdirAll(options.StateDir, 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(options.StateDir, "push.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := filelock.Lock(lock, filelock.Exclusive, wait); err != nil {
		lock.Close()
		if errors.Is(err, filelock.ErrContended) {
			return nil, errPushRunning
		}
		return nil, err
	}
	return lock, nil
}

func pushPending(options pushOptions) bool {
	_, err := os.Stat(options.pendingPath())
	return err == nil
}

func requestPush(options pushOptions) error {
	if err := os.MkdirAll(options.StateDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(options.pendingPath(), nil, 0o600)
}

// drainPush runs passes until none is asked for. In the background it
// leaves a running push alone, which will see the request; in the
// foreground it waits its turn.
func drainPush(ctx context.Context, options pushOptions, foreground bool) (pushPassRecord, error) {
	var record pushPassRecord
	wait := filelock.Try
	if foreground {
		wait = filelock.Wait
	}
	for {
		lock, err := lockPush(options, wait)
		if err != nil {
			return record, err
		}
		for {
			_ = os.Remove(options.pendingPath())
			record, err = pushOnce(ctx, options, foreground)
			if err != nil || ctx.Err() != nil || !pushPending(options) {
				break
			}
		}
		lock.Close()
		// A request made after the last look and before the unlock found the
		// lock held and left; it is served here.
		if err != nil || ctx.Err() != nil || !pushPending(options) {
			return record, err
		}
	}
}

func sleepContext(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// pushOnce makes one pass and records it. The error it returns is the
// pass's own; a pass skipped because the destination keeps refusing is not
// an error.
func pushOnce(ctx context.Context, options pushOptions, foreground bool) (pushPassRecord, error) {
	run, err := preparePush(options)
	if err != nil {
		record := pushPassRecord{StartedAt: pushNow().UTC().Format(time.RFC3339), FinishedAt: pushNow().UTC().Format(time.RFC3339), Error: err.Error(), Sources: []pushSourceResult{}}
		pushLog(options, record)
		return record, err
	}
	if last := run.state.Last; last != nil && !foreground {
		started, _ := time.Parse(time.RFC3339, last.StartedAt)
		since := pushNow().Sub(started)
		if run.state.Refusals >= pushRefusalLimit && since < pushRefusalBackoff {
			return *last, nil
		}
		if err := sleepContext(ctx, pushMinInterval-since); err != nil {
			return *last, err
		}
	}
	record := run.pass(ctx)
	switch {
	case record.Refused:
		run.state.Refusals++
		if run.state.Refusals == pushRefusalLimit {
			record.Warnings = append(record.Warnings, fmt.Sprintf("The destination has refused %d pushes in a row. Background pushes now try once a day; run `pharos capture-push --check` after fixing access.", pushRefusalLimit))
		}
	case record.OK:
		run.state.Refusals = 0
	}
	run.state.Last = &record
	if err := run.state.save(options.StateDir); err != nil && record.Error == "" {
		record.OK, record.Error = false, err.Error()
	}
	pushLog(options, record)
	if record.Error != "" {
		return record, errors.New(record.Error)
	}
	return record, nil
}

const pushLogLimit = 1 << 20

// pushLog appends one line per pass to <state>/push.log, which
// `capture-push --status` reads, keeping one earlier file.
func pushLog(options pushOptions, record pushPassRecord) {
	file := filepath.Join(options.StateDir, "push.log")
	if info, err := os.Stat(file); err == nil && info.Size() > pushLogLimit {
		_ = os.Rename(file, file+".1")
	}
	line, err := json.Marshal(record)
	if err != nil || os.MkdirAll(options.StateDir, 0o700) != nil {
		return
	}
	if log, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
		_, _ = log.Write(append(line, '\n'))
		log.Close()
	}
}

// spawnPushWorker starts a push that outlives this process and its
// terminal, so the agent hook that asked for it returns at once.
func spawnPushWorker(options pushOptions) error {
	executable := runningExecutable()
	if executable == "" {
		return errors.New("this program cannot find itself")
	}
	worker := exec.CommandContext(context.Background(), executable, "capture-push", "--background", "--config", options.ConfigPath, "--state", options.StateDir)
	if err := configureLoginShellProcess(worker); err != nil {
		return err
	}
	if err := worker.Start(); err != nil {
		return err
	}
	return worker.Process.Release()
}

// watchPush stays running: a pass now, then one whenever a transcript has
// changed and stopped changing, or has kept changing for a whole interval.
func watchPush(ctx context.Context, options pushOptions) error {
	sources := func() string {
		config, err := readPushConfig(options.ConfigPath)
		if err != nil {
			return ""
		}
		return pushSignature(pushSources(config))
	}
	pushed := sources()
	_, _ = drainPush(ctx, options, false)
	seen, since := pushed, time.Time{}
	for {
		if err := sleepContext(ctx, pushWatchPoll); err != nil {
			return nil
		}
		current := sources()
		if current == pushed && !pushPending(options) {
			seen, since = current, time.Time{}
			continue
		}
		if since.IsZero() {
			since = pushNow()
		}
		if settled := current == seen; !settled && pushNow().Sub(since) < pushMinInterval {
			seen = current
			continue
		}
		pushed, seen, since = current, current, time.Time{}
		if _, err := drainPush(ctx, options, false); errors.Is(err, errPushRunning) {
			// Whoever holds the lock may have started before this change.
			_ = requestPush(options)
		}
	}
}

type pushStatus struct {
	Config      string             `json:"config"`
	State       string             `json:"state"`
	Pusher      string             `json:"pusher,omitempty"`
	Sources     []pushStatusSource `json:"sources"`
	Pending     bool               `json:"pending"`
	Paused      string             `json:"paused,omitempty"`
	ChunksKnown int                `json:"chunks_known"`
	Last        *pushPassRecord    `json:"last,omitempty"`
	Problem     string             `json:"problem,omitempty"`
}

type pushStatusSource struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
}

func readPushStatus(options pushOptions) pushStatus {
	status := pushStatus{Config: options.ConfigPath, State: options.StateDir, Sources: []pushStatusSource{}, Pending: pushPending(options)}
	config, err := readPushConfig(options.ConfigPath)
	if err != nil {
		status.Problem = err.Error()
	} else {
		for _, source := range pushSources(config) {
			status.Sources = append(status.Sources, pushStatusSource{Name: source.Name, Kind: source.Kind, Path: source.Path})
		}
		if _, err := resolvePushIdentity(config); err != nil {
			status.Problem = err.Error()
		}
	}
	state, err := loadPushState(options.StateDir)
	if err != nil {
		status.Problem = err.Error()
		return status
	}
	status.Pusher, status.ChunksKnown, status.Last = state.Pusher, len(state.Chunks), state.Last
	if state.Refusals >= pushRefusalLimit {
		status.Paused = fmt.Sprintf("The destination refused the last %d pushes, so background pushes try once a day. Fix access, then run `pharos capture-push --check`.", state.Refusals)
	}
	return status
}

type pushCheckResult struct {
	OK       bool               `json:"ok"`
	Pusher   string             `json:"pusher"`
	Prefix   string             `json:"prefix"`
	Owner    string             `json:"owner,omitempty"`
	Platform *pushPlatform      `json:"platform,omitempty"`
	Sources  []pushStatusSource `json:"sources"`
	Hello    string             `json:"hello"`
}

// checkPush proves the configuration end to end without waiting for a
// conversation: it writes this pusher's hello, which setup looks for.
func checkPush(ctx context.Context, options pushOptions) (pushCheckResult, error) {
	lock, err := lockPush(options, filelock.Wait)
	if err != nil {
		return pushCheckResult{}, err
	}
	defer lock.Close()
	run, err := preparePush(options)
	if err != nil {
		return pushCheckResult{}, err
	}
	sources := pushSources(run.config)
	result := pushCheckResult{Pusher: run.state.Pusher, Prefix: run.identity.Prefix, Owner: run.identity.Owner, Platform: run.identity.Platform,
		Sources: []pushStatusSource{}, Hello: pushKey(run.identity.Prefix, "hello", run.state.Pusher+".json")}
	for _, source := range sources {
		result.Sources = append(result.Sources, pushStatusSource{Name: source.Name, Kind: source.Kind, Path: source.Path})
	}
	if err := run.hello(ctx, sources); err != nil {
		hint := ""
		if run.identity.Platform != nil {
			hint = fmt.Sprintf(" If %s limits outbound network access, allow the bucket's host and the credential's sign-in host.", run.identity.Platform.Name)
		}
		return result, fmt.Errorf("the bucket could not be written to: %w.%s", err, hint)
	}
	// Access works again, whatever earlier passes met.
	run.state.Refusals = 0
	if err := run.state.save(options.StateDir); err != nil {
		return result, err
	}
	result.OK = true
	return result, nil
}

// runCapturePushCLI is pharos capture-push.
func runCapturePushCLI(args []string) error {
	usage := errors.New("usage: pharos capture-push [--config FILE] [--state DIR] [--wait | --watch | --check | --status]")
	flags := flag.NewFlagSet("capture-push", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config, state := &onceString{}, &onceString{}
	flags.Var(config, "config", "push configuration")
	flags.Var(state, "state", "state folder")
	wait := flags.Bool("wait", false, "")
	watch := flags.Bool("watch", false, "")
	check := flags.Bool("check", false, "")
	status := flags.Bool("status", false, "")
	background := flags.Bool("background", false, "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return usage
	}
	modes := 0
	for _, mode := range []bool{*wait, *watch, *check, *status, *background} {
		if mode {
			modes++
		}
	}
	if modes > 1 {
		return usage
	}
	options := pushOptions{ConfigPath: config.value, StateDir: state.value}.resolved()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch {
	case *status:
		return printJSON(readPushStatus(options))
	case *check:
		result, err := checkPush(ctx, options)
		if err != nil {
			return err
		}
		return printJSON(result)
	case *wait:
		record, err := drainPush(ctx, options, true)
		if printErr := printJSON(record); err == nil {
			err = printErr
		}
		return err
	case *watch:
		return watchPush(ctx, options)
	case *background:
		if _, err := drainPush(ctx, options, false); err != nil && !errors.Is(err, errPushRunning) {
			return err
		}
		return nil
	}
	// The default is what an agent hook runs after every turn: ask for a
	// push, return at once, and never fail the turn. What went wrong is in
	// the log --status reads.
	if err := requestPush(options); err != nil {
		return nil
	}
	if err := pushSpawn(options); err != nil {
		_, _ = drainPush(ctx, options, false)
	}
	return nil
}

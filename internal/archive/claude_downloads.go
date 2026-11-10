package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Watching Downloads is opt-in per Mac. Pharos lists the folder's top level,
// opens only files named like Claude's export manifest, and reads the ZIPs
// that a recognized manifest lists. It never reads other files there.
var claudeManifestFilename = regexp.MustCompile(`(?i)^manifest-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}-[^/]*\.json$`)

// A link Pharos opened has this long to start arriving. An in-progress browser
// download extends it, so a large file is never abandoned mid-transfer.
var claudeArrivalTimeout = 3 * time.Minute

const claudeLinkLifetime = 24 * time.Hour

type claudeFetch struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Manifest   string `json:"manifest"`
	ExportedAt string `json:"exported_at"`
	State      string `json:"state"`
	Message    string `json:"message,omitempty"`
	Waiting    string `json:"waiting,omitempty"`
	// Account is the Claude account or organization the export's links
	// belong to; Browser is the profile key its links open in ("" is the
	// default browser).
	Account string `json:"account,omitempty"`
	Browser string `json:"browser"`
	// Revision counts the person's decisions; a check that started before
	// one never overwrites it.
	Revision int `json:"revision"`
	// BrowserLabel is filled in for Settings, never stored.
	BrowserLabel string `json:"browser_label,omitempty"`
	// WaitingFile names the awaited part for people; Waiting is its ID.
	WaitingFile string            `json:"waiting_file,omitempty"`
	Opened      map[string]string `json:"opened"`
	Collected   int               `json:"collected"`
	Expected    int               `json:"expected"`
	UpdatedAt   string            `json:"updated_at"`
}

// Fetch states. Only "fetching" advances on its own; the others wait for the
// person (paused), a new export (expired) or nothing at all (imported).
const (
	claudeFetchActive   = "fetching"
	claudeFetchPaused   = "paused"
	claudeFetchExpired  = "expired"
	claudeFetchImported = "imported"
	claudeFetchStopped  = "stopped"
)

type claudeDownloads struct {
	// mu guards the state file and the fields below; it is never held while
	// collecting or indexing, so Settings stays responsive. run serializes
	// checks.
	mu  sync.Mutex
	run sync.Mutex
	// working and stopWork name and cancel the export a check is advancing,
	// so Stop interrupts a long collection or index.
	working  string
	stopWork context.CancelFunc
	// dir is Downloads. State and the setting live beside the host-local
	// download links, outside the library: permission to read this Mac's
	// Downloads does not travel with the library to another Mac.
	dir     string
	name    string
	config  func() Config
	open    func(browser, link string) error
	collect func(context.Context, claudeImportRequest) (*claudeImportJob, error)
	index   func(context.Context, string) error
	now     func() time.Time
	// problem is the last check's folder error, such as macOS refusing access.
	problem string
}

var errClaudeImportBusy = errors.New("the library is busy with another capture or index")

var errBrowserOpenUnsupported = fmt.Errorf("opening download links in your browser needs macOS: %w", errors.ErrUnsupported)

func browserOpenAvailable() bool { return browserOpenSupported }

// openBrowserURL opens only https links, so a manifest can never make Launch
// Services run a local file or another URL scheme's handler.
var openBrowserURL = func(link string) error {
	if safeClaudeDownloadURL(link) == "" || strings.HasPrefix(link, "-") {
		return errors.New("Pharos opens only Claude https download links")
	}
	return browserOpenNative(link)
}

func claudeDownloadsFolder() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Downloads")
}

func claudeDownloadsState(config Config) string {
	return filepath.Join(filepath.Dir(claudeImportSecrets(config, "x")), "downloads-watch.json")
}

type claudeWatchSetting struct {
	Enabled   bool   `json:"enabled"`
	ChangedAt string `json:"changed_at,omitempty"`
	// Browser is the profile key new exports open in; Accounts remembers,
	// per Claude account, the profile whose download actually arrived.
	Browser  string            `json:"browser"`
	Accounts map[string]string `json:"accounts,omitempty"`
}

// claudeExportAccount is the account (or organization) UUID in the export's
// download links, else in Claude's manifest filename.
var claudeExportLinkAccount = regexp.MustCompile(`^/export/([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})/`)

func claudeExportAccount(manifest claudeManifest, file string) string {
	for _, part := range manifest.DataFiles {
		if parsed, err := url.Parse(part.URL); err == nil {
			if match := claudeExportLinkAccount.FindStringSubmatch(parsed.Path); match != nil {
				return strings.ToLower(match[1])
			}
		}
	}
	if name := filepath.Base(file); claudeManifestFilename.MatchString(name) {
		return strings.ToLower(name[len("manifest-") : len("manifest-")+36])
	}
	return ""
}

func claudeWatchSettingPath(config Config) string {
	return filepath.Join(filepath.Dir(claudeImportSecrets(config, "x")), "downloads-watch-setting.json")
}

// Off unless this Mac's person turned it on for this library.
func loadClaudeWatchSetting(config Config) claudeWatchSetting {
	var setting claudeWatchSetting
	if content, err := os.ReadFile(claudeWatchSettingPath(config)); err == nil {
		_ = json.Unmarshal(content, &setting)
	}
	return setting
}

// The setting is read, changed and written by the service's handlers and its
// watch; one lock keeps a remembered profile from undoing a switch turned off.
var claudeWatchSettingMu sync.Mutex

func updateClaudeWatchSetting(config Config, change func(*claudeWatchSetting)) (claudeWatchSetting, error) {
	claudeWatchSettingMu.Lock()
	defer claudeWatchSettingMu.Unlock()
	setting := loadClaudeWatchSetting(config)
	change(&setting)
	return setting, saveClaudeWatchSetting(config, setting)
}

func saveClaudeWatchSetting(config Config, setting claudeWatchSetting) error {
	name := claudeWatchSettingPath(config)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	setting.ChangedAt = now()
	return writeFileDurably(name, []byte(jsonText(setting)))
}

func (d *claudeDownloads) load() map[string]*claudeFetch {
	fetches := map[string]*claudeFetch{}
	if content, err := os.ReadFile(claudeDownloadsState(d.config())); err == nil {
		_ = json.Unmarshal(content, &fetches)
	}
	for id, fetch := range fetches {
		if fetch == nil || fetch.ID != id {
			delete(fetches, id)
		} else if fetch.Opened == nil {
			fetch.Opened = map[string]string{}
		}
	}
	return fetches
}

func (d *claudeDownloads) save(fetches map[string]*claudeFetch) error {
	name := claudeDownloadsState(d.config())
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	return writeFileDurably(name, []byte(jsonText(fetches)))
}

// Fetches lists recent watch activity, newest export first, for Settings.
func (d *claudeDownloads) Fetches() []claudeFetch {
	d.mu.Lock()
	defer d.mu.Unlock()
	list := []claudeFetch{}
	for _, fetch := range d.load() {
		list = append(list, *fetch)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].ExportedAt != list[j].ExportedAt {
			return list[i].ExportedAt > list[j].ExportedAt
		}
		return list[i].ID < list[j].ID
	})
	return list
}

// Act applies the person's decision to an export. Retrying forgets the
// unanswered links of parts not yet collected, so they are opened again; links
// may be single-use, so Pharos never does that on its own. Stop interrupts
// a collection or index in progress.
func (d *claudeDownloads) Act(id, action string, browser *string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	fetches := d.load()
	fetch := fetches[id]
	if fetch == nil {
		return errors.New("that Claude export is not being watched")
	}
	switch action {
	case "retry":
		if fetch.State == claudeFetchImported || fetch.State == claudeFetchExpired {
			return fmt.Errorf("this export is %s", fetch.State)
		}
		collected := map[string]bool{}
		if job, err := loadClaudeJob(d.config(), d.name, id); err == nil {
			for _, part := range job.Parts {
				collected[part.ID] = part.State == "collected"
			}
		}
		for part := range fetch.Opened {
			if !collected[part] {
				delete(fetch.Opened, part)
			}
		}
		// Another signed-in profile is the usual fix for a link that
		// delivered nothing; the rest of this export opens there too.
		if browser != nil {
			fetch.Browser = *browser
		}
		fetch.State, fetch.Message, fetch.Waiting, fetch.WaitingFile = claudeFetchActive, "", "", ""
	case "stop":
		if fetch.State == claudeFetchImported {
			return errors.New("this export is already imported")
		}
		fetch.State, fetch.Message, fetch.Waiting, fetch.WaitingFile = claudeFetchStopped, "Stopped. Files already collected are kept.", "", ""
		if d.working == id && d.stopWork != nil {
			d.stopWork()
		}
	default:
		return errors.New("choose retry or stop")
	}
	fetch.Revision++
	fetch.UpdatedAt = formatTime(d.now())
	return d.save(fetches)
}

// claudeDownloadsManifests returns recognized manifests at the top level of
// dir with their content hash. Other files are listed by name only.
func claudeDownloadsManifests(dir string) (map[string]string, map[string]claudeManifest, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	hashes, manifests := map[string]string{}, map[string]claudeManifest{}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !claudeManifestFilename.MatchString(entry.Name()) {
			continue
		}
		file := filepath.Join(dir, entry.Name())
		manifest, content, ok, err := readClaudeManifest(file)
		if err != nil || !ok || manifest.CreatedAt == "" || !claudeManifestDownloadable(manifest) {
			continue
		}
		hashes[file], manifests[file] = hashBytes(content), manifest
	}
	return hashes, manifests, nil
}

// Every part of an export manifest is a Claude download link; a file that
// merely shares the name pattern is not one.
// Parts must be Claude-shaped names at the top of Downloads, such as
// conversations-000.zip, so a planted manifest cannot select other files.
func claudeManifestDownloadable(manifest claudeManifest) bool {
	for _, part := range manifest.DataFiles {
		if safeClaudeDownloadURL(part.URL) == "" || !claudePartFilename.MatchString(part.Filename) {
			return false
		}
	}
	return true
}

var claudePartFilename = regexp.MustCompile(`^[a-z0-9_]+-[0-9]{1,6}\.zip$`)

func claudeDownloadInProgress(dir string, since time.Time) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".crdownload" && ext != ".download" && ext != ".part" {
			continue
		}
		if info, err := entry.Info(); err == nil && info.ModTime().After(since) {
			return true
		}
		// Safari's .download is a folder whose own time does not move while
		// the file inside it grows.
		if entry.IsDir() {
			inner, _ := os.ReadDir(filepath.Join(dir, entry.Name()))
			for _, item := range inner {
				if info, err := item.Info(); err == nil && info.ModTime().After(since) {
					return true
				}
			}
		}
	}
	return false
}

// claudeWatchKeep bounds the exports listed and remembered.
const claudeWatchKeep = 20

// Check advances at most one export by one step: collect what has arrived,
// then open the next link. It returns whether anything remains in progress.
func (d *claudeDownloads) Check(ctx context.Context) (bool, error) {
	d.run.Lock()
	defer d.run.Unlock()
	d.mu.Lock()
	hashes, manifests, err := claudeDownloadsManifests(d.dir)
	d.problem = ""
	if err != nil {
		d.problem = "Pharos cannot read your Downloads folder: " + err.Error()
		if errors.Is(err, fs.ErrPermission) {
			d.problem = "macOS has not let Pharos read your Downloads folder. Allow it in System Settings → Privacy & Security → Files and Folders."
		}
		d.mu.Unlock()
		return false, nil
	}
	fetches := d.load()
	now := d.now()
	present := map[string]bool{}
	for _, id := range hashes {
		present[id] = true
	}
	changed := false
	for _, fetch := range fetches {
		// An export whose manifest left Downloads cannot advance.
		if fetch.State == claudeFetchActive && !present[fetch.ID] {
			fetch.State, fetch.Message, fetch.Waiting, fetch.WaitingFile = claudeFetchStopped, "The manifest is no longer in Downloads. Files already collected are kept.", "", ""
			changed = true
		}
	}
	if claudePruneFetches(fetches) {
		changed = true
	}
	files := make([]string, 0, len(hashes))
	for file := range hashes {
		files = append(files, file)
	}
	// Oldest export first: its links expire first.
	sort.Slice(files, func(i, j int) bool {
		a, b := manifests[files[i]].CreatedAt, manifests[files[j]].CreatedAt
		return a < b || a == b && files[i] < files[j]
	})
	var work *claudeFetch
	var exported time.Time
	for _, file := range files {
		id, manifest := hashes[file], manifests[file]
		fetch := fetches[id]
		if fetch != nil && fetch.State != claudeFetchActive {
			continue
		}
		exported, _ = time.Parse(time.RFC3339Nano, manifest.CreatedAt)
		if fetch == nil {
			// Turning the watch on is not a request to revisit old exports.
			if now.After(exported.Add(claudeLinkLifetime)) {
				continue
			}
			// The profile that last delivered this account's export wins
			// over the general choice, so two Claude accounts in two
			// profiles each keep their own.
			setting := loadClaudeWatchSetting(d.config())
			account := claudeExportAccount(manifest, file)
			browser, remembered := setting.Accounts[account]
			if !remembered {
				browser = setting.Browser
			}
			fetch = &claudeFetch{ID: id, Name: d.name, ExportedAt: manifest.CreatedAt, State: claudeFetchActive, Opened: map[string]string{}, Account: account, Browser: browser}
			fetches[id] = fetch
			changed = true
		}
		fetch.Manifest = file
		copied := *fetch
		copied.Opened = map[string]string{}
		for part, at := range fetch.Opened {
			copied.Opened[part] = at
		}
		work = &copied
		break
	}
	if changed {
		if err := d.save(fetches); err != nil {
			d.mu.Unlock()
			return false, err
		}
	}
	if work == nil {
		d.mu.Unlock()
		return false, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.working, d.stopWork = work.ID, cancel
	d.mu.Unlock()

	next, err := d.advance(ctx, work, exported, now)

	d.mu.Lock()
	defer d.mu.Unlock()
	d.working, d.stopWork = "", nil
	fetches = d.load()
	current := fetches[work.ID]
	if current == nil || current.Revision != work.Revision {
		// The person decided while this step ran: keep their decision and
		// record only what was collected.
		if current != nil && work.Expected > 0 {
			current.Collected, current.Expected = work.Collected, work.Expected
			err = errors.Join(err, d.save(fetches))
		}
		return current != nil && current.State == claudeFetchActive, err
	}
	work.UpdatedAt = formatTime(now)
	fetches[work.ID] = work
	if next != nil && work.State == claudeFetchActive {
		// Record the attempt before opening: a crash must not open a
		// possibly single-use link twice.
		work.Opened[next.part], work.Waiting, work.WaitingFile = formatTime(now), next.part, next.file
		if saveErr := d.save(fetches); saveErr != nil {
			return false, errors.Join(err, saveErr)
		}
		if openErr := d.open(work.Browser, next.link); openErr != nil {
			work.State, work.Message = claudeFetchPaused, "Pharos could not open "+browserProfileLabel(work.Browser, listBrowserProfiles())+": "+openErr.Error()
		}
	}
	err = errors.Join(err, d.save(fetches))
	return work.State == claudeFetchActive, err
}

// claudePruneFetches forgets the oldest settled exports beyond the list's
// bound. Collected files and import jobs are untouched.
func claudePruneFetches(fetches map[string]*claudeFetch) bool {
	var settled []*claudeFetch
	for _, fetch := range fetches {
		if fetch.State != claudeFetchActive && fetch.State != claudeFetchPaused {
			settled = append(settled, fetch)
		}
	}
	if len(fetches) <= claudeWatchKeep || len(settled) == 0 {
		return false
	}
	sort.Slice(settled, func(i, j int) bool { return settled[i].ExportedAt < settled[j].ExportedAt })
	for _, fetch := range settled[:min(len(settled), len(fetches)-claudeWatchKeep)] {
		delete(fetches, fetch.ID)
	}
	return true
}

type claudeNextLink struct{ part, file, link string }

// advance works on a copy of the export, without the state lock: it collects
// what arrived, indexes a complete export, and names the next link to open.
func (d *claudeDownloads) advance(ctx context.Context, fetch *claudeFetch, exported, now time.Time) (*claudeNextLink, error) {
	config := d.config()
	if fetch.Waiting != "" {
		opened, _ := time.Parse(time.RFC3339Nano, fetch.Opened[fetch.Waiting])
		if !d.arrived(config, fetch) {
			if now.Before(opened.Add(claudeArrivalTimeout)) || claudeDownloadInProgress(d.dir, now.Add(-claudeArrivalTimeout)) {
				return nil, nil
			}
			fetch.State = claudeFetchPaused
			fetch.Message = "Pharos opened " + fetch.WaitingFile + " in " + browserProfileLabel(fetch.Browser, listBrowserProfiles()) + ", but no file arrived. If another browser or profile is signed in to this Claude account, choose it and try again. The browser must save to Downloads."
			return nil, nil
		}
		if err := d.remember(config, fetch); err != nil {
			return nil, err
		}
	}
	// Collecting re-verifies earlier parts and picks up anything that arrived.
	job, err := d.collect(ctx, claudeImportRequest{Paths: []string{fetch.Manifest}, Name: d.name})
	if job == nil || errors.Is(err, context.Canceled) {
		// Nothing was collected: the library is busy, unavailable or
		// stopping. The next check tries again.
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, errClaudeImportBusy) {
			fetch.Message = err.Error()
		}
		return nil, nil
	}
	fetch.Waiting, fetch.WaitingFile, fetch.Collected, fetch.Expected = "", "", 0, 0
	for _, part := range job.Parts {
		if part.State != claudePartExcluded {
			fetch.Expected++
		}
		if part.State == "collected" {
			fetch.Collected++
		}
	}
	if job.State == "collected" {
		if err := d.index(ctx, job.Name); errors.Is(err, errClaudeImportBusy) || errors.Is(err, context.Canceled) {
			fetch.Message = "Every file is collected. Indexing starts when the library is free."
			return nil, nil
		} else if err != nil {
			fetch.State, fetch.Message = claudeFetchImported, "Every file is collected. Indexing did not finish and runs with the next update: "+err.Error()
			return nil, nil
		}
		fetch.State, fetch.Message = claudeFetchImported, ""
		return nil, nil
	}
	if err != nil {
		fetch.State, fetch.Message = claudeFetchPaused, err.Error()
		return nil, nil
	}
	links := loadClaudeLinks(config, job.ID)
	for _, part := range job.Parts {
		if part.State == "collected" || part.State == claudePartExcluded {
			continue
		}
		link := safeClaudeDownloadURL(links[part.ID])
		if part.State != "missing" || fetch.Opened[part.ID] != "" || link == "" {
			// A damaged, ambiguous or unanswered part needs a decision.
			message := part.Filename + ": " + part.Error
			if part.State == "failed" || part.State == "damaged" {
				message += " Move that file out of Downloads, then try again."
			}
			fetch.State, fetch.Message = claudeFetchPaused, message
			return nil, nil
		}
		if now.After(exported.Add(claudeLinkLifetime)) {
			fetch.State, fetch.Message = claudeFetchExpired, "The download links expired before every file arrived. Request a new export from Claude; collected files are kept."
			return nil, nil
		}
		return &claudeNextLink{part: part.ID, file: part.Filename, link: link}, nil
	}
	return nil, nil
}

// remember records the profile that delivered a file Pharos asked for, so
// this account's later exports open there without asking again.
func (d *claudeDownloads) remember(config Config, fetch *claudeFetch) error {
	if fetch.Account == "" {
		return nil
	}
	if current, ok := loadClaudeWatchSetting(config).Accounts[fetch.Account]; ok && current == fetch.Browser {
		return nil
	}
	_, err := updateClaudeWatchSetting(config, func(setting *claudeWatchSetting) {
		if setting.Accounts == nil {
			setting.Accounts = map[string]string{}
		}
		setting.Accounts[fetch.Account] = fetch.Browser
	})
	return err
}

// arrived reports whether the awaited part has a finished candidate file.
func (d *claudeDownloads) arrived(config Config, fetch *claudeFetch) bool {
	job, err := loadClaudeJob(config, d.name, fetch.ID)
	if err != nil {
		return false
	}
	for _, part := range job.Parts {
		if part.ID == fetch.Waiting {
			return len(claudePartCandidates(*job, part, nil)) > 0
		}
	}
	return false
}

package archive

// Historical preservation is an explicit operator job. Its inventory, receipts
// and cursors live on the capture drive, independently of the live catalog.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const historyDir = ".history-backfill"
const historyFormat = 1

var historyInventoryLimit = 100000
var historyMetadataLimit int64 = 32 << 20
var historyEntityBytes int64 = 16 << 20

// Limits are deliberately hard, and failures never mean an empty population.
// Large libraries can be partitioned by host/source; plan pages are stable
// keyset continuations over a fingerprinted inventory.
type historyScope struct {
	Host   string `json:"host,omitempty"`
	Source string `json:"source,omitempty"`
}
type historyUnit struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	Host     string          `json:"host,omitempty"`
	Source   string          `json:"source,omitempty"`
	Original string          `json:"original,omitempty"`
	Path     string          `json:"path,omitempty"`
	Version  capturedVersion `json:"version,omitempty"`
	Revision *recordObject   `json:"revision,omitempty"`
	Gap      string          `json:"gap,omitempty"`
}
type historySource struct {
	Host         Host              `json:"host"`
	Source       captureSourceInfo `json:"source"`
	ManifestHash string            `json:"manifest_sha256"`
}
type historyPlan struct {
	RootIdentity   string            `json:"capture_root_identity"`
	Version        int               `json:"version"`
	ID             string            `json:"id"`
	Scope          historyScope      `json:"scope"`
	CaptureRoot    string            `json:"capture_root"`
	CatalogPath    string            `json:"catalog_path"`
	CatalogState   sqliteSourceState `json:"catalog_state"`
	Sources        []historySource   `json:"sources"`
	Units          []historyUnit     `json:"units"`
	NativeBytes    int64             `json:"native_input_bytes"`
	PinnedBytes    int64             `json:"existing_pinned_bytes"`
	CatalogBytes   int64             `json:"catalog_and_wal_bytes"`
	ReserveBytes   int64             `json:"free_space_reserve_bytes"`
	FreeBytes      uint64            `json:"free_bytes"`
	Unknown        []string          `json:"unknown_sizes_or_gaps"`
	Schema         []map[string]any  `json:"catalog_boundary"`
	SnapshotPolicy string            `json:"snapshot_policy"`
}

func historyReadJSON(file string, value any) error {
	f, e := os.Open(file)
	if e != nil {
		return e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return e
	}
	if info.Size() > historyMetadataLimit {
		return errors.New("history metadata exceeds bounded read budget; narrow scope")
	}
	return json.NewDecoder(f).Decode(value)
}
func historyWriteJSON(file string, value any) error {
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	if int64(len(b)) > historyMetadataLimit {
		return errors.New("history metadata exceeds budget; narrow scope")
	}
	slash := filepath.ToSlash(file)
	if index := strings.LastIndex(slash, "/"+historyDir+"/"); index >= 0 {
		anchor := filepath.FromSlash(slash[:index] + "/" + historyDir)
		if info, err := os.Stat(anchor); err != nil || !info.IsDir() {
			return errors.New("history drive is unavailable; no metadata directories were recreated")
		}
	} else if index := strings.LastIndex(slash, "/"+recordDir+"/"); index >= 0 {
		anchor := filepath.FromSlash(slash[:index])
		if _, err := os.Stat(filepath.Join(anchor, captureManifestName)); err != nil {
			return errors.New("source capture drive is unavailable")
		}
	}
	if e = os.MkdirAll(filepath.Dir(file), 0700); e != nil {
		return e
	}
	return writeRecordMetadata(file, b)
}
func historySourceDir(root string, s historySource) string {
	return filepath.Join(root, s.Host.ID, s.Source.Name)
}
func (p historyPlan) source(host, source string) (historySource, error) {
	for _, s := range p.Sources {
		if s.Host.ID == host && s.Source.Name == source {
			return s, nil
		}
	}
	return historySource{}, errors.New("history source is not in plan")
}
func historyUnitID(u historyUnit) string { return u.Kind + ":" + hashBytes([]byte(jsonText(u))) }

func planHistory(ctx context.Context, config Config, scope historyScope) (historyPlan, error) {
	p := historyPlan{Version: historyFormat, Scope: scope, CaptureRoot: canonical(config.CaptureRoot), CatalogPath: canonical(config.CatalogPath), ReserveBytes: captureFreeMargin, SnapshotPolicy: "pause selected native snapshots; fixed cutoff; incremental logical acquisition remains available", Unknown: []string{"logical output, compression ratio, scratch catalog growth and temporary WAL sizes require explicit execution budgets"}}
	var metadataBytes int64
	add := func(u historyUnit) error {
		metadataBytes += int64(len(jsonText(u)))
		if metadataBytes > historyMetadataLimit/2 {
			return errors.New("history inventory metadata budget exhausted; narrow scope")
		}
		if len(p.Units) >= historyInventoryLimit {
			return errors.New("history inventory budget exhausted; narrow host/source scope")
		}
		u.ID = historyUnitID(u)
		p.Units = append(p.Units, u)
		return ctx.Err()
	}
	// No live source discovery: another host's original paths are only labels.
	hosts, e := os.ReadDir(config.CaptureRoot)
	if e != nil {
		return p, fmt.Errorf("capture root unavailable; historical planning cannot infer an empty archive: %w", e)
	}
	rootInfo, e := os.Stat(config.CaptureRoot)
	if e != nil {
		return p, e
	}
	if key, ok := keyOf(rootInfo); ok {
		p.RootIdentity = fmt.Sprintf("%d:%d", key.device, key.inode)
	}
	for _, h := range hosts {
		if !h.IsDir() || !validCaptureName(h.Name()) || strings.HasPrefix(h.Name(), ".") || scope.Host != "" && scope.Host != h.Name() {
			continue
		}
		dirs, e := os.ReadDir(filepath.Join(config.CaptureRoot, h.Name()))
		if e != nil {
			return p, e
		}
		for _, d := range dirs {
			if !d.IsDir() || !validCaptureName(d.Name()) || strings.HasPrefix(d.Name(), ".") || scope.Source != "" && scope.Source != d.Name() {
				continue
			}
			dir := filepath.Join(config.CaptureRoot, h.Name(), d.Name())
			info, statErr := os.Stat(filepath.Join(dir, captureManifestName))
			if statErr == nil && info.Size() > historyMetadataLimit {
				return p, errors.New("capture manifest exceeds history inventory budget; narrow the source")
			}
			m, e := historyLoadManifest(dir)
			if e != nil {
				return p, fmt.Errorf("inventory %s/%s: %w", h.Name(), d.Name(), e)
			}
			if m.Version == 0 {
				continue
			}
			if m.Host.ID != h.Name() || m.Source.Name != d.Name() {
				return p, errors.New("capture source identity mismatch")
			}
			digest, _, e := fileSHA256Context(ctx, filepath.Join(dir, captureManifestName))
			if e != nil {
				return p, e
			}
			p.Sources = append(p.Sources, historySource{Host: m.Host, Source: m.Source, ManifestHash: digest})
			pins, e := loadPreservationPins(dir)
			if e != nil {
				return p, e
			}
			for _, pin := range pins.Pins {
				if pin.ReleasedAt == "" {
					p.PinnedBytes += max(pin.Size, pin.ClosureBytes)
				}
			}
			listed := map[string]bool{}
			knownHashes := map[string]bool{}
			addNative := func(kind, original string, v capturedVersion) error {
				if listed[v.Captured] {
					return nil
				}
				listed[v.Captured] = true
				u := historyUnit{Kind: kind, Host: m.Host.ID, Source: m.Source.Name, Original: original, Path: v.Captured, Version: v}
				hash, size, err := fileSHA256Context(ctx, filepath.Join(dir, filepath.FromSlash(v.Captured)))
				if err != nil {
					u.Gap = "unreadable retained input: " + err.Error()
				} else if size != v.Size || v.SHA256 != "" && hash != v.SHA256 {
					u.Gap = "retained input hash/size mismatch"
				} else {
					u.Version.SHA256 = hash
					p.NativeBytes += size
					knownHashes[hash] = true
				}
				return add(u)
			}
			for _, s := range m.Snapshots {
				for _, v := range append([]capturedVersion{{Captured: s.Captured, Size: s.Size, Source: &s.Source, CapturedAt: s.CapturedAt}}, s.Generations...) {
					if e = addNative("database", s.Path, v); e != nil {
						return p, e
					}
				}
			}
			for _, f := range m.Files {
				for _, v := range append([]capturedVersion{{Captured: f.Captured, Size: f.Size, MTimeNS: f.MTimeNS, ObservedNS: f.ObservedNS, SHA256: f.SHA256, CapturedAt: f.CapturedAt}}, f.Previous...) {
					if e = addNative("file", f.Path, v); e != nil {
						return p, e
					}
				}
			}
			// Earlier append inventories and immutable parsing groups are independent
			// observations, even if the current manifest no longer references them.
			inputs, e := os.ReadDir(filepath.Join(dir, "inputs"))
			if e != nil && !errors.Is(e, os.ErrNotExist) {
				return p, e
			}
			for _, entry := range inputs {
				if !entry.IsDir() || !validRecordHash(entry.Name()) {
					continue
				}
				rel := filepath.ToSlash(filepath.Join("inputs", entry.Name(), captureManifestName))
				hash, _, err := fileSHA256Context(ctx, filepath.Join(dir, filepath.FromSlash(rel)))
				u := historyUnit{Kind: "view", Host: m.Host.ID, Source: m.Source.Name, Path: rel}
				u.Version.SHA256 = hash
				if err != nil {
					u.Gap = err.Error()
				}
				if e = add(u); e != nil {
					return p, e
				}
			}
			// Unnamed history is never swept out of the population.
			e = filepath.WalkDir(filepath.Join(dir, captureHistoryDir), func(path string, entry fs.DirEntry, err error) error {
				if errors.Is(err, os.ErrNotExist) && path == filepath.Join(dir, captureHistoryDir) {
					return nil
				}
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				rel, _ := filepath.Rel(dir, path)
				rel = filepath.ToSlash(rel)
				if listed[rel] {
					return nil
				}
				digest, _, hashErr := fileSHA256Context(ctx, path)
				if hashErr == nil && knownHashes[digest] {
					return nil
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				return addNative("orphan-native", "", capturedVersion{Captured: rel, Size: info.Size(), CapturedAt: "unknown"})
			})
			if e != nil {
				return p, e
			}
			c, e := sourceCaptureForTarget(ctx, captureTarget{Host: m.Host, Source: m.Source.Name, Dir: dir})
			if e != nil {
				return p, e
			}
			revs, e := newRecordStore(c).revisions()
			if e != nil {
				if e = add(historyUnit{Kind: "gap", Host: m.Host.ID, Source: m.Source.Name, Gap: "logical inventory: " + e.Error()}); e != nil {
					return p, e
				}
			}
			partials, err := os.ReadDir(filepath.Join(dir, recordDir, "partial"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return p, err
			}
			for _, part := range partials {
				if e = add(historyUnit{Kind: "gap", Host: m.Host.ID, Source: m.Source.Name, Path: filepath.ToSlash(filepath.Join(recordDir, "partial", part.Name())), Gap: "uncommitted SQLite acquisition; retained partial evidence is not dependency closure"}); e != nil {
					return p, e
				}
			}
			for _, r := range revs {
				if e = add(historyUnit{Kind: "logical", Host: m.Host.ID, Source: m.Source.Name, Revision: &r.Ref}); e != nil {
					return p, e
				}
			}
			// Entity/head publication without a group is surviving raw evidence,
			// never a silently omitted population or a complete replay unit.
			known := map[string]bool{}
			for _, r := range revs {
				known[r.Ref.Hash] = true
				for _, other := range r.Companions {
					known[other.Ref.Hash] = true
				}
			}
			err = filepath.WalkDir(filepath.Join(dir, recordDir, "entities"), func(path string, entry fs.DirEntry, walkErr error) error {
				if errors.Is(walkErr, os.ErrNotExist) && path == filepath.Join(dir, recordDir, "entities") {
					return nil
				}
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
					return nil
				}
				hash := strings.TrimSuffix(entry.Name(), ".json")
				if !validRecordHash(hash) || known[hash] {
					return nil
				}
				var ref recordObject
				if e := historyReadJSON(path, &ref); e != nil {
					return e
				}
				if ref.Hash != hash {
					return errors.New("raw row revision inventory mismatch")
				}
				return add(historyUnit{Kind: "logical-orphan", Host: m.Host.ID, Source: m.Source.Name, Revision: &ref})
			})
			if err != nil {
				return p, err
			}

		}
	}
	for _, source := range config.Sources {
		if scope.Host != "" && scope.Host != currentHost().ID || scope.Source != "" && scope.Source != source.Name {
			continue
		}
		found := false
		for _, captured := range p.Sources {
			if captured.Host.ID == currentHost().ID && captured.Source.Name == source.Name {
				found = true
			}
		}
		if !found {
			if e = add(historyUnit{Kind: "gap", Host: currentHost().ID, Source: source.Name, Original: source.Path, Gap: "configured source has no retained manifest; live acquisition excluded from fixed offline scope"}); e != nil {
				return p, e
			}
		}
	}
	if config.Library {
		hostDir := filepath.Join(filepath.Dir(config.Path), hostsDirName)
		entries, err := os.ReadDir(hostDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return p, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
				continue
			}
			host := strings.TrimSuffix(entry.Name(), ".toml")
			if !validCaptureName(host) || host == currentHost().ID || scope.Host != "" && scope.Host != host {
				continue
			}
			sources, err := readSourceBlocks(filepath.Join(hostDir, entry.Name()), filepath.Dir(config.Path))
			if err != nil {
				return p, err
			}
			for _, source := range sources {
				if scope.Source != "" && scope.Source != source.Name {
					continue
				}
				found := false
				for _, captured := range p.Sources {
					if captured.Host.ID == host && captured.Source.Name == source.Name {
						found = true
					}
				}
				if !found {
					if e = add(historyUnit{Kind: "gap", Host: host, Source: source.Name, Original: source.Path, Gap: "recorded host configuration has no retained manifest; no original path was read"}); e != nil {
						return p, e
					}
				}
			}
		}
	}
	// A read-only catalog is an inventory hint until the protected rollback
	// snapshot fixes its consistent export population. No checkpoint is proof.
	state, e := sqliteDependencyState(config.CatalogPath)
	if e != nil {
		return p, e
	}
	p.CatalogState = state
	p.CatalogBytes = state.Size + state.WALSize
	db, e := openReadOnlySQLite(config.CatalogPath)
	if e != nil {
		return p, e
	}
	defer db.Close()
	var exclusions int
	if e = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ask_native_sessions").Scan(&exclusions); e != nil {
		return p, e
	}
	if exclusions > 0 {
		if e = add(historyUnit{Kind: "catalog-exclusions", Original: "ask-native-exclusions"}); e != nil {
			return p, e
		}
	}
	var cursor string
	for {
		ids, e := queryMapsContext(ctx, db, `SELECT w.id FROM workspaces w WHERE w.id>? AND (?='' OR EXISTS(SELECT 1 FROM workspace_sightings s WHERE s.workspace_id=w.id AND s.host_id=?)) AND (?='' OR EXISTS(SELECT 1 FROM workspace_sightings s WHERE s.workspace_id=w.id AND s.source_name=? AND (?='' OR s.host_id=?))) ORDER BY w.id LIMIT 128`, cursor, scope.Host, scope.Host, scope.Source, scope.Source, scope.Host, scope.Host)
		if e != nil {
			return p, e
		}
		if len(ids) == 0 {
			break
		}
		for _, row := range ids {
			cursor = firstString(row["id"])
			if e = add(historyUnit{Kind: "catalog-workspace", Original: cursor}); e != nil {
				return p, e
			}
		}
	}
	rows, e := queryMapsContext(ctx, db, "SELECT installation_id,host_id FROM tl1_installations WHERE (?='' OR host_id=?) ORDER BY installation_id", scope.Host, scope.Host)
	if e != nil {
		return p, e
	}
	for _, row := range rows { // TL1 catalog installations lack source_name; do not guess a scoped association.
		if scope.Source != "" {
			if e = add(historyUnit{Kind: "gap", Host: scope.Host, Source: scope.Source, Original: "tl1-shared-scope", Gap: "TL1 shared installation scope cannot be proven from source name; plan without a source filter to rescue it"}); e != nil {
				return p, e
			}
			break
		}
		if e = add(historyUnit{Kind: "catalog-tl1", Host: firstString(row["host_id"]), Original: firstString(row["installation_id"])}); e != nil {
			return p, e
		}
	}
	p.Schema, e = historyCatalogBoundary(ctx, db)
	if e != nil {
		return p, e
	}
	after, e := sqliteDependencyState(config.CatalogPath)
	if e != nil {
		return p, e
	}
	if !sameSQLiteContentState(state, after) {
		return p, errors.New("catalog changed during planning; retry the bounded dry run")
	}
	if len(p.Units) == 0 && (scope.Host != "" || scope.Source != "") {
		return p, errors.New("selected history scope has no retained or catalog observations")
	}
	sort.Slice(p.Units, func(i, j int) bool { return p.Units[i].ID < p.Units[j].ID })
	_, p.FreeBytes = diskUsage(nearestExisting(config.CaptureRoot))
	identity := p
	identity.FreeBytes = 0
	identity.ID = ""
	p.ID = hashBytes([]byte(jsonText(identity)))
	return p, nil
}

type historyPlanPage struct {
	Plan  historyPlan   `json:"plan"`
	Total int           `json:"total_units"`
	Units []historyUnit `json:"units"`
	Next  string        `json:"next_cursor,omitempty"`
}

func historyPage(p historyPlan, cursor string, limit int) (historyPlanPage, error) {
	if limit < 1 || limit > 256 {
		return historyPlanPage{}, errors.New("limit must be 1..256")
	}
	after := ""
	if cursor != "" {
		prefix := p.ID + ":"
		if !strings.HasPrefix(cursor, prefix) {
			return historyPlanPage{}, errors.New("stale history cursor; re-plan")
		}
		after = strings.TrimPrefix(cursor, prefix)
	}
	page := historyPlanPage{Plan: p, Total: len(p.Units)}
	page.Plan.Units = nil
	for _, u := range p.Units {
		if u.ID > after {
			if len(page.Units) == limit {
				page.Next = p.ID + ":" + page.Units[len(page.Units)-1].ID
				break
			}
			page.Units = append(page.Units, u)
		}
	}
	return page, nil
}

func historyLoadManifest(dir string) (*captureManifest, error) {
	info, e := os.Stat(filepath.Join(dir, captureManifestName))
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if e == nil && info.Size() > historyMetadataLimit {
		return nil, errors.New("historical manifest exceeds bounded read budget")
	}
	return loadCaptureManifest(dir)
}

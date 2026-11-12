package archive

// Claude Desktop's HTTP cache is an input, never a retained payload. These
// types are the entire allowlist that can cross the capture boundary.
import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

const claudeSettingsKind = "claude-ai-settings"
const claudeSettingsExtractor = "claude-ai-settings-v1"
const claudeCacheLimit = 16 << 20

var claudeCacheName = regexp.MustCompile(`^[0-9a-f]{16}_0$`)
var claudeSettingsUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var claudeCacheDate = regexp.MustCompile(`(?i)(?:^|\x00|\r\n)date:[ \t]*([^\x00\r\n]{1,64})`)

type claudeConversationSetting struct {
	Conversation string  `json:"conversation_uuid"`
	Model        *string `json:"model"`
	Effort       *string `json:"effort_level"`
	Thinking     *string `json:"thinking_mode"`
}

type claudeSettingsSnapshot struct {
	Version       int                         `json:"version"`
	Organization  string                      `json:"organization_uuid"`
	ObservedAt    string                      `json:"observed_at"`
	TimeBasis     string                      `json:"time_basis"`
	CacheEntry    string                      `json:"cache_entry"`
	Conversations []claudeConversationSetting `json:"conversations"`
}

type claudeSettingsAdapter struct{ baseAdapter }

func (a *claudeSettingsAdapter) indexVersion() string  { return claudeSettingsExtractor }
func (a *claudeSettingsAdapter) partExtractor() string { return claudeSettingsExtractor }
func (a *claudeSettingsAdapter) Fingerprint() (string, error) {
	var parts []string
	err := a.discoverParts(func(p []sourcePart) bool { parts = append(parts, jsonText(p[0].item)+":"+p[0].signal); return true }, nil)
	return claudeSettingsExtractor + ":" + hashBytes([]byte(strings.Join(parts, "\n"))), err
}
func (a *claudeSettingsAdapter) Discover(emit func(WorkspaceRecord) error) error {
	return a.discoverParts(func([]sourcePart) bool { return false }, func(r *WorkspaceRecord, _ []sourcePart) error { return emit(*r) })
}
func (a *claudeSettingsAdapter) discoverParts(unchanged func([]sourcePart) bool, emit func(*WorkspaceRecord, []sourcePart) error) error {
	plan, err := a.planParts()
	if err != nil {
		return err
	}
	return plan.discover(a.context(), unchanged, emit)
}
func (a *claudeSettingsAdapter) planParts() (*sourceWorkPlan, error) {
	plan := &sourceWorkPlan{}
	if a.view == nil {
		projections, _, err := collectClaudeSettings(a.context(), a.files(), a.config.Path)
		if err != nil {
			return nil, err
		}
		for _, p := range projections {
			parts := []sourcePart{{item: filepath.Join(a.config.Path, p.rel), size: int64(len(p.data)), version: p.observed.UnixNano(), signal: hashBytes(p.data)}}
			plan.groups = append(plan.groups, plannedPartGroup{parts: parts, read: func(emit partEmitter) error {
				var snapshot claudeSettingsSnapshot
				if err := json.Unmarshal(p.data, &snapshot); err != nil {
					return err
				}
				record := claudeSettingsRecord(a.config, snapshot)
				return emit(&record, parts)
			}})
		}
		return plan, nil
	}
	err := a.files().WalkDir(a.config.Path, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".settings.json") {
			return nil
		}
		info, err := a.files().Stat(file)
		if err != nil {
			return err
		}
		parts := []sourcePart{{item: a.original(file), size: info.Size(), version: info.ModTime().UnixNano(), signal: strings.TrimSuffix(entry.Name(), ".settings.json")}}
		plan.groups = append(plan.groups, plannedPartGroup{parts: parts, read: func(emit partEmitter) error {
			in, err := a.files().Open(file)
			if err != nil {
				return err
			}
			defer in.Close()
			var snapshot claudeSettingsSnapshot
			decoder := json.NewDecoder(io.LimitReader(in, claudeCacheLimit))
			decoder.DisallowUnknownFields()
			if err = decoder.Decode(&snapshot); err != nil {
				return errors.New("invalid Claude settings projection")
			}
			if err = ensureEOF(decoder); err != nil {
				return err
			}
			if err = validateClaudeSettings(snapshot); err != nil {
				return err
			}
			record := claudeSettingsRecord(a.config, snapshot)
			return emit(&record, parts)
		}})
		return nil
	})
	return plan, err
}
func claudeSettingsRecord(source SourceConfig, snapshot claudeSettingsSnapshot) WorkspaceRecord {
	return WorkspaceRecord{SourceKind: claudeSettingsKind, SourceID: hashBytes([]byte(jsonText(snapshot))), Account: source.Account, ClaudeSettings: &snapshot}
}

// collectClaudeSettings does not follow cache-file symlinks, open databases,
// launch Claude, or use the network. Only candidate response bodies are read.
func collectClaudeSettings(ctx context.Context, files sourceFiles, root string) ([]captureProjection, []captureError, error) {
	warnings := []captureError{}
	projections := []captureProjection{}
	err := files.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			// Limit traversal to the HTTP cache when the configured root is Claude's
			// application-data folder, while accepting Cache_Data itself as a root.
			if file != root && entry.Name() != "Cache" && entry.Name() != "Cache_Data" {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !claudeCacheName.MatchString(entry.Name()) {
			return nil
		}
		info, err := files.Stat(file)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := files.Open(file)
		if err != nil {
			return err
		}
		defer in.Close()
		header := make([]byte, 24)
		if _, err = io.ReadFull(in, header); err != nil {
			return nil
		} // a live partial entry
		if binary.LittleEndian.Uint64(header) != 0xfcfb6d1ba7725c30 || binary.LittleEndian.Uint32(header[8:]) != 5 {
			return nil
		}
		keySize := binary.LittleEndian.Uint32(header[12:])
		if keySize == 0 || keySize > 8192 {
			return nil
		}
		key := make([]byte, keySize)
		if _, err = io.ReadFull(in, key); err != nil {
			return nil
		}
		organization := claudeSettingsOrganization(string(key))
		if organization == "" {
			return nil
		}
		raw, err := io.ReadAll(io.LimitReader(in, claudeCacheLimit+1))
		if err != nil {
			return err
		}
		if len(raw) > claudeCacheLimit {
			warnings = append(warnings, captureError{Path: file, Error: "Claude conversation settings cache exceeds the read limit"})
			return nil
		}
		after, statErr := files.Stat(file)
		if statErr != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
			warnings = append(warnings, captureError{Path: file, Error: "Claude settings cache changed during the read; retry required"})
			return nil
		}
		snapshot, err := decodeClaudeSettings(raw, organization, entry.Name(), info.ModTime())
		if err != nil {
			warnings = append(warnings, captureError{Path: file, Error: "Claude settings cache is incomplete or unsupported; retry after Claude finishes updating it"})
			return nil
		}
		data, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		observed, _ := time.Parse(time.RFC3339Nano, snapshot.ObservedAt)
		projections = append(projections, captureProjection{rel: hashBytes(data) + ".settings.json", data: data, observed: observed})
		return nil
	})
	sort.Slice(projections, func(i, j int) bool { return projections[i].rel < projections[j].rel })
	return projections, warnings, err
}
func claudeSettingsOrganization(key string) string {
	// Chromium stores credential_key/upload_identifier/[isolation_key]url.
	// As in GetResourceURLFromHttpCacheKey, a partitioned key's resource URL
	// starts after the final space; isolation keys may contain several sites.
	if !strings.HasPrefix(key, "https://") {
		credential, rest, ok := strings.Cut(key, "/")
		if !ok || (credential != "0" && credential != "1") {
			return ""
		}
		upload, resource, ok := strings.Cut(rest, "/")
		if !ok {
			return ""
		}
		if _, err := strconv.ParseInt(upload, 10, 64); err != nil {
			return ""
		}
		key = resource
		if strings.HasPrefix(key, "_dk_") {
			separator := strings.LastIndexByte(key, ' ')
			if separator < 0 {
				return ""
			}
			key = key[separator+1:]
		}
	}
	// Validate only the resource endpoint. Isolation sites and query state are
	// never retained in the sanitized projection.
	u, err := url.Parse(key)
	if err != nil || u.Scheme != "https" || u.Host != "claude.ai" || u.User != nil || u.Fragment != "" {
		return ""
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 5 || parts[1] != "api" || parts[2] != "organizations" || !claudeSettingsUUID.MatchString(parts[3]) || parts[4] != "chat_conversations_v2" {
		return ""
	}
	return strings.ToLower(parts[3])
}
func decodeClaudeSettings(raw []byte, org, entry string, mtime time.Time) (claudeSettingsSnapshot, error) {
	snapshot := claudeSettingsSnapshot{Version: 1, Organization: org, CacheEntry: entry, ObservedAt: mtime.UTC().Format(time.RFC3339Nano), TimeBasis: "cache_file_mtime"}
	var body io.Reader = bytes.NewReader(raw)
	if bytes.HasPrefix(raw, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		decoder, err := zstd.NewReader(body, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(claudeCacheLimit))
		if err != nil {
			return snapshot, err
		}
		defer decoder.Close()
		body = decoder
	}
	var response struct {
		Data []struct {
			UUID     string  `json:"uuid"`
			Model    *string `json:"model"`
			Settings struct {
				Effort   *string `json:"effort_level"`
				Thinking *string `json:"thinking_mode"`
			} `json:"settings"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(body, claudeCacheLimit+1))
	if err := decoder.Decode(&response); err != nil {
		return snapshot, err
	}
	if response.Data == nil {
		return snapshot, errors.New("missing conversation list")
	}
	// HTTP response headers are NUL-delimited in Chromium's response info.
	// Look only after the body start; a JSON string cannot contain literal NULs.
	for _, match := range claudeCacheDate.FindAllSubmatch(raw, -1) {
		if date, err := http.ParseTime(strings.TrimSpace(string(match[1]))); err == nil {
			snapshot.ObservedAt = date.UTC().Format(time.RFC3339Nano)
			snapshot.TimeBasis = "http_date"
			break
		}
	}
	for _, row := range response.Data {
		if !claudeSettingsUUID.MatchString(row.UUID) {
			continue
		}
		snapshot.Conversations = append(snapshot.Conversations, claudeConversationSetting{strings.ToLower(row.UUID), row.Model, row.Settings.Effort, row.Settings.Thinking})
	}
	sort.Slice(snapshot.Conversations, func(i, j int) bool {
		return snapshot.Conversations[i].Conversation < snapshot.Conversations[j].Conversation
	})
	return snapshot, validateClaudeSettings(snapshot)
}
func validateClaudeSettings(s claudeSettingsSnapshot) error {
	if s.Version != 1 || !claudeSettingsUUID.MatchString(s.Organization) || !claudeCacheName.MatchString(s.CacheEntry) || (s.TimeBasis != "http_date" && s.TimeBasis != "cache_file_mtime") {
		return errors.New("invalid Claude settings provenance")
	}
	if _, err := time.Parse(time.RFC3339Nano, s.ObservedAt); err != nil {
		return errors.New("invalid Claude settings observation time")
	}
	seen := map[string]bool{}
	for _, row := range s.Conversations {
		if !claudeSettingsUUID.MatchString(row.Conversation) || seen[row.Conversation] {
			return errors.New("invalid Claude settings conversation identity")
		}
		seen[row.Conversation] = true
		for _, value := range []*string{row.Model, row.Effort, row.Thinking} {
			if value != nil && (len(*value) > 128 || strings.ContainsAny(*value, "\r\n\x00")) {
				return errors.New("invalid Claude settings value")
			}
		}
	}
	return nil
}

func (p *capturePlan) claudeSettings(ctx context.Context, source SourceConfig, info fs.FileInfo) error {
	if !info.IsDir() {
		return errors.New("choose Claude’s app-data or HTTP cache folder")
	}
	var err error
	p.projections, p.warnings, err = collectClaudeSettings(ctx, sourceFiles{ctx: ctx}, source.Path)
	p.retainAbsent = true // cache eviction must never retract durable observations
	return err
}
func (r *probeRun) claudeSettings(dir, foundBy string) {
	candidate, _ := r.locate(claudeSettingsKind, dir, foundBy)
	if candidate == nil {
		return
	}
	candidate.Unit = "settings snapshot"
	candidate.Detail = "Selected model, effort and thinking mode from cached conversation lists. Chat text and credentials are excluded. To make older settings available, open the Claude desktop app, select Chat, click View all below Recents, then scroll down to your oldest conversation. Return to Pharos and capture settings once. Cache coverage is incomplete."
	r.keep(candidate)
}

func ingestClaudeSettings(ctx context.Context, tx *sql.Tx, record WorkspaceRecord, host, source string) error {
	snapshot := record.ClaudeSettings
	if snapshot == nil || record.SourceKind != claudeSettingsKind {
		return errors.New("invalid Claude settings record")
	}
	if err := validateClaudeSettings(*snapshot); err != nil {
		return err
	}
	for _, row := range snapshot.Conversations {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO claude_chat_settings_observations
   (host_id,source_name,account,organization_uuid,conversation_uuid,observed_at,time_basis,cache_entry,snapshot_id,model,effort_level,thinking_mode)
   VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, host, source, record.Account, snapshot.Organization, row.Conversation, snapshot.ObservedAt, snapshot.TimeBasis, snapshot.CacheEntry, record.SourceID, row.Model, row.Effort, row.Thinking); err != nil {
			return err
		}
	}
	return nil
}

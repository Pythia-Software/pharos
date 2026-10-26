package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

type historyDifference struct {
	Entity   string `json:"entity"`
	Category string `json:"category"`
	Detail   string `json:"detail"`
}
type historyVerification struct {
	RebuiltWorkspaces       int                 `json:"rebuilt_workspaces"`
	RawAdditionalWorkspaces int                 `json:"additional_archived_workspaces"`
	AdditionalIDs           []string            `json:"additional_archived_workspace_sample"`
	RebuiltConversations    int                 `json:"rebuilt_conversations"`
	RebuiltMessages         int                 `json:"rebuilt_messages"`
	InputGaps               []historyDifference `json:"input_gap_sample"`
	Version                 int                 `json:"version"`
	Job                     string              `json:"job"`
	State                   string              `json:"state"`
	SourceFaithful          int                 `json:"source_faithful_units"`
	CatalogDerived          int                 `json:"catalog_derived_units"`
	Partial                 int                 `json:"partial_units"`
	Unresolved              int                 `json:"unresolved_units"`
	Pending                 int                 `json:"pending_units"`
	CatalogOnlyApplied      int                 `json:"catalog_only_applied"`
	ExistingKept            int                 `json:"stronger_or_existing_kept"`
	Compared                int                 `json:"compared_workspaces"`
	Differences             []historyDifference `json:"differences"`
	DifferenceCount         int                 `json:"difference_count"`
	Scratch                 string              `json:"scratch_catalog"`
	ScratchBytes            int64               `json:"scratch_bytes"`
	FinishedAt              string              `json:"finished_at"`
	Exclusions              []string            `json:"excluded_state"`
}

func (v *historyVerification) difference(id, category, detail string) {
	v.DifferenceCount++
	if len(v.Differences) < 256 {
		v.Differences = append(v.Differences, historyDifference{id, category, detail})
	}
}
func (j *historyJob) inventoryReceipts(ctx context.Context) (map[string]historyReceipt, historyVerification, error) {
	return j.summarizeReceipts(ctx, true)
}
func (j *historyJob) summarizeReceipts(ctx context.Context, verify bool) (map[string]historyReceipt, historyVerification, error) {
	result := map[string]historyReceipt{}
	v := historyVerification{Version: historyFormat, Job: j.ID, Exclusions: []string{"preferences, saved views, bookmarks, finding decisions, credentials and Ask private lookup context require the full catalog backup", "unknown source fields and opaque values remain evidence; only compatible recovery contract fields are applied"}}
	for _, u := range j.Plan.Units {
		var r historyReceipt
		e := historyReadJSON(j.receiptPath(u.ID), &r)
		if errors.Is(e, os.ErrNotExist) {
			v.Pending++
			continue
		}
		if e != nil {
			return nil, v, e
		}
		if r.Unit != u.ID || r.Version != historyFormat {
			return nil, v, errors.New("invalid history receipt")
		}
		if verify {
			if e = j.verifyReceipt(ctx, u, r); e != nil {
				return nil, v, e
			}
		}
		if r.Gap != "" && len(v.InputGaps) < 128 {
			v.InputGaps = append(v.InputGaps, historyDifference{Entity: u.ID, Category: r.Tier, Detail: r.Gap})
		}
		result[u.ID] = r
		switch r.Tier {
		case "source-faithful":
			v.SourceFaithful++
		case "catalog-derived":
			v.CatalogDerived++
		case "partial":
			v.Partial++
		default:
			v.Unresolved++
		}
	}
	return result, v, nil
}
func historyViewAdapter(ctx context.Context, root string, u historyUnit) (Adapter, error) {
	dir := filepath.Dir(filepath.Join(root, u.Host, u.Source, filepath.FromSlash(u.Path)))
	m, e := historyLoadManifest(dir)
	if e != nil {
		return nil, e
	}
	view := newCaptureView(m.Host, dir, m)
	view.noLocalLookups = true
	view.evidenceRevision = filepath.Base(filepath.Dir(u.Path))
	path, ok := view.captured(m.Source.Path)
	if !ok {
		return nil, errors.New("unmapped history source root")
	}
	reader, e := MakeAdapter(SourceConfig{Name: m.Source.Name, Kind: m.Source.Kind, Path: path, Account: m.Source.Account})
	if e != nil {
		return nil, e
	}
	reader.(interface{ bindCapture(*captureView) }).bindCapture(view)
	setAdapterContext(reader, ctx)
	return reader, nil
}
func (j *historyJob) replayNative(ctx context.Context, c *Catalog, receipts map[string]historyReceipt, report *historyVerification) error {
	// Revisions are replayed in source observation order, never backfill order.
	for _, source := range j.Plan.Sources {
		dir := historySourceDir(j.Plan.CaptureRoot, source)
		retained, e := sourceCaptureForTarget(ctx, captureTarget{Host: source.Host, Source: source.Source.Name, Dir: dir})
		if e != nil {
			return e
		}
		if retained.manifest.Source != source.Source {
			return errors.New("history source identity changed; reconcile plan")
		}
		store := newRecordStore(retained)
		var revisions []retainedSQLiteRevision
		seen := map[string]bool{}
		for _, u := range j.Plan.Units {
			r, ok := receipts[u.ID]
			if !ok || !r.Closed || r.Revision == nil || u.Host != source.Host.ID || u.Source != source.Source.Name || seen[r.Revision.Hash] {
				continue
			}
			seen[r.Revision.Hash] = true
			v, e := store.loadRevision(*r.Revision)
			if e != nil {
				return e
			}
			item := retainedSQLiteRevision{Ref: *r.Revision, Revision: v}
			if !isConductorSource(source.Source.Kind) {
				group, e := store.committedGroup(v.Acquisition)
				if e != nil {
					return e
				}
				for _, ref := range group.Revisions {
					if ref.Hash == item.Ref.Hash {
						continue
					}
					v, e := store.loadRevision(ref)
					if e != nil {
						return e
					}
					item.Companions = append(item.Companions, retainedSQLiteRevision{Ref: ref, Revision: v})
				}
			}
			var bytes int64
			for _, member := range append([]retainedSQLiteRevision{item}, item.Companions...) {
				for _, table := range member.Revision.Tables {
					for _, chunk := range table.Chunks {
						bytes += chunk.Length * 2
					}
				}
				for _, dependency := range member.Revision.Dependencies {
					if dependency.Object != nil {
						bytes += dependency.Object.Length
					}
				}
			}
			if bytes > j.ScratchBudget || !isConductorSource(source.Source.Kind) && bytes > historyEntityBytes {
				report.difference(u.ID, "parser-budget", "raw evidence retained; group exceeds reviewed scratch/parser budget")
				continue
			}
			revisions = append(revisions, item)
		}
		sort.Slice(revisions, func(i, k int) bool {
			a, b := revisions[i], revisions[k]
			if a.Revision.Database != b.Revision.Database {
				return a.Revision.Database < b.Revision.Database
			}
			if a.Revision.Observed.version() != b.Revision.Observed.version() {
				return a.Revision.Observed.version() < b.Revision.Observed.version()
			}
			return a.Ref.Hash < b.Ref.Hash
		})
		if len(revisions) > 0 {
			config := retained.source
			config.Path = dir
			view := newCaptureView(source.Host, dir, retained.manifest)
			view.noLocalLookups = true
			adapter := &sqliteReplayAdapter{baseAdapter: baseAdapter{ctx: ctx, config: config, capability: "retrieval-only", view: view}, store: store, revisions: revisions, scratchRoot: filepath.Dir(c.Path), maxEntityBytes: historyEntityBytes, entityGap: func(id string) {
				report.difference(id, "parser-budget", "raw evidence retained; session exceeds bounded parser budget")
			}}
			result := c.IngestContext(withIngestMode(ctx, ingestMode{force: true}), adapter, nil)
			if result.Error != nil {
				report.difference(source.Host.ID+"/"+source.Source.Name, "native-replay", fmt.Sprint(result.Error))
			}
		}
		var views []historyUnit
		for _, u := range j.Plan.Units {
			r, ok := receipts[u.ID]
			if ok && r.Closed && u.Kind == "view" && u.Host == source.Host.ID && u.Source == source.Source.Name {
				views = append(views, u)
			}
		}
		// A fresh catalog cannot choose between changed byte branches at the
		// same source version. Preserve both inventories, defer their authority
		// and let the separately labelled chosen catalog observation survive.
		stamps := map[string]int64{}
		seenFiles := map[string]string{}
		conflicts := map[string]bool{}
		var inventoryBytes int64
		viewBudgetExceeded := false
		for _, u := range views {
			m, e := historyLoadManifest(filepath.Dir(filepath.Join(dir, filepath.FromSlash(u.Path))))
			if e != nil {
				return e
			}
			for _, f := range m.Files {
				stamps[u.ID] = max(stamps[u.ID], f.MTimeNS)
				key := fmt.Sprintf("%s:%d", f.Path, f.MTimeNS)
				if previous, ok := seenFiles[key]; ok && previous != f.SHA256 {
					conflicts[key] = true
				}
				if _, exists := seenFiles[key]; !exists {
					inventoryBytes += int64(len(key) + len(f.SHA256) + 64)
				}
				if inventoryBytes > historyMetadataLimit {
					viewBudgetExceeded = true
					break
				}
				seenFiles[key] = f.SHA256
			}
			if viewBudgetExceeded {
				break
			}
		}
		if viewBudgetExceeded {
			report.difference(source.Host.ID+"/"+source.Source.Name, "parser-budget", "filesystem observation inventory exceeds bounded verification budget; raw evidence retained")
			continue
		}
		sort.Slice(views, func(i, k int) bool {
			if stamps[views[i].ID] != stamps[views[k].ID] {
				return stamps[views[i].ID] < stamps[views[k].ID]
			}
			return views[i].ID < views[k].ID
		})

		for _, u := range views {
			ambiguous := false
			oversized := false
			m, e := historyLoadManifest(filepath.Dir(filepath.Join(dir, filepath.FromSlash(u.Path))))
			if e != nil {
				return e
			}
			for _, f := range m.Files {
				if conflicts[fmt.Sprintf("%s:%d", f.Path, f.MTimeNS)] {
					ambiguous = true
				}
				if f.Size > historyEntityBytes {
					oversized = true
				}
			}
			if ambiguous {
				report.difference(u.ID, "source-order-conflict", "equal-version raw branches remain evidence; catalog authority unresolved")
				continue
			}
			if oversized {
				report.difference(u.ID, "parser-budget", "filesystem input exceeds bounded parser budget; evidence retained")
				continue
			}
			reader, e := historyViewAdapter(ctx, j.Plan.CaptureRoot, u)
			if e != nil {
				return e
			}
			result := c.IngestContext(withIngestMode(ctx, ingestMode{force: true}), reader, nil)
			if result.Error != nil {
				report.difference(u.ID, "native-replay", fmt.Sprint(result.Error))
			}
			if e = historyScratchBudget(c, j.ScratchBudget); e != nil {
				return e
			}
		}
	}
	return nil
}
func historyScratchBudget(c *Catalog, budget int64) error {
	var bytes int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, e := os.Stat(c.Path + suffix); e == nil {
			bytes += info.Size()
		}
	}
	if bytes > budget {
		return errors.New("history scratch catalog budget exhausted; partial verification retained")
	}
	return ensureFree(filepath.Dir(c.Path), 1<<20)
}
func (j *historyJob) replayRecovery(ctx context.Context, c *Catalog, receipts map[string]historyReceipt, v *historyVerification) error {
	for _, u := range j.Plan.Units {
		r, ok := receipts[u.ID]
		if !ok || r.Recovery == "" || u.Kind == "catalog-exclusions" {
			continue
		}
		var envelope historyRecovery
		if e := historyReadJSON(historyRecoveryPath(j.Plan.CaptureRoot, r.Recovery), &envelope); e != nil {
			return e
		}
		outcome, e := c.applyHistoryRecovery(ctx, envelope)
		if e != nil {
			return e
		}
		if outcome == "catalog-derived" {
			v.CatalogOnlyApplied++
		} else {
			v.ExistingKept++
			if strings.HasPrefix(outcome, "unresolved") || strings.HasPrefix(outcome, "identity-conflict") {
				v.difference(u.Original, "catalog-recovery", outcome)
			}
		}
		if e = historyScratchBudget(c, j.ScratchBudget); e != nil {
			return e
		}
	}
	return nil
}

// Explicitly select strong content/identity fields. Incidental rowids, parser
// timestamps and caches are neither an equivalence oracle nor preservation.
func historyComparisonRows(envelope historyRecovery, kind string) []string {
	keys := map[string][]string{
		"workspaces":             {"id", "source_kind", "source_account", "source_id", "repository_id", "location", "location_history_json"},
		"repositories":           {"id", "canonical_remote", "normalized_remote", "display_name", "owner", "root_commit", "forge_id", "aliases_json", "local_locations_json"},
		"conversations":          {"id", "workspace_id", "work_item_id", "provider", "account", "native_id", "origin", "origin_host_id", "parent_id", "agent_depth", "agent_path", "model", "effort"},
		"messages":               {"conversation_id", "native_id", "role", "kind", "text", "raw_text", "source_order", "selected", "created_at", "parent_native_id", "previous_native_id", "evidence_locator", "model", "effort", "sender", "reasoning_format"},
		"workspace_sightings":    {"workspace_id", "host_id", "source_name", "location"},
		"conversation_sightings": {"conversation_id", "host_id", "origin", "source_name"},
	}
	rows := []string{}
	for _, row := range recoveryMaps(envelope, kind) {
		if fields, ok := keys[kind]; ok {
			m := map[string]any{}
			for _, key := range fields {
				value, err := encodeSQLiteValue(row[key])
				if err == nil {
					m[key] = value
				} else {
					m[key] = row[key]
				}
			}
			rows = append(rows, jsonText(m))
		} else {
			if kind == "metrics" || kind == "metric_ledger" {
				delete(row, "id")
			}
			delete(row, "indexed_at")
			delete(row, "observed_at")
			typed := map[string]sqliteValue{}
			for key, value := range row {
				typed[key], _ = encodeSQLiteValue(value)
			}
			rows = append(rows, jsonText(typed))
		}
	}
	sort.Strings(rows)
	return rows
}
func compareHistoryWorkspace(ctx context.Context, oracle, rebuilt *sql.DB, u historyUnit, v *historyVerification) error {
	tx, e := oracle.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return e
	}
	expected, e := exportHistoryRecovery(ctx, tx, u, "")
	tx.Rollback()
	if e != nil {
		return e
	}
	tx, e = rebuilt.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var exists int
	if e = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM workspaces WHERE id=?", u.Original).Scan(&exists); e != nil {
		return e
	}
	v.Compared++
	if exists == 0 {
		v.difference(u.Original, "missing-workspace", "absent from captures-only rebuild")
		return nil
	}
	actual, e := exportHistoryRecovery(ctx, tx, u, "")
	if e != nil {
		return e
	}
	for _, kind := range append(slices.Clone(historyWorkspaceTables), "conversation_parents") {
		if kind == "hosts" || kind == "copy_versions" || kind == "copy_evidence" {
			continue
		}
		if !slices.Equal(historyComparisonRows(expected, kind), historyComparisonRows(actual, kind)) {
			category := "source-values-or-relationships"
			if slices.Contains([]string{"agent_sessions", "agent_session_usage", "model_requests", "metrics", "metric_ledger"}, kind) {
				category = "accounting-or-parser-derived"
			}
			v.difference(u.Original, category, kind+" differs; exact values and populations compared")
		}
	}
	return nil
}

// Rebuild opens no oracle until every import has finished. Inputs are solely
// committed native receipts and catalog-derived envelopes. Its destination is
// an explicit new scratch path; it can never replace the operator's catalog.
func verifyHistory(ctx context.Context, config Config, id, scratch string) (historyVerification, error) {
	j, e := loadHistoryJob(config.CaptureRoot, id)
	if e != nil {
		return historyVerification{}, e
	}
	lock, e := historyJobLock(j.dir())
	if e != nil {
		return historyVerification{}, e
	}
	defer lock.Close()
	v, c, e := j.rebuild(ctx, config, scratch)
	if e != nil {
		return v, e
	}
	defer c.Close()
	// Comparison is a separate oracle phase, after the offline rebuild.
	hash, _, e := fileSHA256Context(ctx, j.Oracle)
	if e != nil || hash != j.OracleHash {
		return v, errors.New("comparison oracle changed or unavailable; offline rebuild remains available")
	}
	oracle, e := openReadOnlySQLite(j.Oracle)
	if e != nil {
		return v, e
	}
	defer oracle.Close()
	for _, u := range j.Plan.Units {
		if strings.HasPrefix(u.Kind, "catalog-") {
			var receipt historyReceipt
			if e = historyReadJSON(j.receiptPath(u.ID), &receipt); e != nil {
				return v, e
			}
			if receipt.Tier == "unresolved" {
				v.difference(u.Original, "unresolved-catalog-recovery", receipt.Gap)
				continue
			}
		}
		if u.Kind == "catalog-workspace" {
			if e = compareHistoryWorkspace(ctx, oracle, c.DB, u, &v); e != nil {
				return v, e
			}
		} else if u.Kind == "catalog-tl1" {
			for _, table := range append([]string{"tl1_installations"}, tl1SnapshotTables...) {
				expected, e := historyObservationForID(ctx, oracle, table, u.Original)
				if e != nil {
					return v, e
				}
				actual, e := historyObservationForID(ctx, c.DB, table, u.Original)
				if e != nil {
					return v, e
				}
				if !slices.Equal(expected, actual) {
					v.difference(u.Original, "structured-values-or-population", table+" typed values differ")
				}
			}
		}
	}
	expectedIDs := map[string]bool{}
	for _, u := range j.Plan.Units {
		if u.Kind == "catalog-workspace" {
			expectedIDs[u.Original] = true
		}
	}
	cursor := ""
	for {
		rows, e := queryMapsContext(ctx, c.DB, "SELECT id FROM workspaces WHERE id>? ORDER BY id LIMIT 128", cursor)
		if e != nil {
			return v, e
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			cursor = firstString(row["id"])
			v.RebuiltWorkspaces++
			if !expectedIDs[cursor] {
				v.RawAdditionalWorkspaces++
				if len(v.AdditionalIDs) < 128 {
					v.AdditionalIDs = append(v.AdditionalIDs, cursor)
				}
			}
		}
	}
	if e = c.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversations").Scan(&v.RebuiltConversations); e != nil {
		return v, e
	}
	if e = c.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&v.RebuiltMessages); e != nil {
		return v, e
	}
	v.State = "verified"
	if v.Partial+v.Unresolved+v.DifferenceCount > 0 {
		v.State = "verified-with-gaps"
	}
	v.FinishedAt = now()
	if info, e := os.Stat(v.Scratch); e == nil {
		v.ScratchBytes = info.Size()
	}
	if e = historyWriteJSON(filepath.Join(j.dir(), "verification.json"), v); e != nil {
		return v, e
	}
	j.Phase = "complete"
	j.State = v.State
	j.Error = ""
	if e = j.finishPauses(ctx, false); e != nil {
		j.Error = e.Error()
	}
	if e = j.save(); e != nil {
		return v, e
	}
	return v, nil
}

// The recovery reader is also available to ordinary captured-host indexing.
// It discovers immutable committed receipts without a source app or catalog
// oracle; source scope comes from original sightings, not the rescue machine.
func (c *Catalog) indexHistoricalRecovery(ctx context.Context, target captureTarget, exclusionsOnly bool, warnings ...func(captureError)) (int, error) {
	if !exclusionsOnly {
		// A native endpoint may just have been indexed. Reconcile committed
		// pending relationships even when its workspace/body already exists,
		// or this capture root has no recovery job to replay.
		if err := c.reconcileHistoryLinks(ctx); err != nil {
			return 0, err
		}
	}
	root := filepath.Dir(filepath.Dir(target.Dir))
	jobs, e := os.ReadDir(filepath.Join(root, historyDir, "jobs"))
	if errors.Is(e, os.ErrNotExist) {
		return 0, nil
	}
	if e != nil {
		return 0, e
	}
	// Optional job discovery must not strand ordinary captured-file indexing.
	// Explicit job commands and callers without a warning sink stay strict.
	load := func(id string) (historyJob, error) {
		if err := ctx.Err(); err != nil {
			return historyJob{}, err
		}
		j, err := loadHistoryJob(root, id)
		if err != nil && len(warnings) > 0 {
			warnings[0](captureError{Path: filepath.Join(historyDir, "jobs", id, "job.json"), Error: err.Error()})
		}
		return j, err
	}
	// Conflicting degraded observations have no implicit authority order.
	// Never choose a rescue by a lexical job-id/discovery order.
	candidates := map[string]string{}
	conflicts := map[string]bool{}
	count := 0
	if !exclusionsOnly {
		for _, entry := range jobs {
			if !entry.IsDir() || !validRecordHash(entry.Name()) {
				continue
			}
			j, err := load(entry.Name())
			if err != nil {
				if len(warnings) == 0 || ctx.Err() != nil {
					return 0, err
				}
				continue
			}
			for _, u := range j.Plan.Units {
				if u.Kind != "catalog-workspace" && u.Kind != "catalog-tl1" {
					continue
				}
				count++
				if count > historyInventoryLimit {
					err := errors.New("history recovery discovery budget exhausted; use a scoped explicit job rebuild")
					if len(warnings) == 0 {
						return 0, err
					}
					warnings[0](captureError{Path: filepath.Join(historyDir, "jobs"), Error: err.Error()})
					return 0, nil
				}
				var receipt historyReceipt
				e = historyReadJSON(j.receiptPath(u.ID), &receipt)
				if errors.Is(e, os.ErrNotExist) {
					continue
				}
				if e != nil {
					return 0, e
				}
				if receipt.Unit != u.ID || receipt.Version != historyFormat {
					return 0, errors.New("invalid recovery discovery receipt")
				}
				if receipt.Recovery == "" {
					continue
				}
				key := u.Kind + ":" + u.Original
				if old, ok := candidates[key]; ok && old != receipt.Recovery {
					conflicts[key] = true
				}
				candidates[key] = receipt.Recovery
			}
		}
	}
	applied := 0
	for _, entry := range jobs {
		if !entry.IsDir() || !validRecordHash(entry.Name()) {
			continue
		}
		j, err := load(entry.Name())
		if err != nil {
			if len(warnings) == 0 || ctx.Err() != nil {
				return applied, err
			}
			continue
		}
		for _, u := range j.Plan.Units {
			if (u.Kind == "catalog-exclusions") != exclusionsOnly {
				continue
			}
			if !strings.HasPrefix(u.Kind, "catalog-") {
				continue
			}
			if u.Kind == "catalog-workspace" || u.Kind == "catalog-tl1" {
				table, column := "workspaces", "id"
				if u.Kind == "catalog-tl1" {
					table, column = "tl1_installations", "installation_id"
				}
				var exists int
				if err := c.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE "+column+"=?", u.Original).Scan(&exists); err != nil {
					return applied, err
				}
				if exists > 0 {
					continue
				}
			}
			var receipt historyReceipt
			e = historyReadJSON(j.receiptPath(u.ID), &receipt)
			if errors.Is(e, os.ErrNotExist) {
				continue
			}
			if e != nil {
				return applied, e
			}
			if e = j.verifyReceipt(ctx, u, receipt); e != nil {
				return applied, e
			}
			if receipt.Recovery == "" {
				continue
			}
			var envelope historyRecovery
			if e = historyReadJSON(historyRecoveryPath(root, receipt.Recovery), &envelope); e != nil {
				return applied, e
			}
			matches := u.Kind == "catalog-exclusions"
			for _, row := range recoveryMaps(envelope, "workspace_sightings") {
				if firstString(row["host_id"]) == target.Host.ID && firstString(row["source_name"]) == target.Source {
					matches = true
				}
			}
			if u.Kind == "catalog-tl1" {
				for _, row := range recoveryMaps(envelope, "tl1_installations") {
					if firstString(row["host_id"]) == target.Host.ID && strings.EqualFold(targetSourceKind(target), "tl1") {
						matches = true
					}
				}
			}
			if matches {
				if conflicts[u.Kind+":"+u.Original] {
					return applied, errors.New("conflicting catalog-derived observations require an explicit job rebuild/verification; no rescue observation was given implicit authority")
				}
				outcome, e := c.applyHistoryRecovery(ctx, envelope)
				if e != nil {
					return applied, e
				}
				if outcome == "catalog-derived" {
					applied++
				}
			}
		}
	}
	return applied, nil
}
func targetSourceKind(target captureTarget) string {
	m, e := historyLoadManifest(target.Dir)
	if e != nil {
		return ""
	}
	return m.Source.Kind
}

func (j *historyJob) rebuild(ctx context.Context, config Config, scratch string) (historyVerification, *Catalog, error) {
	receipts, v, e := j.inventoryReceipts(ctx)
	if e != nil {
		return v, nil, e
	}
	if v.Pending > 0 {
		return v, nil, errors.New("export receipts are still pending")
	}
	scratch = canonical(scratch)
	if !filepath.IsAbs(scratch) || within(scratch, j.Plan.CaptureRoot) || within(scratch, filepath.Dir(config.CatalogPath)) || scratch == canonical(j.Oracle) {
		return v, nil, errors.New("scratch catalog must be a new absolute path outside the library, captures and rollback catalog")
	}
	if _, e = os.Lstat(scratch); !errors.Is(e, os.ErrNotExist) {
		return v, nil, errors.New("scratch path already exists; choose a new isolated catalog")
	}
	if info, err := os.Stat(filepath.Dir(scratch)); err != nil || !info.IsDir() {
		return v, nil, errors.New("scratch parent must already exist on the reviewed mounted volume")
	}
	if e = ensureFree(filepath.Dir(scratch), j.ScratchBudget); e != nil {
		return v, nil, e
	}
	c, e := newCatalog(scratch)
	if e != nil {
		return v, nil, e
	}
	success := false
	defer func() {
		if !success {
			c.Close()
		}
	}()
	c.evidenceOnly = true
	c.setCaptureRoot(j.Plan.CaptureRoot)
	v.Scratch = scratch
	for _, u := range j.Plan.Units {
		if u.Kind != "catalog-exclusions" {
			continue
		}
		r, ok := receipts[u.ID]
		if !ok || r.Recovery == "" {
			return v, nil, errors.New("replay exclusions lack a committed recovery envelope")
		}
		var envelope historyRecovery
		if e = historyReadJSON(historyRecoveryPath(j.Plan.CaptureRoot, r.Recovery), &envelope); e != nil {
			return v, nil, e
		}
		if _, e = c.applyHistoryRecovery(ctx, envelope); e != nil {
			return v, nil, e
		}
	}
	if e = j.replayNative(ctx, c, receipts, &v); e != nil {
		return v, nil, e
	}
	if e = j.replayRecovery(ctx, c, receipts, &v); e != nil {
		return v, nil, e
	}
	if _, e = c.ReconcileIdentitiesContext(ctx); e != nil {
		return v, nil, e
	}
	if e = c.refreshAllLibrary(ctx); e != nil {
		return v, nil, e
	}
	if e = historyScratchBudget(c, j.ScratchBudget); e != nil {
		return v, nil, e
	}
	success = true
	return v, c, nil
}

func historyObservationForID(ctx context.Context, db *sql.DB, table, id string) ([]string, error) {
	tx, e := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var used int64
	observation, e := historyRows(ctx, tx, table, "SELECT * FROM "+table+" WHERE installation_id=?", &used, id)
	if e != nil {
		return nil, e
	}
	rows, e := observation.maps()
	if e != nil {
		return nil, e
	}
	values := []string{}
	for _, row := range rows {
		delete(row, "synced_at")
		values = append(values, jsonText(row))
	}
	sort.Strings(values)
	return values, nil
}

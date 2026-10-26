package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// retainedInput is also the execution plan's reader. Files in inputs/ are
// immutable links or verified durable copies: capture publishes replacement inodes. No capture
// or catalog lock is held while the parser runs. These revisions are evidence,
// not a cache; only a future evidence-aware collector may remove them.
type retainedInput struct {
	Adapter     Adapter
	Close       func()
	Unavailable []captureError
}

func productionAdapter(adapter Adapter) bool {
	switch adapter.(type) {
	case *jsonlAdapter, *canonicalAdapter, *chatGPTAdapter, *conductorAdapter, *tl1Adapter, *antigravityAdapter:
		return true
	}
	// Synthetic record adapters are package/test inputs, never MakeAdapter results.
	return false
}

// Filesystem inventories currently retain every acquired revision. Keep that
// experimental storage policy out of ordinary indexing until collection is
// bounded. SQLite has its separately gated transactional exporter.
func retainedAcquisitionEnabled(source SourceConfig) bool {
	return source.Options["retained_input_capture"] == true || source.Options["sqlite_record_capture"] == true
}

func (c *Catalog) acquireRetainedInput(ctx context.Context, adapter Adapter, progress ProgressFunc) (input retainedInput, failure error) {
	acquired := captureViewOf(adapter) == nil && productionAdapter(adapter) && retainedAcquisitionEnabled(adapter.Config()) && !isConductorSource(adapter.Config().Kind) && (!strings.EqualFold(adapter.Config().Kind, "tl1") && !strings.EqualFold(adapter.Config().Kind, "antigravity") || adapter.Config().Options["sqlite_record_capture"] == true)
	defer func() {
		if !acquired {
			return
		}
		if failure != nil {
			_ = c.recordInputAcquisition(sourceHost(adapter).ID, adapter.Config(), "", failure)
			return
		}
		if input.Adapter != nil {
			if view := captureViewOf(input.Adapter); view != nil && view.evidenceRevision != "" {
				failure = c.recordInputAcquisition(view.host.ID, adapter.Config(), view.evidenceRevision, nil)
				if failure != nil && input.Close != nil {
					input.Close()
				}
			}
		}
	}()
	result := retainedInput{Adapter: adapter, Close: func() {}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if captureViewOf(adapter) != nil || !productionAdapter(adapter) {
		return result, nil
	}
	source := adapter.Config()
	if !retainedAcquisitionEnabled(source) {
		return result, nil
	}
	if isConductorSource(source.Kind) {
		return result, nil
	} // session exporter; production-size gate remains opt-in
	if strings.EqualFold(source.Kind, "tl1") || strings.EqualFold(source.Kind, "antigravity") {
		if source.Options["sqlite_record_capture"] != true {
			return result, nil
		} // staged SQLite rollout, exposed in status
		return c.acquireLogicalGroup(ctx, source, progress)
	}
	if progress != nil {
		progress("preserving inputs", 0, 0, 0, 0, 0)
	}
	session, err := beginInputCapture(ctx, Config{CaptureRoot: c.captureRootPath(), CaptureGenerations: 1})
	if err != nil {
		return result, err
	}
	defer session.Close()
	if err = session.writeHost(now(), now()); err != nil {
		return result, err
	}
	// Codex naming databases are parsing dependencies, acquired through row
	// export rather than a native backup on each timer tick.
	if strings.EqualFold(source.Kind, "codex") {
		plan, err := planCapture(source)
		if err != nil {
			return result, err
		}
		if len(plan.databases) > 0 {
			if source.Options["sqlite_record_capture"] != true {
				return result, nil
			}
			return c.acquireLogicalGroupLocked(ctx, source, session, plan, progress)
		}
	}
	captured := session.source(ctx, source, nil)
	if captured.State != "complete" || len(captured.Warnings) > 0 {
		return result, captureResultError(captured)
	}
	target := captureTarget{Host: session.host, Source: source.Name, Dir: captured.Dir}
	return freezeCaptureInput(ctx, target, source, true)
}

func captureResultError(result CaptureSourceResult) error {
	failures := []error{}
	for _, failure := range result.Errors {
		if failure.Error == errNativeSnapshotPaused.Error() {
			failures = append(failures, errNativeSnapshotPaused)
			continue
		}
		failures = append(failures, fmt.Errorf("%s: %s", failure.Path, failure.Error))
	}
	for _, failure := range result.Warnings {
		failures = append(failures, fmt.Errorf("%s: %s", failure.Path, failure.Error))
	}
	if len(failures) == 0 {
		return errors.New("input acquisition incomplete; retry required")
	}
	return errors.Join(failures...)
}

// Caller owns the host capture lock. Durable filesystem revisions survive
// crashes before indexing; temporary native leases keep explicit snapshot
// workflows stable without retaining unbounded native generations.
func freezeCaptureInput(ctx context.Context, target captureTarget, source SourceConfig, durable bool) (retainedInput, error) {
	result := retainedInput{Close: func() {}}
	manifest, err := loadCaptureManifest(target.Dir)
	if err != nil {
		return result, err
	}
	return freezeManifestInput(ctx, target, source, durable, manifest)
}

func freezeManifestInput(ctx context.Context, target captureTarget, source SourceConfig, durable bool, manifest *captureManifest) (retainedInput, error) {
	return freezeManifestInputPolicy(ctx, target, source, durable, manifest, false)
}

// Ordinary captured-file indexing can read the surviving inventory after a
// partial restore. Acquisition and verification still require its full closure.
func leaseCaptureForIndex(ctx context.Context, target captureTarget) (retainedInput, error) {
	manifest, err := loadCaptureManifest(target.Dir)
	if err != nil {
		return retainedInput{}, err
	}
	return freezeManifestInputPolicy(ctx, target, SourceConfig{}, len(manifest.Snapshots) == 0 && manifest.RetainedInputCapture, manifest, true)
}

func freezeManifestInputPolicy(ctx context.Context, target captureTarget, source SourceConfig, durable bool, manifest *captureManifest, allowUnavailable bool) (retainedInput, error) {
	result := retainedInput{Close: func() {}}
	var err error
	if manifest.Version == 0 || manifest.Host.ID != target.Host.ID || manifest.Source.Name != target.Source {
		return result, errors.New("capture identity mismatch")
	}
	// As in openCaptureAdapter: older host.json files omitted the machine that
	// this validated source manifest still names.
	target.Host.MachineID = defaultString(target.Host.MachineID, manifest.Host.MachineID)
	data, err := json.Marshal(manifest)
	if err != nil {
		return result, err
	}
	var frozen captureManifest
	if err = json.Unmarshal(data, &frozen); err != nil {
		return result, err
	}
	unavailable := map[string]bool{}
	if allowUnavailable {
		check := func(rel string, size int64, hash string) {
			info, err := os.Stat(filepath.Join(target.Dir, filepath.FromSlash(rel)))
			if err == nil && (!info.Mode().IsRegular() || (size != 0 || hash != "") && info.Size() != size) {
				err = errors.New("captured input no longer matches its recorded size or file type")
			}
			if err != nil {
				unavailable[rel] = true
				result.Unavailable = append(result.Unavailable, captureError{Path: rel, Error: err.Error()})
			}
		}
		for _, file := range frozen.Files {
			check(file.Captured, file.Size, file.SHA256)
		}
		for _, snapshot := range frozen.Snapshots {
			check(snapshot.Captured, snapshot.Size, "")
			for _, generation := range snapshot.Generations {
				check(generation.Captured, generation.Size, generation.SHA256)
			}
		}
		if len(unavailable) > 0 {
			// A partial reader cannot certify the original immutable inventory.
			durable = false
		}
	}
	// History is addressed by the original inventory. The reader's latest files
	// are leased here; generation readers retain their existing guarded workflow.
	for _, f := range frozen.Files {
		f.Previous = nil
		if unavailable[f.Captured] {
			continue
		}
		// Legacy enrichment inventories can omit byte metadata. Resolve it
		// from the locked retained file before certifying a durable inventory.
		if f.Size == 0 && f.SHA256 == "" {
			f.SHA256, f.Size, err = fileSHA256Context(ctx, filepath.Join(target.Dir, filepath.FromSlash(f.Captured)))
			if err != nil {
				return result, err
			}
		}
	}
	if durable {
		for _, snapshot := range frozen.Snapshots {
			if snapshot.MissingSince == "" {
				return result, errors.New("native inputs require a temporary lease or logical export")
			}
		}
		frozen.Snapshots = nil
	}
	identity := frozen
	identity.UpdatedAt = ""
	identity.LastDataAt = ""
	identity.LastRun = nil
	revision := hashBytes([]byte(jsonText(identity)))
	var dir string
	if durable {
		dir = filepath.Join(target.Dir, "inputs", revision)
		if err = os.MkdirAll(dir, 0700); err != nil {
			return result, err
		}
	} else {
		dir, err = os.MkdirTemp(target.Dir, ".reader-*"+captureTempSuffix)
		if err != nil {
			return result, err
		}
		lease, err := os.OpenFile(filepath.Join(dir, ".reader.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			_ = os.RemoveAll(dir)
			return result, err
		}
		if err = syscall.Flock(int(lease.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			lease.Close()
			_ = os.RemoveAll(dir)
			return result, err
		}
		result.Close = func() { lease.Close(); _ = os.RemoveAll(dir) }
	}
	fail := func(err error) (retainedInput, error) { result.Close(); return retainedInput{}, err }
	link := func(rel string, size int64, hash string) error {
		if unavailable[rel] {
			return nil
		}
		return freezeRetainedFile(ctx, target.Dir, dir, rel, size, hash)
	}
	for _, f := range frozen.Files {
		if err = link(f.Captured, f.Size, f.SHA256); err != nil {
			return fail(err)
		}
	}
	for _, s := range frozen.Snapshots {
		if err = link(s.Captured, s.Size, ""); err != nil {
			return fail(err)
		}
		for _, generation := range s.Generations {
			if err = link(generation.Captured, generation.Size, generation.SHA256); err != nil {
				return fail(err)
			}
		}
	}
	view := newCaptureView(target.Host, dir, &frozen)
	if len(frozen.Snapshots) == 0 {
		view.evidenceRevision = revision
		view.evidenceVolatile = !durable
	}
	// A frozen inventory's identity proves its closure; per-record identities
	// below include only the transcript group and metadata that produced it.
	root, ok := view.captured(frozen.Source.Path)
	if !ok {
		return fail(errors.New("unmapped retained source root"))
	}
	if err = os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return fail(err)
	}
	if root == filepath.Join(dir, "files") {
		if err = os.MkdirAll(root, 0700); err != nil {
			return fail(err)
		}
	}
	if allowUnavailable {
		if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
			if isConductorSource(frozen.Source.Kind) || strings.EqualFold(frozen.Source.Kind, "tl1") {
				// Single-file sources may still have older DB generations or
				// independent TL1 installations in this leased directory.
				root = filepath.Dir(root)
			} else {
				return result, nil
			}
		}
	}
	data, err = json.Marshal(&frozen)
	if err != nil {
		return fail(err)
	}
	if durable {
		if err = ensureFree(dir, int64(len(data))+4096); err != nil {
			return fail(err)
		}
		if captureFault != nil {
			if err = captureFault("input-publish", revision); err != nil {
				return fail(err)
			}
		}
		if err = writeFileDurably(filepath.Join(dir, captureManifestName), data); err != nil {
			return fail(err)
		}
	}
	source.Path = root
	if source.Kind == "" {
		source = SourceConfig{Name: frozen.Source.Name, Kind: frozen.Source.Kind, Path: root, Account: frozen.Source.Account, Enabled: true}
	}
	result.Adapter, err = MakeAdapter(source)
	if err != nil {
		return fail(err)
	}
	result.Adapter.(interface{ bindCapture(*captureView) }).bindCapture(view)
	return result, nil
}

// Link when available; exFAT and other linkless volumes use a streamed copy.
// Caller owns the capture lock, so the source inode cannot rotate during this
// operation. Existing copies are checked against the same manifest on resume.
var retainedLinkFile = os.Link

func freezeRetainedFile(ctx context.Context, sourceDir, readerDir, rel string, size int64, hash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	input, dest := filepath.Join(sourceDir, filepath.FromSlash(rel)), filepath.Join(readerDir, filepath.FromSlash(rel))
	a, err := os.Stat(input)
	if err != nil {
		return err
	}
	// Temporary generation/audit views may omit the snapshot size.
	if size == 0 && hash == "" {
		size = a.Size()
	}
	if !a.Mode().IsRegular() || a.Size() != size {
		return errors.New("retained input identity mismatch")
	}
	if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	linkErr := retainedLinkFile(input, dest)
	if linkErr == nil || errors.Is(linkErr, os.ErrExist) {
		b, err := os.Stat(dest)
		if err != nil {
			return err
		}
		if os.SameFile(a, b) {
			return nil
		}
	}
	// Native snapshots have no manifest digest. Hash the locked retained input,
	// never its original source path, to verify copied bytes and resumed copies.
	if hash == "" {
		var n int64
		hash, n, err = fileSHA256Context(ctx, input)
		if err != nil {
			return err
		}
		if n != size {
			return errors.New("retained input changed while freezing")
		}
	}
	if old, n, err := fileSHA256Context(ctx, dest); err == nil {
		b, statErr := os.Stat(dest)
		if old != hash || n != size || statErr != nil || !b.ModTime().Equal(a.ModTime()) {
			return errors.New("retained input copy identity mismatch")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = ensureFree(readerDir, size+4096); err != nil {
		return err
	}
	if err = reserveCaptureAcquisition(sourceDir, size); err != nil {
		return err
	}
	in, err := os.Open(input)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dest), ".freeze-*"+captureTempSuffix)
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	digest := newRecordHash()
	n, err := io.Copy(io.MultiWriter(out, digest), &recordCountReader{r: io.LimitReader(in, size+1), ctx: ctx})
	if err != nil {
		return err
	}
	if n != size || fmt.Sprintf("%x", digest.Sum(nil)) != hash {
		return errors.New("retained input copy disagrees with manifest")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Chtimes(out.Name(), a.ModTime(), a.ModTime()); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = os.Rename(out.Name(), dest); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(dest)); err == nil {
		_ = dir.Sync()
		dir.Close()
	}
	return nil
}

func retainedRecordIdentity(view *captureView, record *WorkspaceRecord, parts []sourcePart) {
	if view == nil || view.evidenceRevision == "" || record == nil || record.EvidenceIdentity != "" {
		return
	}
	selected := map[string]bool{}
	for _, p := range parts {
		selected[p.item] = true
	}
	for _, conversation := range record.Conversations {
		selected[conversation.Origin] = true
	}
	values := []string{}
	for _, part := range parts {
		values = append(values, part.item, part.signal)
	}
	for _, f := range view.manifest.Files {
		if selected[f.Path] {
			values = append(values, f.Path, f.SHA256)
		}
	}
	// For exports without per-part discovery the complete immutable inventory
	// defines one parsing input. JSONL groups use their exact dependency signal.
	if len(values) == 0 && len(parts) == 0 {
		values = append(values, view.evidenceRevision)
	}
	if record.SourceKind == "claude" && len(parts) > 0 {
		observe(record, record.Observed)
	}
	record.EvidenceIdentity = hashBytes([]byte(jsonText(values)))
	if !view.evidenceVolatile {
		record.EvidenceRevision = view.evidenceRevision
	}
}

// File-only Antigravity verification keeps unrelated CLI logs outside its
// evidence budget. SQLite-dependent groups use the logical rollout instead.
func (c *Catalog) acquireAuditFilesystemGroup(ctx context.Context, unit syncUnit) (retainedInput, error) {
	source := unit.Source
	plan, err := planCapture(source)
	if err != nil {
		return retainedInput{}, err
	}
	if len(plan.databases) > 0 {
		return retainedInput{}, errAuditUnstable
	}
	wanted, ids := map[string]bool{}, map[string]bool{}
	for _, part := range unit.Parts {
		if strings.HasPrefix(part.item, "group:") || strings.HasPrefix(part.item, "sqlite:") {
			continue
		}
		wanted[part.item] = true
		id := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(part.item))))
		ids[id] = true
		wanted[filepath.Join(source.Path, "annotations", id+".pbtxt")] = true
	}
	adapter := &antigravityAdapter{baseAdapter: baseAdapter{ctx: ctx, config: source}}
	logs, err := adapter.runLogsFor(ids)
	if err != nil {
		return retainedInput{}, err
	}
	for _, log := range logs {
		wanted[log] = true
	}
	files := []captureItem{}
	for _, file := range plan.files {
		if wanted[file.path] {
			files = append(files, file)
		}
	}
	plan.files, plan.complete = files, false // a subset must never mark siblings missing
	session, err := beginInputCapture(ctx, Config{CaptureRoot: c.captureRootPath(), CaptureGenerations: 1})
	if err != nil {
		return retainedInput{}, err
	}
	defer session.Close()
	result := session.sourcePlanned(ctx, source, nil, &plan)
	if result.State != "complete" || len(result.Warnings) > 0 {
		return retainedInput{}, captureResultError(result)
	}
	manifest, err := loadCaptureManifest(result.Dir)
	if err != nil {
		return retainedInput{}, err
	}
	retained := []*capturedFile{}
	for _, file := range manifest.Files {
		if wanted[file.Path] {
			retained = append(retained, file)
		}
	}
	selected := *manifest
	selected.Files = retained
	selected.Snapshots = nil
	return freezeManifestInput(ctx, captureTarget{Host: session.host, Source: source.Name, Dir: result.Dir}, source, true, &selected)
}

package archive

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const claudeCompactionFormat = "claude-export-compaction-v1"

type claudeCompactionItem struct {
	Job          string `json:"job"`
	Part         string `json:"part"`
	NativeSHA256 string `json:"native_sha256"`
	UnitsSHA256  string `json:"units_sha256"`
	Bytes        int64  `json:"native_bytes"`
	Blocked      string `json:"blocked,omitempty"`
}

type claudeCompactionPlan struct {
	Format       string                 `json:"format"`
	ID           string                 `json:"id"`
	Host         string                 `json:"host"`
	Source       string                 `json:"source"`
	Library      string                 `json:"library_id"`
	Instance     string                 `json:"library_instance"`
	RootIdentity string                 `json:"capture_root_identity"`
	Items        []claudeCompactionItem `json:"items"`
}

type claudeRetirementProof struct {
	Format         string `json:"format"`
	Job            string `json:"job"`
	Part           string `json:"part"`
	NativeSHA256   string `json:"native_sha256"`
	UnitsSHA256    string `json:"units_sha256"`
	Transformation string `json:"transformation"`
}

type claudeCompactionRun struct {
	BlockedItems int             `json:"blocked_items"`
	Plan         string          `json:"plan"`
	State        string          `json:"state"`
	RetiredBytes int64           `json:"retired_bytes"`
	Completed    map[string]bool `json:"completed"`
	Error        string          `json:"error,omitempty"`
}

func claudeCompactionIdentity(config Config) (LibraryDescriptor, string, error) {
	d, err := readLibraryDescriptor(config)
	if err != nil {
		return d, "", err
	}
	info, err := os.Stat(config.CaptureRoot)
	if err != nil {
		return d, "", err
	}
	identity, err := rootIdentity(info)
	return d, identity, err
}

// Prepare can publish additive source units; planning never deletes native
// inputs or changes the library's writer compatibility gate.
func planClaudeCompaction(ctx context.Context, config Config, name string, prepare bool) (*claudeCompactionPlan, error) {
	use, err := fileLease(libraryUseLock(config), false)
	if err != nil {
		return nil, err
	}
	defer use.Close()
	name, err = claudeImportName(name)
	if err != nil {
		return nil, err
	}
	lease, err := claudeImportLock(config, name)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	if _, err := ensureLibraryDescriptor(config, ""); err != nil {
		return nil, err
	}
	d, identity, err := claudeCompactionIdentity(config)
	if err != nil {
		return nil, err
	}
	p := &claudeCompactionPlan{Format: claudeCompactionFormat, Host: currentHost().ID, Source: name, Library: d.ID, Instance: d.Instance, RootIdentity: identity, Items: []claudeCompactionItem{}}
	root := claudeImportRoot(config, name)
	entries, err := os.ReadDir(filepath.Join(root, "snapshots"))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		job, err := loadClaudeJob(config, name, entry.Name())
		if err != nil {
			return nil, err
		}
		for index := range job.Parts {
			part := &job.Parts[index]
			if part.State == claudePartExcluded || part.Manifest {
				continue
			}
			item := claudeCompactionItem{Job: job.ID, Part: part.ID, NativeSHA256: part.SHA256, UnitsSHA256: part.UnitsSHA256, Bytes: part.Size}
			if job.State != "collected" || part.State != "collected" {
				item.Blocked = "incomplete acquisition remains protected"
			} else {
				if prepare && !part.Retired {
					if err := retainClaudeSourceUnits(ctx, config, job, part); err != nil {
						item.Blocked = err.Error()
					} else {
						part.UnitsError = ""
						item.UnitsSHA256 = part.UnitsSHA256
					}
				}
				if item.Blocked == "" {
					if err := verifyClaudeReplacement(ctx, config, job, *part); err != nil {
						item.Blocked = err.Error()
					}
				}
			}
			p.Items = append(p.Items, item)
		}
		if prepare {
			if err := publishClaudeInventory(config, job); err != nil {
				return nil, err
			}
			if err := saveClaudeJob(config, job); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(p.Items, func(i, j int) bool {
		if p.Items[i].Job != p.Items[j].Job {
			return p.Items[i].Job < p.Items[j].Job
		}
		return p.Items[i].Part < p.Items[j].Part
	})
	p.ID = hashBytes([]byte(jsonText(p)))
	name, err = claudeOwnedPath(root, ".compaction/plans/"+p.ID+".json")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return nil, err
	}
	if err := ensureFree(root, int64(len(jsonText(p)))); err != nil {
		return nil, err
	}
	if err := writeFileDurably(name, []byte(jsonText(p))); err != nil {
		return nil, err
	}
	return p, nil
}

func verifyClaudeReplacement(ctx context.Context, config Config, job *claudeImportJob, part claudeImportPart) error {
	if part.Units == "" || part.UnitsError != "" {
		return errors.New("native input has no verified replacement units")
	}
	root := claudeImportRoot(config, job.Name)
	units, err := readClaudeSourceUnits(sourceFiles{ctx: ctx}, filepath.Join(claudeImportJobDir(config, job.Name, job.ID), part.Units), part.SHA256, part.UnitsSHA256)
	if err != nil {
		return err
	}
	if part.Retired {
		// A resumed run must not replace a missing/corrupt proof, even if
		// some native bytes happen to remain after interruption.
		return verifyClaudeStoredPart(ctx, claudeImportJobDir(config, job.Name, job.ID), part)
	}
	if err := verifyClaudeStoredPart(ctx, claudeImportJobDir(config, job.Name, job.ID), part); err != nil {
		return err
	}
	if err := verifyClaudeUnitPacks(ctx, root, units); err != nil {
		return err
	}
	byMember := map[string][]claudeConversationUnit{}
	for _, unit := range units.Conversations {
		byMember[unit.Member] = append(byMember[unit.Member], unit)
	}
	matched := 0
	members := map[string]claudeSourceMember{}
	for _, member := range units.Members {
		members[member.Name] = member
	}
	conversationFiles := []string{}
	directories := []string{}
	decode := func(reader io.Reader, name string) error {
		conversationFiles = append(conversationFiles, name)
		decoder := json.NewDecoder(claudeContextReader{ctx, reader})
		if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
			return errors.New("invalid native conversation array")
		}
		index := 0
		for decoder.More() {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return err
			}
			original, err := claudeSourceJSON(raw)
			if err != nil {
				return err
			}
			if index >= len(byMember[name]) {
				return errors.New("replacement omitted a native conversation")
			}
			rawValue, err := units.conversationJSON(sourceFiles{ctx: ctx}, root, byMember[name][index])
			if err != nil {
				return err
			}
			value, err := claudeSourceJSON(rawValue)
			if err != nil {
				return err
			}
			if jsonText(original) != jsonText(value) {
				return errors.New("replacement differs from native conversation source values")
			}
			index++
			matched++
		}
		if _, err := decoder.Token(); err != nil {
			return err
		}
		if index != len(byMember[name]) {
			return errors.New("replacement has unmatched conversations")
		}
		return ensureEOF(decoder)
	}
	native := filepath.Join(claudeImportJobDir(config, job.Name, job.ID), part.Storage)
	if strings.EqualFold(filepath.Ext(native), ".zip") {
		archive, err := zip.OpenReader(native)
		if err != nil {
			return err
		}
		defer archive.Close()
		if _, err := claudeZipListing(&archive.Reader); err != nil {
			return err
		}
		for _, entry := range archive.File {
			if entry.FileInfo().IsDir() {
				directories = append(directories, entry.Name)
				continue
			}
			reader, err := entry.Open()
			if err != nil {
				return err
			}
			if claudeConversationName(entry.Name) {
				err = decode(reader, entry.Name)
			} else {
				member, ok := members[entry.Name]
				if !ok {
					reader.Close()
					return errors.New("replacement omitted a native ZIP member")
				}
				hash := sha256.New()
				var size int64
				size, err = io.Copy(hash, claudeContextReader{ctx, reader})
				if err == nil && (size != member.Size || hex.EncodeToString(hash.Sum(nil)) != member.SHA256) {
					err = errors.New("replacement differs from native member bytes")
				}
				if err == nil {
					_, err = units.member(sourceFiles{ctx: ctx}, root, member)
				}
				delete(members, entry.Name)
			}
			closeErr := reader.Close()
			if err != nil || closeErr != nil {
				return errors.Join(err, closeErr)
			}
		}
	} else {
		file, err := os.Open(native)
		if err != nil {
			return err
		}
		defer file.Close()
		if err := decode(file, ""); err != nil {
			return err
		}
	}
	sort.Strings(directories)
	expectedDirectories := append([]string{}, units.Directories...)
	sort.Strings(expectedDirectories)
	if jsonText(directories) != jsonText(expectedDirectories) {
		return errors.New("replacement directory membership differs from the native input")
	}
	sort.Strings(conversationFiles)
	expected := append([]string{}, units.ConversationMembers...)
	sort.Strings(expected)
	if matched != len(units.Conversations) || len(members) > 0 || jsonText(conversationFiles) != jsonText(expected) {
		return errors.New("replacement membership differs from the native input")
	}
	return nil
}

// Retirement verifies the physical pack identity as well as logical values.
// A modified header/trailer outside an object's section must not leave a graph
// that capture's immutable-pack verification would refuse to preserve.
func verifyClaudeUnitPacks(ctx context.Context, root string, units *claudeSourceUnits) error {
	for _, pack := range units.packs() {
		name, err := claudeOwnedPath(root, pack)
		if err != nil {
			return err
		}
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		hash := sha256.New()
		size, err := io.Copy(hash, claudeContextReader{ctx, io.LimitReader(file, claudeObjectPackLimit+1)})
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
		expected := strings.TrimSuffix(filepath.Base(pack), ".pack")
		if size > claudeObjectPackLimit || hex.EncodeToString(hash.Sum(nil)) != expected {
			return errors.New("Claude source pack failed physical identity verification")
		}
	}
	return nil
}

func verifyClaudeUnitClosure(ctx context.Context, root string, units *claudeSourceUnits) error {
	if err := verifyClaudeUnitPacks(ctx, root, units); err != nil {
		return err
	}
	for hash, ref := range units.Objects {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := units.value(sourceFiles{ctx: ctx}, root, hash, ref, 0); err != nil {
			return err
		}
	}
	return nil
}

// Every supported service, CLI index, MCP reader and backup holds the shared
// library-use lease. Exclusive ownership is required before native retirement.
// Captured copies, durable pins and user-managed Downloads are never unlinked.
func applyClaudeCompaction(ctx context.Context, config Config, name, id string) (*claudeCompactionRun, error) {
	if !validRecordHash(id) {
		return nil, errors.New("invalid Claude compaction plan ID")
	}
	name, err := claudeImportName(name)
	if err != nil {
		return nil, err
	}
	use, err := fileLease(libraryUseLock(config), true)
	if err != nil {
		return nil, err
	}
	defer use.Close()
	lease, err := claudeImportLock(config, name)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	root := claudeImportRoot(config, name)
	planPath, err := claudeOwnedPath(root, ".compaction/plans/"+id+".json")
	if err != nil {
		return nil, err
	}
	var p claudeCompactionPlan
	if err := claudeReadJSON(sourceFiles{ctx: ctx}, planPath, &p); err != nil {
		return nil, err
	}
	planID := p.ID
	p.ID = ""
	if planID != id || hashBytes([]byte(jsonText(p))) != id || p.Format != claudeCompactionFormat || p.Host != currentHost().ID || p.Source != name {
		return nil, errors.New("invalid or changed Claude compaction plan")
	}
	p.ID = id
	d, identity, err := claudeCompactionIdentity(config)
	if err != nil {
		return nil, err
	}
	if p.Library != d.ID || p.Instance != d.Instance || p.RootIdentity != identity {
		return nil, errors.New("Claude compaction plan belongs to another library or volume")
	}
	run := &claudeCompactionRun{Plan: id, State: "running", Completed: map[string]bool{}}
	runPath, err := claudeOwnedPath(root, ".compaction/runs/"+id+".json")
	if err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(runPath); err == nil {
		if json.Unmarshal(data, run) != nil || run.Plan != id || run.Completed == nil {
			return nil, errors.New("invalid Claude compaction progress")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(runPath), 0700); err != nil {
		return nil, err
	}
	finish := func(err error) (*claudeCompactionRun, error) {
		if err != nil {
			run.State = "interrupted"
			run.Error = err.Error()
		} else {
			run.State = "complete"
			if run.BlockedItems > 0 {
				run.State = "partial"
			}
			run.Error = ""
		}
		return run, errors.Join(err, writeFileDurably(runPath, []byte(jsonText(run))))
	}
	run.BlockedItems = 0
	for _, item := range p.Items {
		if item.Blocked != "" {
			run.BlockedItems++
		}
	}
	for _, item := range p.Items {
		if err := ctx.Err(); err != nil {
			return finish(err)
		}
		if item.Blocked != "" {
			continue
		}
		key := item.Job + ":" + item.Part
		if _, err := claudeOwnedPath(root, "snapshots/"+item.Job+"/job.json"); err != nil {
			return finish(err)
		}
		job, err := loadClaudeJob(config, name, item.Job)
		if err != nil {
			return finish(err)
		}
		index := -1
		for i := range job.Parts {
			if job.Parts[i].ID == item.Part {
				index = i
				break
			}
		}
		if index < 0 {
			return finish(errors.New("planned Claude part disappeared"))
		}
		part := &job.Parts[index]
		if job.State != "collected" || part.State != "collected" || part.SHA256 != item.NativeSHA256 || part.UnitsSHA256 != item.UnitsSHA256 || part.Size != item.Bytes {
			return finish(errors.New("Claude compaction plan is stale; prepare a new plan"))
		}
		if err := verifyClaudeReplacement(ctx, config, job, *part); err != nil {
			return finish(err)
		}
		current, rootID, err := claudeCompactionIdentity(config)
		if err != nil || current.ID != p.Library || current.Instance != p.Instance || rootID != p.RootIdentity {
			return finish(errors.Join(err, errors.New("Claude compaction library or volume changed")))
		}
		d = current
		if d.Version < libraryClaudeObjectsVersion {
			d.Version = libraryClaudeObjectsVersion
			if err := durableJSON(descriptorPath(config), d); err != nil {
				return finish(err)
			}
		}
		proof := claudeRetirementProof{Format: claudeCompactionFormat, Job: job.ID, Part: part.ID, NativeSHA256: part.SHA256, UnitsSHA256: part.UnitsSHA256, Transformation: claudeSourceTransformation}
		data := []byte(jsonText(proof))
		proofID := hashBytes(data)
		proofPath, err := claudeOwnedPath(root, ".compaction/proofs/"+proofID+".json")
		if err != nil {
			return finish(err)
		}
		if err := os.MkdirAll(filepath.Dir(proofPath), 0700); err != nil {
			return finish(err)
		}
		if err := ensureFree(root, int64(len(data))); err != nil {
			return finish(err)
		}
		if err := writeFileDurably(proofPath, data); err != nil {
			return finish(err)
		}
		if captureFault != nil {
			if err := captureFault("claude-retirement-proof", key); err != nil {
				return finish(err)
			}
		}
		job.Version = 2
		part.Retired = true
		part.Retirement = proofID
		if err := ctx.Err(); err != nil {
			return finish(err)
		}
		if _, rootID, err := claudeCompactionIdentity(config); err != nil || rootID != p.RootIdentity {
			return finish(errors.Join(err, errors.New("Claude compaction volume changed before inventory publication")))
		}
		if err := saveClaudeJob(config, job); err != nil {
			return finish(err)
		}
		if err := publishClaudeInventory(config, job); err != nil {
			return finish(err)
		}
		if captureFault != nil {
			if err := captureFault("claude-retirement-inventory", key); err != nil {
				return finish(err)
			}
		}
		native, err := claudeOwnedPath(claudeImportJobDir(config, name, job.ID), part.Storage)
		if err != nil {
			return finish(err)
		}
		if info, err := os.Stat(native); err == nil {
			copy := *part
			copy.Retired = false
			if err := verifyClaudeStoredPart(ctx, claudeImportJobDir(config, name, job.ID), copy); err != nil {
				return finish(err)
			}
			after, err := os.Lstat(native)
			if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
				return finish(errors.New("managed Claude input changed before retirement"))
			}
			if err := ctx.Err(); err != nil {
				return finish(err)
			}
			if _, rootID, err := claudeCompactionIdentity(config); err != nil || rootID != p.RootIdentity {
				return finish(errors.Join(err, errors.New("Claude compaction volume changed before unlink")))
			}
			// Keep deletion rooted even if an uncooperative filesystem writer
			// swaps a parent for a symlink after our identity checks.
			directoryRoot, err := os.OpenRoot(config.CaptureRoot)
			if err != nil {
				return finish(err)
			}
			relative, relErr := filepath.Rel(config.CaptureRoot, native)
			if relErr != nil {
				directoryRoot.Close()
				return finish(relErr)
			}
			err = directoryRoot.Remove(relative)
			directoryRoot.Close()
			if err != nil {
				return finish(err)
			}
			directory, err := os.Open(filepath.Dir(native))
			if err != nil {
				return finish(err)
			}
			err = syncToDrive(directory)
			directory.Close()
			if err != nil {
				return finish(err)
			}
		} else if !os.IsNotExist(err) {
			return finish(err)
		}
		if captureFault != nil {
			if err := captureFault("claude-retirement-unlink", key); err != nil {
				return finish(err)
			}
		}
		if !run.Completed[key] {
			run.RetiredBytes += part.Size
			run.Completed[key] = true
		}
		if err := writeFileDurably(runPath, []byte(jsonText(run))); err != nil {
			return finish(err)
		}
	}
	return finish(nil)
}

func readClaudeRetirementProof(files sourceFiles, root, job, part string, asset claudeExportAsset) (string, error) {
	if !validRecordHash(asset.Retirement) {
		return "", errors.New("retired Claude input has no durable proof")
	}
	name, err := claudeOwnedPath(root, ".compaction/proofs/"+asset.Retirement+".json")
	if err != nil {
		return "", err
	}
	file, err := files.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil {
		return "", err
	}
	var proof claudeRetirementProof
	if len(data) > 8192 || hashBytes(data) != asset.Retirement || json.Unmarshal(data, &proof) != nil || proof.Format != claudeCompactionFormat || proof.Job != job || proof.Part != part || proof.NativeSHA256 != asset.SHA256 || proof.UnitsSHA256 != asset.UnitsSHA256 || proof.Transformation != claudeSourceTransformation {
		return "", errors.New("Claude retirement proof disagrees with its input")
	}
	return name, nil
}

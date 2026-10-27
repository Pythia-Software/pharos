package archive

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
	"filippo.io/age/armor"
	"github.com/klauspost/compress/zstd"
)

const cloudMagic = "PHAROS-CLOUD-1\n"
const cloudManifestName = "pharos-cloud-manifest.json"

var cloudArchiveSpace = ensureFree

// Guard the actual bytes written after compression and optional encryption.
// Snapshot space alone cannot budget this output, and other library writes can
// reduce the available space while compression runs. Keep failures sticky so
// encoder cleanup cannot flush more bytes after a refused write.
type cloudArchiveWriter struct {
	ctx  context.Context
	file *os.File
	err  error
}

func (w *cloudArchiveWriter) Write(data []byte) (int, error) {
	if w.err == nil {
		w.err = w.ctx.Err()
	}
	if w.err == nil {
		if err := cloudArchiveSpace(filepath.Dir(w.file.Name()), int64(len(data))); err != nil {
			w.err = fmt.Errorf("cloud archive stopped to preserve the library drive's free-space reserve: %w", err)
		}
	}
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.file.Write(data)
	if err != nil {
		w.err = err
	}
	return n, err
}

type cloudFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Link   string `json:"link,omitempty"`
}
type cloudManifest struct {
	Version     int         `json:"version"`
	RunID       string      `json:"run_id"`
	Kind        string      `json:"kind"`
	CatalogPath string      `json:"catalog_path,omitempty"`
	ConfigPath  string      `json:"config_path,omitempty"`
	Files       []cloudFile `json:"files"`
}

func cloudCopy(ctx context.Context, w io.Writer, r io.Reader) (int64, error) {
	buf := make([]byte, 1<<20)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := r.Read(buf)
		if n > 0 {
			written, e := w.Write(buf[:n])
			total += int64(written)
			if e != nil {
				return total, e
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}
func cloudWorkDir(config Config, prefix string) (string, error) {
	parent := expandPath(config.StagingRoot)
	if !filepath.IsAbs(parent) {
		return "", errors.New("cloud staging requires an absolute staging_root")
	}
	if _, err := os.Stat(filepath.Dir(parent)); err != nil {
		return "", errors.New("staging drive is unavailable")
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	return os.MkdirTemp(parent, prefix)
}

// Only remove directories named for this interrupted run, after its process
// has been proven gone. A crash must not accumulate a full snapshot per retry.
func cleanInterruptedCloudStaging(config Config, runID string) error {
	parent := expandPath(config.StagingRoot)
	if !filepath.IsAbs(parent) || runID == "" {
		return errors.New("invalid cloud staging location")
	}
	entries, err := os.ReadDir(parent)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "cloud-upload-"+runID+"-") {
			if err := os.RemoveAll(filepath.Join(parent, entry.Name())); err != nil {
				return fmt.Errorf("remove interrupted cloud staging: %w", err)
			}
		}
	}
	return nil
}
func cloudSnapshot(ctx context.Context, config Config, dir string, progress func(string)) (string, string, error) {
	plan, err := planBackup(config)
	if err != nil {
		return "", "", err
	}
	plan.exclude[canonical(dir)] = true
	// This is an owned temporary snapshot, deliberately on the library volume.
	// It is not a durable local backup and must never enter backups.json.
	job := &backupJob{config: config, plan: plan, dest: dir, ephemeral: true, buffer: make([]byte, 1<<20), run: BackupRun{ID: randomToken(), Kind: "backup", State: "pending", Errors: []captureError{}, Warnings: []string{}, Missing: []string{}}}
	run := job.Run(ctx, func(r BackupRun) { progress("Snapshot: " + r.Phase) })
	if run.State != "complete" || job.symlinks > 0 {
		if len(run.Errors) > 0 {
			return "", "", fmt.Errorf("snapshot failed: %s", run.Errors[0].Error)
		}
		return "", "", errors.New("snapshot is incomplete: check unavailable library folders or symbolic links")
	}
	return plan.catalog.Path, plan.files[0].Path, nil
}
func buildCloudArchive(ctx context.Context, d cloudDestination, r *cloudRun, config Config, dir string, progress func(string)) (string, cloudManifest, error) {
	m := cloudManifest{Version: 1, RunID: r.ID, Kind: r.Kind, Files: []cloudFile{}}
	source := filepath.Join(dir, "snapshot")
	if err := os.Mkdir(source, 0700); err != nil {
		return "", m, err
	}
	if r.Kind == "sample" {
		data := make([]byte, 1<<20)
		if _, err := rand.Read(data); err != nil {
			return "", m, err
		}
		if err := ensureFree(source, int64(len(data))); err != nil {
			return "", m, err
		}
		if err := os.WriteFile(filepath.Join(source, "round-trip-test.bin"), data, 0600); err != nil {
			return "", m, err
		}
	} else {
		var err error
		m.CatalogPath, m.ConfigPath, err = cloudSnapshot(ctx, config, source, progress)
		if err != nil {
			return "", m, err
		}
	}
	path := filepath.Join(dir, "archive")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", m, err
	}
	defer file.Close()
	digest := sha256.New()
	var output io.Writer = io.MultiWriter(&cloudArchiveWriter{ctx: ctx, file: file}, digest)
	var encrypted io.WriteCloser
	if d.plan.PasswordProtected {
		if d.recovery == nil {
			return "", m, errors.New("recovery bundle is missing")
		}
		header, _ := json.Marshal(d.recovery)
		if _, err = io.WriteString(output, cloudMagic); err != nil {
			return "", m, err
		}
		if err = binary.Write(output, binary.BigEndian, uint32(len(header))); err != nil {
			return "", m, err
		}
		if _, err = output.Write(header); err != nil {
			return "", m, err
		}
		recipient, e := age.ParseX25519Recipient(d.plan.PublicRecipient)
		if e != nil {
			return "", m, e
		}
		encrypted, err = age.Encrypt(output, recipient)
		if err != nil {
			return "", m, err
		}
		output = encrypted
	}
	progress("Compressing archive")
	compressed, err := zstd.NewWriter(output, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(3)), zstd.WithEncoderConcurrency(2))
	if err != nil {
		return "", m, err
	}
	defer compressed.Close()
	tw := tar.NewWriter(compressed)
	links := map[backupLinkKey]cloudFile{}
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() == backupLockName {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("snapshot contains a non-regular file")
		}
		if info.Size() > 5*(1<<40) {
			return errors.New("a snapshot file exceeds the 5 TiB restore limit")
		}
		if len(m.Files) >= 200000 {
			return errors.New("the library exceeds the supported 200,000-file restore limit")
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == cloudManifestName {
			return errors.New("reserved manifest name appears in snapshot")
		}
		key, keyed := linkKey(info)
		if previous, ok := links[key]; keyed && ok {
			if err = tw.WriteHeader(&tar.Header{Name: rel, Mode: 0600, ModTime: info.ModTime(), Typeflag: tar.TypeLink, Linkname: previous.Path}); err != nil {
				return err
			}
			m.Version = 2
			m.Files = append(m.Files, cloudFile{Path: rel, Size: previous.Size, SHA256: previous.SHA256, Link: previous.Path})
			return nil
		}
		header := &tar.Header{Name: rel, Mode: 0600, Size: info.Size(), ModTime: info.ModTime(), Typeflag: tar.TypeReg}
		if err = tw.WriteHeader(header); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		count, err := cloudCopy(ctx, io.MultiWriter(tw, hash), io.LimitReader(in, info.Size()+1))
		in.Close()
		if err != nil {
			return err
		}
		if count != info.Size() {
			return errors.New("snapshot changed while archiving")
		}
		m.Files = append(m.Files, cloudFile{Path: rel, Size: count, SHA256: hex.EncodeToString(hash.Sum(nil))})
		if keyed {
			links[key] = m.Files[len(m.Files)-1]
		}
		r.FileBytes += count
		r.Files++
		if r.Files%128 == 0 {
			progress("Compressing archive")
		}
		return nil
	})
	if err != nil {
		return "", m, err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "", m, err
	}
	if len(raw) > 64<<20 {
		return "", m, errors.New("archive manifest exceeds the supported restore size")
	}
	if err = tw.WriteHeader(&tar.Header{Name: cloudManifestName, Mode: 0600, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
		return "", m, err
	}
	if _, err = tw.Write(raw); err != nil {
		return "", m, err
	}
	if err = tw.Close(); err != nil {
		return "", m, err
	}
	if err = compressed.Close(); err != nil {
		return "", m, err
	}
	if encrypted != nil {
		if err = encrypted.Close(); err != nil {
			return "", m, err
		}
	}
	if err = file.Sync(); err != nil {
		return "", m, err
	}
	info, err := file.Stat()
	if err != nil {
		return "", m, err
	}
	r.TotalBytes = info.Size()
	r.SHA256 = hex.EncodeToString(digest.Sum(nil))
	return path, m, nil
}
func cloudIdentities(bundle cloudRecovery, password []byte) ([]age.Identity, error) {
	if bundle.Version != 1 || bundle.Format != "age-password-recovery-v1" {
		return nil, errors.New("invalid archive recovery bundle")
	}
	key, err := age.NewScryptIdentity(string(password))
	if err != nil {
		return nil, err
	}
	decrypted, err := age.Decrypt(armor.NewReader(strings.NewReader(bundle.EncryptedIdentity)), key)
	if err != nil {
		return nil, errors.New("the recovery password did not unlock this archive")
	}
	identity, err := io.ReadAll(io.LimitReader(decrypted, 16385))
	if err != nil || len(identity) > 16384 {
		return nil, errors.New("could not authenticate the recovery identity")
	}
	defer clear(identity)
	identities, err := age.ParseIdentities(bytes.NewReader(identity))
	if err != nil {
		return nil, errors.New("invalid recovered identity")
	}
	return identities, nil
}
func (c *Catalog) verifyCloudArchive(ctx context.Context, config Config, r *cloudRun, path string, password []byte) (string, error) {
	var raw string
	if err := c.reader().QueryRowContext(ctx, "SELECT manifest_json FROM cloud_backup_runs WHERE id=?", r.ID).Scan(&raw); err != nil {
		return "", err
	}
	var expected cloudManifest
	if err := json.Unmarshal([]byte(raw), &expected); err != nil {
		return "", errors.New("backup manifest is unavailable")
	}
	file, err := os.OpenFile(expandPath(path), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", errors.New("downloaded archive not found; check the local file path")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != r.TotalBytes {
		return "", errors.New("downloaded archive has the wrong size")
	}
	hash := sha256.New()
	if _, err = cloudCopy(ctx, hash, file); err != nil {
		return "", err
	}
	if hex.EncodeToString(hash.Sum(nil)) != r.SHA256 {
		return "", errors.New("downloaded archive does not match the uploaded revision (SHA-256 mismatch)")
	}
	if _, err = file.Seek(0, 0); err != nil {
		return "", err
	}
	d, err := c.cloudDestination(ctx, r.DestinationID, true)
	if err != nil {
		return "", err
	}
	recipient := ""
	if d.plan.PasswordProtected {
		recipient = d.plan.PublicRecipient
	}
	payload, err := cloudPayload(file, password, recipient)
	if err != nil {
		return "", err
	}
	return extractCloudArchive(ctx, config, payload, &expected)
}

func cloudPayload(file io.Reader, password []byte, recipient string) (io.Reader, error) {
	reader := bufio.NewReader(file)
	magic, err := reader.Peek(len(cloudMagic))
	if err != nil {
		return nil, errors.New("invalid cloud archive")
	}
	if string(magic) != cloudMagic {
		if recipient != "" {
			return nil, errors.New("expected an encrypted cloud archive")
		}
		return reader, nil
	}
	if _, err = reader.Discard(len(cloudMagic)); err != nil {
		return nil, err
	}
	var size uint32
	if err = binary.Read(reader, binary.BigEndian, &size); err != nil || size > 1<<16 {
		return nil, errors.New("invalid recovery bundle size")
	}
	header := make([]byte, size)
	if _, err = io.ReadFull(reader, header); err != nil {
		return nil, err
	}
	var bundle cloudRecovery
	if json.Unmarshal(header, &bundle) != nil || (recipient != "" && bundle.Recipient != recipient) {
		return nil, errors.New("archive recovery bundle does not match this destination")
	}
	identities, err := cloudIdentities(bundle, password)
	if err != nil {
		return nil, err
	}
	payload, err := age.Decrypt(reader, identities...)
	if err != nil {
		return nil, errors.New("the recovery identity cannot decrypt this payload")
	}
	return payload, nil
}
func safeCloudPath(name string) bool {
	return filepath.IsLocal(name) && name != "." && !strings.Contains(name, "\\") && filepath.ToSlash(filepath.Clean(filepath.FromSlash(name))) == name
}

// Both normal verification and disaster recovery use this extractor. A known
// manifest anchors validation to the upload ledger; recovery without a catalog
// uses the embedded manifest after authenticating/decompressing the whole file.
func extractCloudArchive(ctx context.Context, config Config, payload io.Reader, expected *cloudManifest) (string, error) {
	decoded, err := zstd.NewReader(payload, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(256<<20))
	if err != nil {
		return "", errors.New("invalid compressed archive")
	}
	defer decoded.Close()
	directory, err := cloudWorkDir(config, "cloud-restore-")
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(directory)
		}
	}()
	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", err
	}
	defer root.Close()
	original := map[string]cloudFile{}
	var total int64
	if expected != nil {
		for _, file := range expected.Files {
			if !safeCloudPath(file.Path) || file.Size < 0 || original[file.Path].Path != "" {
				return "", errors.New("invalid or duplicate recorded file path")
			}
			original[file.Path] = file
			if file.Link == "" {
				total += file.Size
			}
		}
		if err = ensureFree(directory, total+(128<<20)); err != nil {
			return "", err
		}
	}
	actual := map[string]cloudFile{}
	var embedded *cloudManifest
	reader := tar.NewReader(decoded)
	for {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		header, e := reader.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", fmt.Errorf("archive decompression failed: %w", e)
		}
		if (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeLink) || !safeCloudPath(header.Name) || actual[header.Name].Path != "" || header.Size < 0 || header.Size > 5*(1<<40) || embedded != nil {
			return "", errors.New("archive has unsafe paths, links, duplicate entries or files after its manifest")
		}
		if header.Name == cloudManifestName {
			if header.Typeflag != tar.TypeReg {
				return "", errors.New("archive manifest must be a regular file")
			}
			if header.Size > 64<<20 {
				return "", errors.New("archive manifest is too large")
			}
			data, e := io.ReadAll(reader)
			if e != nil {
				return "", e
			}
			embedded = &cloudManifest{}
			if json.Unmarshal(data, embedded) != nil {
				return "", errors.New("invalid embedded archive manifest")
			}
			continue
		}
		if len(actual) >= 200000 {
			return "", errors.New("archive exceeds the supported 200,000-file restore limit")
		}
		if header.Typeflag == tar.TypeLink {
			previous, ok := actual[header.Linkname]
			if !safeCloudPath(header.Linkname) || !ok || previous.Link != "" || header.Size != 0 {
				return "", errors.New("archive link must name an earlier regular file")
			}
			entry := cloudFile{Path: header.Name, Size: previous.Size, SHA256: previous.SHA256, Link: header.Linkname}
			if expected != nil && entry != original[header.Name] {
				return "", errors.New("archive link differs from recorded snapshot")
			}
			if err = root.MkdirAll(filepath.Dir(header.Name), 0700); err != nil {
				return "", err
			}
			if err = root.Link(header.Linkname, header.Name); err != nil {
				if err = ensureFree(directory, previous.Size+(128<<20)); err != nil {
					return "", err
				}
				in, e := root.Open(header.Linkname)
				if e != nil {
					return "", e
				}
				out, e := root.OpenFile(header.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if e != nil {
					in.Close()
					return "", e
				}
				_, e = cloudCopy(ctx, out, in)
				in.Close()
				closeErr := out.Close()
				if e != nil {
					return "", e
				}
				if closeErr != nil {
					return "", closeErr
				}
			}
			actual[header.Name] = entry
			continue
		}
		if expected != nil {
			entry, ok := original[header.Name]
			if !ok || entry.Size != header.Size {
				return "", fmt.Errorf("unexpected file or size: %q", header.Name)
			}
		}
		if err = ensureFree(directory, header.Size+(128<<20)); err != nil {
			return "", err
		}
		if err = root.MkdirAll(filepath.Dir(header.Name), 0700); err != nil {
			return "", err
		}
		out, e := root.OpenFile(header.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return "", e
		}
		hash := sha256.New()
		n, e := cloudCopy(ctx, io.MultiWriter(out, hash), reader)
		closeErr := out.Close()
		if e != nil {
			return "", e
		}
		if closeErr != nil {
			return "", closeErr
		}
		entry := cloudFile{Path: header.Name, Size: n, SHA256: hex.EncodeToString(hash.Sum(nil))}
		actual[header.Name] = entry
		if expected != nil && entry != original[header.Name] {
			return "", fmt.Errorf("restored content does not match: %q", header.Name)
		}
	}
	// Drain through the authenticated final age chunk and zstd frame checksum.
	if n, e := cloudCopy(ctx, io.Discard, decoded); e != nil || n != 0 {
		return "", errors.New("archive has unauthenticated or unexpected trailing content")
	}
	if embedded == nil || (embedded.Version != 1 && embedded.Version != 2) || (embedded.Kind != "sample" && embedded.Kind != "full") || len(actual) != len(embedded.Files) {
		return "", errors.New("archive is missing files or its manifest")
	}
	if expected != nil && !reflect.DeepEqual(*embedded, *expected) {
		return "", errors.New("archive manifest differs from the uploaded snapshot")
	}
	seen := map[string]bool{}
	for _, entry := range embedded.Files {
		if entry.Link != "" && embedded.Version < 2 {
			return "", errors.New("archive link requires manifest version 2")
		}
		if !safeCloudPath(entry.Path) || seen[entry.Path] || entry != actual[entry.Path] {
			return "", errors.New("recovered files do not match the embedded manifest")
		}
		seen[entry.Path] = true
	}
	if embedded.Kind == "full" {
		if !seen[embedded.ConfigPath] || !seen[embedded.CatalogPath] || !seen[backupManifestName] {
			return "", errors.New("restored library is missing its catalog, configuration or snapshot record")
		}
		dbpath := filepath.Join(directory, filepath.FromSlash(embedded.CatalogPath))
		dsn := (&url.URL{Scheme: "file", Path: dbpath}).String() + "?mode=ro&immutable=1"
		db, e := sql.Open("sqlite", dsn)
		if e != nil {
			return "", e
		}
		rows, e := db.QueryContext(ctx, "PRAGMA integrity_check")
		if e != nil {
			db.Close()
			return "", e
		}
		count := 0
		for rows.Next() {
			var result string
			if e = rows.Scan(&result); e != nil || result != "ok" {
				rows.Close()
				db.Close()
				return "", errors.New("recovered catalog failed SQLite integrity_check")
			}
			count++
		}
		e = rows.Err()
		rows.Close()
		db.Close()
		if e != nil || count != 1 {
			return "", errors.New("recovered catalog integrity check did not complete")
		}
	}
	complete = true
	return directory, nil
}

// Retain known files for explicit recovery. No live-library paths are replaced.
func (c *Catalog) finishCloudVerification(ctx context.Context, config Config, r *cloudRun, path string, password []byte) error {
	lockPath := filepath.Join(expandPath(config.StagingRoot), "cloud-verify-"+r.ID+".lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("verification is already running for this revision")
	}

	r.Verified = false
	r.VerificationState = "running"
	r.VerificationPID = os.Getpid()
	r.VerificationHost = currentHost().ID
	r.VerificationError = ""
	r.RestoredDirectory = ""
	if err := c.saveCloudRun(ctx, r, nil); err != nil {
		return err
	}
	directory, err := c.verifyCloudArchive(ctx, config, r, path, password)
	r.VerificationState = "complete"
	if err != nil {
		r.VerificationState = "failed"
		r.VerificationError = err.Error()
	} else {
		r.Verified = true
		r.RestoredDirectory = directory
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if e := c.saveCloudRun(saveCtx, r, nil); e != nil {
		return e
	}
	return err
}

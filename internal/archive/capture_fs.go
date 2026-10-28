package archive

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

// Adapters read a captured source through its captureView, which knows the
// manifest, rather than through the OS (docs/capture-compression.md, phase 2).
// Names are the same either way, so listing still walks the directory; what
// the view changes is what a captured file of an encoded entry looks like:
//
//   - stat reports its original size (the manifest's size) and the original
//     mtime, which compression keeps on the encoded file;
//   - open streams its original bytes, decoding as it reads;
//   - materialize decodes it into the reader's lease, for readers that need a
//     path (SQLite) or random access.
//
// Nothing is decoded by listing or stat, so planning, which compares sizes
// and versions with source_item_states, decodes nothing; only parsing does.
// A live source has no view and reads the OS directly.

// sourceFiles is an adapter's access to its source's files.
type sourceFiles struct {
	view *captureView
	ctx  context.Context
}

func (a baseAdapter) files() sourceFiles { return sourceFiles{view: a.view, ctx: a.context()} }

// rel is path's captured path in the view, if it is inside it.
func (v *captureView) rel(path string) (string, bool) {
	rel, err := filepath.Rel(v.dir, path)
	if err != nil || !filepath.IsLocal(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// encodedAt is the manifest entry of the captured file at path when it is
// stored encoded and not yet decoded in this view.
func (v *captureView) encodedAt(path string) (capturedVersion, bool) {
	if v == nil || v.manifest == nil {
		return capturedVersion{}, false
	}
	rel, ok := v.rel(path)
	if !ok || v.decoded[rel] {
		return capturedVersion{}, false
	}
	entry, ok := v.manifest.entry(rel)
	return entry, ok && entry.Encoding != ""
}

// originalInfo reports an encoded file as its original: the recorded size.
type originalInfo struct {
	fs.FileInfo
	size int64
}

func (i originalInfo) Size() int64 { return i.size }

// Stat is os.Stat, with an encoded captured file at its original size.
func (s sourceFiles) Stat(path string) (fs.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return s.original(path, info), nil
}

// Info is entry.Info() for a walk or directory listing, likewise.
func (s sourceFiles) Info(path string, entry fs.DirEntry) (fs.FileInfo, error) {
	info, err := entry.Info()
	if err != nil {
		return nil, err
	}
	return s.original(path, info), nil
}

func (s sourceFiles) original(path string, info fs.FileInfo) fs.FileInfo {
	if entry, ok := s.view.encodedAt(path); ok && info.Mode().IsRegular() {
		return originalInfo{FileInfo: info, size: entry.Size}
	}
	return info
}

// Open opens path for reading its original bytes forward.
func (s sourceFiles) Open(path string) (*sourceFile, error) {
	if s.view != nil {
		if rel, ok := s.view.rel(path); ok {
			return s.view.open(rel)
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	return &sourceFile{Reader: file, file: file, info: info}, nil
}

// ReadFile reads path's original bytes.
func (s sourceFiles) ReadFile(path string) ([]byte, error) {
	file, err := s.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

// Tail is the last limit bytes of path's original bytes. A plain file is read
// at its end; an encoded one is streamed through, written nowhere.
func (s sourceFiles) Tail(path string, limit int64) ([]byte, error) {
	file, err := s.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	size := file.info.Size()
	offset := max(size-limit, 0)
	if file.decoder == nil {
		buffer := make([]byte, size-offset)
		n, err := file.file.ReadAt(buffer, offset)
		if err != nil && err != io.EOF {
			return nil, err
		}
		return buffer[:n], nil
	}
	if _, err := io.CopyN(io.Discard, file, offset); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(file, limit))
}

// sourceFile is an open source file: its original bytes, read forward, and
// its original FileInfo.
type sourceFile struct {
	io.Reader
	file    *os.File
	decoder *zstd.Decoder
	info    fs.FileInfo
}

func (f *sourceFile) Stat() (fs.FileInfo, error) { return f.info, nil }

func (f *sourceFile) Close() error {
	if f.decoder != nil {
		f.decoder.Close()
	}
	return f.file.Close()
}

// open streams the original bytes of the captured entry rel for a reader that
// only reads forward; nothing is written. The entry is classified from the
// open handle, so a compression renaming over the name meanwhile cannot make
// it read zstd as plain.
func (v *captureView) open(rel string) (*sourceFile, error) {
	file, err := os.Open(filepath.Join(v.dir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	entry, ok := v.manifest.entry(rel)
	if !ok || v.decoded[rel] {
		return &sourceFile{Reader: file, file: file, info: info}, nil
	}
	entry = storedAsFile(file, entry)
	switch entry.Encoding {
	case "":
		return &sourceFile{Reader: file, file: file, info: info}, nil
	case captureEncodingZstd:
	default:
		file.Close()
		return nil, fmt.Errorf("%s has unsupported capture encoding %q; upgrade Pharos", rel, entry.Encoding)
	}
	if capturedContentHook != nil {
		capturedContentHook(file.Name())
	}
	decoder, err := newCaptureDecoder(bufio.NewReaderSize(file, 1<<20))
	if err != nil {
		file.Close()
		return nil, err
	}
	return &sourceFile{Reader: decoder, file: file, decoder: decoder, info: originalInfo{FileInfo: info, size: entry.Size}}, nil
}

// materialize returns a path at which the captured entry rel can be opened
// by path (SQLite, ReadAt): the leased file, or for an encoded entry its
// decoded copy, which replaces the lease's link at the same relative path so
// that every path translation of the view still holds. Only a reader lease's
// view decodes; a view over the capture directory itself never writes there.
func (v *captureView) materialize(ctx context.Context, rel string) (string, error) {
	file := filepath.Join(v.dir, filepath.FromSlash(rel))
	entry, ok := v.manifest.entry(rel)
	if !ok || entry.Encoding == "" || v.decoded[rel] {
		return file, nil
	}
	if !v.lease {
		return "", fmt.Errorf("%s is stored %s-encoded and this reader holds no lease to decode it into", rel, entry.Encoding)
	}
	if err := materializeCaptured(ctx, file, file, entry); err != nil {
		return "", err
	}
	if v.decoded == nil {
		v.decoded = map[string]bool{}
	}
	v.decoded[rel] = true
	return file, nil
}

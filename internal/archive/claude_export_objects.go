package archive

// Additive retained JSON source units. Native parts remain authoritative and
// available to older readers; no input is retired by publishing these units.

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const claudeUnitsFormat = "claude-json-source-units-v1"
const claudeObjectPackLimit = 8 << 20
const claudeObjectSizeLimit = 64 << 20

var claudeObjectReadHook func(string)

type claudeObjectRef struct {
	Pack   string   `json:"pack"`
	Offset int64    `json:"offset"`
	Length int64    `json:"length"`
	Size   int64    `json:"size"`
	Pieces []string `json:"pieces,omitempty"`
}

type claudeConversationUnit struct {
	ID         string   `json:"id"`
	Member     string   `json:"member,omitempty"`
	Envelope   string   `json:"envelope"`
	Messages   []string `json:"messages"`
	Occurrence string   `json:"observation"`
	Observed   int64    `json:"observed_ns,string"`
}

type claudeUnitHead struct {
	Content       string `json:"content"`
	Occurrence    string `json:"observation"`
	Observed      int64  `json:"observed_ns,string"`
	FirstObserved int64  `json:"first_observed_ns,string"`
}

type claudeSourceUnits struct {
	Directories         []string                   `json:"directories,omitempty"`
	ConversationMembers []string                   `json:"conversation_members,omitempty"`
	Format              string                     `json:"format"`
	SourceSHA256        string                     `json:"source_sha256"`
	Objects             map[string]claudeObjectRef `json:"objects"`
	Conversations       []claudeConversationUnit   `json:"conversations"`
	Supplements         bool                       `json:"supplements,omitempty"`
	Members             []claudeSourceMember       `json:"members,omitempty"`
	Groups              []claudeSupplementUnit     `json:"groups,omitempty"`
}

type claudeSourceMember struct {
	Name   string `json:"name"`
	Object string `json:"object"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type claudeObjectWriter struct {
	ctx       context.Context
	root      string
	data      bytes.Buffer
	refs      map[string]claudeObjectRef
	pending   map[string]claudeObjectRef
	remaining int64
}

// Only collector-owned relative paths enter the pool. Reject links and special
// files before reading or publishing; native exports themselves use their
// separately rooted acquisition path.
func claudeOwnedPath(root, relative string) (string, error) {
	if !claudeExportPath(relative) {
		return "", errors.New("unsafe Claude retained object path")
	}
	current := root
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("Claude retained object path contains a symbolic link")
		}
		if index < len(parts)-1 {
			if !info.IsDir() {
				return "", errors.New("Claude retained object parent is not a directory")
			}
		} else if !info.Mode().IsRegular() {
			return "", errors.New("Claude retained object is not a regular file")
		}
	}
	return current, nil
}

func (w *claudeObjectWriter) charge(size int64) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	if size < 0 || size > w.remaining {
		return errors.New("Claude retained source units exceed the collection budget")
	}
	if err := ensureFree(w.root, size); err != nil {
		return err
	}
	w.remaining -= size
	return nil
}

func claudeObjectHash(data []byte) string {
	return hashBytes(append([]byte("claude-json-source-v1\x00"), data...))
}

func (w *claudeObjectWriter) put(data []byte) (string, error) {
	if int64(len(data)) > claudeExportLimits.Member {
		return "", errors.New("Claude source value exceeds the member limit")
	}
	if err := w.ctx.Err(); err != nil {
		return "", err
	}
	hash := claudeObjectHash(data)
	if _, ok := w.refs[hash]; ok {
		return hash, nil
	}
	if _, ok := w.pending[hash]; ok {
		return hash, nil
	}
	// This index is a disposable acquisition accelerator. Portable receipts
	// carry the full references and never depend on the cache for replay.
	cache, err := claudeOwnedPath(w.root, ".object-refs/"+hash[:2]+"/"+hash+".json")
	if err != nil {
		return "", err
	}
	var ref claudeObjectRef
	if raw, err := os.ReadFile(cache); err == nil {
		if json.Unmarshal(raw, &ref) != nil || !validClaudeObjectRef(ref) {
			return "", errors.New("invalid Claude object lookup; verify retained inputs")
		}
		if len(ref.Pieces) > 0 {
			if err := w.loadPieces(ref, 0); err != nil {
				return "", err
			}
		} else {
			name, err := claudeOwnedPath(w.root, ref.Pack)
			if err != nil {
				return "", err
			}
			info, err := os.Stat(name)
			if err != nil || !info.Mode().IsRegular() || info.Size() < ref.Offset+ref.Length {
				return "", errors.New("Claude object pack is missing or truncated; repair retained inputs")
			}
		}
		w.refs[hash] = ref
		return hash, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if len(data) > 256<<10 {
		pieces := []string{}
		for _, chunk := range claudeSourceChunks(data) {
			hash, err := w.put(chunk)
			if err != nil {
				return "", err
			}
			pieces = append(pieces, hash)
		}
		if err := w.flush(); err != nil {
			return "", err
		}
		ref := claudeObjectRef{Size: int64(len(data)), Pieces: pieces}
		w.refs[hash] = ref
		if err := w.charge(int64(len(jsonText(ref)))); err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
			return "", err
		}
		if err := writeFileDurably(cache, []byte(jsonText(ref))); err != nil {
			return "", err
		}
		return hash, nil
	}
	var compressed bytes.Buffer
	encoder := gzip.NewWriter(&compressed)
	if _, err := encoder.Write(data); err != nil {
		return "", err
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	if w.data.Len() > 0 && w.data.Len()+compressed.Len() > claudeObjectPackLimit {
		if err := w.flush(); err != nil {
			return "", err
		}
	}
	ref = claudeObjectRef{Offset: int64(w.data.Len()), Length: int64(compressed.Len()), Size: int64(len(data))}
	w.data.Write(compressed.Bytes())
	w.pending[hash] = ref
	return hash, nil
}

func validClaudeObjectRef(ref claudeObjectRef) bool {
	if len(ref.Pieces) > 0 {
		if ref.Pack != "" || ref.Offset != 0 || ref.Length != 0 || ref.Size <= 256<<10 || ref.Size > claudeExportLimits.Member || len(ref.Pieces) > 1<<16 {
			return false
		}
		for _, hash := range ref.Pieces {
			if !validRecordHash(hash) {
				return false
			}
		}
		return true
	}
	return strings.HasPrefix(ref.Pack, "objects/") && strings.HasSuffix(ref.Pack, ".pack") && validRecordHash(strings.TrimSuffix(strings.TrimPrefix(ref.Pack, "objects/"), ".pack")) && ref.Offset >= 0 && ref.Length > 0 && ref.Length <= claudeExportLimits.Member && ref.Size >= 0 && ref.Size <= claudeObjectSizeLimit && ref.Offset <= claudeExportLimits.Expanded-ref.Length
}

func (w *claudeObjectWriter) loadPieces(ref claudeObjectRef, depth int) error {
	if depth > 2 {
		return errors.New("Claude chunk reference cycle or excessive depth")
	}
	for _, hash := range ref.Pieces {
		if _, ok := w.refs[hash]; ok {
			continue
		}
		name, err := claudeOwnedPath(w.root, ".object-refs/"+hash[:2]+"/"+hash+".json")
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		var child claudeObjectRef
		if json.Unmarshal(data, &child) != nil || !validClaudeObjectRef(child) {
			return errors.New("invalid Claude source chunk reference")
		}
		if len(child.Pieces) > 0 {
			if err := w.loadPieces(child, depth+1); err != nil {
				return err
			}
		} else {
			name, err := claudeOwnedPath(w.root, child.Pack)
			if err != nil {
				return err
			}
			info, err := os.Stat(name)
			if err != nil || !info.Mode().IsRegular() || info.Size() < child.Offset+child.Length {
				return errors.New("Claude source chunk pack unavailable")
			}
		}
		w.refs[hash] = child
	}
	return nil
}

// gear-v1: fixed SplitMix64 table seed, 32 KiB minimum, 64 KiB mask,
// 256 KiB maximum, resetting at each source value/chunk. These constants are
// part of claude-json-source-units-v1, not runtime tuning parameters.
func claudeSourceChunks(data []byte) [][]byte {
	var gear [256]uint64
	seed := uint64(0x706861726f732d31)
	for i := range gear {
		seed += 0x9e3779b97f4a7c15
		value := seed
		value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
		value = (value ^ (value >> 27)) * 0x94d049bb133111eb
		gear[i] = value ^ (value >> 31)
	}
	chunks := [][]byte{}
	start := 0
	rolling := uint64(0)
	for i, value := range data {
		rolling = (rolling << 1) + gear[value]
		length := i + 1 - start
		if length >= 32<<10 && (rolling&0xffff == 0 || length >= 256<<10) {
			chunks = append(chunks, data[start:i+1])
			start = i + 1
			rolling = 0
		}
	}
	if start < len(data) {
		chunks = append(chunks, data[start:])
	}
	return chunks
}

func (w *claudeObjectWriter) flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	if err := w.ctx.Err(); err != nil {
		return err
	}
	data := w.data.Bytes()
	pack := "objects/" + hashBytes(data) + ".pack"
	destination, err := claudeOwnedPath(w.root, pack)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	metadata := int64(0)
	for _, ref := range w.pending {
		ref.Pack = pack
		metadata += int64(len(jsonText(ref)))
	}
	if err := w.charge(int64(len(data)) + metadata); err != nil {
		return err
	}
	if old, err := os.ReadFile(destination); err == nil {
		if !bytes.Equal(old, data) {
			return errors.New("Claude pack conflicts with its immutable identity")
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if err := writeFileDurably(destination, data); err != nil {
		return err
	}
	if captureFault != nil {
		if err := captureFault("claude-source-pack", pack); err != nil {
			return err
		}
	}
	for hash, ref := range w.pending {
		ref.Pack = pack
		cache, err := claudeOwnedPath(w.root, ".object-refs/"+hash[:2]+"/"+hash+".json")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
			return err
		}
		if err := writeFileDurably(cache, []byte(jsonText(ref))); err != nil {
			return err
		}
		w.refs[hash] = ref
	}
	w.pending = map[string]claudeObjectRef{}
	w.data.Reset()
	return nil
}

// Strict JSON eligibility is separate from the legacy parser: duplicate keys
// and malformed Unicode stay as native-only evidence, rather than silently
// claiming a lossless value transformation after Go's decoder replaced them.
func claudeSourceJSON(data []byte) (map[string]any, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("invalid UTF-8 in Claude source JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 128 {
			return errors.New("Claude source JSON exceeds nesting limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delim != '{' && delim != '[' {
			return errors.New("unexpected JSON delimiter")
		}
		keys := map[string]bool{}
		for decoder.More() {
			if delim == '{' {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || keys[name] {
					return errors.New("duplicate or invalid Claude JSON key")
				}
				keys[name] = true
			}
			if err := value(depth + 1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	}
	if err := value(0); err != nil {
		return nil, err
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}
	// Marshal/Unmarshal equality cannot detect lone surrogate replacement.
	// Check each source string's escapes before decoding into values.
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			continue
		}
		i++
		for ; i < len(data) && data[i] != '"'; i++ {
			if data[i] != '\\' {
				continue
			}
			i++
			if data[i] != 'u' {
				continue
			}
			var code uint16
			if _, err := fmt.Sscanf(string(data[i+1:i+5]), "%04x", &code); err != nil {
				return nil, err
			}
			i += 4
			if code >= 0xd800 && code <= 0xdbff {
				if i+6 >= len(data) || string(data[i+1:i+3]) != "\\u" {
					return nil, errors.New("unpaired JSON surrogate")
				}
				var low uint16
				if _, err := fmt.Sscanf(string(data[i+3:i+7]), "%04x", &low); err != nil || low < 0xdc00 || low > 0xdfff {
					return nil, errors.New("unpaired JSON surrogate")
				}
				i += 6
			} else if code >= 0xdc00 && code <= 0xdfff {
				return nil, errors.New("unpaired JSON surrogate")
			}
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("Claude conversation must be an object")
	}
	return object, nil
}

func retainClaudeSourceUnits(ctx context.Context, config Config, job *claudeImportJob, part *claudeImportPart) error {
	if !part.IsConversation && (part.Manifest || !strings.EqualFold(filepath.Ext(part.Storage), ".zip")) {
		return nil
	}
	root := claudeImportRoot(config, job.Name)
	observed := int64(0)
	if stamp, ok := parseTime(job.ExportedAt); ok {
		observed = stamp.UnixNano()
	} else if stamp, ok := parseTime(part.SourceModified); ok {
		observed = stamp.UnixNano()
	}
	if part.Units != "" {
		name, err := claudeOwnedPath(claudeImportJobDir(config, job.Name, job.ID), part.Units)
		if err != nil {
			return err
		}
		if units, err := readClaudeSourceUnits(sourceFiles{ctx: ctx}, name, part.SHA256, part.UnitsSHA256); err == nil {
			complete := true
			for _, ref := range units.Objects {
				if len(ref.Pieces) > 0 {
					continue
				}
				name, err := claudeOwnedPath(root, ref.Pack)
				if err != nil {
					return err
				}
				info, err := os.Stat(name)
				if err != nil || !info.Mode().IsRegular() || info.Size() < ref.Offset+ref.Length {
					complete = false
					break
				}
			}
			if complete {
				return publishClaudeUnitHeads(root, units, observed)
			}
		}
		part.Units, part.UnitsSHA256 = "", ""
	}
	// A legacy migration must not bind newly read values to a stale native
	// digest. Check before extraction and again before publishing its receipt.
	if err := verifyClaudeStoredPart(ctx, claudeImportJobDir(config, job.Name, job.ID), *part); err != nil {
		return err
	}
	retained, err := claudeImportRetainedBytes(root)
	if err != nil {
		return err
	}
	w := &claudeObjectWriter{ctx: ctx, root: root, refs: map[string]claudeObjectRef{}, pending: map[string]claudeObjectRef{}, remaining: claudeImportBudget - retained}
	units := claudeSourceUnits{Format: claudeUnitsFormat, SourceSHA256: part.SHA256, Conversations: []claudeConversationUnit{}}
	heads := map[string]claudeUnitHead{}
	decode := func(reader io.Reader, member string) error {
		units.ConversationMembers = append(units.ConversationMembers, member)
		decoder := json.NewDecoder(claudeContextReader{ctx, reader})
		if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
			return errors.New("Claude conversations must be an array")
		}
		for decoder.More() {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return err
			}
			object, err := claudeSourceJSON(raw)
			if err != nil {
				return err
			}
			messages, ok := object["chat_messages"].([]any)
			if !ok {
				return errors.New("Claude conversation has no messages array")
			}
			unit := claudeConversationUnit{ID: firstString(object["uuid"]), Member: member, Messages: []string{}}
			for _, message := range messages {
				hash, err := w.put([]byte(jsonText(message)))
				if err != nil {
					return err
				}
				unit.Messages = append(unit.Messages, hash)
			}
			delete(object, "chat_messages")
			unit.Envelope, err = w.put([]byte(jsonText(object)))
			if err != nil {
				return err
			}
			content := hashBytes([]byte(jsonText([]any{unit.Envelope, unit.Messages})))
			headPath, err := claudeOwnedPath(root, ".unit-heads/"+hashBytes([]byte(unit.ID))+".json")
			if err != nil {
				return err
			}
			head := claudeUnitHead{}
			if data, err := os.ReadFile(headPath); err == nil {
				if json.Unmarshal(data, &head) != nil || !validRecordHash(head.Content) || !validRecordHash(head.Occurrence) {
					return errors.New("invalid Claude observation lookup")
				}
			} else if !os.IsNotExist(err) {
				return err
			}
			unit.Observed = observed
			unit.Occurrence = hashBytes([]byte(jsonText([]any{unit.ID, content, observed})))
			if head.Content == content {
				unit.Occurrence = head.Occurrence
			}
			if observed >= head.Observed {
				first := observed
				if unit.Occurrence == head.Occurrence {
					first = head.FirstObserved
				}
				heads[headPath] = claudeUnitHead{Content: content, Occurrence: unit.Occurrence, Observed: observed, FirstObserved: first}
			}
			units.Conversations = append(units.Conversations, unit)
		}
		if _, err := decoder.Token(); err != nil {
			return err
		}
		return ensureEOF(decoder)
	}
	name := filepath.Join(claudeImportJobDir(config, job.Name, job.ID), filepath.FromSlash(part.Storage))
	if strings.EqualFold(filepath.Ext(name), ".zip") {
		archive, err := zip.OpenReader(name)
		if err != nil {
			return err
		}
		defer archive.Close()
		entries, err := claudeZipListing(&archive.Reader)
		if err != nil {
			return err
		}
		for _, entry := range archive.File {
			if entry.FileInfo().IsDir() {
				units.Directories = append(units.Directories, entry.Name)
				continue
			}
			units.Supplements = units.Supplements || claudeSupplementGroup(entry.Name) != ""
			if claudeConversationName(entry.Name) {
				continue
			}
			reader, err := entry.Open()
			if err != nil {
				return err
			}
			data, err := io.ReadAll(claudeContextReader{ctx, io.LimitReader(reader, claudeExportLimits.Member+1)})
			closeErr := reader.Close()
			if err != nil || closeErr != nil {
				return errors.Join(err, closeErr)
			}
			if int64(len(data)) != int64(entry.UncompressedSize64) {
				return errors.New("Claude member changed during source-unit retention")
			}
			hash, err := w.put(data)
			if err != nil {
				return err
			}
			units.Members = append(units.Members, claudeSourceMember{Name: entry.Name, Object: hash, SHA256: hashBytes(data), Size: int64(len(data))})
		}
		for _, entry := range entries {
			reader, err := entry.Open()
			if err != nil {
				return err
			}
			err = decode(reader, entry.Name)
			closeErr := reader.Close()
			if err != nil || closeErr != nil {
				return errors.Join(err, closeErr)
			}
		}
	} else {
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		defer file.Close()
		if err := decode(file, ""); err != nil {
			return err
		}
	}
	if err := verifyClaudeStoredPart(ctx, claudeImportJobDir(config, job.Name, job.ID), *part); err != nil {
		return err
	}
	if err := w.flush(); err != nil {
		return err
	}
	units.Objects = w.refs
	units.describeGroups(observed)
	content := []byte(jsonText(units))
	if int64(len(content)) > claudeExportLimits.Manifest {
		return errors.New("Claude source-unit receipt exceeds metadata limit")
	}
	part.Units = "units/" + part.ID + ".json"
	destination, err := claudeOwnedPath(claudeImportJobDir(config, job.Name, job.ID), part.Units)
	if err != nil {
		part.Units = ""
		return err
	}
	metadata := int64(len(content))
	for _, head := range heads {
		metadata += int64(len(jsonText(head)))
	}
	if err := w.charge(metadata); err != nil {
		part.Units = ""
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		part.Units = ""
		return err
	}
	if err := writeFileDurably(destination, content); err != nil {
		part.Units = ""
		return err
	}
	part.UnitsSHA256 = hashBytes(content)
	if captureFault != nil {
		if err := captureFault("claude-source-receipt", destination); err != nil {
			return err
		}
	}
	for name, head := range heads {
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		if err := writeFileDurably(name, []byte(jsonText(head))); err != nil {
			return err
		}
	}
	return nil
}

func publishClaudeUnitHeads(root string, units *claudeSourceUnits, observed int64) error {
	for _, unit := range units.Conversations {
		name, err := claudeOwnedPath(root, ".unit-heads/"+hashBytes([]byte(unit.ID))+".json")
		if err != nil {
			return err
		}
		var previous claudeUnitHead
		if data, err := os.ReadFile(name); err == nil {
			if err := json.Unmarshal(data, &previous); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if previous.Observed > observed {
			continue
		}
		head := claudeUnitHead{Content: hashBytes([]byte(jsonText([]any{unit.Envelope, unit.Messages}))), Occurrence: unit.Occurrence, Observed: observed, FirstObserved: unit.Observed}
		if previous.Occurrence == unit.Occurrence {
			head.FirstObserved = previous.FirstObserved
		}
		if jsonText(head) == jsonText(previous) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		if err := writeFileDurably(name, []byte(jsonText(head))); err != nil {
			return err
		}
	}
	return nil
}

func readClaudeSourceUnits(files sourceFiles, name, sourceHash, receiptHash string) (*claudeSourceUnits, error) {
	var units claudeSourceUnits
	file, err := files.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, claudeExportLimits.Manifest+1))
	if err != nil {
		return nil, err
	}
	if !validRecordHash(receiptHash) || int64(len(data)) > claudeExportLimits.Manifest || hashBytes(data) != receiptHash {
		return nil, errors.New("Claude source-unit receipt failed integrity verification")
	}
	if err := json.Unmarshal(data, &units); err != nil {
		return nil, err
	}
	if units.Format != claudeUnitsFormat || units.SourceSHA256 != sourceHash {
		return nil, errors.New("Claude source-unit receipt disagrees with its native input")
	}
	for hash, ref := range units.Objects {
		if !validRecordHash(hash) || !validClaudeObjectRef(ref) {
			return nil, errors.New("unsafe Claude source object reference")
		}
	}
	conversationMembers := map[string]bool{}
	for _, name := range units.ConversationMembers {
		if name != "" && (!claudeExportPath(name) || !claudeConversationName(name)) || conversationMembers[name] {
			return nil, errors.New("invalid Claude conversation member membership")
		}
		conversationMembers[name] = true
	}
	for _, unit := range units.Conversations {
		if !conversationMembers[unit.Member] {
			return nil, errors.New("unlisted Claude conversation member")
		}
		if unit.ID == "" || !validRecordHash(unit.Occurrence) || !validRecordHash(unit.Envelope) || unit.Member != "" && !claudeExportPath(unit.Member) {
			return nil, errors.New("invalid Claude conversation unit")
		}
		for _, hash := range append([]string{unit.Envelope}, unit.Messages...) {
			if _, ok := units.Objects[hash]; !ok {
				return nil, errors.New("missing Claude source object reference")
			}
		}
	}
	for _, ref := range units.Objects {
		for _, hash := range ref.Pieces {
			if _, ok := units.Objects[hash]; !ok {
				return nil, errors.New("missing Claude source chunk")
			}
		}
	}
	seen := map[string]bool{}
	for _, name := range units.Directories {
		if !strings.HasSuffix(name, "/") || !claudeExportPath(strings.TrimSuffix(name, "/")) || seen[name] {
			return nil, errors.New("unsafe or repeated Claude directory entry")
		}
		seen[name] = true
	}
	for _, member := range units.Members {
		ref, ok := units.Objects[member.Object]
		if !claudeExportPath(member.Name) || seen[member.Name] || !ok || member.Size != ref.Size || !validRecordHash(member.SHA256) || claudeAccountRecord(member.Name) {
			return nil, errors.New("unsafe or incomplete Claude retained member")
		}
		seen[member.Name] = true
	}
	if err := units.validateGroups(); err != nil {
		return nil, err
	}
	return &units, nil
}

func (units *claudeSourceUnits) member(files sourceFiles, root string, member claudeSourceMember) ([]byte, error) {
	ref, ok := units.Objects[member.Object]
	if !ok {
		return nil, errors.New("missing retained Claude member")
	}
	data, err := units.value(files, root, member.Object, ref, 0)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != member.Size || hashBytes(data) != member.SHA256 {
		return nil, errors.New("retained Claude member failed integrity verification")
	}
	return data, nil
}

func readClaudeSourceObject(files sourceFiles, root, hash string, ref claudeObjectRef) ([]byte, error) {
	if claudeObjectReadHook != nil {
		claudeObjectReadHook(hash)
	}
	if !validRecordHash(hash) || !validClaudeObjectRef(ref) {
		return nil, errors.New("invalid Claude object reference")
	}
	name, err := claudeOwnedPath(root, ref.Pack)
	if err != nil {
		return nil, err
	}
	if _, err := files.Database(name); err != nil {
		return nil, err
	}
	file, err := files.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder, err := gzip.NewReader(io.NewSectionReader(file.file, ref.Offset, ref.Length))
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(decoder, ref.Size+1))
	closeErr := decoder.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	if int64(len(data)) != ref.Size || claudeObjectHash(data) != hash {
		return nil, errors.New("Claude source object failed integrity verification")
	}
	return data, nil
}

// Reconstruct source values independently of the current catalog parser.
func (units *claudeSourceUnits) conversationJSON(files sourceFiles, root string, unit claudeConversationUnit) ([]byte, error) {
	read := func(hash string) ([]byte, error) {
		ref, ok := units.Objects[hash]
		if !ok {
			return nil, errors.New("Claude conversation dependency missing")
		}
		return units.value(files, root, hash, ref, 0)
	}
	envelope, err := read(unit.Envelope)
	if err != nil {
		return nil, err
	}
	fields, err := claudeSourceJSON(envelope)
	if err != nil {
		return nil, err
	}
	if firstString(fields["uuid"]) != unit.ID {
		return nil, errors.New("Claude conversation identity disagrees with source units")
	}
	messages := []json.RawMessage{}
	for _, hash := range unit.Messages {
		raw, err := read(hash)
		if err != nil {
			return nil, err
		}
		messages = append(messages, raw)
	}
	fields["chat_messages"] = messages
	return []byte(jsonText(fields)), nil
}

func (units *claudeSourceUnits) conversation(files sourceFiles, root string, unit claudeConversationUnit) (claudeAIConversation, error) {
	var conversation claudeAIConversation
	raw, err := units.conversationJSON(files, root, unit)
	if err != nil {
		return conversation, err
	}
	err = decodeClaudeConversations(bytes.NewReader(append(append([]byte{'['}, raw...), ']')), func(c claudeAIConversation) error { conversation = c; return nil })
	return conversation, err
}

func (units *claudeSourceUnits) value(files sourceFiles, root, hash string, ref claudeObjectRef, depth int) ([]byte, error) {
	if depth > 2 {
		return nil, errors.New("Claude source chunk reference cycle or excessive depth")
	}
	if len(ref.Pieces) == 0 {
		return readClaudeSourceObject(files, root, hash, ref)
	}
	var data bytes.Buffer
	for _, piece := range ref.Pieces {
		child, ok := units.Objects[piece]
		if !ok {
			return nil, errors.New("missing Claude source chunk")
		}
		raw, err := units.value(files, root, piece, child, depth+1)
		if err != nil {
			return nil, err
		}
		if int64(data.Len()+len(raw)) > ref.Size {
			return nil, errors.New("Claude source chunks exceed their recorded size")
		}
		data.Write(raw)
	}
	if int64(data.Len()) != ref.Size || claudeObjectHash(data.Bytes()) != hash {
		return nil, errors.New("Claude source chunks failed integrity verification")
	}
	return data.Bytes(), nil
}

func (units *claudeSourceUnits) packs() []string {
	set := map[string]bool{}
	for _, ref := range units.Objects {
		if len(ref.Pieces) == 0 {
			set[ref.Pack] = true
		}
	}
	names := []string{}
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

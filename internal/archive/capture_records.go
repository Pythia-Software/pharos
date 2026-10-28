package archive

// The raw evidence format is deliberately independent of provider parsers and
// catalog schemas. Captured SQL is data only; replay never executes it.
import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	sqlite "modernc.org/sqlite"
)

const recordFormat = 1
const recordDir = "records/v1"
const recordWriter = "sqlite-rows-v1"

var (
	recordRunTimeout              = 10 * time.Minute
	recordMaxBytes          int64 = 32 << 30 // uncompressed input budget per database transaction
	recordMaxValueBytes     int64 = 16 << 20
	recordMaxRows           int64 = 5_000_000
	recordMaxWALGrowth      int64 = 512 << 20
	recordChunkBytes              = 1 << 20
	recordReconcileInterval       = 24 * time.Hour
	errRecordBudget               = errors.New("SQLite preservation budget exhausted; reconciliation remains overdue; raise the documented source budget or export a consistent snapshot")
)

func newRecordHash() hash.Hash { return sha256.New() }
func validRecordHash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && strings.ToLower(s) == s
}
func quoteRecordIdentifier(s string) (string, error) {
	if s == "" || strings.ContainsRune(s, 0) || !utf8.ValidString(s) {
		return "", errors.New("unsupported SQLite identifier; native snapshot required")
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`, nil
}

type sqliteValue struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
}

func encodeSQLiteValue(v any) (sqliteValue, error) {
	switch x := v.(type) {
	case nil:
		return sqliteValue{Type: "null"}, nil
	case int64:
		return sqliteValue{Type: "integer", Data: strconv.FormatInt(x, 10)}, nil
	case float64:
		return sqliteValue{Type: "real", Data: fmt.Sprintf("%016x", math.Float64bits(x))}, nil
	case string:
		return sqliteValue{Type: "text", Data: base64.StdEncoding.EncodeToString([]byte(x))}, nil
	case []byte:
		return sqliteValue{Type: "blob", Data: base64.StdEncoding.EncodeToString(x)}, nil
	default:
		return sqliteValue{}, fmt.Errorf("unsupported SQLite storage value %T", v)
	}
}
func (v sqliteValue) decode() (any, error) {
	switch v.Type {
	case "null":
		if v.Data != "" {
			return nil, errors.New("nonempty NULL")
		}
		return nil, nil
	case "integer":
		return strconv.ParseInt(v.Data, 10, 64)
	case "real":
		n, e := strconv.ParseUint(v.Data, 16, 64)
		return math.Float64frombits(n), e
	case "text", "blob":
		b, e := base64.StdEncoding.DecodeString(v.Data)
		if v.Type == "text" {
			return string(b), e
		}
		if b == nil {
			b = []byte{}
		}
		return b, e
	default:
		return nil, errors.New("unknown SQLite value encoding; upgrade Pharos")
	}
}

type recordObject struct {
	Hash     string `json:"sha256"`
	Length   int64  `json:"length"`
	Encoding string `json:"encoding"`
}
type recordColumn struct {
	ImplicitRowID bool         `json:"implicit_rowid,omitempty"`
	Name          string       `json:"name"`
	DeclaredType  string       `json:"declared_type"`
	PK            int          `json:"pk"`
	Hidden        int          `json:"hidden"`
	NotNull       int          `json:"not_null"`
	Default       *sqliteValue `json:"default,omitempty"`
}
type recordTable struct {
	Name     string         `json:"name"`
	Columns  []recordColumn `json:"columns"`
	Chunks   []recordObject `json:"chunks"`
	Rows     int64          `json:"rows"`
	Coverage string         `json:"coverage"` // full, selected, opaque, failed, excluded
	Order    string         `json:"order"`    // observed sequence; Conductor message parser order when selected
}
type recordDependency struct {
	MissingSince string        `json:"missing_since,omitempty"`
	Path         string        `json:"original_path"`
	Object       *recordObject `json:"object,omitempty"`
	State        string        `json:"state"`
	MTimeNS      int64         `json:"mtime_ns,string"`
}

// Source authority versions also use decimal strings: nanosecond mtimes must
// survive inspection/round trips through JavaScript without rounding.
type recordSourceState struct {
	Size        int64  `json:"size,string"`
	MTimeNS     int64  `json:"mtime_ns,string"`
	WALSize     int64  `json:"wal_size,string"`
	WALMTimeNS  int64  `json:"wal_mtime_ns,string"`
	Identity    string `json:"identity,omitempty"`
	WALIdentity string `json:"wal_identity,omitempty"`
}

func (s recordSourceState) version() int64 { return sqliteSourceState(s).version() }

type sqliteRevision struct {
	EntityIdentities map[string]string       `json:"entity_identities,omitempty"`
	NativeCapturedAt string                  `json:"native_captured_at,omitempty"`
	Acquisition      string                  `json:"acquisition"`
	ParserInputs     map[string]recordObject `json:"parser_inputs,omitempty"`
	Mappings         []capturePathMapping    `json:"path_map"`
	Installations    []capturedInstallation  `json:"installations,omitempty"`
	Version          int                     `json:"version"`
	Writer           string                  `json:"writer"`
	Encoding         string                  `json:"encoding"`
	Host             Host                    `json:"host"`
	Source           captureSourceInfo       `json:"source"`
	Database         string                  `json:"database"`
	Entity           string                  `json:"entity"`
	Input            string                  `json:"retained_input,omitempty"`
	InputHash        string                  `json:"input_sha256,omitempty"`
	Observed         recordSourceState       `json:"source_version"`
	AcquiredAt       string                  `json:"acquired_at"`
	Parent           *recordObject           `json:"parent,omitempty"`
	Schema           recordObject            `json:"schema"`
	Tables           []recordTable           `json:"tables"`
	Dependencies     []recordDependency      `json:"dependencies"`
	Complete         bool                    `json:"complete"`
	Full             bool                    `json:"full_reconciliation"`
	Gaps             []string                `json:"gaps,omitempty"`
	// Identity excludes exporter observation timestamps, parent, and database
	// mtimes. Dependency versions remain part of the raw input closure.
	Identity string `json:"identity"`
}
type sqliteRecordHead struct {
	Version    int          `json:"version"`
	Revision   recordObject `json:"revision"`
	LastFullAt string       `json:"last_full_at,omitempty"`
}
type sqliteExportReceipt struct {
	Version int    `json:"version"`
	Input   string `json:"input"`
	// Rotation relocates native bytes, while the evidence revision and its
	// verified dependency group remain immutable. Bind both names explicitly.
	RevisionInput string       `json:"revision_input,omitempty"`
	InputHash     string       `json:"input_sha256"`
	Revision      recordObject `json:"revision"`
	VerifiedAt    string       `json:"verified_at"`
}

func (r sqliteExportReceipt) revisionInput() string {
	return defaultString(r.RevisionInput, r.Input)
}

type sqliteRecordBudget struct {
	Timeout                time.Duration
	Bytes, Rows, WALGrowth int64
}

type recordStore struct {
	capacityReserved bool
	inputCapturedAt  string
	monitoredFile    string
	walBaseline      int64
	lastWALCheck     time.Time
	budget           sqliteRecordBudget
	c                *sourceCapture
	written, rows    int64
	started          time.Time
}

func newRecordStore(c *sourceCapture) *recordStore {
	budget := sqliteRecordBudget{Timeout: recordRunTimeout, Bytes: recordMaxBytes, Rows: recordMaxRows, WALGrowth: recordMaxWALGrowth}
	option := func(key string, fallback, ceiling int64) int64 {
		v := integer(c.source.Options[key])
		if v > 0 && v <= ceiling {
			return v
		}
		return fallback
	}
	budget.Timeout = time.Duration(option("sqlite_capture_timeout_seconds", int64(budget.Timeout/time.Second), 3600)) * time.Second
	budget.Bytes = option("sqlite_capture_max_bytes", budget.Bytes, 1<<40)
	budget.Rows = option("sqlite_capture_max_rows", budget.Rows, 50_000_000)
	budget.WALGrowth = option("sqlite_capture_max_wal_growth", budget.WALGrowth, 8<<30)
	return &recordStore{c: c, started: time.Now(), budget: budget}
}

func (s *recordStore) objectPath(h string) string {
	return filepath.Join(s.c.dir, recordDir, "objects", h+".gz")
}
func (s *recordStore) check(n int64) error {
	if err := s.c.ctx.Err(); err != nil {
		return err
	}
	if !s.capacityReserved {
		// A preservation job budgets exporter output as well as native
		// rotation. Reserve the configured maximum before any temporary;
		// crashes/unused allowance never manufacture additional capacity.
		allowance := s.budget.Bytes + s.budget.Bytes/128 + (64 << 20)
		if e := reserveCaptureAcquisition(s.c.dir, allowance); e != nil {
			return e
		}
		s.capacityReserved = true
	}
	if s.monitoredFile != "" && time.Since(s.lastWALCheck) >= 250*time.Millisecond {
		s.lastWALCheck = time.Now()
		if info, e := os.Stat(s.monitoredFile + "-wal"); e == nil && info.Size()-s.walBaseline > s.budget.WALGrowth {
			return errRecordBudget
		} else if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	if time.Since(s.started) > s.budget.Timeout || s.written+n > s.budget.Bytes || s.rows > s.budget.Rows {
		return errRecordBudget
	}
	return s.c.ensureSpace(n*2 + 4096)
}

// put streams raw bytes through a deterministic gzip encoder, hashing the
// uncompressed bytes. Each object is fsynced; the later head full-flush commits
// the closure. Callers own the capture lock until head publication.
func (s *recordStore) put(r io.Reader) (recordObject, error) {
	if err := s.check(0); err != nil {
		return recordObject{}, err
	}
	dir := filepath.Join(s.c.dir, recordDir, "objects")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return recordObject{}, err
	}
	temp := filepath.Join(dir, "object"+captureTempSuffix)
	f, e := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return recordObject{}, e
	}
	defer os.Remove(temp)
	z := gzip.NewWriter(&recordOutputWriter{c: s.c, file: f})
	h := newRecordHash()
	buf := make([]byte, 64<<10)
	var n int64
	for {
		if e = s.check(int64(len(buf))); e != nil {
			break
		}
		var count int
		count, e = r.Read(buf)
		if count > 0 {
			n += int64(count)
			s.written += int64(count)
			h.Write(buf[:count])
			if _, w := z.Write(buf[:count]); w != nil {
				e = w
				break
			}
		}
		if e != nil {
			break
		}
	}
	if e == io.EOF {
		e = nil
	}
	if closeErr := z.Close(); e == nil {
		e = closeErr
	}
	if e == nil {
		e = f.Sync()
	}
	if closeErr := f.Close(); e == nil {
		e = closeErr
	}
	if e != nil {
		return recordObject{}, e
	}
	ref := recordObject{Hash: fmt.Sprintf("%x", h.Sum(nil)), Length: n, Encoding: "gzip"}
	if old, err := os.Stat(s.objectPath(ref.Hash)); err == nil && old.Mode().IsRegular() {
		if err = s.walkObject(ref, func(io.Reader) error { return nil }); err != nil {
			return recordObject{}, err
		}
		return ref, nil
	}
	if e = os.Rename(temp, s.objectPath(ref.Hash)); e != nil {
		return recordObject{}, e
	}
	return ref, nil
}
func (s *recordStore) putJSON(v any) (recordObject, error) {
	b, e := json.Marshal(v)
	if len(b) > 64<<20 {
		return recordObject{}, errRecordBudget
	}
	if e != nil {
		return recordObject{}, e
	}
	return s.put(bytes.NewReader(b))
}
func (s *recordStore) walkObject(ref recordObject, read func(io.Reader) error) error {
	if !validRecordHash(ref.Hash) || ref.Length < 0 || ref.Length > 1<<40 || ref.Encoding != "gzip" {
		return errors.New("unsupported or invalid record object; upgrade Pharos")
	}
	f, e := os.Open(s.objectPath(ref.Hash))
	if e != nil {
		return e
	}
	defer f.Close()
	z, e := gzip.NewReader(f)
	if e != nil {
		return e
	}
	defer z.Close()
	h := newRecordHash()
	r := io.TeeReader(io.LimitReader(z, ref.Length+1), h)
	count := &recordCountReader{r: r, ctx: s.c.ctx}
	if e = read(count); e != nil {
		return e
	}
	if _, e = io.Copy(io.Discard, count); e != nil {
		return e
	}
	if count.n != ref.Length || fmt.Sprintf("%x", h.Sum(nil)) != ref.Hash {
		return errors.New("record object length/hash mismatch")
	}
	return nil
}

type recordCountReader struct {
	r   io.Reader
	ctx context.Context
	n   int64
}

func (r *recordCountReader) Read(b []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	n, e := r.r.Read(b)
	r.n += int64(n)
	return n, e
}
func (s *recordStore) readJSON(ref recordObject, v any) error {
	if ref.Length > 64<<20 {
		return errors.New("record descriptor exceeds format limit")
	}
	return s.walkObject(ref, func(r io.Reader) error { return json.NewDecoder(r).Decode(v) })
}
func recordEntityKey(database, entity string) string {
	return hashBytes([]byte(database + "\x00" + entity))
}
func (s *recordStore) headPath(database, entity string) string {
	return filepath.Join(s.c.dir, recordDir, "entities", recordEntityKey(database, entity), "head.json")
}
func (s *recordStore) head(database, entity string) (sqliteRecordHead, error) {
	var h sqliteRecordHead
	b, e := os.ReadFile(s.headPath(database, entity))
	if errors.Is(e, os.ErrNotExist) {
		return h, nil
	}
	if e != nil {
		return h, e
	}
	if e = json.Unmarshal(b, &h); e != nil {
		return h, e
	}
	if h.Version != recordFormat {
		return h, errors.New("unsupported SQLite records head; upgrade Pharos")
	}
	return h, nil
}
func (s *recordStore) loadRevision(ref recordObject) (sqliteRevision, error) {
	var v sqliteRevision
	e := s.readJSON(ref, &v)
	if e != nil {
		return v, e
	}
	if v.Version != recordFormat || v.Writer != recordWriter || v.Encoding != "typed-sqlite-ndjson-v1" {
		return v, errors.New("unsupported SQLite evidence format; upgrade Pharos")
	}
	probe := captureManifest{PathMap: v.Mappings, Installations: v.Installations}
	if probe.unsafePath() != "" {
		return v, errors.New("unsafe record path mapping")
	}
	if v.Identity != sqliteEvidenceIdentity(v) {
		return v, errors.New("raw evidence identity mismatch")
	}
	return v, nil
}
func (s *recordStore) verify(ref recordObject) error {
	v, e := s.loadRevision(ref)
	if e != nil {
		return e
	}
	if !v.Complete {
		return errors.New("incomplete SQLite evidence")
	}
	refs := []recordObject{v.Schema}
	if v.Parent != nil {
		refs = append(refs, *v.Parent)
	}
	for _, t := range v.Tables {
		if t.Coverage == "failed" {
			return errors.New("failed table coverage")
		}
		refs = append(refs, t.Chunks...)
	}
	for _, ref := range v.ParserInputs {
		var t recordTable
		if e = s.readJSON(ref, &t); e != nil {
			return e
		}
		refs = append(refs, ref)
		refs = append(refs, t.Chunks...)
	}
	for _, d := range v.Dependencies {
		if d.State != "retained" || d.Object == nil {
			return errors.New("unresolved evidence dependency")
		}
		refs = append(refs, *d.Object)
	}
	for _, r := range refs {
		if e = s.walkObject(r, func(io.Reader) error { return nil }); e != nil {
			return e
		}
	}
	return nil
}
func (s *recordStore) publish(v *sqliteRevision) (recordObject, error) {
	h, e := s.head(v.Database, v.Entity)
	if e != nil {
		return recordObject{}, e
	}
	// Observation metadata has its own small revision; unchanged row chunks and
	// dependency payloads deduplicate even if the acquisition time advances.
	if h.Version != 0 {
		parent := h.Revision
		v.Parent = &parent
	}
	v.Identity = sqliteEvidenceIdentity(*v)
	changed := h.Version == 0
	if h.Version != 0 {
		previous, e := s.loadRevision(h.Revision)
		if e != nil {
			return recordObject{}, e
		}
		changed = previous.Identity != v.Identity
	}
	ref, e := s.putJSON(v)
	if e != nil {
		return ref, e
	}
	if captureFault != nil {
		if e = captureFault("records-objects", v.Database); e != nil {
			return ref, e
		}
	}
	if e = s.verify(ref); e != nil {
		return ref, e
	}
	entityDir := filepath.Dir(s.headPath(v.Database, v.Entity))
	if e = os.MkdirAll(entityDir, 0700); e != nil {
		return ref, e
	}
	// The immutable revision inventory is the catalog-independent discovery
	// surface. Publish it before the mutable head; an interrupted head is resumable.
	b, _ := json.Marshal(ref)
	if e = s.c.ensureSpace(int64(len(b)) + 4096); e != nil {
		return ref, e
	}
	if e = writeRecordMetadata(filepath.Join(entityDir, ref.Hash+".json"), b); e != nil {
		return ref, e
	}
	if captureFault != nil {
		if e = captureFault("records-inventory", v.Database); e != nil {
			return ref, e
		}
	}
	h.Version, h.Revision = recordFormat, ref
	if v.Full && v.Complete && !strings.HasPrefix(v.Input, "history/") {
		h.LastFullAt = v.AcquiredAt
	}
	if v.Full && v.Complete && !strings.HasPrefix(v.Input, "history/") {
		if s.c.manifest.RecordCoverage == nil {
			s.c.manifest.RecordCoverage = map[string]sqliteCoverageStatus{}
		}
		s.c.manifest.RecordCoverage[v.Database] = sqliteCoverageStatus{IntervalSeconds: int64(recordInterval(s.c.source) / time.Second), LastAttemptAt: v.AcquiredAt, LastCompleteAt: v.AcquiredAt}
	}
	b, _ = json.Marshal(h)
	if e = writeRecordMetadata(s.headPath(v.Database, v.Entity), b); e != nil {
		return ref, e
	}
	if changed {
		s.c.manifest.LastDataAt = v.AcquiredAt
	}
	if captureFault != nil {
		if e = captureFault("records-head", v.Database); e != nil {
			return ref, e
		}
	}
	return ref, nil
}

func recordColumns(ctx context.Context, tx *sql.Tx, table string) ([]recordColumn, error) {
	q, e := quoteRecordIdentifier(table)
	if e != nil {
		return nil, e
	}
	rows, e := tx.QueryContext(ctx, "PRAGMA table_xinfo("+q+")")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var columns []recordColumn
	for rows.Next() {
		var cid int
		var c recordColumn
		var d any
		if e = rows.Scan(&cid, &c.Name, &c.DeclaredType, &c.NotNull, &d, &c.PK, &c.Hidden); e != nil {
			return nil, e
		}
		if _, e = quoteRecordIdentifier(c.Name); e != nil {
			return nil, e
		}
		if d != nil {
			v, e := encodeSQLiteValue(d)
			if e != nil {
				return nil, e
			}
			c.Default = &v
		}
		columns = append(columns, c)
	}
	return columns, rows.Err()
}

// table reads storage values, not queryMaps' lossy BLOB->string projection.
// CASE bounds a single driver allocation before Scan. Oversized cells leave
// this export incomplete and its native snapshot protected.
func (s *recordStore) table(tx *sql.Tx, name, where, order string, args []any) (recordTable, error) {
	t := recordTable{Name: name, Coverage: "full", Order: "observed-sequence"}
	var e error
	t.Columns, e = recordColumns(s.c.ctx, tx, name)
	if e != nil {
		return t, e
	}
	quoted, e := quoteRecordIdentifier(name)
	if e != nil {
		return t, e
	}
	// Retain SQLite's native row key too, including gaps and duplicate
	// keyless rows. A flag distinguishes this metadata from declared columns.
	for _, alias := range []string{"rowid", "_rowid_", "oid"} {
		shadowed := false
		for _, column := range t.Columns {
			shadowed = shadowed || strings.EqualFold(column.Name, alias)
		}
		if shadowed {
			continue
		}
		probe, err := tx.QueryContext(s.c.ctx, "SELECT "+alias+" FROM "+quoted+" LIMIT 0")
		if err == nil {
			probe.Close()
			t.Columns = append(t.Columns, recordColumn{Name: alias, DeclaredType: "INTEGER", ImplicitRowID: true})
			break
		}
		if s.c.ctx.Err() != nil {
			return t, s.c.ctx.Err()
		}
		if !strings.Contains(err.Error(), "no such column") {
			return t, err
		}
	}
	lengths := []string{}
	for _, c := range t.Columns {
		q, _ := quoteRecordIdentifier(c.Name)
		lengths = append(lengths, "coalesce(length(CAST("+q+" AS BLOB)),0)")
	}
	rowLength := "(" + strings.Join(lengths, "+") + ")"
	fields := []string{}
	for _, c := range t.Columns {
		q, _ := quoteRecordIdentifier(c.Name)
		fields = append(fields, "typeof("+q+"),length(CAST("+q+" AS BLOB)),CASE WHEN "+rowLength+">"+strconv.FormatInt(recordMaxValueBytes, 10)+" THEN NULL ELSE "+q+" END")
	}
	if len(fields) == 0 {
		return t, errors.New("table has no readable columns")
	}
	rows, e := tx.QueryContext(s.c.ctx, "SELECT "+strings.Join(fields, ",")+" FROM "+quoted+where+order, args...)
	if e != nil {
		return t, e
	}
	defer rows.Close()
	var chunk bytes.Buffer
	flush := func() error {
		if chunk.Len() == 0 {
			return nil
		}
		ref, e := s.put(&chunk)
		if e == nil {
			t.Chunks = append(t.Chunks, ref)
		}
		chunk.Reset()
		return e
	}
	for rows.Next() {
		if e = s.check(0); e != nil {
			return t, e
		}
		raw := make([]any, len(t.Columns)*3)
		dest := make([]any, len(raw))
		for i := range raw {
			dest[i] = &raw[i]
		}
		if e = rows.Scan(dest...); e != nil {
			return t, e
		}
		var rowBytes int64
		for i := range t.Columns {
			if n, ok := raw[i*3+1].(int64); ok {
				rowBytes += n
			}
		}
		if rowBytes > recordMaxValueBytes {
			return t, errRecordBudget
		}
		values := make([]sqliteValue, len(t.Columns))
		for i := range values {
			if n, ok := raw[i*3+1].(int64); ok && n > recordMaxValueBytes {
				return t, errRecordBudget
			}
			values[i], e = encodeSQLiteValue(raw[i*3+2])
			if e != nil {
				return t, e
			}
			typ, _ := raw[i*3].(string)
			if values[i].Type != typ {
				return t, errors.New("SQLite driver changed storage type")
			}
		}
		if e = json.NewEncoder(&chunk).Encode(values); e != nil {
			return t, e
		}
		t.Rows++
		s.rows++
		if chunk.Len() >= recordChunkBytes {
			if e = flush(); e != nil {
				return t, e
			}
		}
	}
	if e = rows.Err(); e != nil {
		return t, e
	}
	return t, flush()
}

// exportSQLite reads exactly one consistent transaction. Selection is used
// only by the Conductor fast path; unknown tables are covered by full scans.
func (s *recordStore) exportSQLite(file, original, entity string, state sqliteSourceState, selected string, dependencies []recordDependency) (recordObject, error) {
	// input is the captured file the revision names; file is what SQLite
	// opens, a decoded copy in a reader lease when the input is stored encoded.
	input := file
	if rel, err := filepath.Rel(s.c.dir, file); err == nil && strings.HasPrefix(file, s.c.dir+string(filepath.Separator)) {
		if version, ok := s.c.storedVersion(filepath.ToSlash(rel)); ok {
			decoded, _, release, err := leaseDecoded(s.c.ctx, s.c.dir, version)
			if err != nil {
				return recordObject{}, err
			}
			defer release()
			file = decoded
		}
	}
	ctx, cancel := context.WithTimeout(s.c.ctx, s.budget.Timeout)
	defer cancel()
	oldCtx := s.c.ctx
	s.c.ctx = ctx
	defer func() { s.c.ctx = oldCtx }()
	db, e := openReadOnlySQLite(file)
	if e != nil {
		return recordObject{}, e
	}
	defer db.Close()
	conn, e := db.Conn(ctx)
	if e != nil {
		return recordObject{}, e
	}
	defer conn.Close()
	// SQLITE_LIMIT_LENGTH bounds SQLite's allocation too, before the driver
	// can see a generated expression or a large stored cell. This is a
	// connection limit, never a source schema/journal mutation.
	if _, e = sqlite.Limit(conn, 0, int(recordMaxValueBytes)+(64<<10)); e != nil {
		return recordObject{}, e
	}
	tx, e := conn.BeginTx(ctx, nil)
	if e != nil {
		return recordObject{}, e
	}
	defer tx.Rollback()
	before, e := sqliteDependencyState(file)
	if e != nil {
		return recordObject{}, e
	}
	s.monitoredFile = file
	s.walBaseline = before.WALSize
	schemaRows, e := tx.QueryContext(ctx, "SELECT type,name,tbl_name,rootpage,sql FROM sqlite_schema ORDER BY type,name")
	if e != nil {
		return recordObject{}, e
	}
	var schema bytes.Buffer
	var tables []string
	for schemaRows.Next() {
		var kind, name, table string
		var page int64
		var ddl any
		if e = schemaRows.Scan(&kind, &name, &table, &page, &ddl); e != nil {
			schemaRows.Close()
			return recordObject{}, e
		}
		values := []sqliteValue{}
		for _, v := range []any{kind, name, table, page, ddl} {
			x, e := encodeSQLiteValue(v)
			if e != nil {
				schemaRows.Close()
				return recordObject{}, e
			}
			values = append(values, x)
		}
		json.NewEncoder(&schema).Encode(values)
		if schema.Len() > 64<<20 {
			schemaRows.Close()
			return recordObject{}, errRecordBudget
		}
		if kind == "table" {
			tables = append(tables, name)
		}
	}
	e = schemaRows.Err()
	schemaRows.Close()
	if e != nil {
		return recordObject{}, e
	}
	if !strings.HasPrefix(input, s.c.dir+string(filepath.Separator)) {
		state, e = sqliteDependencyState(file)
		if e != nil {
			return recordObject{}, e
		}
	}
	var user, app, encoding any
	if e = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&user); e != nil {
		return recordObject{}, e
	}
	if e = tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app); e != nil {
		return recordObject{}, e
	}
	if e = tx.QueryRowContext(ctx, "PRAGMA encoding").Scan(&encoding); e != nil {
		return recordObject{}, e
	}
	if encoding != "UTF-8" {
		return recordObject{}, errors.New("SQLite TEXT encoding requires native snapshot preservation; logical replay currently supports UTF-8 databases")
	}
	for _, v := range []any{user, app, encoding} {
		x, e := encodeSQLiteValue(v)
		if e != nil {
			return recordObject{}, e
		}
		json.NewEncoder(&schema).Encode([]sqliteValue{x})
	}
	schemaRef, e := s.put(&schema)
	if e != nil {
		return recordObject{}, e
	}
	v := sqliteRevision{NativeCapturedAt: s.inputCapturedAt, Acquisition: defaultString(s.c.result.StartedAt, now()), Mappings: s.c.manifest.PathMap, Installations: s.c.manifest.Installations, Version: recordFormat, Writer: recordWriter, Encoding: "typed-sqlite-ndjson-v1", Host: s.c.session.host, Source: captureSourceInfo{Name: s.c.source.Name, Kind: s.c.source.Kind, Account: s.c.source.Account, Path: s.c.source.Path}, Database: original, Entity: entity, Observed: recordSourceState(state), AcquiredAt: now(), Schema: schemaRef, Dependencies: dependencies, Complete: true, Full: selected == ""}
	finished := false
	defer func() {
		if !finished {
			s.savePartial(v, errRecordBudget)
		}
	}()
	names := map[string]bool{}
	for _, n := range tables {
		names[n] = true
	}
	st, mt := firstPresent(names, "sessions", "workspaces", "threads"), firstPresent(names, "messages", "session_messages")
	for _, name := range tables {
		where, order := "", ""
		var args []any
		if selected != "" {
			cols, e := recordColumns(ctx, tx, name)
			if e != nil {
				return recordObject{}, e
			}
			colset := map[string]bool{}
			for _, c := range cols {
				colset[c.Name] = true
			}
			switch name {
			case st:
				key := firstPresent(colset, "id", "session_id", "uuid")
				q, e := quoteRecordIdentifier(key)
				if e != nil {
					return recordObject{}, e
				}
				where = " WHERE " + q + "=?"
				args = []any{selected}
			case mt:
				key := firstPresent(colset, "session_id", "workspace_id", "thread_id")
				q, e := quoteRecordIdentifier(key)
				if e != nil {
					return recordObject{}, e
				}
				where = " WHERE " + q + "=?"
				args = []any{selected}
				if colset["created_at"] && colset["id"] {
					order = " ORDER BY created_at,id"
				}
			case "workspaces", "repos": // Shared metadata is normally tiny and invalidates every dependent entity.
			default:
				v.Tables = append(v.Tables, recordTable{Name: name, Columns: cols, Coverage: "excluded", Order: "not-selected; full reconciliation covers opaque rows"})
				continue
			}
		}
		t, e := s.table(tx, name, where, order, args)
		if e != nil {
			t.Coverage = "failed"
			v.Tables = append(v.Tables, t)
			return recordObject{}, fmt.Errorf("SQLite preservation table %q: %w", name, e)
		}
		if selected != "" {
			t.Coverage = "selected"
		} else if !knownRecordTable(s.c.source.Kind, name) {
			t.Coverage = "opaque"
		}
		if order != "" {
			t.Order = "parser-created_at-id"
		}
		v.Tables = append(v.Tables, t)
	}
	if isConductorSource(s.c.source.Kind) && st != "" && mt != "" {
		sc, e := tableColumns(tx, st)
		if e != nil {
			return recordObject{}, e
		}
		mc, e := tableColumns(tx, mt)
		if e != nil {
			return recordObject{}, e
		}
		sidcol := firstPresent(sc, "id", "session_id", "uuid")
		fk := firstPresent(mc, "session_id", "workspace_id", "thread_id")
		if sidcol == "" || fk == "" {
			return recordObject{}, errors.New("unsupported Conductor identity schema")
		}
		sq, _ := quoteRecordIdentifier(st)
		idq, _ := quoteRecordIdentifier(sidcol)
		fkq, _ := quoteRecordIdentifier(fk)
		query := "SELECT " + idq + " FROM " + sq
		var args []any
		if selected != "" {
			query += " WHERE " + idq + "=?"
			args = []any{selected}
		}
		rows, e := tx.QueryContext(ctx, query, args...)
		if e != nil {
			return recordObject{}, e
		}
		var ids []string
		for rows.Next() {
			var id any
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return recordObject{}, e
			}
			if len(ids) >= 100_000 {
				rows.Close()
				return recordObject{}, errRecordBudget
			}
			ids = append(ids, firstString(id))
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return recordObject{}, e
		}
		v.ParserInputs = map[string]recordObject{}
		order := ""
		if mc["created_at"] && mc["id"] {
			order = " ORDER BY created_at,id"
		}
		for _, id := range ids {
			t, e := s.table(tx, mt, " WHERE "+fkq+"=?", order, []any{id})
			if e != nil {
				return recordObject{}, e
			}
			t.Order = "parser-observed-sequence"
			ref, e := s.putJSON(t)
			if e != nil {
				return recordObject{}, e
			}
			v.ParserInputs[id] = ref
		}
	}
	if isConductorSource(s.c.source.Kind) && st != "" && mt != "" {
		identities, e := s.conductorEntityIdentities(v, st)
		if e != nil {
			return recordObject{}, e
		}
		v.EntityIdentities = identities
	}
	if e = tx.Commit(); e != nil {
		return recordObject{}, e
	}
	after, e := sqliteDependencyState(file)
	if e != nil {
		return recordObject{}, e
	}
	if before.Identity != after.Identity || before.WALIdentity != after.WALIdentity && before.WALIdentity != "" || after.WALSize-before.WALSize > s.budget.WALGrowth {
		return recordObject{}, errRecordBudget
	}
	// A live file/WAL change makes cross-file authority uncertain. A retained
	// immutable snapshot does not change while exported.
	if before != after && len(dependencies) > 0 {
		return recordObject{}, errors.New("SQLite source moved during acquisition; retry required")
	}
	if strings.HasPrefix(input, s.c.dir+string(filepath.Separator)) {
		rel, _ := filepath.Rel(s.c.dir, input)
		v.Input = filepath.ToSlash(rel)
		// The decoded copy's digest: the original bytes the manifest describes.
		v.InputHash, _, e = fileSHA256Context(s.c.ctx, file)
		if e != nil {
			return recordObject{}, e
		}
	}
	ref, err := s.publish(&v)
	finished = err == nil
	return ref, err
}

func (s *recordStore) dependencies() ([]recordDependency, error) {
	if s.c.recordDependenciesLoaded {
		return s.c.recordDependencies, nil
	}
	deps := []recordDependency{}
	for _, f := range sortedCaptures(s.c.files) {
		if cached, ok := s.c.recordDependencyCache[f.Path]; ok && cached.Object != nil && cached.Object.Hash == f.SHA256 && cached.Object.Length == f.Size {
			cached.MTimeNS, cached.MissingSince = f.MTimeNS, f.MissingSince
			deps = append(deps, cached)
			continue
		}
		file := filepath.Join(s.c.dir, filepath.FromSlash(f.Captured))
		h, size, e := fileSHA256Context(s.c.ctx, file)
		if e != nil || h != f.SHA256 || size != f.Size {
			return nil, errors.New("retained dependency changed or is unreadable")
		}
		r, e := os.Open(file)
		if e != nil {
			return nil, e
		}
		ref, e := s.put(r)
		r.Close()
		if e != nil {
			return nil, e
		}
		deps = append(deps, recordDependency{Path: f.Path, Object: &ref, State: "retained", MTimeNS: f.MTimeNS, MissingSince: f.MissingSince})
	}
	s.c.recordDependencies = deps
	s.c.recordDependenciesLoaded = true
	return deps, nil
}
func (c *sourceCapture) exportSnapshot(snapshot *capturedSnapshot) error {
	if c.source.Options["sqlite_record_capture"] != true {
		return nil
	}
	s := newRecordStore(c)
	deps, e := s.dependencies()
	if e != nil {
		return e
	}
	// Export earlier inputs too, before pruning. This is limited to generations
	// in the current manifest, never a historical migration over the real library.
	versions := append([]capturedVersion{{Captured: snapshot.Captured, Size: snapshot.Size, CapturedAt: snapshot.CapturedAt, Source: &snapshot.Source}}, snapshot.Generations...)
	slices.Reverse(versions)
	for _, v := range versions {
		if e = c.ctx.Err(); e != nil {
			return e
		}
		if v.Source == nil {
			continue
		}
		if v.Captured != snapshot.Captured && c.canPruneSnapshot(v.Captured) {
			continue
		}
		file := filepath.Join(c.dir, filepath.FromSlash(v.Captured))
		// Historical cross-file groups cannot be reconstructed by pairing old DBs
		// with today's transcripts. Retain the native generation for backfill.
		if v.Captured != snapshot.Captured && len(deps) > 0 {
			continue
		}
		s.inputCapturedAt = v.CapturedAt
		ref, e := s.exportSQLite(file, snapshot.Path, "database", *v.Source, "", deps)
		if e != nil {
			return e
		}
		revision, e := s.loadRevision(ref)
		if e != nil {
			return e
		}
		receipt := sqliteExportReceipt{Version: recordFormat, Input: v.Captured, InputHash: revision.InputHash, Revision: ref, VerifiedAt: now()}
		b, _ := json.Marshal(receipt)
		dir := filepath.Join(c.dir, recordDir, "receipts")
		if e = os.MkdirAll(dir, 0700); e != nil {
			return e
		}
		if captureFault != nil {
			if e = captureFault("records-receipt", v.Captured); e != nil {
				return e
			}
		}
		if e = writeRecordMetadata(filepath.Join(dir, hashBytes([]byte(v.Captured))+".json"), b); e != nil {
			return e
		}
	}
	return nil
}
func (c *sourceCapture) canPruneSnapshot(rel string) bool {
	pinned, e := pinnedPath(c.dir, rel)
	if e != nil || pinned {
		return false
	}
	if c.source.Options["sqlite_record_capture"] != true {
		return true
	}
	_, verified := c.verifiedSnapshotReceipt(rel, rel)
	return verified
}

// receiptInput names the association; retainedInput names the bytes to verify.
// They differ while rotation transfers that association to immutable history,
// including when a filesystem requires rename instead of a hard link.
func (c *sourceCapture) verifiedSnapshotReceipt(receiptInput, retainedInput string) (sqliteExportReceipt, bool) {
	var receipt sqliteExportReceipt
	b, e := os.ReadFile(filepath.Join(c.dir, recordDir, "receipts", hashBytes([]byte(receiptInput))+".json"))
	if e != nil {
		return receipt, false
	}
	if json.Unmarshal(b, &receipt) != nil || receipt.Version != recordFormat || receipt.Input != receiptInput {
		return receipt, false
	}
	ctx, cancel := context.WithTimeout(c.ctx, recordRunTimeout)
	defer cancel()
	h, _, e := capturedSHA256(ctx, filepath.Join(c.dir, filepath.FromSlash(retainedInput)), c.encodingOf(retainedInput))
	if e != nil || h != receipt.InputHash {
		return receipt, false
	}
	reader := *c
	reader.ctx = ctx
	s := newRecordStore(&reader)
	v, e := s.loadRevision(receipt.Revision)
	if e == nil && !isConductorSource(c.source.Kind) {
		group, err := s.committedGroup(v.Acquisition)
		if err != nil {
			return receipt, false
		}
		found := false
		for _, ref := range group.Revisions {
			found = found || ref == receipt.Revision
		}
		if !found {
			return receipt, false
		}
		for _, ref := range group.Revisions {
			if s.verify(ref) != nil {
				return receipt, false
			}
		}
	}

	return receipt, e == nil && v.Full && v.Input == receipt.revisionInput() && v.InputHash == h && s.verify(receipt.Revision) == nil
}

// Carry only an already verified export association across rotation. The
// immutable revision still names its original input and dependency group;
// canPruneSnapshot rechecks the relocated native bytes before any deletion.
// Missing/partial/unverified receipts leave the native generation protected.
func (c *sourceCapture) carrySQLiteExportReceipt(from, to string) error {
	receipt, verified := c.verifiedSnapshotReceipt(from, to)
	if !verified {
		return nil
	}
	dir := filepath.Join(c.dir, recordDir, "receipts")
	receipt.RevisionInput = receipt.revisionInput()
	receipt.Input = to
	b, e := json.Marshal(receipt)
	if e != nil {
		return e
	}
	return writeRecordMetadata(filepath.Join(dir, hashBytes([]byte(to))+".json"), b)
}

// scanRecordRows checks every chunk before exposing rows. Consumers stage
// their work and publish only once the whole closure has been verified.
func (s *recordStore) scanRecordRows(t recordTable, emit func([]sqliteValue) error) error {
	var count int64
	for _, ref := range t.Chunks {
		e := s.walkObject(ref, func(r io.Reader) error {
			scanner := bufio.NewScanner(r)
			scanner.Buffer(make([]byte, 64<<10), int(recordMaxValueBytes)*2+4096)
			for scanner.Scan() {
				if e := s.c.ctx.Err(); e != nil {
					return e
				}
				var row []sqliteValue
				if e := json.Unmarshal(scanner.Bytes(), &row); e != nil {
					return e
				}
				if len(row) != len(t.Columns) {
					return errors.New("SQLite row column count mismatch")
				}
				if e := emit(row); e != nil {
					return e
				}
				count++
			}
			return scanner.Err()
		})
		if e != nil {
			return e
		}
	}
	if count != t.Rows {
		return errors.New("SQLite row count mismatch")
	}
	return nil
}

func (s *recordStore) conductorParserRows(v sqliteRevision, sid string) ([]map[string]any, error) {
	ref, ok := v.ParserInputs[sid]
	if !ok {
		return nil, errors.New("unresolved Conductor parser ordinal for retained observation")
	}
	var t recordTable
	if e := s.readJSON(ref, &t); e != nil {
		return nil, e
	}
	var rows []map[string]any
	err := s.scanRecordRows(t, func(raw []sqliteValue) error {
		row := map[string]any{}
		for i, x := range raw {
			value, e := x.decode()
			if e != nil {
				return e
			}
			if blob, ok := value.([]byte); ok {
				value = string(blob)
			}
			if !t.Columns[i].ImplicitRowID {
				row[t.Columns[i].Name] = value
			}
		}
		rows = append(rows, row)
		return nil
	})
	return rows, err
}

// Check compressed output as well, including gzip finalization. A sticky
// refusal prevents Close from writing another trailer after a space failure.
type recordOutputWriter struct {
	c    *sourceCapture
	file *os.File
	err  error
}

func (w *recordOutputWriter) Write(b []byte) (int, error) {
	if w.err == nil {
		w.err = w.c.ctx.Err()
	}
	if w.err == nil {
		w.err = w.c.ensureSpace(int64(len(b)))
	}
	if w.err != nil {
		return 0, w.err
	}
	n, e := w.file.Write(b)
	w.err = e
	return n, e
}

func knownRecordTable(kind, name string) bool {
	known := map[string][]string{
		"conductor":   {"sessions", "workspaces", "threads", "messages", "session_messages", "repos"},
		"antigravity": {"conversation_summaries", "gen_metadata"},
		"tl1":         {"tasks", "task_attempts", "transcript_summaries", "resource_usage", "tool_usage", "handoffs", "candidates", "task_flavors", "task_events", "review_findings", "human_responses", "human_comments"},
		"codex":       {"threads"},
	}
	return slices.Contains(known[strings.ToLower(kind)], name)
}

func sqliteEvidenceIdentity(v sqliteRevision) string {
	if len(v.ParserInputs) == 0 {
		v.ParserInputs = nil
	}
	if len(v.EntityIdentities) == 0 {
		v.EntityIdentities = nil
	}
	return hashBytes([]byte(jsonText([]any{v.Schema, v.Tables, v.ParserInputs, v.Dependencies, v.EntityIdentities})))
}

// Session authority is the exact raw parser-input closure, independent of
// whether it was acquired alone or during a whole-database reconciliation.
// Opaque tables retain their own database/shared history; normalized messages
// are never used as the authority identity.
func (s *recordStore) conductorEntityIdentities(v sqliteRevision, sessionTable string) (map[string]string, error) {
	var sessions recordTable
	shared := []recordTable{}
	for _, table := range v.Tables {
		if table.Name == sessionTable {
			sessions = table
		}
		if (table.Name == "workspaces" || table.Name == "repos") && table.Name != sessionTable {
			table.Coverage = ""
			shared = append(shared, table)
		}
	}
	column := -1
	for _, key := range []string{"id", "session_id", "uuid"} {
		for i, c := range sessions.Columns {
			if c.Name == key {
				column = i
				break
			}
		}
		if column >= 0 {
			break
		}
	}
	if column < 0 {
		return nil, errors.New("unresolved Conductor entity identity column")
	}
	hashes := map[string]hash.Hash{}
	err := s.scanRecordRows(sessions, func(row []sqliteValue) error {
		native, e := row[column].decode()
		if e != nil {
			return e
		}
		id := firstString(native)
		h := hashes[id]
		if h == nil {
			if len(hashes) >= 100_000 {
				return errRecordBudget
			}
			h = newRecordHash()
			hashes[id] = h
		}
		return json.NewEncoder(h).Encode(row)
	})
	if err != nil {
		return nil, err
	}
	dependencies := v.Dependencies
	if len(dependencies) == 0 {
		dependencies = nil
	}
	identities := map[string]string{}
	for id, h := range hashes {
		parserInput, ok := v.ParserInputs[id]
		if !ok {
			return nil, errors.New("unresolved Conductor parser sequence")
		}
		identities[id] = hashBytes([]byte(jsonText([]any{v.Schema, sessions.Columns, id, fmt.Sprintf("%x", h.Sum(nil)), parserInput, shared, dependencies})))
	}
	return identities, nil
}

func isConductorSource(kind string) bool { return strings.EqualFold(kind, "conductor") }

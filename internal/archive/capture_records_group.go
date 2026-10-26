package archive

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type sqliteRecordGroup struct {
	AbsentDatabases []string       `json:"absent_databases,omitempty"`
	Version         int            `json:"version"`
	Acquisition     string         `json:"acquisition"`
	Revisions       []recordObject `json:"revisions"`
	CompletedAt     string         `json:"completed_at"`
}

func recordGroupPath(dir, id string) string {
	return filepath.Join(dir, recordDir, "groups", hashBytes([]byte(id))+".json")
}

// Cross-database/file closure is committed only after validating every live
// input against the versions copied. Different DB transactions are never
// described as one SQLite transaction. Moving groups stay pending.
func (c *sourceCapture) commitRecordGroup() error {
	if isConductorSource(c.source.Kind) || len(c.snapshots) == 0 {
		return nil
	}
	plan, err := planCapture(c.source)
	if err != nil || !plan.complete {
		return errors.New("source discovery changed during SQLite group acquisition")
	}
	capturedFiles := map[string]bool{}
	for _, f := range c.files {
		if f.MissingSince == "" {
			capturedFiles[f.Path] = true
		}
	}
	capturedDBs := map[string]bool{}
	for _, d := range c.snapshots {
		if d.MissingSince == "" {
			capturedDBs[d.Path] = true
		}
	}
	for _, f := range plan.files {
		if !capturedFiles[f.path] {
			return errors.New("new dependency appeared during SQLite group acquisition")
		}
	}
	for _, d := range plan.databases {
		if !capturedDBs[d.path] {
			return errors.New("new database appeared during SQLite group acquisition")
		}
	}
	group := sqliteRecordGroup{Version: recordFormat, Acquisition: c.result.StartedAt, CompletedAt: now()}
	s := newRecordStore(c)
	var absentRefs []recordObject
	for _, f := range c.files {
		if f.MissingSince != "" {
			continue
		}
		info, e := os.Stat(f.Path)
		if e != nil || info.Size() != f.Size || info.ModTime().UnixNano() != f.MTimeNS {
			return errors.New("dependency moved during SQLite group acquisition; retry required")
		}
	}
	for _, snapshot := range sortedCaptures(c.snapshots) {
		if snapshot.MissingSince != "" {
			head, e := s.head(snapshot.Path, "database")
			if e != nil {
				return e
			}
			if head.Version == 0 {
				return errors.New("absent database has no durable revision; preserve its native input for backfill")
			}
			if e = s.verify(head.Revision); e != nil {
				return e
			}
			group.AbsentDatabases = append(group.AbsentDatabases, snapshot.Path)
			absentRefs = append(absentRefs, head.Revision)
			continue
		}
		state, e := sqliteDependencyState(snapshot.Path)
		if e != nil || !sameSQLiteContentState(state, snapshot.Source) {
			return errors.New("database moved during SQLite group acquisition; retry required")
		}
		head, e := s.head(snapshot.Path, "database")
		if e != nil {
			return e
		}
		v, e := s.loadRevision(head.Revision)
		if e != nil {
			return e
		}
		if v.Acquisition != group.Acquisition || !v.Complete || !v.Full {
			return errors.New("incomplete SQLite group acquisition")
		}
		if e = s.verify(head.Revision); e != nil {
			return e
		}
		group.Revisions = append(group.Revisions, head.Revision)
	}
	if len(group.Revisions) == 0 {
		return nil
	}
	group.Revisions = append(group.Revisions, absentRefs...)
	dir := filepath.Dir(recordGroupPath(c.dir, group.Acquisition))
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	b, _ := json.Marshal(group)
	return writeRecordMetadata(recordGroupPath(c.dir, group.Acquisition), b)
}
func (s *recordStore) committedGroup(id string) (sqliteRecordGroup, error) {
	var g sqliteRecordGroup
	b, e := os.ReadFile(recordGroupPath(s.c.dir, id))
	if e != nil {
		return g, e
	}
	if e = json.Unmarshal(b, &g); e != nil {
		return g, e
	}
	if g.Version != recordFormat || g.Acquisition != id {
		return g, errors.New("unsupported SQLite acquisition group; upgrade Pharos")
	}
	return g, nil
}
func (s *recordStore) savePartial(v sqliteRevision, cause error) {
	v.Complete = false
	v.Gaps = append(v.Gaps, "acquisition incomplete; retry the original consistent input")
	// Avoid source payload/error strings in status. Objects flushed before the
	// failure remain reachable from this durable partial inventory.
	b, e := json.Marshal(v)
	if e != nil {
		return
	}
	dir := filepath.Join(s.c.dir, recordDir, "partial")
	if e = os.MkdirAll(dir, 0700); e != nil {
		return
	}
	if s.c.ensureSpace(int64(len(b))+4096) != nil {
		return
	}
	_ = writeRecordMetadata(filepath.Join(dir, hashBytes(b)+".json"), b)
}

// Opening a read-only WAL database with no active writer may create an empty
// WAL sidecar. That carries no committed pages and is not a content revision.
func sameSQLiteContentState(a, b sqliteSourceState) bool {
	if a.WALSize == 0 && b.WALSize == 0 {
		a.WALMTimeNS = 0
		b.WALMTimeNS = 0
		a.WALIdentity = ""
		b.WALIdentity = ""
	}
	return a == b
}

func writeRecordMetadata(file string, data []byte) error {
	if e := ensureFree(filepath.Dir(file), int64(len(data))+4096); e != nil {
		return e
	}
	return writeFileDurably(file, data)
}

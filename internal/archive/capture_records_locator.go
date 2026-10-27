package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type sqliteResolvedField struct {
	Revision string      `json:"revision"`
	Table    string      `json:"table"`
	Session  string      `json:"session"`
	Ordinal  int         `json:"ordinal"`
	Column   string      `json:"column"`
	Value    sqliteValue `json:"value"`
}

// resolveLocator always requires an observation. An old ordinal without an
// associated revision cannot be resolved against today's database by guessing.
func (s *recordStore) resolveLocator(revision, locator string) (sqliteResolvedField, error) {
	out := sqliteResolvedField{Revision: revision}
	if !validRecordHash(revision) {
		return out, errors.New("a committed evidence revision is required; legacy ordinal authority unresolved")
	}
	matches, e := filepath.Glob(filepath.Join(s.c.dir, recordDir, "entities", "*", revision+".json"))
	if e != nil || len(matches) != 1 {
		return out, errors.New("evidence revision is absent or ambiguous")
	}
	b, e := os.ReadFile(matches[0])
	if e != nil {
		return out, e
	}
	var ref recordObject
	if e = json.Unmarshal(b, &ref); e != nil {
		return out, e
	}
	if ref.Hash != revision {
		return out, errors.New("evidence revision identity mismatch")
	}
	if e = s.verify(ref); e != nil {
		return out, e
	}
	v, e := s.loadRevision(ref)
	if e != nil {
		return out, e
	}
	prefix := "sqlite:" + v.Database + ":"
	if !strings.HasPrefix(locator, prefix) {
		return out, errors.New("locator does not belong to this original database")
	}
	tail := strings.TrimPrefix(locator, prefix)
	tableName, remaining, ok := strings.Cut(tail, ":")
	if !ok {
		return out, errors.New("unresolved legacy SQLite locator")
	}
	pop := func(value string) (string, string, bool) {
		at := strings.LastIndex(value, ":")
		if at < 0 {
			return "", "", false
		}
		return value[:at], value[at+1:], true
	}
	prefix, last, ok := pop(remaining)
	if !ok {
		return out, errors.New("unresolved legacy SQLite ordinal")
	}
	variant := ""
	ordinal, e := strconv.Atoi(last)
	if e != nil {
		variant = last
		prefix, last, ok = pop(prefix)
		if !ok {
			return out, errors.New("unresolved legacy SQLite ordinal")
		}
		ordinal, e = strconv.Atoi(last)
	}
	if e != nil || ordinal < 0 {
		return out, errors.New("invalid parser ordinal")
	}
	sid, field, ok := pop(prefix)
	if !ok {
		return out, errors.New("unresolved legacy SQLite payload")
	}
	if variant != "" {
		field = variant
	}
	tableRef, ok := v.ParserInputs[sid]
	if !ok {
		return out, errors.New("unresolved session in retained observation")
	}
	var table recordTable
	if e = s.readJSON(tableRef, &table); e != nil {
		return out, e
	}
	if table.Name != tableName {
		return out, errors.New("locator table does not match retained parser input")
	}
	column := -1
	for i, c := range table.Columns {
		if c.Name == field {
			column = i
		}
	}
	if column < 0 {
		return out, errors.New("unresolved retained payload column")
	}
	out.Table, out.Session, out.Ordinal, out.Column = table.Name, sid, ordinal, field
	found := false
	row := 0
	e = s.scanRecordRows(table, func(values []sqliteValue) error {
		if row == ordinal {
			out.Value = values[column]
			found = true
		}
		row++
		return nil
	})
	if e != nil {
		return out, e
	}
	if found {
		return out, nil
	}

	return out, fmt.Errorf("unresolved legacy SQLite locator for observation %s", revision)
}

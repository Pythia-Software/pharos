package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
)

// Interface preferences live in the catalog's ui_preferences table, so a
// library on a drive keeps its theme, optional buttons, bookmarks, and saved
// queries on every Mac it is opened on. The page reads them all at load from
// GET /assets/preferences.js (a script, so they are in hand before anything
// paints) and writes a change with POST /api/preferences:
//
//	{"set": {"pharos-theme": "light"}, "delete": ["pharos-usage-view"]}
//
// Values are any JSON. What stays in the web view's own storage is per Mac by
// design: page history, unsent annotations, and whether a panel is open.

var preferenceKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.-]{0,199}$`)

const maxPreferenceBytes = 512 << 10

// UIPreferences returns every stored preference, keyed by name.
func (c *Catalog) UIPreferences(ctx context.Context) (map[string]json.RawMessage, error) {
	values := map[string]json.RawMessage{}
	rows, err := c.DB.QueryContext(ctx, "SELECT key,value FROM ui_preferences")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if json.Valid([]byte(value)) {
			values[key] = json.RawMessage(value)
		}
	}
	return values, rows.Err()
}

// SetUIPreferences stores the values in body["set"] and removes the names in
// body["delete"], all or nothing.
func (c *Catalog) SetUIPreferences(ctx context.Context, body map[string]any) error {
	set, _ := body["set"].(map[string]any)
	remove, _ := body["delete"].([]any)
	updates := make(map[string]string, len(set))
	for key, value := range set {
		if !preferenceKey.MatchString(key) {
			return fmt.Errorf("%q is not a valid preference name", key)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if len(encoded) > maxPreferenceBytes {
			return fmt.Errorf("preference %q is larger than %d KB", key, maxPreferenceBytes>>10)
		}
		updates[key] = string(encoded)
	}
	deletes := make([]string, 0, len(remove))
	for _, item := range remove {
		key := firstString(item)
		if !preferenceKey.MatchString(key) {
			return fmt.Errorf("%q is not a valid preference name", key)
		}
		deletes = append(deletes, key)
	}
	if len(updates) == 0 && len(deletes) == 0 {
		return nil
	}
	return c.writeTransaction(ctx, "ui-preferences", func(tx *sql.Tx) error {
		for key, value := range updates {
			if _, err := tx.Exec("INSERT INTO ui_preferences(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value); err != nil {
				return err
			}
		}
		for _, key := range deletes {
			if _, err := tx.Exec("DELETE FROM ui_preferences WHERE key=?", key); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Server) getPreferences(w http.ResponseWriter, r *http.Request) {
	values, err := s.Catalog.UIPreferences(r.Context())
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	if r.URL.Path != "/assets/preferences.js" {
		writeJSON(w, map[string]any{"values": values}, http.StatusOK)
		return
	}
	// The page loads this as a blocking script. Failing the request (above)
	// leaves it without the preferences object, which it treats as a page
	// with no library behind it.
	payload, err := json.Marshal(values)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(append([]byte("window.pharosPreferenceValues="), payload...), ';'))
}

func (s *Server) postPreferences(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if err := s.Catalog.SetUIPreferences(r.Context(), body); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	values, err := s.Catalog.UIPreferences(r.Context())
	writeResult(w, map[string]any{"values": values}, err)
}

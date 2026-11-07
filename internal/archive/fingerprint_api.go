package archive

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type fingerprintRequest struct {
	Entity  string `json:"entity"`
	ID      string `json:"id"`
	Profile string `json:"profile"`
}
type fingerprintMetadata struct {
	fingerprintRequest
	State  string            `json:"state"`
	Images map[string]string `json:"images"`
	Href   string            `json:"href,omitempty"`
	Stale  bool              `json:"stale,omitempty"`
}

// Batch metadata is the only read demand that writes: it promotes visible
// entities and can request event-hidden variants. It never loads conversation
// evidence or renders images. The operation registry accounts for that write.
func (s *Server) postFingerprints(w http.ResponseWriter, r *http.Request, body map[string]any) {
	raw, ok := body["items"].([]any)
	if !ok || len(raw) == 0 || len(raw) > 250 {
		writeError(w, fmt.Errorf("request 1–250 fingerprint items"), 400)
		return
	}
	items := make([]fingerprintRequest, 0, len(raw))
	for _, rawItem := range raw {
		item, ok := rawItem.(map[string]any)
		if !ok {
			writeError(w, fmt.Errorf("invalid fingerprint item"), 400)
			return
		}
		entry := fingerprintRequest{firstString(item["entity"]), firstString(item["id"]), firstString(item["profile"])}
		_, validProfile := fingerprintProfiles[entry.Profile]
		if (entry.Entity != "conversation" && entry.Entity != "workspace") || !validProfile || len(entry.ID) == 0 || len(entry.ID) > 512 {
			writeError(w, fmt.Errorf("invalid fingerprint entity or profile"), 400)
			return
		}
		items = append(items, entry)
	}
	if value, exists := body["events"]; exists {
		if _, ok := value.(bool); !ok {
			writeError(w, fmt.Errorf("events must be a boolean"), 400)
			return
		}
	}
	events := body["events"] != false
	if !s.Catalog.readOnly && !s.Catalog.queryOnly {
		pass := s.Catalog.beginPass(r.Context(), "maintenance.fingerprints", triggerRead)
		changed := int64(0)
		err := s.Catalog.writeTransaction(pass.context(r.Context()), "request fingerprint images", func(tx *sql.Tx) error {
			for _, item := range items {
				mask := 1
				if !events {
					mask = 2
				}
				result, err := tx.ExecContext(r.Context(), `UPDATE fingerprint_entities SET priority=100,events_mask=events_mask|?,dirty=CASE WHEN events_mask&?=0 THEN 1 ELSE dirty END WHERE entity=? AND id=? AND (dirty=1 AND priority<>100 OR events_mask&?=0)`, mask, mask, item.Entity, item.ID, mask)
				if err != nil {
					return err
				}
				n, _ := result.RowsAffected()
				changed += n
			}
			return nil
		})
		pass.worked(int(changed), "fingerprint")
		pass.end(err)
		if err != nil {
			writeError(w, err, 500)
			return
		}
	}
	entries := make([]fingerprintMetadata, 0, len(items))
	for _, item := range items {
		metadata, err := s.Catalog.fingerprintMetadata(r.Context(), item, events)
		if err != nil {
			writeError(w, err, 500)
			return
		}
		entries = append(entries, metadata)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"items": entries, "renderer": fingerprintRendererVersion()}, 200)
}

func (c *Catalog) fingerprintMetadata(ctx context.Context, item fingerprintRequest, events bool) (fingerprintMetadata, error) {
	result := fingerprintMetadata{fingerprintRequest: item, State: "pending", Images: map[string]string{}}
	var workID string
	query := `SELECT workspace_id FROM conversations WHERE id=?`
	if item.Entity == "workspace" {
		query = `SELECT id FROM workspaces WHERE id=?`
	}
	if err := c.reader().QueryRowContext(ctx, query, item.ID).Scan(&workID); errors.Is(err, sql.ErrNoRows) {
		result.State = "not_found"
		return result, nil
	} else if err != nil {
		return result, err
	}
	result.Href = "/work/" + url.PathEscape(workID)
	if item.Entity == "conversation" {
		result.Href += "/conversation/" + url.PathEscape(item.ID)
	}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT i.theme,i.hash,i.renderer,i.revision=s.revision current,s.last_error FROM fingerprint_entities s LEFT JOIN fingerprint_images i ON i.entity=s.entity AND i.entity_id=s.id AND i.profile=? AND i.events=? WHERE s.entity=? AND s.id=?`, item.Profile, events, item.Entity, item.ID)
	if err != nil {
		return result, err
	}
	fresh := true
	for _, row := range rows {
		if firstString(row["last_error"]) != "" {
			result.State = "retrying"
		}
		if hash := firstString(row["hash"]); hash != "" {
			result.Images[firstString(row["theme"])] = "/api/fingerprints/images/" + hash + ".png"
			fresh = fresh && integer(row["current"]) != 0 && firstString(row["renderer"]) == fingerprintRendererVersion()
		}
	}
	if len(result.Images) == len(fingerprintThemes) {
		if fresh {
			result.State = "ready"
		} else {
			result.Stale = true
		}
	}
	return result, nil
}

func (s *Server) getFingerprintPNG(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/fingerprints/images/"), ".png")
	if _, err := hex.DecodeString(hash); err != nil || len(hash) != 64 {
		http.NotFound(w, r)
		return
	}
	var data []byte
	if err := s.Catalog.reader().QueryRowContext(r.Context(), `SELECT png FROM fingerprint_blobs WHERE hash=?`, hash).Scan(&data); errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		writeError(w, err, 500)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+hash+`"`)
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Header.Get("If-None-Match") == `"`+hash+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(data)
}

func (s *Server) getFingerprintStatus(w http.ResponseWriter, r *http.Request) {
	rows, err := queryMapsContext(r.Context(), s.Catalog.reader(), `SELECT entity,COUNT(*) entities,SUM(dirty) pending,SUM(CASE WHEN last_error<>'' THEN 1 ELSE 0 END) retrying FROM fingerprint_entities GROUP BY entity`)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	var images, blobs, bytes int64
	err = s.Catalog.reader().QueryRowContext(r.Context(), `SELECT (SELECT COUNT(*) FROM fingerprint_images),COUNT(*),COALESCE(SUM(length(png)),0) FROM fingerprint_blobs`).Scan(&images, &blobs, &bytes)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	writeJSON(w, map[string]any{"entities": rows, "images": images, "unique_images": blobs, "png_bytes": bytes, "renderer": fingerprintRendererVersion()}, 200)
}

package uifeedback

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"
)

const objectPrefix = "uifeedback/v1/submissions/"

var storedDocumentLimit = MaxBodyBytes + 4096
var documentCacheLimit = 32 << 20

type object struct {
	Name           string            `json:"name"`
	Generation     string            `json:"generation"`
	Metageneration string            `json:"metageneration"`
	Created        time.Time         `json:"timeCreated"`
	Metadata       map[string]string `json:"metadata"`
}

func (o object) revision() string { return o.Generation + ":" + o.Metageneration }

type cachedDocument struct {
	generation string
	document   Document
	bytes      int
	used       time.Time
}

// GCS uses the Storage JSON API and an ADC-authenticated HTTP client. Content
// never changes; tags and notification receipts are custom object metadata.
// Listing is always fresh (including across instances), while immutable bodies
// are cached by generation with a 32 MiB bound.
type GCS struct {
	Client   *http.Client
	Bucket   string
	Endpoint string // Empty in production; test server override.
	mu       sync.Mutex
	cache    map[string]cachedDocument
}

func (g *GCS) endpoint() string {
	if g.Endpoint != "" {
		return g.Endpoint
	}
	return "https://storage.googleapis.com"
}
func (g *GCS) objectURL(id string) string {
	return g.endpoint() + "/storage/v1/b/" + url.PathEscape(g.Bucket) + "/o/" + url.PathEscape(objectPrefix+id+".json")
}
func (g *GCS) request(ctx context.Context, method, target, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := g.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case 404:
		return ErrNotFound
	case 409, 412:
		return ErrConflict
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		// Google error reasons identify permission/scope failures without logging
		// the response body, which may contain request-specific information.
		var failure struct {
			Error struct {
				Errors []struct {
					Reason string `json:"reason"`
				} `json:"errors"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&failure)
		reason := "unknown"
		if len(failure.Error.Errors) > 0 && len(failure.Error.Errors[0].Reason) <= 80 {
			reason = failure.Error.Errors[0].Reason
		}
		return fmt.Errorf("GCS %s returned HTTP %d (%s)", method, res.StatusCode, reason)
	}
	if out == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, int64(storedDocumentLimit)+1))
	if err != nil {
		return err
	}
	if len(data) > storedDocumentLimit {
		return fmt.Errorf("GCS response exceeds limit")
	}
	return json.Unmarshal(data, out)
}
func (g *GCS) metadata(ctx context.Context, id string) (object, error) {
	var o object
	err := g.request(ctx, "GET", g.objectURL(id), "", nil, &o)
	return o, err
}
func (g *GCS) Create(ctx context.Context, doc Document) (bool, error) {
	submission, err := json.Marshal(doc.Submission)
	if err != nil {
		return false, err
	}
	hash := sha256.Sum256(submission)
	digest := hex.EncodeToString(hash[:])
	data, err := json.Marshal(doc)
	if err != nil {
		return false, err
	}
	// Intake bounds the request bytes, but marshaling can expand HTML and other
	// characters. Never acknowledge a document the bounded reader cannot read.
	if len(data) > storedDocumentLimit {
		return false, ErrTooLarge
	}
	meta, _ := json.Marshal(map[string]any{"name": objectPrefix + doc.ID + ".json", "contentType": "application/json", "metadata": map[string]string{"sha256": digest, "tags": "[]"}})
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, part := range []struct {
		contentType string
		data        []byte
	}{{"application/json; charset=utf-8", meta}, {"application/json", data}} {
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", part.contentType)
		w, e := writer.CreatePart(h)
		if e != nil {
			return false, e
		}
		if _, e = w.Write(part.data); e != nil {
			return false, e
		}
	}
	if err = writer.Close(); err != nil {
		return false, err
	}
	var created object
	err = g.request(ctx, "POST", g.endpoint()+"/upload/storage/v1/b/"+url.PathEscape(g.Bucket)+"/o?uploadType=multipart&ifGenerationMatch=0", "multipart/related; boundary="+writer.Boundary(), &body, &created)
	if err == ErrConflict {
		old, e := g.metadata(ctx, doc.ID)
		if e != nil {
			return false, e
		}
		if old.Metadata["sha256"] == digest {
			return false, nil
		}
	}
	return err == nil, err
}
func (g *GCS) body(ctx context.Context, id string, o object) (Document, error) {
	g.mu.Lock()
	cached, ok := g.cache[id]
	if ok && cached.generation == o.Generation {
		cached.used = time.Now()
		g.cache[id] = cached
		g.mu.Unlock()
		return cached.document, nil
	}
	g.mu.Unlock()
	var doc Document
	if err := g.request(ctx, "GET", g.objectURL(id)+"?alt=media&generation="+url.QueryEscape(o.Generation), "", nil, &doc); err != nil {
		return doc, err
	}
	if doc.ID != id || doc.ReceivedAt.IsZero() {
		return doc, fmt.Errorf("invalid stored document")
	}
	data, _ := json.Marshal(doc)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cache == nil {
		g.cache = map[string]cachedDocument{}
	}
	g.cache[id] = cachedDocument{o.Generation, doc, len(data), time.Now()}
	for {
		total := 0
		oldest := ""
		var oldestAt time.Time
		for key, item := range g.cache {
			total += item.bytes
			if oldest == "" || item.used.Before(oldestAt) {
				oldest = key
				oldestAt = item.used
			}
		}
		if total <= documentCacheLimit {
			break
		}
		delete(g.cache, oldest)
	}
	return doc, nil
}
func (g *GCS) entry(ctx context.Context, id string, o object) (Entry, error) {
	var e Entry
	if time.Since(o.Created) >= Retention {
		return e, ErrNotFound
	}
	doc, err := g.body(ctx, id, o)
	if err != nil {
		return e, err
	}
	e.Document = doc
	e.Revision = o.revision()
	e.Tags = []string{}
	if tags := o.Metadata["tags"]; tags != "" {
		if err = json.Unmarshal([]byte(tags), &e.Tags); err != nil {
			return e, fmt.Errorf("invalid stored tags")
		}
	}
	return e, nil
}
func (g *GCS) Get(ctx context.Context, id string) (Entry, error) {
	o, err := g.metadata(ctx, id)
	if err != nil {
		return Entry{}, err
	}
	return g.entry(ctx, id, o)
}
func (g *GCS) List(ctx context.Context) ([]Summary, error) {
	entries := []Summary{}
	live := map[string]bool{}
	token := ""
	for {
		var page struct {
			Items []object `json:"items"`
			Next  string   `json:"nextPageToken"`
		}
		target := g.endpoint() + "/storage/v1/b/" + url.PathEscape(g.Bucket) + "/o?prefix=" + url.QueryEscape(objectPrefix) + "&maxResults=100&pageToken=" + url.QueryEscape(token)
		if err := g.request(ctx, "GET", target, "", nil, &page); err != nil {
			return nil, err
		}
		for _, o := range page.Items {
			id := strings.TrimSuffix(strings.TrimPrefix(o.Name, objectPrefix), ".json")
			if !validID(id) || time.Since(o.Created) >= Retention {
				continue
			}
			live[id] = true
			e, err := g.entry(ctx, id, o)
			if err == ErrNotFound {
				continue
			}
			if err != nil {
				return nil, err
			}
			entries = append(entries, summarize(e))
		}
		token = page.Next
		if token == "" {
			break
		}
	}
	g.mu.Lock()
	for id := range g.cache {
		if !live[id] {
			delete(g.cache, id)
		}
	}
	g.mu.Unlock()
	return entries, nil
}
func (g *GCS) patch(ctx context.Context, id, revision string, metadata map[string]string) error {
	parts := strings.Split(revision, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ErrConflict
	}
	data, _ := json.Marshal(map[string]any{"metadata": metadata})
	return g.request(ctx, "PATCH", g.objectURL(id)+"?ifGenerationMatch="+url.QueryEscape(parts[0])+"&ifMetagenerationMatch="+url.QueryEscape(parts[1]), "application/json", bytes.NewReader(data), nil)
}
func (g *GCS) SetTags(ctx context.Context, id, revision string, tags []string) error {
	data, _ := json.Marshal(tags)
	return g.patch(ctx, id, revision, map[string]string{"tags": string(data)})
}
func (g *GCS) Delete(ctx context.Context, id, revision string) error {
	parts := strings.Split(revision, ":")
	if len(parts) != 2 {
		return ErrConflict
	}
	err := g.request(ctx, "DELETE", g.objectURL(id)+"?ifGenerationMatch="+url.QueryEscape(parts[0])+"&ifMetagenerationMatch="+url.QueryEscape(parts[1]), "", nil, nil)
	if err == nil || err == ErrNotFound {
		g.mu.Lock()
		delete(g.cache, id)
		g.mu.Unlock()
		return nil
	}
	return err
}

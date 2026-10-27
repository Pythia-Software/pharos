package uifeedback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type storedObject struct {
	meta object
	body []byte
}
type storageFixture struct {
	mu         sync.Mutex
	objects    map[string]storedObject
	media      int
	generation int
}

func gcsFixture(t *testing.T) (*GCS, *storageFixture) {
	t.Helper()
	fixture := &storageFixture{objects: map[string]storedObject{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		if r.Method == "POST" {
			_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			reader := multipart.NewReader(r.Body, params["boundary"])
			part, err := reader.NextPart()
			if err != nil {
				t.Error(err)
				return
			}
			var meta object
			if err = json.NewDecoder(part).Decode(&meta); err != nil {
				t.Error(err)
				return
			}
			part, err = reader.NextPart()
			if err != nil {
				t.Error(err)
				return
			}
			body, err := io.ReadAll(part)
			if err != nil {
				t.Error(err)
				return
			}
			if r.URL.Query().Get("ifGenerationMatch") != "0" {
				t.Error("missing create condition")
			}
			if _, ok := fixture.objects[meta.Name]; ok {
				w.WriteHeader(412)
				return
			}
			fixture.generation++
			meta.Generation = strconv.Itoa(fixture.generation)
			meta.Metageneration = "1"
			meta.Created = time.Now().UTC()
			fixture.objects[meta.Name] = storedObject{meta, body}
			_ = json.NewEncoder(w).Encode(meta)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/storage/v1/b/test-bucket/o/")
		if r.URL.Path == "/storage/v1/b/test-bucket/o" {
			// One object per page exercises continuation even for tiny inboxes.
			keys := []string{}
			for key := range fixture.objects {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			start, _ := strconv.Atoi(r.URL.Query().Get("pageToken"))
			items := []object{}
			next := ""
			if start < len(keys) {
				items = append(items, fixture.objects[keys[start]].meta)
				if start+1 < len(keys) {
					next = strconv.Itoa(start + 1)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "nextPageToken": next})
			return
		}
		value, ok := fixture.objects[name]
		if !ok {
			w.WriteHeader(404)
			return
		}
		for query, current := range map[string]string{"ifGenerationMatch": value.meta.Generation, "generation": value.meta.Generation, "ifMetagenerationMatch": value.meta.Metageneration} {
			if expected := r.URL.Query().Get(query); expected != "" && expected != current {
				w.WriteHeader(412)
				return
			}
		}
		switch r.Method {
		case "GET":
			if r.URL.Query().Get("alt") == "media" {
				fixture.media++
				_, _ = w.Write(value.body)
			} else {
				_ = json.NewEncoder(w).Encode(value.meta)
			}
		case "PATCH":
			var input struct {
				Metadata map[string]string `json:"metadata"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
				return
			}
			for k, v := range input.Metadata {
				value.meta.Metadata[k] = v
			}
			revision, _ := strconv.Atoi(value.meta.Metageneration)
			value.meta.Metageneration = strconv.Itoa(revision + 1)
			fixture.objects[name] = value
			_ = json.NewEncoder(w).Encode(value.meta)
		case "DELETE":
			delete(fixture.objects, name)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected storage request: %s %s", r.Method, r.URL)
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(server.Close)
	return &GCS{Client: server.Client(), Bucket: "test-bucket", Endpoint: server.URL}, fixture
}
func TestGCSImmutableCacheTagsAndDeletion(t *testing.T) {
	g, f := gcsFixture(t)
	ctx := context.Background()
	d := Document{Submission: submission(), ReceivedAt: time.Now().UTC()}
	if created, err := g.Create(ctx, d); err != nil || !created {
		t.Fatalf("create: %t %v", created, err)
	}
	d.ReceivedAt = d.ReceivedAt.Add(time.Minute)
	if created, err := g.Create(ctx, d); err != nil || created {
		t.Fatalf("idempotent retry: %t %v", created, err)
	}
	d.Comment = "changed"
	if _, err := g.Create(ctx, d); !errors.Is(err, ErrConflict) {
		t.Fatalf("overwrote submission: %v", err)
	}
	entries, err := g.List(ctx)
	if err != nil || len(entries) != 1 {
		t.Fatalf("list: %#v %v", entries, err)
	}
	first := entries[0]
	if err = g.SetTags(ctx, first.ID, first.Revision, []string{"acknowledged", "ui"}); err != nil {
		t.Fatal(err)
	}
	entries, err = g.List(ctx)
	if err != nil || len(entries[0].Tags) != 2 {
		t.Fatalf("tags: %#v %v", entries, err)
	}
	if f.media != 1 {
		t.Fatalf("tag edit fetched immutable body again: %d", f.media)
	}
	if err = g.SetTags(ctx, first.ID, first.Revision, []string{}); !errors.Is(err, ErrConflict) {
		t.Fatal("lost concurrent tag edit")
	}
	if err = g.Delete(ctx, first.ID, first.Revision); !errors.Is(err, ErrConflict) {
		t.Fatal("deleted using a stale revision")
	}
	if err = g.Delete(ctx, first.ID, entries[0].Revision); err != nil {
		t.Fatal(err)
	}
	if len(f.objects) != 0 || len(g.cache) != 0 {
		t.Fatal("delete retained stored or cached report")
	}
	entries, err = g.List(ctx)
	if err != nil || len(entries) != 0 {
		t.Fatalf("resurrected deleted body: %v %v", entries, err)
	}
	t.Run("encoded size is checked before acceptance", func(t *testing.T) {
		g, f := gcsFixture(t)
		d := Document{Submission: submission(), ReceivedAt: time.Now().UTC()}
		d.Comment = strings.Repeat("<&>", 400)
		if err := d.Submission.Validate(); err != nil {
			t.Fatal(err)
		}
		var wire bytes.Buffer
		encoder := json.NewEncoder(&wire)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(d.Submission); err != nil {
			t.Fatal(err)
		}
		old := storedDocumentLimit
		t.Cleanup(func() { storedDocumentLimit = old })
		storedDocumentLimit = wire.Len() + 128
		server, _, _ := testInbox(t)
		server.Store = g
		req := httptest.NewRequest("POST", "/v1/submissions", &wire)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, req)
		if w.Code != 413 || len(f.objects) != 0 {
			t.Fatalf("unreadable report accepted: HTTP %d, %d objects", w.Code, len(f.objects))
		}
		encoded, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		storedDocumentLimit = len(encoded)
		if created, err := g.Create(ctx, d); err != nil || !created {
			t.Fatalf("boundary create: %v", err)
		}
		got, err := g.Get(ctx, d.ID)
		if err != nil || got.Comment != d.Comment {
			t.Fatalf("accepted boundary report unreadable: %v", err)
		}
		if rows, err := g.List(ctx); err != nil || len(rows) != 1 {
			t.Fatalf("boundary list: %v", err)
		}
	})

}
func TestGCSPaginationRetentionAndExternalDelete(t *testing.T) {
	g, f := gcsFixture(t)
	ctx := context.Background()
	d := Document{Submission: submission(), ReceivedAt: time.Now().UTC()}
	_, _ = g.Create(ctx, d)
	d.ID = annotationID
	_, _ = g.Create(ctx, d)
	entries, err := g.List(ctx)
	if err != nil || len(entries) != 2 {
		t.Fatalf("pagination: %d %v", len(entries), err)
	}
	f.mu.Lock()
	old := f.objects[objectPrefix+reportID+".json"]
	old.meta.Created = time.Now().Add(-Retention - time.Hour)
	f.objects[old.meta.Name] = old
	delete(f.objects, objectPrefix+annotationID+".json")
	f.mu.Unlock()
	entries, err = g.List(ctx)
	if err != nil || len(entries) != 0 || len(g.cache) != 0 {
		t.Fatalf("retention/external deletion: %d %d %v", len(entries), len(g.cache), err)
	}
	if _, err = g.Get(ctx, reportID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired report readable: %v", err)
	}
	t.Run("summaries stay lightweight as attachments exceed the cache budget", func(t *testing.T) {
		g, _ := gcsFixture(t)
		old := documentCacheLimit
		documentCacheLimit = 4096
		t.Cleanup(func() { documentCacheLimit = old })
		d := Document{Submission: submission(), ReceivedAt: time.Now().UTC()}
		d.Comment = ""
		d.Annotations = []Annotation{{ID: annotationID, Comment: "Annotated feedback", Screenshot: &Screenshot{MIME: "image/png", Data: strings.Repeat("image", 400)}}}
		d.Diagnostics = &Diagnostics{Service: strings.Repeat("logs", 400)}
		ids := []string{reportID, annotationID, "cc0d0d0e-270b-4f85-8b75-b01234567890"}
		for _, id := range ids {
			d.ID = id
			if _, err := g.Create(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := g.List(ctx)
		if err != nil || len(rows) != len(ids) {
			t.Fatalf("summaries: %v", err)
		}
		for _, row := range rows {
			if row.Comment != "Annotated feedback" || row.Annotations != 1 || row.Screenshots != 1 || !row.Diagnostics {
				t.Fatalf("summary lost report information: %#v", row)
			}
		}
		payload, _ := json.Marshal(rows)
		if strings.Contains(string(payload), d.Annotations[0].Screenshot.Data) || strings.Contains(string(payload), d.Diagnostics.Service) {
			t.Fatal("listing retained attachment payloads")
		}
		retained := 0
		for _, cached := range g.cache {
			retained += cached.bytes
		}
		if retained > documentCacheLimit {
			t.Fatalf("cache exceeds budget: %d", retained)
		}
		entry, err := g.Get(ctx, ids[0])
		if err != nil || entry.Annotations[0].Screenshot.Data != d.Annotations[0].Screenshot.Data || entry.Diagnostics.Service != d.Diagnostics.Service {
			t.Fatalf("detail lost attachments after summary listing: %v", err)
		}
	})

}
func TestNotificationRetryAndMetadataReceipt(t *testing.T) {
	g, f := gcsFixture(t)
	ctx := context.Background()
	d := Document{Submission: submission(), ReceivedAt: time.Now().UTC()}
	d.Email = "private@example.test"
	_, _ = g.Create(ctx, d)
	calls := 0
	mail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		user, secret, ok := r.BasicAuth()
		if !ok || user != "key" || secret != "secret" {
			t.Error("missing Twilio API credentials")
		}
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), d.Email) || strings.Contains(string(b), d.Comment) {
			t.Error("notification leaked submission contents")
		}
		if calls == 1 {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(202)
		}
	}))
	defer mail.Close()
	n := Notifications{Store: g, Email: Email{KeySID: "key", KeySecret: "secret", From: "notify@example.test", Origin: "https://inbox.example.test", Endpoint: mail.URL, Client: mail.Client()}}
	generation := f.objects[objectPrefix+reportID+".json"].meta.Generation
	if err := n.Handle(ctx, reportID, generation); err == nil {
		t.Fatal("failed delivery acknowledged")
	}
	if err := n.Handle(ctx, reportID, generation); err != nil {
		t.Fatal(err)
	}
	if err := n.Handle(ctx, reportID, generation); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("completed delivery repeated: %d", calls)
	}
	entry, err := g.Get(ctx, reportID)
	if err != nil {
		t.Fatal(err)
	}
	if err = g.SetTags(ctx, reportID, entry.Revision, []string{"fixed"}); err != nil {
		t.Fatal(err)
	}
	if err = n.Handle(ctx, reportID, generation); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("tag mutation erased notification receipt")
	}
}

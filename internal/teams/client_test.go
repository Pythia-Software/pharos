package teams

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeviceClientSyntheticExchange(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	id := "a4c09e42-d0a6-45f0-89ef-2a03b7cff630"
	config := json.RawMessage(`{"question":"synthetic"}`)
	payload := json.RawMessage(`{"outcome":"completed"}`)
	payloadHash := hashBytes(payload)
	calls := 0
	var submittedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		message, e := signedMessage(id, r.Method, r.URL.EscapedPath(), r.Header.Get("X-Pharos-Timestamp"), r.Header.Get("X-Pharos-Nonce"), raw)
		sig, e2 := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Pharos-Signature"))
		if e != nil || e2 != nil || !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte(message), sig) {
			t.Error("signature failed")
		}
		switch r.URL.Path {
		case "/v1/device/poll":
			json.NewEncoder(w).Encode(PollResponse{ServerTime: time.Now(), Work: []Work{{ID: id, Config: config, ConfigSHA256: hashBytes(config)}}, Cancellations: []Cancellation{}, NextPollSeconds: 900})
		case "/v1/device/work/" + id + "/sources":
			json.NewEncoder(w).Encode(SourcesResponse{ManifestSHA256: hashBytes(raw), OwnershipRevision: 1, OwnedSourceTokens: []string{}})
		case "/v1/device/work/" + id + "/result", "/v1/device/work/" + id + "/receipt":
			if r.Method == http.MethodPut {
				submittedBody = append([]byte(nil), raw...)
			}
			json.NewEncoder(w).Encode(Receipt{ID: id, WorkID: id, PayloadSHA256: payloadHash, SubmissionSHA256: hashBytes(submittedBody), IdempotencyKey: id, InstallationID: id, ResultRevision: 1, AcceptedAt: time.Now(), Outcome: "completed"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := NewDeviceClient(srv.URL, id, key, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	poll, err := c.Poll(ctx, PollRequest{AppVersion: "1.0.0", LibraryIDs: []string{"synthetic-library"}})
	if err != nil || len(poll.Work) != 1 {
		t.Fatalf("poll: %+v %v", poll, err)
	}
	beforeCapabilities := calls
	for _, manifest := range []CapabilityManifest{
		{SchemaVersion: 1, AppVersion: "0.9.9", Capabilities: []string{}},
		{SchemaVersion: 1, AppVersion: "1.0.0", Capabilities: []string{"source_snapshot_v2", "deterministic_query_v1"}},
		{SchemaVersion: 1, AppVersion: "1.0.0", Capabilities: []string{"source_snapshot_v2", "source_snapshot_v2"}},
		{SchemaVersion: 1, AppVersion: "1.0.0", Capabilities: []string{"unknown"}},
	} {
		if _, e := c.Poll(ctx, PollRequest{AppVersion: "1.0.0", LibraryIDs: []string{"synthetic-library"}, CapabilityManifest: &manifest}); e == nil {
			t.Fatal("invalid capability manifest accepted")
		}
	}
	if calls != beforeCapabilities {
		t.Fatal("invalid capability manifest reached server")
	}
	m := Manifest{LocalRevision: 1, Complete: true, SourceManifest: []Source{}}
	if _, err = c.RegisterSources(ctx, id, m); err != nil {
		t.Fatal(err)
	}
	submission := Submission{IdempotencyKey: id, Payload: payload, PayloadSHA256: payloadHash}
	body, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Submit(ctx, id, body, payloadHash); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Receipt(ctx, id, payloadHash, hashBytes(body), id); err != nil {
		t.Fatal(err)
	}
	before := calls
	if _, err = c.Submit(ctx, id, body, hashBytes([]byte("different"))); err == nil || calls != before {
		t.Fatal("mismatched payload reached server")
	}
	if _, err = c.Receipt(ctx, id, hashBytes([]byte("different")), hashBytes(body), id); err == nil {
		t.Fatal("mismatched receipt accepted")
	}
}

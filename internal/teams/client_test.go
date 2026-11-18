package teams

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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

func TestPollNegotiatesManifestAndFallsBack(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	var versions []int
	discoveries := 0
	reject := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/device/capabilities":
			discoveries++
			json.NewEncoder(w).Encode(map[string]any{"manifest_schema_versions": []int{1, 2}})
		case "/v1/device/poll":
			var body struct {
				Manifest struct {
					SchemaVersion int                 `json:"schema_version"`
					Features      []string            `json:"features"`
					Capabilities  []string            `json:"capabilities"`
					Profiles      []CapabilityProfile `json:"profiles"`
				} `json:"capability_manifest"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			versions = append(versions, body.Manifest.SchemaVersion)
			if body.Manifest.SchemaVersion == 2 && (!slices.Contains(body.Manifest.Features, "report_deterministic_v2") || !slices.Contains(body.Manifest.Features, "reader_labels_v1")) {
				t.Error("missing negotiated v2 feature")
			}
			if body.Manifest.SchemaVersion == 1 && (slices.Contains(body.Manifest.Capabilities, "report_deterministic_v2") || slices.Contains(body.Manifest.Capabilities, "reader_labels_v1")) {
				t.Error("v2 feature leaked into v1 fallback")
			}
			if body.Manifest.SchemaVersion == 2 && reject {
				reject = false
				json.NewEncoder(w).Encode(PollResponse{CapabilityStatus: "manifest_rejected", Work: []Work{}, Cancellations: []Cancellation{}})
				return
			}
			if body.Manifest.SchemaVersion == 2 && (body.Manifest.Features == nil || body.Manifest.Profiles == nil) {
				t.Error("v2 arrays missing")
			}
			json.NewEncoder(w).Encode(PollResponse{Work: []Work{}, Cancellations: []Cancellation{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewDeviceClient(server.URL, "a4c09e42-d0a6-45f0-89ef-2a03b7cff630", key, true)
	if err != nil {
		t.Fatal(err)
	}
	request := PollRequest{AppVersion: "0.10.0", LibraryIDs: []string{}, CapabilityManifest: &CapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"deterministic_query_v1", "source_snapshot_v2"}, V2OnlyFeatures: []string{"reader_labels_v1", "report_deterministic_v2"}}}
	for i := 0; i < 3; i++ {
		if _, err = client.Poll(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	if discoveries != 1 || len(versions) != 4 || versions[0] != 2 || versions[1] != 1 || versions[2] != 1 || versions[3] != 1 {
		t.Fatalf("discovery=%d versions=%v", discoveries, versions)
	}
}

func TestDiscoveryFailureDoesNotDowngradeOrPoll(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/device/capabilities" {
			t.Errorf("unexpected poll after discovery failure")
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client, err := NewDeviceClient(server.URL, "a4c09e42-d0a6-45f0-89ef-2a03b7cff630", key, true)
	if err != nil {
		t.Fatal(err)
	}
	request := PollRequest{AppVersion: "0.10.0", LibraryIDs: []string{}, CapabilityManifest: &CapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"deterministic_query_v1", "source_snapshot_v2"}}}
	if _, err = client.Poll(context.Background(), request); err == nil || calls != 1 {
		t.Fatalf("discovery failure: %v, calls=%d", err, calls)
	}
}

func TestPollOldServerRejectsV2AfterDiscovery(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/device/capabilities" {
			json.NewEncoder(w).Encode(map[string]any{"manifest_schema_versions": []int{1, 2}})
			return
		}
		polls++
		if polls == 1 {
			w.WriteHeader(422)
			w.Write([]byte(`{"error":{"code":"invalid_input"}}`))
			return
		}
		json.NewEncoder(w).Encode(PollResponse{Work: []Work{}, Cancellations: []Cancellation{}})
	}))
	defer server.Close()
	client, err := NewDeviceClient(server.URL, "a4c09e42-d0a6-45f0-89ef-2a03b7cff630", key, true)
	if err != nil {
		t.Fatal(err)
	}
	request := PollRequest{AppVersion: "0.10.0", LibraryIDs: []string{}, CapabilityManifest: &CapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"deterministic_query_v1", "source_snapshot_v2"}}}
	if _, err = client.Poll(context.Background(), request); err != nil || polls != 2 {
		t.Fatalf("fallback: %v, polls=%d", err, polls)
	}
}

func TestPollRejectsManualSeriesOnLegacyCarrier(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	valid := json.RawMessage(`{"question":"valid"}`)
	series := json.RawMessage(`{"publication_v2":{"schema_version":2}}`)
	alias := json.RawMessage(`{"Publication_V2":{"schema_version":2}}`)
	duplicate := json.RawMessage(`{"publication_v2":null,"publication_v2":{}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/device/capabilities" {
			json.NewEncoder(w).Encode(map[string]any{"manifest_schema_versions": []int{1}})
			return
		}
		json.NewEncoder(w).Encode(PollResponse{Work: []Work{{Config: valid, ConfigSHA256: hashBytes(valid)}, {Config: series, ConfigSHA256: hashBytes(series)}, {Config: alias, ConfigSHA256: hashBytes(alias)}, {Config: duplicate, ConfigSHA256: hashBytes(duplicate)}, {Config: valid, ConfigSHA256: strings.Repeat("0", 64)}}, Cancellations: []Cancellation{{WorkID: "00000000-0000-4000-8000-000000000001"}}})
	}))
	defer server.Close()
	client, err := NewDeviceClient(server.URL, "a4c09e42-d0a6-45f0-89ef-2a03b7cff630", key, true)
	if err != nil {
		t.Fatal(err)
	}
	manifest := CapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"deterministic_query_v1", "source_snapshot_v2"}, V2OnlyFeatures: []string{"report_deterministic_v2"}}
	out, err := client.Poll(context.Background(), PollRequest{AppVersion: "0.10.0", LibraryIDs: []string{}, CapabilityManifest: &manifest})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Work) != 1 || string(out.Work[0].Config) != string(valid) || len(out.Cancellations) != 1 {
		t.Fatalf("lost valid work or cancellation: %+v", out)
	}
}

func TestPollLegacyDiscoveryUsesOnlyV1Capabilities(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	var polls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/device/capabilities" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/v1/device/poll" {
			http.NotFound(w, r)
			return
		}
		polls++
		var body struct {
			Manifest struct {
				SchemaVersion int             `json:"schema_version"`
				Capabilities  []string        `json:"capabilities"`
				Features      json.RawMessage `json:"features"`
			} `json:"capability_manifest"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Manifest.SchemaVersion != 1 || len(body.Manifest.Features) != 0 || slices.Contains(body.Manifest.Capabilities, "reader_labels_v1") || slices.Contains(body.Manifest.Capabilities, "report_deterministic_v2") {
			t.Error("legacy server received v2 declaration")
		}
		json.NewEncoder(w).Encode(PollResponse{Work: []Work{}, Cancellations: []Cancellation{}})
	}))
	defer server.Close()
	client, err := NewDeviceClient(server.URL, "a4c09e42-d0a6-45f0-89ef-2a03b7cff630", key, true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Destroy()
	m := CapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"deterministic_query_v1", "source_snapshot_v2"}, V2OnlyFeatures: []string{"reader_labels_v1", "report_deterministic_v2"}}
	if _, err = client.Poll(context.Background(), PollRequest{AppVersion: m.AppVersion, LibraryIDs: []string{}, CapabilityManifest: &m}); err != nil || polls != 1 {
		t.Fatalf("legacy poll: %v, calls=%d", err, polls)
	}
}

func TestPollResponseWorkBoundBeforeFiltering(t *testing.T) {
	for _, count := range []int{200, 201} {
		raw, err := json.Marshal(map[string]any{"work": make([]any, count)})
		if err != nil {
			t.Fatal(err)
		}
		var response PollResponse
		err = decodePollResponse(raw, &response)
		if (err != nil) != (count > 200) {
			t.Fatalf("%d items: %v", count, err)
		}
	}
}

func TestPollSourceCarrierUsesOnlyNegotiatedV2(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, v2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("v2-%t", v2), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/device/capabilities" {
					versions := []int{1}
					if v2 {
						versions = append(versions, 2)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"manifest_schema_versions": versions})
					return
				}
				var body struct {
					Manifest struct {
						SchemaVersion int                 `json:"schema_version"`
						Features      []string            `json:"features"`
						Capabilities  []string            `json:"capabilities"`
						Profiles      []CapabilityProfile `json:"profiles"`
					} `json:"capability_manifest"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if v2 {
					if body.Manifest.SchemaVersion != 2 || !slices.Contains(body.Manifest.Features, "source_protection_v1") || !slices.Contains(body.Manifest.Features, "source_recovery_v1") || len(body.Manifest.Profiles) != 0 {
						t.Errorf("unexpected v2 source carrier: %+v", body.Manifest)
					}
				} else if body.Manifest.SchemaVersion != 1 || slices.Contains(body.Manifest.Capabilities, "source_protection_v1") || slices.Contains(body.Manifest.Capabilities, "source_recovery_v1") {
					t.Errorf("source features leaked to v1: %+v", body.Manifest)
				}
				_ = json.NewEncoder(w).Encode(PollResponse{Work: []Work{}, Cancellations: []Cancellation{}})
			}))
			defer server.Close()
			client, e := NewDeviceClient(server.URL, "a4c09e42-d0a6-45f0-89ef-2a03b7cff630", key, true)
			if e != nil {
				t.Fatal(e)
			}
			m := CapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"source_snapshot_v2"}, V2OnlyFeatures: []string{"source_protection_v1", "source_recovery_v1"}}
			out, e := client.Poll(context.Background(), PollRequest{AppVersion: m.AppVersion, LibraryIDs: []string{}, CapabilityManifest: &m})
			if e != nil || out.NegotiatedManifestV2 != v2 {
				t.Fatalf("negotiated=%t err=%v", out.NegotiatedManifestV2, e)
			}
		})
	}
}

func TestPollDropsScheduledAssignmentOnContentAndDifferentWork(t *testing.T) {
	const good = `{"id":"07000000-0000-4000-8000-000000000001","kind":"llm","config":{}}`
	const assignment = `"scheduled_assignment":{"assignment_id":"07000000-0000-4000-8000-000000000001","period_key":"period:0","timezone":"UTC","start_at":"2026-01-02T00:00:00Z","deadline_at":"2026-01-02T01:00:00Z"}`
	for _, tc := range []struct{ kind, id string }{
		{"llm", "07000000-0000-4000-8000-000000000001"},
		{"deterministic", "07000000-0000-4000-8000-000000000002"},
	} {
		raw := []byte(`{"work":[` + good + `,{"id":"` + tc.id + `","kind":"` + tc.kind + `","config":{},` + assignment + `}]}`)
		var out PollResponse
		if err := decodePollResponse(raw, &out); err != nil || len(out.Work) != 1 || out.Work[0].ScheduledAssignment != nil {
			t.Fatalf("invalid item damaged unrelated work: %s", tc.kind)
		}
	}
}

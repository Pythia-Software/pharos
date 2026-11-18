package teams

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func typedResultFixture(t *testing.T) (TypedResultCommand, string, string, string) {
	t.Helper()
	raw, e := os.ReadFile("testdata/typed-usage-multipart-v2-candidate.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Work            string `json:"work_id"`
		Installation    string `json:"installation_id"`
		Reservation     string `json:"reservation_json"`
		Page            string `json:"page_json"`
		Commit          string `json:"commit_json"`
		ReservationHash string `json:"reservation_sha256"`
		PageHash        string `json:"page_sha256"`
	}
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	return TypedResultCommand{WorkID: f.Work, Reservation: []byte(f.Reservation), Pages: [][]byte{[]byte(f.Page)}, Commit: []byte(f.Commit)}, f.Installation, f.ReservationHash, f.PageHash
}
func TestTypedResultSignedWire(t *testing.T) {
	cmd, installation, resHash, pageHash := typedResultFixture(t)
	prepared, err := PrepareTypedResult(cmd, installation)
	if err != nil {
		t.Fatal(err)
	}
	identity := prepared.Identity()
	if err != nil || identity.ReservationSHA256 != resHash || identity.PageSHA256[0] != pageHash || identity.CommitBodySHA256 != "e8f9b18fd5f0b05be8790351843bf5f954ea201aa3a5c647238c44bf7199b551" {
		t.Fatalf("vector: %+v %v", identity, err)
	}
	raw, err := os.ReadFile("testdata/typed-usage-reconcile-v2-candidate.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Responses map[string]json.RawMessage `json:"responses"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	// The published server fixture is a candidate with a reduced receipt. Add the
	// source fields emitted by the actual typed commit before testing acceptance.
	var accepted map[string]any
	json.Unmarshal(fixture.Responses["accepted"], &accepted)
	rec := accepted["receipt"].(map[string]any)
	rec["source_binding_id"] = identity.SourceBindingID
	rec["assignment_sha256"] = identity.AssignmentSHA256
	rec["inventory_revision"] = identity.InventoryRevision
	rec["catalog_revision"] = identity.CatalogRevision
	rec["publication_state"] = "published"
	fixture.Responses["accepted"], _ = json.Marshal(accepted)
	_, key, _ := ed25519.GenerateKey(nil)
	mode := "open"
	nonces := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		nonce := r.Header.Get("X-Pharos-Nonce")
		msg, e := signedMessage(installation, r.Method, r.URL.EscapedPath(), r.Header.Get("X-Pharos-Timestamp"), nonce, body)
		sig, e2 := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Pharos-Signature"))
		if e != nil || e2 != nil || !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte(msg), sig) || nonces[nonce] {
			t.Error("invalid signature or reused nonce")
		}
		nonces[nonce] = true
		base := "/v1/device/work/" + cmd.WorkID + "/typed-result-uploads"
		path := base
		method := http.MethodPost
		expected := cmd.Reservation
		switch mode {
		case "staged":
			path += "/" + identity.UploadID + "/pages/0"
			method = http.MethodPut
			expected = cmd.Pages[0]
		case "commit", "commitbad":
			path += "/" + identity.UploadID + "/commit"
			method = http.MethodPut
			expected = cmd.Commit
		case "lookup":
			path = "/v1/device/work/" + cmd.WorkID + "/receipt"
			method = http.MethodGet
			expected = nil
		case "accepted", "closed", "indeterminate", "missing", "badclosed":
			path += "/reconcile"
			expected = []byte(`{"upload_id":"` + identity.UploadID + `","reservation_sha256":"` + resHash + `","commit_body_sha256":"` + identity.CommitBodySHA256 + `"}`)
		}
		if r.Method != method || r.URL.Path != path || string(body) != string(expected) {
			t.Errorf("unexpected request %s %s %s", r.Method, r.URL.Path, body)
		}
		switch mode {
		case "open":
			io.WriteString(w, `{"status":"open","upload_id":"`+identity.UploadID+`","reservation_sha256":"`+resHash+`"}`)
		case "staged":
			io.WriteString(w, `{"status":"staged","upload_id":"`+identity.UploadID+`","page_sha256":"`+pageHash+`"}`)
		case "commit", "commitbad":
			var v struct {
				Receipt json.RawMessage `json:"receipt"`
			}
			json.Unmarshal(fixture.Responses["accepted"], &v)
			if mode == "commitbad" {
				v.Receipt = json.RawMessage(strings.Replace(string(v.Receipt), identity.PayloadSHA256, strings.Repeat("a", 64), 1))
			}
			io.WriteString(w, `{"status":"accepted","upload_id":"`+identity.UploadID+`","receipt":`+string(v.Receipt)+`}`)
		case "lookup":
			var v struct {
				Receipt json.RawMessage `json:"receipt"`
			}
			json.Unmarshal(fixture.Responses["accepted"], &v)
			w.Write(v.Receipt)
		case "missing":
			http.Error(w, "missing", 404)
		case "badclosed":
			io.WriteString(w, strings.Replace(string(fixture.Responses["closed"]), identity.CommitBodySHA256, strings.Repeat("a", 64), 1))
		default:
			w.Write(fixture.Responses[mode])
		}
	}))
	defer server.Close()
	client, err := NewDeviceClient(server.URL, installation, key, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	authorize := func(context.Context, TypedResultIdentity, string, string) error { return nil }
	if _, err = client.ReserveTypedResult(ctx, prepared, nil, authorize); err != nil {
		t.Fatal(err)
	}
	mode = "staged"
	if _, err = client.StageTypedResultPage(ctx, prepared, 0, nil, authorize); err != nil {
		t.Fatal(err)
	}
	mode = "commit"
	receipt, err := client.CommitTypedResult(ctx, prepared, nil, authorize)
	if err != nil {
		t.Fatal(err)
	}
	mode = "lookup"
	if _, err = client.LookupTypedResultReceipt(ctx, identity, receipt.Receipt); err != nil {
		t.Fatal(err)
	}
	mode = "accepted"
	if _, err = client.CloseTypedResult(ctx, identity, receipt.Receipt); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"closed", "indeterminate"} {
		mode = s
		out, e := client.CloseTypedResult(ctx, identity, nil)
		if e != nil || out.Status != s {
			t.Fatalf("%s: %+v %v", s, out, e)
		}
	}
	for _, s := range []string{"missing", "badclosed"} {
		mode = s
		if _, e := client.CloseTypedResult(ctx, identity, nil); !errors.Is(e, ErrTypedResultUncertain) {
			t.Fatalf("accepted %s", s)
		}
	}
	mode = "commitbad"
	if _, e := client.CommitTypedResult(ctx, prepared, receipt.Receipt, authorize); e == nil {
		t.Fatal("accepted mismatched receipt")
	}
	altered := *receipt.Receipt
	altered.ID = "55555555-5555-4555-8555-555555555556"
	mode = "accepted"
	if _, e := client.CloseTypedResult(ctx, identity, &altered); e == nil {
		t.Fatal("accepted inconsistent receipt")
	}
}
func TestTypedResultGraphBounds(t *testing.T) {
	cmd, installation, _, _ := typedResultFixture(t)
	for _, change := range []func(*TypedResultCommand){
		func(c *TypedResultCommand) { c.WorkID = "../bad" },
		func(c *TypedResultCommand) { c.Pages = append(c.Pages, c.Pages[0]) },
		func(c *TypedResultCommand) {
			c.Pages[0] = []byte(strings.Replace(string(c.Pages[0]), `"index":0`, `"index":1`, 1))
		},
		func(c *TypedResultCommand) {
			c.Commit = []byte(strings.Replace(string(c.Commit), `"byte_count":179`, `"byte_count":180`, 1))
		},
		func(c *TypedResultCommand) { c.Reservation = append(c.Reservation, make([]byte, 32769)...) },
	} {
		c := cmd
		c.Pages = append([][]byte(nil), cmd.Pages...)
		change(&c)
		if _, e := c.identity(installation); e == nil {
			t.Fatal("accepted invalid graph")
		}
	}
}

func TestTypedResultPreparationOwnsBytesAndRequiresAuthorization(t *testing.T) {
	cmd, installation, _, _ := typedResultFixture(t)
	prepared, err := PrepareTypedResult(cmd, installation)
	if err != nil {
		t.Fatal(err)
	}
	original := string(prepared.pages[0])
	cmd.Pages[0][0] ^= 1
	cmd.Reservation[0] ^= 1
	cmd.Commit[0] ^= 1
	if string(prepared.pages[0]) != original {
		t.Fatal("prepared bytes changed")
	}
	id := prepared.Identity()
	id.PageSHA256[0] = "changed"
	if prepared.Identity().PageSHA256[0] == "changed" {
		t.Fatal("identity mutated")
	}
	_, key, _ := ed25519.GenerateKey(nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unauthorized dispatch") }))
	defer server.Close()
	client, err := NewDeviceClient(server.URL, installation, key, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReserveTypedResult(context.Background(), prepared, nil, nil); !errors.Is(err, ErrTypedResult) {
		t.Fatalf("missing authorization: %v", err)
	}
}

func TestTypedReceiptStoredPrecision(t *testing.T) {
	cmd, installation, _, _ := typedResultFixture(t)
	prepared, err := PrepareTypedResult(cmd, installation)
	if err != nil {
		t.Fatal(err)
	}
	id := prepared.Identity()
	now := time.Date(2026, 11, 16, 1, 5, 0, 123456789, time.UTC)
	first := Receipt{ID: "55555555-5555-4555-8555-555555555555", WorkID: id.WorkID, InstallationID: id.InstallationID, IdempotencyKey: id.UploadID, PayloadSHA256: id.PayloadSHA256, SubmissionSHA256: id.CommitBodySHA256, ResultRevision: 1, AcceptedAt: now, Outcome: "completed", SourceBindingID: id.SourceBindingID, AssignmentSHA256: id.AssignmentSHA256, InventoryRevision: id.InventoryRevision, CatalogRevision: id.CatalogRevision, PublicationState: "published"}
	replay := first
	replay.AcceptedAt = now.Truncate(time.Microsecond)
	if !id.validReceipt(&first, nil) || !id.validReceipt(&replay, &first) {
		t.Fatal("valid first and persisted replay rejected")
	}
	for _, state := range []string{"withheld", "superseded"} {
		replay.PublicationState = state
		if !id.validReceipt(&replay, &first) {
			t.Fatalf("lost accepted identity after publication became %s", state)
		}
	}
	replay.PublicationState = "staged"
	if id.validReceipt(&replay, &first) {
		t.Fatal("accepted staged visibility without pending projection")
	}
	replay.PublicationState = "published"
	replay.SourceBindingID = ""
	if id.validReceipt(&replay, nil) {
		t.Fatal("missing binding accepted")
	}
}

func TestTypedCloseBeforeReserveAndExpiryPrecision(t *testing.T) {
	cmd, installation, _, _ := typedResultFixture(t)
	prepared, err := PrepareTypedResult(cmd, installation)
	if err != nil {
		t.Fatal(err)
	}
	id := prepared.Identity()
	id.ReservationExpiresAt = "2026-11-16T02:00:00.123456789Z"
	_, key, _ := ed25519.GenerateKey(nil)
	closed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !closed {
			io.WriteString(w, `{"status":"indeterminate"}`)
			return
		}
		json.NewEncoder(w).Encode(TypedResultResolution{Status: "closed", UploadID: id.UploadID, ReservationSHA256: id.ReservationSHA256, CommitBodySHA256: id.CommitBodySHA256, ReservationExpiresAt: "2026-11-16T02:00:00.123456Z", RetainUntil: "2026-11-23T02:00:00.123456Z"})
	}))
	defer server.Close()
	client, err := NewDeviceClient(server.URL, installation, key, true)
	if err != nil {
		t.Fatal(err)
	}
	out, err := client.CloseTypedResult(context.Background(), id, nil)
	if err != nil || out.Status != "indeterminate" {
		t.Fatalf("before reserve: %+v %v", out, err)
	}
	closed = true
	out, err = client.CloseTypedResult(context.Background(), id, nil)
	if err != nil || out.Status != "closed" {
		t.Fatalf("stored expiry: %+v %v", out, err)
	}
}

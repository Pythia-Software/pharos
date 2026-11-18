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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

func TestTypedBindingSignedTransport(t *testing.T) {
	var f struct {
		Binding    sourcewire.TypedBindingEnvelope `json:"binding"`
		Assignment sourcewire.TypedAssignmentPage  `json:"assignment_page"`
	}
	data, err := os.ReadFile("../../testdata/source-profile-v1.json")
	if err != nil || json.Unmarshal(data, &f) != nil {
		t.Fatal("fixture", err)
	}
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	const org = "11111111-1111-4111-8111-111111111111"
	const person = "22222222-2222-4222-8222-222222222222"
	b := f.Binding
	req := sourcewire.TypedBindingRequest{SnapshotID: b.SnapshotID, InventoryRevision: 1, InventorySHA256: b.InventorySHA256, LocalRevision: b.LocalRevision, CatalogRevision: b.LocalRevision, EvaluationRevision: b.EvaluationRevision, ConfigSHA256: b.ConfigSHA256, OwnershipRevision: b.OwnershipRevision, PrivacyRevision: b.PrivacyRevision, IssuedAt: time.Now().Unix()}
	req.BindingID, err = sourcewire.TypedBindingID(org, person, b.InstallationID, b.LibraryID, b.WorkID, req)
	if err != nil {
		t.Fatal(err)
	}
	b.BindingID = req.BindingID
	b.CatalogRevision = req.CatalogRevision
	page := f.Assignment
	page.BindingSHA256 = strings.Repeat("0", 64)
	page.PageCount = 2
	_, h, err := page.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	second := page
	second.Index = 1
	second.PreviousPageSHA256 = &h
	second.Items = []sourcewire.TypedAssignment{}
	_, last, err := second.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	b.Profiles[0].AssignmentPageCount = 2
	b.Profiles[0].AssignmentSHA256 = last
	_, hash, err := b.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	page.BindingSHA256 = hash
	second.BindingSHA256 = hash
	gen := int64(1)
	until := time.Now().UTC().Add(time.Hour)
	floor := TypedBindingFloor{ServerTime: time.Unix(req.IssuedAt, 0).UTC(), NextIssuedAt: req.IssuedAt}
	receipt := TypedBindingReceipt{Status: "issued", Binding: &b, BindingSHA256: hash, EligibilityGeneration: &gen, ValidUntil: &until}
	mode := "ok"
	seen := map[string]bool{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		nonce := r.Header.Get("X-Pharos-Nonce")
		timestamp, tsErr := strconv.ParseInt(r.Header.Get("X-Pharos-Timestamp"), 10, 64)
		nonceBytes, nonceErr := base64.RawURLEncoding.DecodeString(nonce)
		msg, e := signedMessage(b.InstallationID, r.Method, r.URL.EscapedPath(), r.Header.Get("X-Pharos-Timestamp"), nonce, body)
		sig, e2 := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Pharos-Signature"))
		if e != nil || e2 != nil || tsErr != nil || time.Since(time.Unix(timestamp, 0)).Abs() > time.Minute || nonceErr != nil || len(nonceBytes) != 16 || seen[nonce] || r.Header.Get("X-Pharos-Installation") != b.InstallationID || !ed25519.Verify(pub, []byte(msg), sig) {
			t.Error("invalid signed request")
		}
		seen[nonce] = true
		base := "/v1/device/work/" + b.WorkID + "/typed-source-bindings"
		switch {
		case r.Method == "GET" && r.URL.Path == base+"/floor":
			json.NewEncoder(w).Encode(floor)
		case r.Method == "POST" && r.URL.Path == base:
			var got sourcewire.TypedBindingRequest
			if json.Unmarshal(body, &got) != nil || got != req {
				t.Error("wrong request")
			}
			switch mode {
			case "old":
				http.NotFound(w, r)
			case "superseded":
				next := floor
				next.NextIssuedAt++
				json.NewEncoder(w).Encode(TypedBindingReceipt{Status: "superseded", Floor: &next})
			case "multiple-profiles":
				extra := b.Profiles[0]
				extra.ProfileID = "future_usage"
				v := b
				v.Profiles = append(append([]sourcewire.ProfileReference{}, b.Profiles...), extra)
				_, hash, e := v.Bytes()
				if e != nil {
					t.Fatal(e)
				}
				result := receipt
				result.Binding, result.BindingSHA256 = &v, hash
				json.NewEncoder(w).Encode(result)
			case "stale":
				x := receipt
				x.Status = "stale"
				json.NewEncoder(w).Encode(x)
			case "work":
				x := *receipt.Binding
				x.WorkID = org
				_, xhash, _ := x.Bytes()
				v := receipt
				v.Binding = &x
				v.BindingSHA256 = xhash
				json.NewEncoder(w).Encode(v)
			case "nonadvancing":
				bad := floor
				bad.ServerTime = time.Unix(req.IssuedAt, 0).UTC()
				bad.NextIssuedAt = req.IssuedAt
				json.NewEncoder(w).Encode(TypedBindingReceipt{Status: "superseded", Floor: &bad})
			case "early-expiry":
				v := receipt
				expiry := time.Unix(req.IssuedAt, 0).Add(-sourcewire.RecoveryFutureSkew)
				v.ValidUntil = &expiry
				json.NewEncoder(w).Encode(v)
			case "expired":
				v := receipt
				expiry := time.Now().Add(-time.Second)
				v.ValidUntil = &expiry
				json.NewEncoder(w).Encode(v)
			case "digest":
				v := receipt
				v.BindingSHA256 = strings.Repeat("a", 64)
				json.NewEncoder(w).Encode(v)
			default:
				json.NewEncoder(w).Encode(receipt)
			}
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, base+"/"+b.BindingID+"/profiles/"+b.Profiles[0].ProfileID+"/1/assignments/"):
			p := page
			if strings.HasSuffix(r.URL.Path, "/1") {
				if mode == "incomplete" {
					http.NotFound(w, r)
					return
				}
				p = second
				if mode == "page" {
					wrong := strings.Repeat("a", 64)
					p.PreviousPageSHA256 = &wrong
				}
				if mode == "page-digest" {
					p.BindingSHA256 = strings.Repeat("a", 64)
				}
				if mode == "page-index" {
					p.Index = 0
				}
			}
			json.NewEncoder(w).Encode(p)
		default:
			t.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	origin := "http://127.0.0.1"
	var server *httptest.Server
	if os.Getenv("PHAROS_TYPED_IN_MEMORY") == "" {
		server = httptest.NewServer(handler)
		defer server.Close()
		origin = server.URL
	}
	c, err := NewDeviceClient(origin, b.InstallationID, key, true)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Destroy()
	if server == nil {
		c.http.Transport = typedTestTransport{handler}
	}
	ctx := context.Background()
	verified := func() (VerifiedTypedBinding, error) {
		return c.IssueVerifiedTypedBinding(ctx, org, person, b.LibraryID, b.WorkID, b.WindowSHA256, req)
	}
	if (VerifiedTypedBinding{}).Unexpired(time.Now()) {
		t.Fatal("zero result is valid")
	}
	v, err := verified()
	if err != nil || !v.Unexpired(time.Now()) || v.Unexpired(until) {
		t.Fatalf("verified issuance: %v", err)
	}
	credential := &EnrollmentCredential{Origin: origin, InstallationID: b.InstallationID, OrgID: org, PersonID: person, LibraryID: b.LibraryID, key: key}
	if !v.MatchesCredential(credential) || (VerifiedTypedBinding{}).MatchesCredential(credential) {
		t.Fatal("verified binding lost its credential scope")
	}
	credential.Origin = origin + "/"
	if !c.MatchesCredential(credential) || !v.MatchesCredential(credential) {
		t.Fatal("equivalent stored origin did not match")
	}
	credential.Origin = origin
	_, otherKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(otherKey)
	for _, altered := range []EnrollmentCredential{
		{Origin: origin, InstallationID: b.InstallationID, OrgID: org, PersonID: person, LibraryID: b.LibraryID, key: otherKey},
		{Origin: origin, InstallationID: org, OrgID: org, PersonID: person, LibraryID: b.LibraryID, key: key},
		{Origin: origin, InstallationID: b.InstallationID, OrgID: person, PersonID: person, LibraryID: b.LibraryID, key: key},
		{Origin: origin, InstallationID: b.InstallationID, OrgID: org, PersonID: person, LibraryID: "other", key: key},
		{Origin: "https://other.invalid", InstallationID: b.InstallationID, OrgID: org, PersonID: person, LibraryID: b.LibraryID, key: key},
		{Origin: origin, InstallationID: b.InstallationID, OrgID: org, PersonID: org, LibraryID: b.LibraryID, key: key},
		{Origin: origin, InstallationID: b.InstallationID, OrgID: org, PersonID: person, LibraryID: b.LibraryID},
	} {
		if v.MatchesCredential(&altered) {
			t.Fatal("accepted another credential scope")
		}
	}
	requestBytes, _ := json.Marshal(req)
	if string(v.RequestBytes()) != string(requestBytes) || v.Receipt().BindingSHA256 != hash || len(v.Pages()) != 1 || len(v.Pages()[0]) != 2 {
		t.Fatal("verified result lost issuance or pages")
	}
	copyRequest, copyReceipt, copyPages := v.RequestBytes(), v.Receipt(), v.Pages()
	copyRequest[0] = 'x'
	copyReceipt.Binding.Profiles[0].ProfileID = "changed"
	copyPages[0][0].Items[0].Decision = "changed"
	if string(v.RequestBytes()) != string(requestBytes) || v.Receipt().Binding.Profiles[0].ProfileID != b.Profiles[0].ProfileID || v.Pages()[0][0].Items[0].Decision != page.Items[0].Decision {
		t.Fatal("accessor changed verified result")
	}
	for _, m := range []string{"incomplete", "page", "expired", "stale", "superseded"} {
		mode = m
		if result, e := verified(); e == nil || result.Unexpired(time.Now()) {
			t.Fatalf("accepted %s", m)
		}
	}
	for modeName, want := range map[string]error{"expired": ErrTypedBindingExpired, "stale": ErrTypedBindingStale, "page": ErrTypedBinding, "multiple-profiles": ErrTypedBindingUnsupported} {
		mode = modeName
		if _, e := verified(); !errors.Is(e, want) {
			t.Fatalf("%s: %v", mode, e)
		}
	}
	mode = "superseded"
	var supersededErr *TypedBindingSuperseded
	if _, e := verified(); !errors.As(e, &supersededErr) || supersededErr.Floor.NextIssuedAt != req.IssuedAt+1 {
		t.Fatalf("superseded floor: %v", e)
	}
	mode = "ok"
	belowFloor := floor
	belowFloor.NextIssuedAt++
	floor = belowFloor
	if result, e := verified(); e != nil || !result.Unexpired(time.Now()) {
		t.Fatalf("exact receipt replay after the issuance floor advanced: %v", e)
	}
	floor.NextIssuedAt--
	wrongRequest := req
	wrongRequest.InventoryRevision++
	if result, e := c.IssueVerifiedTypedBinding(ctx, org, person, b.LibraryID, b.WorkID, b.WindowSHA256, wrongRequest); e == nil || result.Unexpired(time.Now()) {
		t.Fatal("accepted request with mismatched binding identity")
	}
	if _, err = c.TypedBindingFloor(ctx, b.WorkID); err != nil {
		t.Fatal(err)
	}
	issue := func() (TypedBindingReceipt, error) {
		return c.IssueTypedBinding(ctx, org, person, b.LibraryID, b.WorkID, b.WindowSHA256, req)
	}
	got, err := issue()
	if err != nil {
		t.Fatal(err)
	}
	if pages, e := c.TypedAssignments(ctx, b.WorkID, got, b.Profiles[0].ProfileID, 1); e != nil || len(pages) != 2 {
		t.Fatal("valid chain", e)
	}
	for _, m := range []string{"page", "page-digest", "page-index"} {
		mode = m
		if _, e := c.TypedAssignments(ctx, b.WorkID, got, b.Profiles[0].ProfileID, 1); e == nil {
			t.Fatalf("accepted %s", m)
		}
	}
	for _, m := range []string{"work", "digest", "old", "nonadvancing", "early-expiry"} {
		mode = m
		if _, e := issue(); e == nil {
			t.Fatalf("accepted %s", m)
		}
	}
	mode = "stale"
	stale, e := issue()
	if e != nil || stale.Status != "stale" {
		t.Fatal("stale", e)
	}
	if _, e = c.TypedAssignments(ctx, b.WorkID, stale, b.Profiles[0].ProfileID, 1); e == nil {
		t.Fatal("stale assignments")
	}
	mode = "superseded"
	superseded, e := issue()
	if e != nil || superseded.Floor == nil {
		t.Fatal("superseded", e)
	}
	if _, e = c.TypedAssignments(ctx, org, got, b.Profiles[0].ProfileID, 1); e == nil {
		t.Fatal("wrong work")
	}
}

func TestTypedResponseRequiresExactNestedFields(t *testing.T) {
	var f struct {
		Assignment sourcewire.TypedAssignmentPage `json:"assignment_page"`
	}
	data, err := os.ReadFile("../../testdata/source-profile-v1.json")
	if err != nil || json.Unmarshal(data, &f) != nil {
		t.Fatal("fixture", err)
	}
	raw, err := json.Marshal(f.Assignment)
	if err != nil {
		t.Fatal(err)
	}
	var page sourcewire.TypedAssignmentPage
	if !exactTypedJSON(raw, &page) {
		t.Fatal("valid page rejected")
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"required":true,`, "", 1),
		strings.Replace(string(raw), `"required":true`, `"required":true,"extra":0`, 1),
		strings.Replace(string(raw), `"required":true`, `"required":true,"required":true`, 1),
	} {
		if bad == string(raw) || exactTypedJSON([]byte(bad), &page) {
			t.Fatal("accepted malformed page", bad)
		}
	}
}

// The in-memory option exercises the same HTTP handler where loopback is sandboxed.
type typedTestTransport struct{ handler http.Handler }

func (t typedTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func TestTypedFloorBindsRetryTime(t *testing.T) {
	now := time.Unix(1794790800, 300).UTC()
	floor := TypedBindingFloor{ServerTime: now, NextIssuedAt: now.Add(sourcewire.RecoveryFutureSkew).Unix()}
	if !validTypedFloor(&floor) {
		t.Fatal("available floor rejected")
	}
	floor.RetryAt = &now
	if validTypedFloor(&floor) {
		t.Fatal("accepted an unnecessary retry delay")
	}
	floor.NextIssuedAt++
	floor.RetryAt = nil
	if validTypedFloor(&floor) {
		t.Fatal("accepted missing retry delay")
	}
	expected := time.Unix(floor.NextIssuedAt, 0).Add(-sourcewire.RecoveryFutureSkew)
	floor.RetryAt = &expected
	if !validTypedFloor(&floor) {
		t.Fatal("valid retry delay rejected")
	}
	different := expected.Add(time.Second)
	floor.RetryAt = &different
	if validTypedFloor(&floor) {
		t.Fatal("accepted an unrelated retry time")
	}
}

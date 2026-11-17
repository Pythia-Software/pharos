package teams

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSignedProfilePageAndSeal(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/source-profile-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		PageJSON string `json:"page_json"`
		PageSHA  string `json:"page_sha256"`
		SealJSON string `json:"seal_json"`
		SealSHA  string `json:"seal_sha256"`
	}
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	device := "44444444-4444-4444-8444-444444444444"
	work := "33333333-3333-4333-8333-333333333333"
	snapshot := "11111111-1111-4111-8111-111111111111"
	held := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		msg, e := signedMessage(device, r.Method, r.URL.EscapedPath(), r.Header.Get("X-Pharos-Timestamp"), r.Header.Get("X-Pharos-Nonce"), body)
		sig, e2 := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Pharos-Signature"))
		if e != nil || e2 != nil || !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte(msg), sig) {
			t.Error("profile signature invalid")
		}
		if strings.HasSuffix(r.URL.Path, "/pages/0") {
			if string(body) != f.PageJSON {
				t.Error("page bytes changed")
			}
			status := "accepted"
			if held {
				status = "held"
			}
			json.NewEncoder(w).Encode(ProfilePageReceipt{Status: status, SnapshotID: snapshot, ProfileID: "codex_request_usage", ProfileVersion: 1, ProfileRevision: 1, Index: 0, PageSHA256: f.PageSHA})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/seal") {
			if string(body) != f.SealJSON {
				t.Error("seal bytes changed")
			}
			if held {
				json.NewEncoder(w).Encode(ProfileSealReceipt{Status: "held", SnapshotID: snapshot, ProfileID: "codex_request_usage", ProfileVersion: 1, ProfileRevision: 1})
				return
			}
			json.NewEncoder(w).Encode(ProfileSealReceipt{Status: "accepted", SnapshotID: snapshot, ProfileID: "codex_request_usage", ProfileVersion: 1, ProfileRevision: 1, SealSHA256: f.SealSHA, Complete: true, ItemCount: 1})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	c, err := NewDeviceClient(server.URL, device, key, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.StageSourceProfilePage(context.Background(), work, []byte(f.PageJSON)); err != nil {
		t.Fatal(err)
	}
	if _, err = c.SealSourceProfile(context.Background(), work, []byte(f.SealJSON)); err != nil {
		t.Fatal(err)
	}
	held = true
	page, err := c.StageSourceProfilePage(context.Background(), work, []byte(f.PageJSON))
	if err != nil || page.Status != "held" {
		t.Fatalf("held page: %+v %v", page, err)
	}
	seal, err := c.SealSourceProfile(context.Background(), work, []byte(f.SealJSON))
	if err != nil || seal.Status != "held" || seal.SealSHA256 != "" {
		t.Fatalf("held seal: %+v %v", seal, err)
	}
}

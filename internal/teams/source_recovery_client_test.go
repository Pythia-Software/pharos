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

	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

func TestSignedSourceRecoveryRemainsProofRequired(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	const installation = "00000000-0000-4000-8000-000000000003"
	const run = "11111111-1111-4111-8111-111111111111"
	tamper := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		msg, e := signedMessage(installation, r.Method, r.URL.EscapedPath(), r.Header.Get("X-Pharos-Timestamp"), r.Header.Get("X-Pharos-Nonce"), raw)
		sig, e2 := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Pharos-Signature"))
		if e != nil || e2 != nil || !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte(msg), sig) {
			t.Error("invalid recovery signature")
		}
		switch r.URL.Path {
		case "/v1/device/libraries/synthetic/source-recovery/start":
			json.NewEncoder(w).Encode(sourcewire.RecoveryStartReceipt{Status: "proof_required", RunID: run, Generation: 2, ServerTime: 1794931200, ExpiresAt: 1794934800, ExternalProofRequired: true})
		case "/v1/device/libraries/synthetic/source-recovery/" + run + "/pages":
			family := "origins"
			if tamper {
				family = "guards"
			}
			json.NewEncoder(w).Encode(sourcewire.RecoveryPageReceipt{Status: "held", RunID: run, Generation: 2, ServerTime: 1794931200, Held: true, ExternalProofRequired: true, Family: family, Items: []json.RawMessage{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c, err := NewDeviceClient(server.URL, installation, key, true)
	if err != nil {
		t.Fatal(err)
	}
	start, _ := json.Marshal(sourcewire.RecoveryStart{Version: 1, Contract: sourcewire.SourceRecoveryContract, IssuedAt: 1794931200, StartID: run, Purpose: "protection"})
	receipt, err := c.StartSourceRecovery(context.Background(), "synthetic", start)
	if err != nil || !receipt.ExternalProofRequired {
		t.Fatalf("start: %+v %v", receipt, err)
	}
	page, _ := json.Marshal(sourcewire.RecoveryPageRequest{Version: 1, Contract: sourcewire.SourceRecoveryContract, IssuedAt: 1794931200, RunID: run, Family: "origins"})
	p, err := c.PageSourceRecovery(context.Background(), "synthetic", page)
	if err != nil || !p.Held {
		t.Fatalf("page: %+v %v", p, err)
	}
	tamper = true
	if _, err = c.PageSourceRecovery(context.Background(), "synthetic", page); err == nil {
		t.Fatal("wrong family accepted")
	}
}

func TestSignedSourceRecoveryStageReceipt(t *testing.T) {
	data, err := os.ReadFile("testdata/source-recovery-stage-profile-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Chunk0JSON string `json:"chunk0_json"`
		Chunk0SHA  string `json:"chunk0_sha256"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	page, err := sourcewire.DecodeRecoveryStagePage([]byte(fixture.Chunk0JSON))
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	const installation = "00000000-0000-4000-8000-000000000003"
	tamper := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		message, e := signedMessage(installation, r.Method, r.URL.EscapedPath(), r.Header.Get("X-Pharos-Timestamp"), r.Header.Get("X-Pharos-Nonce"), raw)
		sig, e2 := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Pharos-Signature"))
		if e != nil || e2 != nil || !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte(message), sig) || r.URL.Path != "/v1/device/libraries/synthetic/source-recovery/"+page.RunID+"/stage" || string(raw) != fixture.Chunk0JSON {
			t.Error("stage request lost signature, path or original bytes")
		}
		hash := fixture.Chunk0SHA
		if tamper {
			hash = strings.Repeat("0", 64)
		}
		_ = json.NewEncoder(w).Encode(sourcewire.RecoveryStageReceipt{Status: "accepted", CommandID: page.CommandID, RunID: page.RunID, PageSHA256: hash})
	}))
	defer server.Close()
	client, err := NewDeviceClient(server.URL, installation, key, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.StageSourceRecovery(context.Background(), "synthetic", []byte(fixture.Chunk0JSON)); err != nil {
		t.Fatal(err)
	}
	tamper = true
	if _, err = client.StageSourceRecovery(context.Background(), "synthetic", []byte(fixture.Chunk0JSON)); err == nil {
		t.Fatal("wrong stage receipt accepted")
	}
}

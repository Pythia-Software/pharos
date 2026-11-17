package teams

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestDeviceSigningVector(t *testing.T) {
	b, err := os.ReadFile("testdata/device-signing-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Body      string `json:"body"`
		ID        string `json:"installation_id"`
		Method    string `json:"method"`
		Nonce     string `json:"nonce"`
		Seed      string `json:"seed_hex"`
		Signature string `json:"signature"`
		Message   string `json:"signing_message"`
		Target    string `json:"target"`
		Timestamp string `json:"timestamp"`
	}
	if err = json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	seed, err := hex.DecodeString(v.Seed)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := signedMessage(v.ID, v.Method, v.Target, v.Timestamp, v.Nonce, []byte(v.Body))
	if err != nil || msg != v.Message {
		t.Fatalf("message %q: %v", msg, err)
	}
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(seed), []byte(msg)))
	if sig != v.Signature {
		t.Fatalf("signature %s", sig)
	}
	for _, target := range []string{"/v1/device/poll?q=1", "/v1/device/poll?", "https://example.com/v1/device/poll", "//elsewhere/v1/device/poll", "/v1/device/poll#fragment"} {
		if _, err = signedMessage(v.ID, v.Method, target, v.Timestamp, v.Nonce, nil); err == nil {
			t.Errorf("accepted target %q", target)
		}
	}
}

func TestDeviceTransportSignsAndDoesNotRedirect(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	nonces := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		nonce := r.Header.Get("X-Pharos-Nonce")
		if nonces[nonce] {
			t.Error("reused nonce")
		}
		nonces[nonce] = true
		msg, e := signedMessage(r.Header.Get("X-Pharos-Installation"), r.Method, r.URL.EscapedPath(), r.Header.Get("X-Pharos-Timestamp"), nonce, body)
		sig, e2 := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Pharos-Signature"))
		if e != nil || e2 != nil || !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte(msg), sig) {
			t.Error("invalid signature")
		}
		http.Redirect(w, r, "/v1/device/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := NewDeviceClient(server.URL, "a4c09e42-d0a6-45f0-89ef-2a03b7cff630", key, true)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		resp, e := client.Request(context.Background(), "POST", "/v1/device/poll", []byte(`{"test":true}`))
		if e == nil || resp != nil {
			t.Fatalf("redirect was not refused: %v %v", resp, e)
		}
	}
	if calls != 2 || len(nonces) != 2 {
		t.Fatalf("calls %d nonces %d", calls, len(nonces))
	}
	if _, err = NewDeviceClient(strings.Replace(server.URL, "127.0.0.1", "example.com", 1), "a4c09e42-d0a6-45f0-89ef-2a03b7cff630", key, true); err == nil {
		t.Fatal("accepted insecure remote origin")
	}
	if _, err = NewDeviceClient(server.URL, "a4c09e42-d0a6-45f0-89ef-2a03b7cff630", key, false); err == nil {
		t.Fatal("accepted implicit insecure loopback")
	}
}

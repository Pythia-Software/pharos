package teams

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEnrollmentFingerprintVector(t *testing.T) {
	b, err := os.ReadFile("testdata/device-enrollment-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Seed          string `json:"seed_hex"`
		StartResponse struct {
			ID string `json:"enrollment_id"`
		} `json:"start_response"`
		AuthorizeRequest struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"authorize_request"`
		RedeemRequest struct {
			Method, Target, Body string
			Headers              map[string]string
			Message              string `json:"signing_message"`
		} `json:"redeem_request"`
	}
	if json.Unmarshal(b, &v) != nil {
		t.Fatal("invalid vector")
	}
	seed, err := hex.DecodeString(v.Seed)
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(seed)
	fp, err := enrollmentFingerprint(key.Public().(ed25519.PublicKey), v.StartResponse.ID)
	if err != nil || fp != v.AuthorizeRequest.Fingerprint {
		t.Fatalf("fingerprint %q %v", fp, err)
	}
	msg, err := signedMessage(v.StartResponse.ID, v.RedeemRequest.Method, v.RedeemRequest.Target, v.RedeemRequest.Headers["X-Pharos-Timestamp"], v.RedeemRequest.Headers["X-Pharos-Nonce"], []byte(v.RedeemRequest.Body))
	if err != nil || msg != v.RedeemRequest.Message {
		t.Fatalf("message mismatch %v", err)
	}
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(msg)))
	if sig != v.RedeemRequest.Headers["X-Pharos-Signature"] {
		t.Fatal("signature mismatch")
	}
}

func TestEnrollmentLoopbackAndRetry(t *testing.T) {
	const id = "a4c09e42-d0a6-45f0-89ef-2a03b7cff630"
	var start enrollmentStart
	var base string
	nonces := map[string]bool{}
	attempts := 0
	secret := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/device/enrollments" {
			if json.NewDecoder(r.Body).Decode(&start) != nil {
				t.Error("invalid start")
			}
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(enrollmentStarted{ID: id, ApprovalURL: base + "/enroll/" + id + "#confirmation=" + secret, ExpiresAt: time.Now().Add(time.Minute)})
			return
		}
		if r.URL.Path != "/v1/device/enrollments/"+id+"/redeem" {
			http.NotFound(w, r)
			return
		}
		attempts++
		body, _ := io.ReadAll(r.Body)
		nonce := r.Header.Get("X-Pharos-Nonce")
		if nonces[nonce] {
			t.Error("reused nonce")
		}
		nonces[nonce] = true
		msg, e := signedMessage(id, r.Method, r.URL.EscapedPath(), r.Header.Get("X-Pharos-Timestamp"), nonce, body)
		sig, e2 := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Pharos-Signature"))
		pub, e3 := base64.RawURLEncoding.DecodeString(start.PublicKey)
		if e != nil || e2 != nil || e3 != nil || !ed25519.Verify(pub, []byte(msg), sig) {
			t.Error("invalid redemption signature")
		}
		if attempts == 1 {
			h := w.(http.Hijacker)
			conn, _, _ := h.Hijack()
			conn.Close()
			return
		}
		json.NewEncoder(w).Encode(enrollmentRedeemed{OrgID: "00000000-0000-4000-8000-000000000001", PersonID: "00000000-0000-4000-8000-000000000002", InstallationID: "00000000-0000-4000-8000-000000000003", LibraryID: start.LibraryID, SourceTokenKey: secret, MetricsDefault: MetricsDefault{Value: false, PolicyVersion: 1}})
	}))
	defer srv.Close()
	base = srv.URL
	a, err := BeginEnrollment(context.Background(), srv.URL, "synthetic-library", "Synthetic library", "1", true)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !strings.Contains(a.ApprovalURL, "#confirmation=") || len(a.Fingerprint) != 10 {
		t.Fatal("missing approval or fingerprint")
	}
	callback := "http://" + a.CallbackAddress() + "/teams/enrollment?"
	bad := url.Values{"state": {"wrong"}, "code": {secret}}
	r, err := http.Get(callback + bad.Encode())
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 400 {
		t.Fatalf("wrong state: %d", r.StatusCode)
	}
	good := url.Values{"state": {start.CallbackState}, "code": {secret}}
	r, err = http.Get(callback + good.Encode())
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("callback: %d", r.StatusCode)
	}
	if _, err = a.Redeem(context.Background()); err == nil {
		t.Fatal("lost response appeared successful")
	}
	credential, err := a.Redeem(context.Background())
	if err != nil || credential.LibraryID != "synthetic-library" || len(credential.SigningKey()) != ed25519.PrivateKeySize || len(credential.SourceKey()) != 32 {
		t.Fatalf("retry: %v", err)
	}
	if attempts != 2 || len(nonces) != 2 {
		t.Fatal(fmt.Sprintf("attempts %d nonces %d", attempts, len(nonces)))
	}
	if _, err = a.Redeem(context.Background()); err == nil {
		t.Fatal("completed attempt reused")
	}
}

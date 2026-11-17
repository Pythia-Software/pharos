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
	"testing"

	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

func TestStageSourcePageBindsExactSignedBytes(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/teams/source-snapshot-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Page string `json:"page_json"`
	}
	if json.Unmarshal(fixture, &v) != nil {
		t.Fatal("fixture")
	}
	page, err := sourcewire.DecodePage([]byte(v.Page))
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	id := "a4c09e42-d0a6-45f0-89ef-2a03b7cff630"
	badReceipt := false
	calls := 0
	var firstNonce string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if string(raw) != v.Page || r.Method != http.MethodPut || r.URL.EscapedPath() != "/v1/device/work/"+id+"/source-snapshots/"+page.SnapshotID+"/pages/0" {
			t.Error("unexpected page request")
		}
		nonce := r.Header.Get("X-Pharos-Nonce")
		if firstNonce == "" {
			firstNonce = nonce
		} else if nonce == firstNonce {
			t.Error("reused nonce")
		}
		message, e := signedMessage(id, r.Method, r.URL.EscapedPath(), r.Header.Get("X-Pharos-Timestamp"), nonce, raw)
		signature, e2 := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Pharos-Signature"))
		if e != nil || e2 != nil || !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte(message), signature) {
			t.Error("invalid signature")
		}
		hash := sourcewire.SHA256(raw)
		if badReceipt {
			hash = sourcewire.SHA256([]byte("different"))
		}
		json.NewEncoder(w).Encode(SourcePageReceipt{SnapshotID: page.SnapshotID, Index: page.Index, PageSHA256: hash})
	}))
	defer server.Close()
	c, err := NewDeviceClient(server.URL, id, key, true)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Destroy()
	if _, err = c.StageSourcePage(context.Background(), id, []byte(v.Page)); err != nil {
		t.Fatal(err)
	}
	badReceipt = true
	if _, err = c.StageSourcePage(context.Background(), id, []byte(v.Page)); err == nil {
		t.Fatal("mismatched receipt accepted")
	}
	if _, err = c.StageSourcePage(context.Background(), id, []byte(" "+v.Page)); err == nil || calls != 2 {
		t.Fatal("noncanonical page sent")
	}
}

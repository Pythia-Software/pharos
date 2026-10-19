//go:build probe

// Serves the Library text search UI on synthetic fixtures for two minutes,
// with no capture or sync background jobs, and prints its URL. Probes build
// only with the probe tag, so they stay out of go test ./...:
//
//	PHAROS_SEARCH_PREVIEW=1 go test -tags probe -run TestTextSearchPreview -v ./internal/archive

package archive

import (
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestTextSearchPreview(t *testing.T) {
	if os.Getenv("PHAROS_SEARCH_PREVIEW") != "1" {
		t.Skip("set PHAROS_SEARCH_PREVIEW=1 for a two-minute synthetic UI preview")
	}
	catalog, config := libraryFixture(t)
	if _, e := catalog.DB.Exec(`INSERT INTO messages_fts(message_id,text) SELECT id,text FROM messages`); e != nil {
		t.Fatal(e)
	}
	if e := catalog.refreshAllLibrary(t.Context()); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(testServer(t, config, catalog))
	defer server.Close()
	fmt.Println("PREVIEW_URL=" + server.URL + "/library?token=test-token&search=parser&text_plain=1&text_surface=user")
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	select {
	case <-t.Context().Done():
	case <-timer.C:
	}
}

package archive

import (
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Opt-in UI preview uses synthetic fixtures and no capture/sync background jobs.
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

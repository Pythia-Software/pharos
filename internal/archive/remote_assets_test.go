package archive

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A web image a reply showed is fetched once after its conversation is
// indexed, served as a stored file from then on, and kept when the
// conversation is indexed again; an address that is not an image stays
// shown as not fetched.
func TestWebImagesAreFetchedOnceAndKept(t *testing.T) {
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 32)
	requests := map[string]int{}
	site := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests[r.URL.Path]++
		if r.URL.Path == "/lake.png" {
			w.Write([]byte(png))
			return
		}
		w.Write([]byte("<html>not an image</html>"))
	}))
	defer site.Close()
	override(t, &remoteAssetClient, site.Client())
	override(t, &remoteAssetsEnabled, true)
	override(t, &remoteAssetAttempts, 1)

	c, config := testCatalog(t)
	record := WorkspaceRecord{SourceKind: "chatgpt", SourceID: "swim", Account: "local", Title: "Swim", ActivityAt: "2026-10-04T12:00:00Z",
		Conversations: []ConversationRecord{{NativeID: "swim", Provider: "openai", Messages: []MessageRecord{{NativeID: "reply", Role: "assistant", Kind: "message", Text: "Lakes", Selected: true}},
			Assets: []AssetRecord{
				{MessageNativeID: "reply", Ref: "lake", Kind: "image", Name: "Lake", Relation: "web", Availability: assetRemote, Details: map[string]any{"url": site.URL + "/lake.png"}},
				{MessageNativeID: "reply", Ref: "page", Kind: "image", Name: "Page", Relation: "web", Availability: assetRemote, Details: map[string]any{"url": site.URL + "/page"}},
				{MessageNativeID: "reply", Ref: "plain", Kind: "image", Name: "Plain", Relation: "web", Availability: assetRemote, Details: map[string]any{"url": "http://plain.example/a.png"}},
			}}}}
	ingestRecords(t, c, record)
	ctx := context.Background()
	shown := func() map[string]map[string]any {
		t.Helper()
		var conversation string
		if err := c.reader().QueryRow("SELECT id FROM conversations").Scan(&conversation); err != nil {
			t.Fatal(err)
		}
		attach, err := c.messageAssetsAttacher(ctx, conversation)
		if err != nil {
			t.Fatal(err)
		}
		message := map[string]any{"id": stableID("message", conversation, "reply")}
		attach(message)
		found := map[string]map[string]any{}
		for _, asset := range message["assets"].([]map[string]any) {
			found[firstString(asset["name"])] = asset
		}
		return found
	}
	if before := shown(); before["Lake"]["availability"] != assetRemote || before["Lake"]["url"] != nil {
		t.Fatalf("before the fetch: %v", before)
	}
	// Passes made with no network reach no host, and use up no address's
	// one attempt: the images are fetched once there is a network.
	online := remoteAssetClient
	remoteAssetClient = &http.Client{Transport: markdownImageTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("no route to host") })}
	for range 2 {
		if fetched, err := c.fetchRemoteAssets(ctx); err != nil || fetched != 0 {
			t.Fatal(fetched, err)
		}
	}
	remoteAssetClient = online
	if len(requests) != 0 {
		t.Fatalf("requests made offline: %v", requests)
	}
	for range 2 {
		if _, err := c.fetchRemoteAssets(ctx); err != nil {
			t.Fatal(err)
		}
		// Indexing the conversation again keeps what was fetched.
		ingestRecords(t, c, record)
	}
	after := shown()
	if after["Lake"]["availability"] != assetStored || after["Page"]["availability"] != assetRemote || after["Plain"]["availability"] != assetRemote || requests["/lake.png"] != 1 || requests["/page"] != 1 {
		t.Fatalf("after the fetch: %v, requests %v", after, requests)
	}
	server := testServer(t, config, c)
	serve := func(asset map[string]any) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		server.asset(response, httptest.NewRequest("GET", "/api/assets/"+firstString(asset["id"]), nil))
		return response
	}
	if response := serve(after["Lake"]); response.Code != 200 || response.Body.String() != png || response.Header().Get("Content-Type") != "image/png" {
		t.Fatal(response.Code, response.Header())
	}
	if response := serve(after["Page"]); response.Code != 404 {
		t.Fatal("an address that is not an image was served", response.Code)
	}
}

// A widget is a piece of a page: it is served inside a page that supplies
// the theme its host did, cut off from the network, and downloads as written.
// A widget as a chat application's agent writes one: a piece of a page that
// asks a CDN for its chart library.
const widgetFixture = `<div style="color:var(--text-secondary)">100% drawn</div><canvas id="c"></canvas>
<script src="https://cdnjs.cloudflare.com/ajax/libs/Chart.js/4.4.1/chart.umd.js"></script>
<script>new Chart(document.getElementById('c'),{type:'bar',data:{labels:['a'],datasets:[{data:[1]}]}})</script>`

func TestWidgetIsServedInsideAHostPage(t *testing.T) {
	c, config := testCatalog(t)
	ingestRecords(t, c, WorkspaceRecord{SourceKind: "claude-ai", SourceID: "plots", Account: "local", Title: "Plots", ActivityAt: "2026-10-04T12:00:00Z",
		Conversations: []ConversationRecord{{NativeID: "plots", Provider: "claude", Messages: []MessageRecord{{NativeID: "reply", Role: "assistant", Kind: "message", Text: "Plots", Selected: true}},
			Assets: []AssetRecord{{MessageNativeID: "reply", Ref: "w", Kind: "document", Name: "plots.html", MimeType: "text/html", Relation: "generated", AgentAuthored: true, Availability: assetStored,
				Content: widgetFixture, Details: map[string]any{"fragment": true}}}}}})
	var id string
	if err := c.reader().QueryRow("SELECT id FROM message_assets").Scan(&id); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, c)
	serve := func(query string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		server.asset(response, httptest.NewRequest("GET", "/api/assets/"+id+query, nil))
		return response
	}
	dark, light := serve(""), serve("?theme=light")
	for _, response := range []*httptest.ResponseRecorder{dark, light} {
		if body := response.Body.String(); response.Code != 200 || !strings.Contains(body, "--text-secondary:") || !strings.Contains(body, "100% drawn</div>") || strings.Contains(body, "cdnjs") || !strings.Contains(body, "new Chart(") || len(body) < 100_000 || !strings.Contains(response.Header().Get("Content-Security-Policy"), "default-src 'none'") {
			t.Fatal(response.Code, response.Header(), body)
		}
	}
	if dark.Body.String() == light.Body.String() {
		t.Fatal("the theme asked for made no difference")
	}
	if response := serve("?download=1"); response.Body.String() != widgetFixture {
		t.Fatal("a download is not the widget as written", response.Body.String())
	}
}

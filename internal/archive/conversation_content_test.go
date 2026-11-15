package archive

import (
	"context"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestConversationsAreFoundByWhatTheyHold(t *testing.T) {
	c, _ := testCatalog(t)
	reply := func(name string) MessageRecord {
		return MessageRecord{NativeID: name, Role: "assistant", Kind: "message", Text: "Reply " + name, Selected: true}
	}
	asset := func(kind, relation, availability string, details map[string]any) AssetRecord {
		return AssetRecord{MessageNativeID: "reply", Ref: kind + relation + availability + jsonText(details), Kind: kind, Name: kind, Relation: relation, Availability: availability, Details: details}
	}
	rich := WorkspaceRecord{SourceKind: "chatgpt", SourceID: "rich", Account: "local", Title: "Rich", ActivityAt: "2026-10-04T12:00:00Z",
		Conversations: []ConversationRecord{{NativeID: "rich", Provider: "chatgpt", Messages: []MessageRecord{reply("reply"), reply("second")},
			Assets: []AssetRecord{
				asset("audio", "recording", assetStored, nil), asset("audio", "recording", assetNotExported, nil),
				asset("image", "uploaded", assetStored, nil), asset("document", "uploaded", assetNotExported, nil),
				asset("image", "generated", assetStored, nil), asset("document", "generated", assetStored, nil),
				asset("document", "generated", assetStored, map[string]any{"fragment": true}),
				asset("artifact", "generated", assetNotExported, nil), asset("image", "web", assetRemote, map[string]any{"url": "https://birds.example/a.png"})},
			Sources: []SourceRecord{{MessageNativeID: "reply", URL: "https://birds.example/a"}, {MessageNativeID: "reply", URL: "https://birds.example/b"}},
			Notes: []NoteRecord{{MessageNativeID: "reply", Details: map[string]any{"spoken_chars": 4, "citations": []any{}}}, {MessageNativeID: "second", Details: map[string]any{"cut_off": true}}}}}}
	plain := WorkspaceRecord{SourceKind: "chatgpt", SourceID: "plain", Account: "local", Title: "Plain", ActivityAt: "2026-10-04T12:00:00Z",
		Conversations: []ConversationRecord{{NativeID: "plain", Provider: "chatgpt", Messages: []MessageRecord{reply("reply")}}}}
	ingestRecords(t, c, rich, plain)
	var holder, workspace string
	if err := c.reader().QueryRow("SELECT id,workspace_id FROM conversations WHERE native_id='rich'").Scan(&holder, &workspace); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"voice": 3, "voice_recordings": 1, "uploads": 2, "uploaded_images": 1, "generated_images": 1, "generated_files": 1,
		"artifacts": 1, "widgets": 1, "web_sources": 2, "citations": 1, "missing_files": 3}
	held := func() map[string]map[string]int64 {
		t.Helper()
		var all []string
		rows, err := queryMaps(c.reader(), "SELECT id FROM conversations")
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			all = append(all, firstString(row["id"]))
		}
		contents, err := c.conversationContents(context.Background(), all)
		if err != nil {
			t.Fatal(err)
		}
		return contents
	}
	if got := held(); !reflect.DeepEqual(got, map[string]map[string]int64{holder: want}) {
		t.Fatalf("content = %v", got)
	}
	// Every name in the vocabulary is one a conversation can hold.
	for _, name := range contentFeatureNames() {
		if want[name] == 0 {
			t.Fatalf("%s is never recorded", name)
		}
	}
	// A search for conversations, and one for workspaces, asks for kinds by
	// name; all asked for must be held, and an unknown name is refused.
	found := func(has any) []string {
		t.Helper()
		result, err := c.searchConversationsContext(context.Background(), map[string]any{"has": has, "limit": 20})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, card := range result["items"].([]map[string]any) {
			ids = append(ids, firstString(card["conversation_id"]))
			if firstString(card["conversation_id"]) == holder && !reflect.DeepEqual(card["has"], want) {
				t.Fatalf("card has = %v", card["has"])
			}
		}
		return ids
	}
	if all := found(nil); len(all) != 2 {
		t.Fatalf("unfiltered = %v", all)
	}
	for _, has := range []any{[]any{"voice"}, []any{"uploads", "artifacts"}, "voice, Widgets", []string{"missing_files"}} {
		if ids := found(has); !reflect.DeepEqual(ids, []string{holder}) {
			t.Fatalf("has %v = %v", has, ids)
		}
	}
	if _, err := c.searchConversationsContext(context.Background(), map[string]any{"has": []any{"attachments"}}); err == nil || !strings.Contains(err.Error(), "voice_recordings") {
		t.Fatalf("unknown kind: %v", err)
	}
	options, err := searchOptions(url.Values{"has": {"voice", "uploads"}})
	if err != nil || options.Has != "voice,uploads" {
		t.Fatal(options.Has, err)
	}
	for has, count := range map[string]int{"": 2, "voice,uploads": 1, "artifacts": 1} {
		options.Has = has
		rows, err := c.searchRows(options, libraryCompactFields)
		if err != nil || len(rows) != count || has != "" && firstString(rows[0]["id"]) != workspace {
			t.Fatalf("workspaces with %q = %v, %v", has, rows, err)
		}
	}
	if _, err := c.searchRows(SearchOptions{Has: "nothing"}, libraryCompactFields); err == nil {
		t.Fatal("unknown kind found workspaces")
	}
	work, err := c.searchWorkContext(context.Background(), map[string]any{"has": []any{"voice"}})
	if err != nil || len(work["items"].([]map[string]any)) != 1 {
		t.Fatalf("work = %v, %v", work, err)
	}
	if overview, err := c.conversationOverview(map[string]any{"conversation_id": holder}); err != nil || !reflect.DeepEqual(overview["has"], want) {
		t.Fatalf("overview has = %v, %v", overview["has"], err)
	}
	// A catalog from before the rows has them derived from what it stores.
	execSQL(t, c.DB, "DELETE FROM conversation_content", "DELETE FROM meta WHERE key='conversation_content_version'")
	if err := c.ensureConversationContent(); err != nil {
		t.Fatal(err)
	}
	if got := held(); !reflect.DeepEqual(got, map[string]map[string]int64{holder: want}) {
		t.Fatalf("derived again = %v", got)
	}
	// A read that no longer finds the content leaves none recorded.
	rich.Conversations[0].Assets, rich.Conversations[0].Sources, rich.Conversations[0].Notes = nil, nil, nil
	rich.Conversations[0].Messages = append(rich.Conversations[0].Messages, reply("third"))
	ingestRecords(t, c, rich)
	if got := held(); len(got) != 0 {
		t.Fatalf("after its content went = %v", got)
	}
}

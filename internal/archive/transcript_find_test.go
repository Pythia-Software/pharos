package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

// transcriptFindFixture is a case in tests/transcript-find-fixtures.json,
// whose expectations come from the reader's JavaScript; the transcript-ui
// tests check the same file, so workspace find and the Show filter agree.
type transcriptFindFixture struct {
	Name     string
	Messages []struct {
		NativeID       string          `json:"native_id"`
		Role           string          `json:"role"`
		Kind           string          `json:"kind"`
		ParentNativeID string          `json:"parent_native_id"`
		CallID         string          `json:"call_id"`
		Text           json.RawMessage `json:"text"`
	}
	Events []struct {
		Kind string   `json:"kind"`
		Path []string `json:"path"`
	}
	Queries []struct {
		Term          string   `json:"term"`
		Show          []string `json:"show"`
		Output        bool     `json:"output"`
		Regex         bool     `json:"regex"`
		CaseSensitive bool     `json:"case"`
		Want          struct {
			Shown       int      `json:"shown"`
			Hidden      int      `json:"hidden"`
			HiddenKinds []string `json:"hidden_kinds"`
		} `json:"want"`
	}
}

func transcriptFindFixtures(t *testing.T) []transcriptFindFixture {
	t.Helper()
	raw, err := os.ReadFile("../../tests/transcript-find-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []transcriptFindFixture
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

func (test transcriptFindFixture) records() []MessageRecord {
	records := []MessageRecord{}
	for _, message := range test.Messages {
		var text string
		if json.Unmarshal(message.Text, &text) != nil {
			text = string(message.Text)
		}
		records = append(records, MessageRecord{NativeID: message.NativeID, Role: message.Role, Kind: message.Kind, ParentNativeID: message.ParentNativeID, CallID: message.CallID, Text: text})
	}
	return records
}

func TestTranscriptFindMatchesReaderFixtures(t *testing.T) {
	for _, test := range transcriptFindFixtures(t) {
		structure := newTranscriptStructure(normalizeTranscript(test.records()))
		if len(structure.events) != len(test.Events) {
			t.Fatalf("%s: %d events, want %d", test.Name, len(structure.events), len(test.Events))
		}
		for index, want := range test.Events {
			if got := structure.events[index]; got.kind != want.Kind || !reflect.DeepEqual(structure.paths[index], want.Path) {
				t.Errorf("%s: event %d is %s %v, want %s %v", test.Name, index, got.kind, structure.paths[index], want.Kind, want.Path)
			}
		}
		for _, query := range test.Queries {
			pattern, err := transcriptFindPattern(query.Term, query.Regex, query.CaseSensitive)
			if err != nil {
				t.Fatal(err)
			}
			shown := map[string]bool{}
			for _, key := range query.Show {
				shown[key] = true
			}
			if query.Show == nil {
				for _, key := range transcriptKinds {
					shown[key] = true
				}
			}
			visible, hidden, kinds := structure.find(pattern, shown, query.Output)
			if visible != query.Want.Shown || hidden != query.Want.Hidden || !reflect.DeepEqual(kinds, query.Want.HiddenKinds) {
				t.Errorf("%s: %+v found %d shown, %d hidden in %v; want %+v", test.Name, query, visible, hidden, kinds, query.Want)
			}
		}
	}
}

// Workspace find reads a cached structure and prefilters rows in SQL; stored
// in a catalog, every fixture must still count as the reader counts it.
func TestWorkConversationMatchesAgreeWithReaderFixtures(t *testing.T) {
	catalog, _ := testCatalog(t)
	for index, test := range transcriptFindFixtures(t) {
		workspace, conversation := fmt.Sprintf("fixture-%d", index), fmt.Sprintf("fixture-%d-conversation", index)
		if _, err := catalog.DB.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES (?,'claude','local',?,?,'2026-09-28T00:00:00Z')`, workspace, workspace, test.Name); err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.DB.Exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,started_at) VALUES (?,?,'claude','local',?,'2026-09-28T01:00:00Z')`, conversation, workspace, conversation); err != nil {
			t.Fatal(err)
		}
		for order, record := range test.records() {
			id := fmt.Sprintf("%s-%d", conversation, order)
			if _, err := catalog.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,source_order,parent_native_id,call_id,content_hash) VALUES (?,?,?,?,?,?,?,?,?,?)`,
				id, conversation, record.NativeID, record.Role, record.Kind, record.Text, order, nilIfEmpty(record.ParentNativeID), nilIfEmpty(record.CallID), id); err != nil {
				t.Fatal(err)
			}
		}
		for _, query := range test.Queries {
			got, err := catalog.WorkConversationMatches(context.Background(), workspace, transcriptFindOptions{Term: query.Term, Show: query.Show, Output: query.Output, Regex: query.Regex, CaseSensitive: query.CaseSensitive})
			if err != nil {
				t.Fatal(err)
			}
			want := []conversationFindCount{}
			if query.Want.Shown+query.Want.Hidden > 0 {
				want = append(want, conversationFindCount{ID: conversation, Shown: query.Want.Shown, Hidden: query.Want.Hidden, HiddenKinds: query.Want.HiddenKinds})
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s: %+v found %s, want %s", test.Name, query, jsonText(got), jsonText(want))
			}
		}
	}
}

func TestWorkConversationMatchesCountShownAndHiddenKinds(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedReaderWorkspace(t, catalog)
	// Stored JSON may escape characters a literal scan of the text would miss.
	if _, err := catalog.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,source_order,content_hash) VALUES ('m8','agent','m8','assistant','metadata',?,9,'m8')`,
		`{"type":"agent_message","message":"Wrap it in \u003cb\u003e tags"}`); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		options transcriptFindOptions
		want    string
	}{
		{transcriptFindOptions{Term: "SAFFRON"}, `[{"id":"agent","shown":1,"hidden":0,"hidden_kinds":[]}]`},
		{transcriptFindOptions{Term: "SAFFRON", CaseSensitive: true}, `[]`},
		{transcriptFindOptions{Term: "config.toml"}, `[{"id":"agent","shown":1,"hidden":0,"hidden_kinds":[]}]`},
		{transcriptFindOptions{Term: "config.toml", Show: []string{"prompts", "response"}}, `[{"id":"agent","shown":0,"hidden":1,"hidden_kinds":["reads"]}]`},
		{transcriptFindOptions{Term: "build output"}, `[]`},
		{transcriptFindOptions{Term: "build output", Output: true}, `[{"id":"main","shown":1,"hidden":0,"hidden_kinds":[]}]`},
		{transcriptFindOptions{Term: "build output", Output: true, Show: []string{"prompts"}}, `[{"id":"main","shown":0,"hidden":1,"hidden_kinds":["commands"]}]`},
		{transcriptFindOptions{Term: `pull/\d+`, Regex: true}, `[{"id":"main","shown":1,"hidden":0,"hidden_kinds":[]}]`},
		{transcriptFindOptions{Term: "50%", Output: true}, `[]`},
		{transcriptFindOptions{Term: "<b> tags"}, `[{"id":"agent","shown":1,"hidden":0,"hidden_kinds":[]}]`},
		{transcriptFindOptions{Term: "SAFFRON", Show: []string{}}, `[{"id":"agent","shown":0,"hidden":1,"hidden_kinds":["prompts"]}]`},
	} {
		got, err := catalog.WorkConversationMatches(context.Background(), "work", test.options)
		if err != nil || jsonText(got) != test.want {
			t.Fatalf("%+v: got %s %v", test.options, jsonText(got), err)
		}
	}
	// The cached structure gives way to any change in the catalog.
	if _, err := catalog.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,source_order,content_hash) VALUES ('m9','main','m9','user','message','Now add nutmeg',10,'m9')`); err != nil {
		t.Fatal(err)
	}
	if got, err := catalog.WorkConversationMatches(context.Background(), "work", transcriptFindOptions{Term: "nutmeg", Show: []string{"prompts"}}); err != nil || jsonText(got) != `[{"id":"main","shown":1,"hidden":0,"hidden_kinds":[]}]` {
		t.Fatalf("a new message is found: %s %v", jsonText(got), err)
	}
	if _, err := catalog.WorkConversationMatches(context.Background(), "work", transcriptFindOptions{Term: "(", Regex: true}); err == nil {
		t.Fatal("an invalid expression is an error")
	}
}

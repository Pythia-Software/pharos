package archive

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The reader's transcript-ui tests check the same fixtures against its
// JavaScript, so workspace find and the Show filter agree on every kind.
func TestTranscriptFindMatchesReaderFixtures(t *testing.T) {
	raw, err := os.ReadFile("../../tests/transcript-find-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
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
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		records := []MessageRecord{}
		for _, message := range test.Messages {
			var text string
			if json.Unmarshal(message.Text, &text) != nil {
				text = string(message.Text)
			}
			records = append(records, MessageRecord{NativeID: message.NativeID, Role: message.Role, Kind: message.Kind, ParentNativeID: message.ParentNativeID, CallID: message.CallID, Text: text})
		}
		structure := newTranscriptStructure(normalizeTranscript(records))
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

func TestWorkConversationMatchesCountShownAndHiddenKinds(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedReaderWorkspace(t, catalog)
	// Stored JSON may escape characters a literal scan of the text would miss.
	if _, err := catalog.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,source_order,content_hash) VALUES ('m8','agent','m8','assistant','metadata',?,9,'m8')`,
		`{"type":"agent_message","message":"Wrap it in <b> tags"}`); err != nil {
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

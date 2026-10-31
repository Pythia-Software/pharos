package archive

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// The JAYHAWK model's invented-string fixture (FRAMING.md, reference.py).
type agentEventParity struct {
	Lexicon   string `json:"lexicon"`
	Normalize []struct {
		Text       string              `json:"text"`
		Normalized string              `json:"normalized"`
		Kinds      map[string][]string `json:"kinds"`
	} `json:"normalize"`
	Fences []struct {
		Sentences  []string `json:"sentences"`
		InCode     []bool   `json:"in_code"`
		Candidates [][3]any `json:"candidates"`
	} `json:"fences"`
	Clip []struct {
		Text    string `json:"text"`
		Clipped string `json:"clipped"`
	} `json:"clip"`
	CuePatterns [][3]string `json:"cue_patterns"`
	Framing     []struct {
		Family         string   `json:"family"`
		Source         string   `json:"source"`
		Sentences      []string `json:"sentences"`
		Anchor         int      `json:"anchor"`
		Window         [2]int   `json:"window"`
		Header         string   `json:"header"`
		Lines          []string `json:"lines"`
		AnchorInWindow int      `json:"anchor_in_window"`
	} `json:"framing"`
}

func readAgentEventParity(t *testing.T) agentEventParity {
	t.Helper()
	data, err := os.ReadFile("../../testdata/agent-event-parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var parity agentEventParity
	if err := json.Unmarshal(data, &parity); err != nil {
		t.Fatal(err)
	}
	return parity
}

func TestAgentEventCandidatesMatchReference(t *testing.T) {
	parity := readAgentEventParity(t)
	version, kinds := agentEventLexicon()
	if version != parity.Lexicon {
		t.Fatalf("lexicon %s, fixture %s", version, parity.Lexicon)
	}
	// The lexicon's kinds are the ones the product reports.
	var lexiconKinds, productKinds []string
	for _, kind := range kinds {
		lexiconKinds = append(lexiconKinds, kind.Kind)
	}
	for _, family := range agentEventFamilies {
		productKinds = append(productKinds, family.Key)
	}
	slices.Sort(lexiconKinds)
	slices.Sort(productKinds)
	if !slices.Equal(lexiconKinds, productKinds) {
		t.Fatalf("lexicon kinds %v, product kinds %v", lexiconKinds, productKinds)
	}
	for _, cue := range parity.CuePatterns {
		// The stand-ins for non-ASCII word characters (X, D) rely on this.
		if strings.ContainsFunc(cue[1], func(r rune) bool { return r >= utf8.RuneSelf || unicode.IsUpper(r) }) {
			t.Errorf("%s cue %q is not lower-case ASCII", cue[0], cue[1])
		}
		if got := agentEventCompileCue(cue[1]); got != cue[2] {
			t.Errorf("%s cue %q compiles to %q, want %q", cue[0], cue[1], got, cue[2])
		}
	}
	for _, c := range parity.Normalize {
		if got := agentEventNormalize(c.Text); got != c.Normalized {
			t.Errorf("normalize %q = %q, want %q", c.Text, got, c.Normalized)
		}
		got := map[string][]string{}
		for _, match := range agentEventMatch(c.Text) {
			got[match.Kind] = match.Cues
		}
		if !reflect.DeepEqual(got, c.Kinds) {
			t.Errorf("match %q = %v, want %v", c.Text, got, c.Kinds)
		}
	}
	for _, c := range parity.Fences {
		if got := agentEventInCode(c.Sentences); !slices.Equal(got, c.InCode) {
			t.Errorf("in code %q = %v, want %v", c.Sentences, got, c.InCode)
		}
		var want []agentEventCandidate
		for _, candidate := range c.Candidates {
			var cues []string
			for _, cue := range candidate[2].([]any) {
				cues = append(cues, cue.(string))
			}
			want = append(want, agentEventCandidate{int(candidate[0].(float64)), candidate[1].(string), cues})
		}
		if got := agentEventCandidates(c.Sentences); !reflect.DeepEqual(got, want) {
			t.Errorf("candidates %q = %v, want %v", c.Sentences, got, want)
		}
	}
}

func TestAgentEventLinesMatchReference(t *testing.T) {
	parity := readAgentEventParity(t)
	for _, c := range parity.Clip {
		if got := agentEventClip(c.Text); got != c.Clipped {
			t.Errorf("clip %q = %q, want %q", c.Text, got, c.Clipped)
		}
	}
	// The fixture's headers carry the model's definitions; read them back.
	for _, c := range parity.Framing {
		definition := c.Header[len("Family under test: "+c.Family+" — "):]
		definition = definition[:len(definition)-len("\nSource: "+agentEventSourceLabels[c.Source]+"\nSentences:\n")]
		header, lines, at := agentEventLines(map[string]string{c.Family: definition}, c.Family, c.Source, c.Sentences, c.Anchor)
		lo, hi := agentEventWindow(len(c.Sentences), c.Anchor)
		if header != c.Header || !slices.Equal(lines, c.Lines) || at != c.AnchorInWindow || [2]int{lo, hi} != c.Window {
			t.Errorf("%s anchor %d of %d: got window [%d,%d) anchor %d\n%q\n%q\nwant window %v anchor %d\n%q\n%q",
				c.Family, c.Anchor, len(c.Sentences), lo, hi, at, header, lines, c.Window, c.AnchorInWindow, c.Header, c.Lines)
		}
	}
}

func TestAgentEventSentencesLeaveOutSecrets(t *testing.T) {
	text := "\n  The build still fails. I'll work around it.\nNext line here!  " +
		"The key sk-abcdefghijklmnopqrstuvwx leaked.\n\n"
	var got []string
	for _, r := range agentEventSentences(text) {
		got = append(got, text[r[0]:r[1]])
	}
	want := []string{"The build still fails.", "I'll work around it.", "Next line here!", "The key", "leaked."}
	if !slices.Equal(got, want) {
		t.Fatalf("sentences %q, want %q", got, want)
	}
	if got := agentEventSentences(" \n\t"); got != nil {
		t.Fatalf("blank text has sentences %v", got)
	}
}

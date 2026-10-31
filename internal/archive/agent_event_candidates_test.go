package archive

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	labelrt "github.com/gbdubs/pharos/internal/labeler"
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
		Family         string            `json:"family"`
		Source         string            `json:"source"`
		Sentences      []string          `json:"sentences"`
		Anchor         int               `json:"anchor"`
		Window         [2]int            `json:"window"`
		Header         string            `json:"header"`
		Lines          []string          `json:"lines"`
		AnchorInWindow int               `json:"anchor_in_window"`
		IDs            []int32           `json:"ids"`
		Pool           []int32           `json:"pool"`
		Segment        []int32           `json:"segment"`
		Kept           []bool            `json:"kept"`
		Int8           agentEventOutputs `json:"int8"`
		FP32           agentEventOutputs `json:"fp32"`
	} `json:"framing"`
}

// agentEventOutputs are the reference's outputs for a case scored alone.
type agentEventOutputs struct {
	Verdict   []float32   `json:"verdict"`
	Families  []float32   `json:"families"`
	Sentences [][]float32 `json:"sentences"`
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

func TestAgentEventFramingMatchesReference(t *testing.T) {
	parity := readAgentEventParity(t)
	for _, c := range parity.Clip {
		if got := agentEventClip(c.Text); got != c.Clipped {
			t.Errorf("clip %q = %q, want %q", c.Text, got, c.Clipped)
		}
	}
	// The fixture's headers carry the model's definitions; read them back.
	tok := committedTokenizer(t)
	for _, c := range parity.Framing {
		definition := c.Header[len("Family under test: "+c.Family+" — "):]
		definition = definition[:len(definition)-len("\nSource: "+agentEventSourceLabels[c.Source]+"\nSentences:\n")]
		header, lines, at := agentEventLines(map[string]string{c.Family: definition}, c.Family, c.Source, c.Sentences, c.Anchor)
		lo, hi := agentEventWindow(len(c.Sentences), c.Anchor)
		if header != c.Header || !slices.Equal(lines, c.Lines) || at != c.AnchorInWindow || [2]int{lo, hi} != c.Window {
			t.Errorf("%s anchor %d of %d: got window [%d,%d) anchor %d\n%q\n%q\nwant window %v anchor %d\n%q\n%q",
				c.Family, c.Anchor, len(c.Sentences), lo, hi, at, header, lines, c.Window, c.AnchorInWindow, c.Header, c.Lines)
		}
		// Tokens, the budget and the pool and segment inputs, as the
		// runtime makes them for the model's input contract.
		row := agentEventFrame(tok, map[string]string{c.Family: definition}, c.Family, c.Source, c.Sentences, c.Anchor, agentEventFraming.MaxTokens)
		tensors := labelrt.BatchInputs(labelrt.InputContracts[labelrt.InputContractIDsAttentionPoolSegments].Inputs, []labelrt.Framed{row}, 1)
		var kept []bool
		for _, span := range row.Extra[labelrt.SegmentSpans] {
			kept = append(kept, span.Len() > 0)
		}
		if !slices.Equal(row.IDs, c.IDs) || !slices.Equal(tensors[2].Int32, c.Pool) || !slices.Equal(tensors[3].Int32, c.Segment) || !slices.Equal(kept, c.Kept) {
			t.Errorf("%s anchor %d of %d: %d ids, kept %v; want %d ids, kept %v", c.Family, c.Anchor, len(c.Sentences), len(row.IDs), kept, len(c.IDs), c.Kept)
		}
	}
}

// committedTokenizer is the ettin tokenizer both local models use.
func committedTokenizer(t *testing.T) *labelrt.Tokenizer {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "labeler", "kestrel-68m-tokenizer.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := labelrt.LoadTokenizer(data)
	if err != nil {
		t.Fatal(err)
	}
	return tok
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

// The real verifier through a real worker gives the reference's outputs for
// every fixture case, in batches packed and padded as the runtime packs
// them, for both variants. It needs the model directory, a worker and its
// runtime, so it is skipped without them.
func TestAgentEventModelParityWithWorker(t *testing.T) {
	dir, worker := os.Getenv("PHAROS_AGENT_EVENTS_MODEL_DIR"), os.Getenv("PHAROS_LABELER_WORKER")
	if dir == "" || worker == "" {
		t.Skip("set PHAROS_AGENT_EVENTS_MODEL_DIR (the verifier's model directory), PHAROS_LABELER_WORKER and PHAROS_ORT_LIBRARY to run the verifier parity test")
	}
	parity := readAgentEventParity(t)
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Definitions map[string]string `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	args := []string{}
	if runtime := os.Getenv("PHAROS_ORT_LIBRARY"); runtime != "" {
		args = append(args, "--runtime", runtime)
	}
	for _, variant := range []string{"int8", "fp32"} {
		model, err := labelrt.LoadFramedModel(dir, variant, agentEventFraming)
		if err != nil {
			t.Fatal(err)
		}
		runner, err := labelrt.NewRunner(context.Background(), model, labelrt.RunnerConfig{Worker: worker, Args: args, Threads: 4, MaxBatch: 8})
		if err != nil {
			t.Fatal(err)
		}
		rows := make([]labelrt.Framed, len(parity.Framing))
		for i, c := range parity.Framing {
			rows[i] = agentEventFrame(model.Tokenizer, manifest.Definitions, c.Family, c.Source, c.Sentences, c.Anchor, model.MaxSeq)
		}
		outputs, err := runner.Run(context.Background(), rows, []string{"verdict", "families", "sentences"}, 2048)
		runner.Pool().Close()
		if err != nil {
			t.Fatal(err)
		}
		worst := 0.0
		for i, c := range parity.Framing {
			want := c.Int8
			if variant == "fp32" {
				want = c.FP32
			}
			for name, values := range map[string][]float32{"verdict": want.Verdict, "families": want.Families, "sentences": slices.Concat(want.Sentences...)} {
				got := outputs[i][name]
				if len(got) != len(values) {
					t.Fatalf("%s case %d: %s has %d values, want %d", variant, i, name, len(got), len(values))
				}
				for j := range got {
					worst = max(worst, math.Abs(float64(got[j]-values[j])))
				}
			}
		}
		if worst > 1e-4 {
			t.Errorf("%s: outputs differ from the reference's by up to %g", variant, worst)
		}
		t.Logf("%s: %d cases, largest difference %g", variant, len(rows), worst)
	}
}

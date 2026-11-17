package archive

import (
	"fmt"
	"strings"
	"testing"
)

func TestTeamsCodexTotalPrefix(t *testing.T) {
	checkpoint := func(total, last string) string {
		return fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":%s},"last_token_usage":{"total_tokens":%s}}}}`+"\n", total, last)
	}
	header := `{"type":"session_meta","payload":{"id":"thread-a"}}` + "\n"
	cases := []struct {
		name, input string
		ok          bool
		total       int64
	}{
		{"complete", header + checkpoint("100", "100") + checkpoint("150", "50"), true, 150},
		{"duplicate", header + checkpoint("100", "100") + checkpoint("100", "100"), true, 100},
		{"inherited", header + checkpoint("100", "20"), false, 0},
		{"gap", header + checkpoint("100", "100") + checkpoint("150", "20"), false, 0},
		{"reset", header + checkpoint("100", "100") + checkpoint("20", "20"), false, 0},
		{"fractional", header + checkpoint("1.5", "1.5"), false, 0},
		{"overflow", header + checkpoint("9007199254740992", "9007199254740992"), false, 0},
		{"missing", header, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var quality teamsJSONLQuality
			events, err := readJSONLinesObserved(strings.NewReader(tc.input), int64(len(tc.input)), quality.observe)
			if err != nil {
				t.Fatal(err)
			}
			proof := teamsCodexNativeProof(events, "thread-a", quality)
			value, digest, ok := teamsCodexTotalPrefix(proof, events)
			if ok != tc.ok || value != tc.total || (ok && len(digest) != 64) {
				t.Fatalf("got total=%d digest=%q ok=%v", value, digest, ok)
			}
		})
	}
}

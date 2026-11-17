package archive

import (
	"strings"
	"testing"
)

func TestTeamsCodexNativeProofUsesGapFreeHeader(t *testing.T) {
	cases := []struct {
		name, input, indexed, want string
		partial                    bool
	}{
		{"root", `{"type":"session_meta","payload":{"id":"thread-a"}}` + "\n" + `{"type":"event_msg","payload":{"type":"token_count"}}` + "\n", "thread-a", "header_independent_unvalidated", false},
		{"spawn", `{"type":"session_meta","payload":{"id":"thread-a","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent"}}}}}` + "\n", "thread-a", "header_independent_unvalidated", false},
		{"missing", `{"type":"event_msg","payload":{}}` + "\n", "filename-derived", "header_missing", false},
		{"conflicting headers", `{"type":"session_meta","payload":{"id":"thread-a"}}` + "\n" + `{"type":"session_meta","payload":{"id":"thread-b"}}` + "\n", "thread-a", "header_conflict", false},
		{"fork", `{"type":"session_meta","payload":{"id":"thread-a","forked_from_id":"parent"}}` + "\n", "thread-a", "inherited_unproved", false},
		{"orphan fork ordinal", `{"type":"session_meta","payload":{"id":"thread-a","forked_from_ordinal_exclusive":2}}` + "\n", "thread-a", "inherited_unproved", false},
		{"malformed ordinal", `{"type":"session_meta","payload":{"id":"thread-a","forked_from_ordinal_exclusive":"unknown"}}` + "\n", "thread-a", "inherited_unproved", false},
		{"negative ordinal", `{"type":"session_meta","payload":{"id":"thread-a","subagent_history_start_ordinal":-1}}` + "\n", "thread-a", "inherited_unproved", false},
		{"malformed history", `{"type":"session_meta","payload":{"id":"thread-a","history_base":"unknown"}}` + "\n", "thread-a", "inherited_unproved", false},
		{"filename rewrite", `{"type":"session_meta","payload":{"id":"thread-a"}}` + "\n", "rewritten-thread", "native_id_rewritten", false},
		{"completed gap", `{"type":"session_meta","payload":{"id":"thread-a"}}` + "\n" + `{bad}` + "\n", "thread-a", "capture_gap", false},
		{"duplicate header key", `{"type":"session_meta","payload":{"id":"thread-a","id":"thread-a"}}` + "\n", "thread-a", "capture_gap", false},
		{"torn tail", `{"type":"session_meta","payload":{"id":"thread-a"}}` + "\n" + `{"type":"event_msg"`, "thread-a", "header_independent_unvalidated", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var q teamsJSONLQuality
			events, err := readJSONLinesObserved(strings.NewReader(tc.input), int64(len(tc.input)), q.observe)
			if err != nil {
				t.Fatal(err)
			}
			proof := teamsCodexNativeProof(events, tc.indexed, q)
			if proof.Status != tc.want || proof.TrailingPartial != tc.partial {
				t.Fatalf("proof status %q, trailing=%t", proof.Status, proof.TrailingPartial)
			}
		})
	}
}

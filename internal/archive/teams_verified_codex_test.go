package archive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTeamsVerifyCodexFileRechecksNativeEvidence(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "thread.jsonl")
	data := codexLines("synthetic-thread", 1) + `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":123},"last_token_usage":{"total_tokens":123}}}}` + "\n"
	writeSourceFile(t, file, data)
	var quality teamsJSONLQuality
	events, err := readJSONLinesObserved(strings.NewReader(data), int64(len(data)), quality.observe)
	if err != nil {
		t.Fatal(err)
	}
	_, digest, ok := teamsCodexTotalPrefix(teamsCodexNativeProof(events, "synthetic-thread", quality), events)
	if !ok {
		t.Fatal("fixture")
	}
	if value, err := teamsVerifyCodexFile(root, file, "synthetic-thread", digest); err != nil || value != 123 {
		t.Fatalf("value=%d error=%v", value, err)
	}
	if _, err := teamsVerifyCodexFile(root, file, "other-thread", digest); err == nil {
		t.Fatal("different native identity accepted")
	}
	writeSourceFile(t, file, strings.ReplaceAll(data, "123", "124"))
	if _, err := teamsVerifyCodexFile(root, file, "synthetic-thread", digest); err == nil {
		t.Fatal("changed counters accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	writeSourceFile(t, outside, data)
	if _, err := teamsVerifyCodexFile(root, outside, "synthetic-thread", digest); err == nil {
		t.Fatal("outside root accepted")
	}
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := teamsVerifyCodexFile(root, file, "synthetic-thread", digest); err == nil {
		t.Fatal("missing file accepted")
	}
}

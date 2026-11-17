package archive

import (
	"bytes"
	"testing"
)

func TestTeamsCheckpointGuardCover(t *testing.T) {
	key := bytes.Repeat([]byte{42}, 32)
	keyID, err := teamsCheckpointKeyID(key)
	if err != nil {
		t.Fatal(err)
	}
	a, err := teamsCheckpointGuard(key, "positive", true, "codex", "account", "thread")
	if err != nil {
		t.Fatal(err)
	}
	if !teamsCheckpointGuardMatches(a, key, keyID, true, "codex", "account", "thread") || teamsCheckpointGuardMatches(a, key, keyID, true, "codex", "account", "other") {
		t.Fatal("exact identity matching failed")
	}
	if !teamsCheckpointGuardMatches(a, key, keyID, false, "codex", "account", "other") {
		t.Fatal("unknown closure escaped guard")
	}
	unknown, err := teamsCheckpointGuard(key, "unknown", false, "codex", "account", "thread")
	if err != nil || unknown.Scope != "library" {
		t.Fatal("unproved closure lacks library hold", err)
	}
	if !teamsCheckpointGuardMatches(unknown, key, keyID, true, "codex", "account", "other") {
		t.Fatal("library hold skipped")
	}
	empty, err := teamsCheckpointGuard(key, "positive", true, "codex", "")
	if err != nil || empty.Scope != "library" {
		t.Fatal("empty identity did not hold library", err)
	}
	if !teamsCheckpointGuardMatches(a, bytes.Repeat([]byte{1}, 32), keyID, true, "codex", "account", "other") {
		t.Fatal("wrong key bypassed guard")
	}
	if _, err = teamsCheckpointGuard(nil, "positive", true, "codex"); err == nil {
		t.Fatal("accepted missing host key")
	}
}

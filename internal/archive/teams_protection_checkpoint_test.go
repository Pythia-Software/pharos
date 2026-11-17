package archive

import (
	"bytes"
	"strings"
	"testing"
)

func TestTeamsProtectionCheckpointRecord(t *testing.T) {
	tuple := &teamsCheckpointTuple{Generation: 1, Instance: strings.Repeat("a", 64), Revision: 1, CommitID: strings.Repeat("b", 32), Digest: strings.Repeat("c", 64)}
	next := &teamsCheckpointTuple{Generation: 4, Instance: strings.Repeat("e", 64), Revision: 2, CommitID: strings.Repeat("f", 32), Digest: strings.Repeat("0", 64)}
	scope := teamsCheckpointScope("synthetic-application", "synthetic-library")
	if scope == teamsCheckpointScope("synthetic-application", "other-library") || scope == teamsCheckpointScope("other-application", "synthetic-library") {
		t.Fatal("checkpoint scope collided")
	}
	for _, r := range []teamsCheckpointRecord{
		{Version: 1, Scope: scope, KeyID: strings.Repeat("9", 64), Sequence: 1, Phase: "unactivated", PreviousUnactivated: strings.Repeat("1", 64), Cover: []teamsCheckpointGuardCover{}},
		{Version: 1, Scope: scope, KeyID: strings.Repeat("9", 64), Sequence: 2, Phase: "current", Current: tuple, Cover: []teamsCheckpointGuardCover{}},
		{Version: 1, Scope: scope, KeyID: strings.Repeat("9", 64), Sequence: 1, Phase: "pending", PreviousUnactivated: strings.Repeat("1", 64), Next: tuple, Operation: strings.Repeat("d", 32), Transition: "no_tightening", Cover: []teamsCheckpointGuardCover{}},
		{Version: 1, Scope: scope, KeyID: strings.Repeat("9", 64), Sequence: 4, Phase: "pending", Previous: tuple, Next: next, Operation: strings.Repeat("d", 32), Transition: "tightening", Cover: []teamsCheckpointGuardCover{{Kind: "positive", Scope: "identity", Commitment: strings.Repeat("2", 64)}}},
	} {
		raw, err := teamsEncodeCheckpoint(r)
		if err != nil {
			t.Fatal(err)
		}
		got, err := teamsParseCheckpoint(raw)
		if err != nil || !got.valid() {
			t.Fatalf("round trip: %v", err)
		}
		for _, bad := range [][]byte{append(bytes.Clone(raw), 'x'), bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1), bytes.Replace(raw, []byte(`"phase":`), []byte(`"unknown":0,"phase":`), 1)} {
			if _, err := teamsParseCheckpoint(bad); err == nil {
				t.Fatalf("accepted malformed record: %s", bad)
			}
		}
	}
	bad := teamsCheckpointRecord{Version: 1, Scope: scope, KeyID: strings.Repeat("9", 64), Sequence: 1, Phase: "pending", PreviousUnactivated: strings.Repeat("1", 64), Next: tuple, Operation: strings.Repeat("d", 32), Transition: "tightening", Cover: []teamsCheckpointGuardCover{}}
	if _, err := teamsEncodeCheckpoint(bad); err == nil {
		t.Fatal("accepted tightening without guard cover")
	}
}

func TestTeamsCheckpointPendingReaderAndWriter(t *testing.T) {
	scope := teamsCheckpointScope("app", "library")
	prior := &teamsCheckpointTuple{Generation: 1, Revision: 1, Instance: strings.Repeat("a", 64), CommitID: strings.Repeat("b", 32), Digest: strings.Repeat("c", 64)}
	next := &teamsCheckpointTuple{Generation: 3, Revision: 2, Instance: prior.Instance, CommitID: strings.Repeat("d", 32), Digest: strings.Repeat("e", 64)}
	record := teamsCheckpointRecord{Version: 1, Scope: scope, KeyID: strings.Repeat("9", 64), Sequence: 3, Phase: "pending", Previous: prior, Next: next, Operation: strings.Repeat("f", 32), Transition: "tightening", Cover: []teamsCheckpointGuardCover{{Kind: "positive", Scope: "library", Commitment: strings.Repeat("1", 64)}}}
	for _, tc := range []struct {
		catalog            *teamsCheckpointTuple
		projection, writer bool
		want               teamsCheckpointDisposition
	}{
		{prior, true, false, teamsCheckpointDefer}, {prior, true, true, teamsCheckpointRecover},
		{next, true, false, teamsCheckpointDefer}, {next, true, true, teamsCheckpointFinalize},
		{prior, false, true, teamsCheckpointUnproved}, {nil, true, true, teamsCheckpointUnproved},
	} {
		if got := teamsClassifyCheckpoint(record, scope, tc.catalog, "", tc.projection, tc.writer); got != tc.want {
			t.Fatalf("classification %v, want %v", got, tc.want)
		}
	}
}

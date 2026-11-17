package archive

import (
	"encoding/hex"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// teamsCodexProfileItem derives a profile child from an already established
// thread identity. It carries no ownership, source values or send permission;
// in particular, this is not the old numeric anchor eligibility decision.
func teamsCodexProfileItem(key []byte, anchor sourcewire.Item, intrinsicDigest string) (sourcewire.ProfileItem, error) {
	var out sourcewire.ProfileItem
	if anchor.Kind != "codex_thread" || (anchor.Protection != "public" && anchor.Protection != "private" && anchor.Protection != "unknown") || (anchor.Profile != nil && (anchor.Profile.ID != "codex_thread_usage" || anchor.Profile.Version != 1)) {
		return out, errTeamsSourceUnavailable
	}
	digest, err := hex.DecodeString(intrinsicDigest)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != intrinsicDigest {
		return out, errTeamsSourceUnavailable
	}
	token, witness, err := teams.SourceProfileIdentity(key, anchor.Token, anchor.Witness, "codex_request_usage", 1)
	if err != nil {
		return out, errTeamsSourceUnavailable
	}
	revision, err := teams.SourceEvidenceRevision(key, token, "codex_request_usage", 1, digest)
	if err != nil {
		return out, errTeamsSourceUnavailable
	}
	return sourcewire.ProfileItem{Token: token, Witness: witness, Dependencies: []string{anchor.Token}, EvidenceRevision: revision}, nil
}

// A local proof cannot authorize typed access. This seam remains closed until
// the server assignment, recovery generation and local protection proof are
// integrated together; callers must not infer eligibility from capability.
func teamsRequireTypedUsageEligibility() error { return errTeamsSourceUnavailable }

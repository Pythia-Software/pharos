package archive

import (
	"sort"

	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

type teamsCodexProfilePages struct {
	Pages      [][]byte
	Seal       []byte
	SealSHA256 string
}

// teamsBuildCodexProfilePages builds bounded canonical evidence only. The
// caller must journal these exact bytes before sending; a local seal is not an
// accepted server receipt and cannot authorize facts or results.
func teamsBuildCodexProfilePages(installation, library, snapshot, inventory string, revision, local, issued int64, prior string, items []sourcewire.ProfileItem, complete bool, coverage sourcewire.Coverage) (teamsCodexProfilePages, error) {
	var out teamsCodexProfilePages
	if items == nil || len(items) > 10000 {
		return out, errTeamsSourceUnavailable
	}
	items = append([]sourcewire.ProfileItem{}, items...)
	sort.Slice(items, func(i, j int) bool { return items[i].Token < items[j].Token })
	for i := 1; i < len(items); i++ {
		if items[i-1].Token == items[i].Token {
			return out, errTeamsSourceUnavailable
		}
	}
	upload, err := sourcewire.ProfileUploadID(installation, library, snapshot, inventory, "codex_request_usage", 1, revision, local, issued, prior)
	if err != nil {
		return out, errTeamsSourceUnavailable
	}
	var previous *string
	for start := 0; start < len(items) || len(out.Pages) == 0; {
		if len(out.Pages) >= sourcewire.MaxPages {
			return teamsCodexProfilePages{}, errTeamsSourceUnavailable
		}
		end := min(start+sourcewire.MaxItemsPerPage, len(items))
		encode := func(n int) ([]byte, string, error) {
			p := sourcewire.ProfilePage{Contract: sourcewire.SourceRecoveryContract, IssuedAt: issued, UploadID: upload, SnapshotID: snapshot, InventorySHA256: inventory, ProfileID: "codex_request_usage", ProfileVersion: 1, ProfileRevision: revision, LocalRevision: local, ExpectedPriorSHA256: prior, Index: len(out.Pages), PreviousPageSHA256: previous, Items: append([]sourcewire.ProfileItem{}, items[start:n]...)}
			return p.Bytes()
		}
		var body []byte
		var hash string
		if start == end {
			body, hash, err = encode(end)
		} else {
			low, high := start+1, end
			for low <= high {
				mid := (low + high) / 2
				b, h, e := encode(mid)
				if e == nil && len(b) <= sourcewire.MaxPageBytes {
					body, hash = b, h
					low = mid + 1
				} else {
					high = mid - 1
				}
			}
			if body == nil {
				return teamsCodexProfilePages{}, errTeamsSourceUnavailable
			}
			parsed, e := sourcewire.DecodeProfilePage(body)
			if e != nil {
				return teamsCodexProfilePages{}, e
			}
			end = start + len(parsed.Items)
		}
		if err != nil || len(body) > sourcewire.MaxPageBytes {
			return teamsCodexProfilePages{}, errTeamsSourceUnavailable
		}
		out.Pages = append(out.Pages, body)
		previous = &hash
		start = end
	}
	seal := sourcewire.ProfileSeal{Contract: sourcewire.SourceRecoveryContract, IssuedAt: issued, UploadID: upload, SnapshotID: snapshot, InventorySHA256: inventory, ProfileID: "codex_request_usage", ProfileVersion: 1, ProfileRevision: revision, LocalRevision: local, ExpectedPriorSHA256: prior, PageCount: len(out.Pages), ItemCount: len(items), FinalPageSHA256: *previous, Complete: complete, Coverage: coverage}
	out.Seal, out.SealSHA256, err = seal.Bytes()
	if err != nil {
		return teamsCodexProfilePages{}, errTeamsSourceUnavailable
	}
	return out, nil
}

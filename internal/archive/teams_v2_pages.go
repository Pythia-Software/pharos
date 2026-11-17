package archive

import (
	"encoding/json"
	"errors"

	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

type teamsV2Pages struct {
	InventorySHA256 string
	Pages           [][]byte
	Seal            []byte
}

// teamsBuildV2Pages creates exact immutable wire bytes. The caller must persist
// them and the exposure fence before emitting the first page, and replay the
// same bytes after any uncertain result.
func teamsBuildV2Pages(inventory teamsV2Inventory, snapshotID string, localRevision, inventoryRevision, evaluationRevision int64, prior string) (teamsV2Pages, error) {
	var out teamsV2Pages
	if inventory.Items == nil {
		return out, errTeamsSourceUnavailable
	}
	h, err := sourcewire.NewInventoryHasher(inventory.Complete, inventory.Coverage)
	if err != nil {
		return out, err
	}
	for _, item := range inventory.Items {
		if err = h.Add(item); err != nil {
			return out, err
		}
	}
	digest, count, err := h.Finish()
	if err != nil {
		return out, err
	}
	out.InventorySHA256 = digest
	var previous *string
	start := 0
	for start < len(inventory.Items) || len(out.Pages) == 0 {
		if len(out.Pages) >= sourcewire.MaxPages {
			return teamsV2Pages{}, errTeamsSourceUnavailable
		}
		end := min(start+sourcewire.MaxItemsPerPage, len(inventory.Items))
		encode := func(n int) ([]byte, error) {
			page := sourcewire.Page{SnapshotID: snapshotID, LocalRevision: localRevision, InventoryRevision: inventoryRevision, InventorySHA256: digest, EvaluationRevision: evaluationRevision, ExpectedPriorSHA256: prior, Index: len(out.Pages), PreviousPageSHA256: previous, Items: append([]sourcewire.Item{}, inventory.Items[start:n]...)}
			raw, e := json.Marshal(page)
			if e != nil {
				return nil, e
			}
			if _, e = sourcewire.DecodePage(raw); e != nil {
				return nil, e
			}
			return raw, nil
		}
		var page []byte
		if start == end {
			page, err = encode(end)
		} else {
			low, high := start+1, end
			for low <= high {
				mid := (low + high) / 2
				candidate, e := encode(mid)
				if e == nil {
					page = candidate
					low = mid + 1
				} else {
					high = mid - 1
				}
			}
			if page == nil {
				return teamsV2Pages{}, errTeamsSourceUnavailable
			}
			var parsed sourcewire.Page
			parsed, err = sourcewire.DecodePage(page)
			end = start + len(parsed.Items)
		}
		if err != nil || len(page) > sourcewire.MaxPageBytes {
			return teamsV2Pages{}, errTeamsSourceUnavailable
		}
		out.Pages = append(out.Pages, page)
		hash := sourcewire.SHA256(page)
		previous = &hash
		start = end
	}
	seal := sourcewire.Seal{SnapshotID: snapshotID, LocalRevision: localRevision, InventoryRevision: inventoryRevision, InventorySHA256: digest, EvaluationRevision: evaluationRevision, ExpectedPriorSHA256: prior, PageCount: len(out.Pages), ItemCount: count, FinalPageSHA256: *previous, InventoryComplete: inventory.Complete, Coverage: inventory.Coverage}
	out.Seal, err = json.Marshal(seal)
	if err != nil {
		return teamsV2Pages{}, err
	}
	if _, err = sourcewire.DecodeSeal(out.Seal); err != nil {
		return teamsV2Pages{}, errors.New("invalid source seal")
	}
	return out, nil
}

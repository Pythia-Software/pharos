package archive

import (
	"bytes"
	"context"
	"sort"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

func teamsContentMatchesUpload(inventory teamsContentInventory, upload teamsV2Upload) bool {
	if !inventory.Complete || inventory.SourceRevision!=upload.SourceRevision || inventory.PrivacyRevision!=upload.PrivacyRevision || inventory.Generation!=upload.Generation { return false }
	seal,err:=sourcewire.DecodeSeal(upload.Seal);if err!=nil{return false}
	pages,err:=teamsBuildV2Pages(inventory.teamsV2Inventory,upload.SnapshotID,upload.LocalRevision,upload.InventoryRevision,upload.EvaluationRevision,seal.ExpectedPriorSHA256)
	return err==nil && pages.InventorySHA256==upload.InventoryHash && bytes.Equal(pages.Seal,upload.Seal)
}

// The closure is derived from locally sealed source records, never from a
// model-supplied ID or a server page. Only locally reviewed, public entries
// can be selected; any absent, private, stale or unknown dependency fails.
func teamsContentClosure(inventory teamsContentInventory, selected []string) ([]teams.ContentEvidence, error) {
	if !inventory.Complete || len(selected) == 0 || len(selected) > 1000 {
		return nil, errTeamsSourceUnavailable
	}
	byToken := make(map[string]sourcewire.Item, len(inventory.Items))
	for _, item := range inventory.Items {
		if _, ok := byToken[item.Token]; ok {
			return nil, errTeamsSourceUnavailable
		}
		byToken[item.Token] = item
	}
	selectedSet := map[string]bool{}
	for _, token := range selected {
		if selectedSet[token] {
			return nil, errTeamsSourceUnavailable
		}
		selectedSet[token] = true
		if item, ok := byToken[token]; !ok || item.Kind != "content_component" {
			return nil, errTeamsSourceUnavailable
		}
	}
	state := map[string]uint8{}
	closure := map[string]teams.ContentEvidence{}
	var visit func(string, int) error
	visit = func(token string, depth int) error {
		if depth > 128 || state[token] == 1 {
			return errTeamsSourceUnavailable
		}
		if state[token] == 2 {
			return nil
		}
		item, ok := byToken[token]
		if !ok || item.Protection != "public" || item.Dependencies == nil {
			return errTeamsSourceUnavailable
		}
		state[token] = 1
		for _, dep := range item.Dependencies {
			if err := visit(dep, depth+1); err != nil {
				return err
			}
		}
		evidence := teams.ContentEvidence{Token: item.Token, Kind: item.Kind, EvidenceRevision: item.EvidenceRevision, Protection: item.Protection, Dependencies: append([]string{}, item.Dependencies...), Selected: selectedSet[token]}
		if item.Profile != nil {
			evidence.ProfileID = item.Profile.ID
			evidence.ProfileVersion = item.Profile.Version
		}
		closure[token] = evidence
		state[token] = 2
		return nil
	}
	for _, token := range selected {
		if err := visit(token, 0); err != nil {
			return nil, err
		}
	}
	out := make([]teams.ContentEvidence, 0, len(closure))
	for _, item := range closure {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Token < out[j].Token })
	return out, nil
}

// Revalidate current consent, exact reviewed inputs, local source ownership
// and privacy under the sharing lease before any provider disclosure. The
// caller adds the server's one-use start authorization and holds the lease
// through prompt handoff; no SQL transaction is held across that handoff.
func (c *Catalog) teamsRevalidateContent(ctx context.Context, cfg Config, d LibraryDescriptor, credential *teams.EnrollmentCredential, prior teamsContentInventory, selected []string) error {
	if !teamsSourceConfigCurrent(cfg) {
		return errTeamsSnapshotChanged
	}
	current, err := c.teamsObserveV2Content(ctx, cfg, d, credential)
	if err != nil {
		return err
	}
	if !current.Complete || current.SourceRevision != prior.SourceRevision || current.PrivacyRevision != prior.PrivacyRevision || current.Generation != prior.Generation {
		return errTeamsSnapshotChanged
	}
	now, e := teamsContentClosure(current, selected)
	if e != nil {
		return e
	}
	old, e := teamsContentClosure(prior, selected)
	if e != nil {
		return e
	}
	if len(now) != len(old) {
		return errTeamsSnapshotChanged
	}
	for i := range now {
		if now[i].Token != old[i].Token || now[i].Kind != old[i].Kind || now[i].EvidenceRevision != old[i].EvidenceRevision || now[i].Protection != old[i].Protection || len(now[i].Dependencies) != len(old[i].Dependencies) {
			return errTeamsSnapshotChanged
		}
		for j := range now[i].Dependencies {
			if now[i].Dependencies[j] != old[i].Dependencies[j] {
				return errTeamsSnapshotChanged
			}
		}
	}
	_, _, err = c.loadTeamsModelPermission(ctx, d, credential)
	return err
}

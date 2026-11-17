package archive

import (
	"context"
	"errors"
	"testing"
)

func TestTeamsSourceMutationOrdersAgainstSharing(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	lease, err := c.teamsSharingLease()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.beginTeamsSourceMutation(ctx); !errors.Is(err, errTeamsSharingBusy) {
		t.Fatalf("mutation was not fenced: %v", err)
	}
	lease.Close()
	var before, after int64
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	lease, err = c.beginTeamsSourceMutation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.teamsSharingLease(); !errors.Is(err, errTeamsSharingBusy) {
		t.Fatalf("send was not fenced: %v", err)
	}
	lease.Close()
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&after); err != nil || after != before+1 {
		t.Fatalf("source revision %d -> %d: %v", before, after, err)
	}
}

func TestTeamsSourceConfigurationMustMatchDisk(t *testing.T) {
	original := Config{Path: "synthetic", Sources: []SourceConfig{{Name: "codex", Kind: "codex", Path: "/synthetic", Enabled: true}}}
	current := original
	override(t, &teamsLoadSourceConfig, func(string) (Config, error) { return current, nil })
	if !teamsSourceConfigCurrent(original) {
		t.Fatal("matching source settings rejected")
	}
	current.Sources = append([]SourceConfig(nil), original.Sources...)
	current.Sources[0].Enabled = false
	if teamsSourceConfigCurrent(original) {
		t.Fatal("changed source settings accepted")
	}
}

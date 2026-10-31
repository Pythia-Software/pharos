package archive

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// preparedItems reads a prepared dataset's items as the labeler frames them.
func preparedItems(t *testing.T, dir string) map[string]labelingItem {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, labelingDatasetFile)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT i.id,i.unit_id,u.message_id,u.conversation_id,i.chunk_index,i.chunk_count,i.visible_start,i.visible_end,i.text,u.context_prompt,
		u.cohort,u.sender_role,i.segment,u.provider,u.kind,u.channel,i.target_hash FROM items i JOIN units u ON u.id=i.unit_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	items := map[string]labelingItem{}
	for rows.Next() {
		var item labelingItem
		if err := rows.Scan(&item.ID, &item.UnitID, &item.MessageID, &item.ConversationID, &item.ChunkIndex, &item.ChunkCount, &item.VisibleStart, &item.VisibleEnd,
			&item.Text, &item.Context, &item.Cohort, &item.SenderRole, &item.Segment, &item.Provider, &item.Kind, &item.Channel, &item.TargetHash); err != nil {
			t.Fatal(err)
		}
		items[item.ID] = item
	}
	return items
}

// liveItems builds every conversation's items from the catalog and its
// stored authorship ledger, as the labels ledger does.
func liveItems(t *testing.T, catalog *Catalog) map[string]labelingItem {
	t.Helper()
	builder, err := newLabelsItemBuilder(t.Context(), catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for id := range builder.p.conversations {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	items := map[string]labelingItem{}
	for _, id := range ids {
		units, err := builder.conversation(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, unit := range units {
			for _, item := range unit.items() {
				items[item.ID] = item
			}
		}
	}
	return items
}

// The labeler's items must be the items it was trained on: prepare's, field
// for field, from the same catalog, Codex continuations included (prepare
// and the stored ledger classify them by one rule).
func TestLabelsItemsEqualPreparedItems(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedLabelingCatalog(t, catalog)
	if err := catalog.RebuildAuthorship(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "dataset")
	opts := labelingPrepareOptions{Dir: dir, CatalogPath: catalog.Path, Live: true, FreshDays: 14, Now: func() time.Time { return labelingFixtureNow }}
	if _, err := prepareLabelingDataset(context.Background(), opts, false); err != nil {
		t.Fatal(err)
	}
	prepared, live := preparedItems(t, dir), liveItems(t, catalog)
	if len(prepared) < 20 {
		t.Fatalf("the fixture prepared only %d items", len(prepared))
	}
	segments := map[string]int{}
	for id, want := range prepared {
		got, ok := live[id]
		if !ok {
			t.Errorf("item %s (%s, %s) is missing from the live items", id, want.MessageID, want.Cohort)
			continue
		}
		if got != want {
			t.Errorf("item %s differs:\nprepared %+v\nlive     %+v", id, want, got)
		}
		segments[want.Segment]++
	}
	for id, got := range live {
		if _, ok := prepared[id]; !ok {
			t.Errorf("live item %s (%s, %s) was not prepared", id, got.MessageID, got.Cohort)
		}
	}
	t.Logf("%d items; segments %v", len(prepared), segments)
}

func TestLabelingSegmentRule(t *testing.T) {
	piece := func(role, status string, start, end int) labelingPiece {
		return labelingPiece{Role: role, Status: status, VisibleStart: start, VisibleEnd: end}
	}
	typed := piece(pieceTarget, "estimated_typed", 0, 40)
	template := piece(pieceTarget, "reused_template", 50, 90)
	quote := piece(pieceContext, "quoted", -1, -1) // shown as a marker
	first, second := labelingChunk{Start: 0, End: 45}, labelingChunk{Start: 45, End: 95}
	for _, c := range []struct {
		name, cohort, want string
		pieces             []labelingPiece
		chunk              labelingChunk
	}{
		{"agent reply", "agent_reply", segmentAR, nil, first},
		{"thinking", "thinking", segmentTH, nil, first},
		{"typed only", "human", segmentTyped, []labelingPiece{typed}, first},
		{"typed with a quote anywhere", "human", segmentTypedContext, []labelingPiece{typed, quote}, first},
		{"typed beside a template", "human", segmentTypedContext, []labelingPiece{typed, template}, labelingChunk{Start: 0, End: 95}},
		{"only the template is in this chunk", "human", segmentTemplate, []labelingPiece{typed, template}, second},
		{"feedback outranks a template", "human", segmentFeedback, []labelingPiece{template, piece(pieceTarget, "feedback_comment", 60, 70)}, second},
		{"re-sent", "human", segmentResent, []labelingPiece{piece(pieceTarget, "reused_own_message", 0, 30), quote}, first},
		{"unshown typed whitespace counts in every chunk", "human", segmentTypedContext, []labelingPiece{piece(pieceTarget, "estimated_typed", -1, -1), template}, second},
		{"dropped pieces do not count", "human", segmentTyped, []labelingPiece{typed, piece(pieceDropped, spanHarness, -1, -1)}, first},
	} {
		if got := labelingSegment(c.cohort, c.pieces, c.chunk); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

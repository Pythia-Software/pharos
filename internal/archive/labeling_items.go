package archive

import (
	"fmt"
	"slices"
	"unicode/utf8"
)

// labelingSegmentsVersion names the segment rule: the one AVOCET defined over
// a prepared dataset's pieces, which the labeler's thresholds and framing are
// keyed by (docs/local-labeler.md).
const labelingSegmentsVersion = "avocet-segments-v1"

// Segments: the human ones frame their context by tokens, the rest by
// characters (kestrel-framing-v1).
const (
	segmentTyped        = "H-typed"
	segmentTypedContext = "H-typed+ctx"
	segmentTemplate     = "H-template"
	segmentResent       = "H-resent"
	segmentFeedback     = "H-feedback"
	// segmentHumanOther is a human item with no target status the rule
	// knows. Prepared libraries have none; IBIS named it for public data.
	segmentHumanOther = "H-other"
	segmentAIUR       = "AIUR"
	segmentAR         = "AR"
	segmentDEL        = "DEL"
	segmentTH         = "TH"
)

// labelingItem is one item a teacher or the local labeler labels: a chunk of
// a unit's visible text with what the model sees beside it. Prepare writes
// these to a dataset's items table and the labels ledger labels them, from
// the same function, so training and inference inputs cannot drift apart.
type labelingItem struct {
	ID, UnitID, MessageID, ConversationID string
	ChunkIndex, ChunkCount                int
	// VisibleStart and VisibleEnd are the chunk's byte range in the unit's
	// visible text.
	VisibleStart, VisibleEnd int
	// Text is the chunk; Context the unit's context-v3 prompt.
	Text, Context string
	// Cohort and SenderRole make the framing tag; SenderRole is the message's
	// role (units.sender_role), never its sender identity.
	Cohort, SenderRole, Segment string
	Provider, Kind, Channel     string
	TargetHash                  string
}

// items are the unit's items, one per chunk.
func (unit *labelingUnit) items() []labelingItem {
	items := make([]labelingItem, 0, len(unit.chunks))
	for index, chunk := range unit.chunks {
		items = append(items, labelingItem{
			ID:     labelingID("i_", labelingUnitsVersion, labelingChunkVersion, unit.messageID, fmt.Sprint(index)),
			UnitID: unit.id, MessageID: unit.messageID, ConversationID: unit.conversation,
			ChunkIndex: index, ChunkCount: len(unit.chunks), VisibleStart: chunk.Start, VisibleEnd: chunk.End,
			Text: unit.visible[chunk.Start:chunk.End], Context: unit.contextPrompt,
			Cohort: unit.cohort, SenderRole: unit.role, Segment: labelingSegment(unit.cohort, unit.pieces, chunk),
			Provider: unit.provider, Kind: unit.kind, Channel: unit.channel, TargetHash: unit.hashes[index],
		})
	}
	return items
}

// Chars is the item's length in code points.
func (item labelingItem) Chars() int { return utf8.RuneCountInString(item.Text) }

// labelingSegment is AVOCET's segment of an item. Agent cohorts map to their
// segment. A human item takes the statuses of the target and context pieces
// that overlap its chunk of the visible text; a piece that is not shown
// (whitespace between targets, context shown as a marker) counts for every
// chunk. Typed text makes it H-typed when that is its only target status and
// it has no context piece, else H-typed+ctx; otherwise a feedback comment,
// then a template, then re-sent text decides.
func labelingSegment(cohort string, pieces []labelingPiece, chunk labelingChunk) string {
	switch cohort {
	case "agent_in_user_role":
		return segmentAIUR
	case "agent_reply":
		return segmentAR
	case "delegation":
		return segmentDEL
	case "thinking":
		return segmentTH
	}
	targets, contexts := []string{}, 0
	for _, piece := range pieces {
		if piece.Role != pieceTarget && piece.Role != pieceContext {
			continue
		}
		if piece.VisibleStart >= 0 && (piece.VisibleStart >= chunk.End || piece.VisibleEnd <= chunk.Start) {
			continue
		}
		if piece.Role == pieceContext {
			contexts++
		} else if !slices.Contains(targets, piece.Status) {
			targets = append(targets, piece.Status)
		}
	}
	switch {
	case slices.Contains(targets, "estimated_typed"):
		if len(targets) == 1 && contexts == 0 {
			return segmentTyped
		}
		return segmentTypedContext
	case slices.Contains(targets, "feedback_comment"):
		return segmentFeedback
	case slices.Contains(targets, "reused_template"):
		return segmentTemplate
	case slices.Contains(targets, "reused_own_message"):
		return segmentResent
	}
	return segmentHumanOther
}

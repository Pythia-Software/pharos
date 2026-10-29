package archive

import (
	"context"
	"strings"
	"time"
)

var authorshipReplyBatch = 250

type authorshipReply struct {
	at         time.Time
	origin     textOrigin
	text, kind string
}

// Read replies in day windows, and bounded pages within each day. QueryMaps
// closes each statement before classification starts; no cursor pins the WAL
// while the CPU walks the corpus. Advance by timestamp, using an offset only
// within one tied timestamp; later pages never rescan an entire day. Label preparation may still supply its own
// explicit snapshot through q.
type authorshipReplyCursor struct {
	q                queryer
	start, stop      time.Time
	page             []map[string]any
	position, offset int
	lower            string
}

func (cursor *authorshipReplyCursor) next(ctx context.Context) (*authorshipReply, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if cursor.position < len(cursor.page) {
			row := cursor.page[cursor.position]
			cursor.position++
			if at, ok := parseTime(asString(row["created_at"])); ok {
				return &authorshipReply{at: at, origin: textOrigin{asString(row["conversation_id"]), asString(row["id"])}, text: asString(row["text"]), kind: asString(row["kind"])}, nil
			}
			continue
		}
		if !cursor.start.Before(cursor.stop) {
			return nil, nil
		}
		end := cursor.start.Add(24 * time.Hour)
		if cursor.stop.Before(end) {
			end = cursor.stop
		}
		query := strings.Replace(authorshipRepliesQuery, "created_at>=?1", "created_at>=?1 AND created_at<?2", 1)
		query = strings.Replace(query, "t.started_at>=?1", "t.started_at>=?1 AND t.started_at<?2", 1)
		batch := max(1, authorshipReplyBatch)
		lower := cursor.lower
		if lower == "" {
			lower = formatTime(cursor.start)
		}
		page, err := queryMapsContext(ctx, cursor.q, query+" LIMIT ?3 OFFSET ?4", lower, formatTime(end), batch, cursor.offset)
		if err != nil {
			return nil, err
		}
		cursor.page, cursor.position = page, 0
		if len(page) < batch {
			cursor.start = end
			cursor.offset = 0
			cursor.lower = ""
		} else {
			last := asString(page[len(page)-1]["created_at"])
			trailing := 0
			for i := len(page) - 1; i >= 0 && asString(page[i]["created_at"]) == last; i-- {
				trailing++
			}
			if cursor.lower == last {
				cursor.offset += trailing
			} else {
				cursor.offset = trailing
			}
			cursor.lower = last
		}
	}
}

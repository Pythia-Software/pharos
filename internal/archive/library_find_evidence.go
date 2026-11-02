package archive

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// A Search conversation card shows a work's matching messages in its
// summary, between its first ask and latest response, with how many
// messages separate each from the next. Cards load this one work at a time,
// so table pages carry only their single keyword hit.

// libraryEvidenceLimit is how many matching messages a card shows.
var libraryEvidenceLimit = 3

type workMatchItem struct {
	// Kind is first (the first ask), match, or last (the latest response).
	Kind           string   `json:"kind"`
	MessageID      string   `json:"message_id"`
	ConversationID string   `json:"conversation_id,omitempty"`
	Role           string   `json:"role,omitempty"`
	MessageKind    string   `json:"message_kind,omitempty"`
	Snippet        string   `json:"snippet,omitempty"`
	Highlights     [][2]int `json:"highlights,omitempty"`
	// Preview names the summary message a match also is: first or last.
	Preview   string `json:"preview,omitempty"`
	createdAt string
}

type workMatches struct {
	Items []workMatchItem `json:"items"`
	// Between[i] counts the messages after Items[i] and before Items[i+1].
	Between []int `json:"between"`
	// Complete is false when the scoped search stopped before it could
	// establish the newest matches.
	Complete bool `json:"complete"`
}

// WorkMatches finds a work's newest matching messages for a Library text
// search (find= parameters) and places them in order.
func (c *Catalog) WorkMatches(ctx context.Context, workspaceID string, values url.Values) (workMatches, error) {
	result := workMatches{Items: []workMatchItem{}, Between: []int{}}
	o, active, err := libraryFindRequest(values)
	if err != nil {
		return result, err
	}
	if !active || (o.Kind != "" && o.Kind != "text") {
		return result, fmt.Errorf("matching messages need a message search")
	}
	options := o.Search
	options.Query, options.Fuzzy, options.CaseSensitive, options.Separators = o.Query, o.Fuzzy, o.CaseSensitive, o.Separators
	options.SelectedOnly = true
	options.WorkspaceID = workspaceID
	options.Limit = libraryEvidenceLimit
	if options.TimeoutMS == 0 {
		options.TimeoutMS = 1500
	}
	search, err := c.SearchText(ctx, options)
	if err != nil {
		return result, err
	}
	result.Complete = search.Complete || len(search.Items) >= libraryEvidenceLimit
	items := []workMatchItem{}
	for _, item := range search.Items[:min(len(search.Items), libraryEvidenceLimit)] {
		items = append(items, workMatchItem{Kind: "match", MessageID: item.MessageID, ConversationID: item.ConversationID, Role: item.Role, MessageKind: item.MessageKind,
			Snippet: item.Snippet, Highlights: snippetHighlights(search.Query, item.Snippet, o.CaseSensitive, o.Separators)})
	}
	previews, err := queryMapsContext(ctx, c.reader(), `SELECT first_input_id,last_response_id FROM workspace_previews WHERE workspace_id=?
		UNION ALL SELECT first_input_id,last_response_id FROM (`+libraryPreviewSelect(`w.id=? AND w.id NOT IN (SELECT workspace_id FROM workspace_previews)`)+`)`, workspaceID, workspaceID)
	if err != nil {
		return result, err
	}
	for _, preview := range previews {
		for kind, field := range map[string]string{"first": "first_input_id", "last": "last_response_id"} {
			id := firstString(preview[field])
			if id == "" {
				continue
			}
			if at := slices.IndexFunc(items, func(item workMatchItem) bool { return item.MessageID == id }); at >= 0 {
				items[at].Preview = kind
			} else {
				items = append(items, workMatchItem{Kind: kind, MessageID: id})
			}
		}
	}
	if len(items) == 0 {
		return result, nil
	}
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.MessageID
	}
	stamps, err := queryMapsContext(ctx, c.reader(), `SELECT id,COALESCE(created_at,'') created_at FROM messages WHERE id IN (SELECT value FROM json_each(?))`, jsonText(ids))
	if err != nil {
		return result, err
	}
	created := map[string]string{}
	for _, stamp := range stamps {
		created[firstString(stamp["id"])] = firstString(stamp["created_at"])
	}
	for i := range items {
		items[i].createdAt = created[items[i].MessageID]
	}
	// The order the summary previews use: creation time, then ID.
	slices.SortFunc(items, func(a, b workMatchItem) int {
		return cmp.Or(strings.Compare(a.createdAt, b.createdAt), strings.Compare(a.MessageID, b.MessageID))
	})
	result.Items = items
	if len(items) < 2 {
		return result, nil
	}
	// Count prose messages, as the previews are: tool calls, tool output and
	// reasoning between two messages are not messages a person reads past.
	sums, arguments := []string{}, []any{}
	for i := range items[1:] {
		sums = append(sums, fmt.Sprintf("COALESCE(sum((COALESCE(m.created_at,''),m.id)>(?,?) AND (COALESCE(m.created_at,''),m.id)<(?,?)),0) g%d", i))
		arguments = append(arguments, items[i].createdAt, items[i].MessageID, items[i+1].createdAt, items[i+1].MessageID)
	}
	arguments = append(arguments, workspaceID)
	var counts []int
	row := c.reader().QueryRowContext(ctx, `SELECT `+strings.Join(sums, ",")+` FROM conversations c JOIN messages m INDEXED BY messages_conversation_idx ON m.conversation_id=c.id
		WHERE c.workspace_id=? AND +m.role IN ('user','assistant') AND m.kind='message'`, arguments...)
	counts = make([]int, len(sums))
	targets := make([]any, len(counts))
	for i := range counts {
		targets[i] = &counts[i]
	}
	if err := row.Scan(targets...); err != nil {
		return result, err
	}
	result.Between = counts
	return result, nil
}

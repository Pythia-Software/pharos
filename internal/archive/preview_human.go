package archive

import (
	"context"
	"encoding/json"
	"strings"
)

// firstHumanPreview uses retained authorship where available, falling back to
// the local rules while classification is pending. It never substitutes the
// raw message when it contains only harness or copied text.
func (c *Catalog) firstHumanPreview(ctx context.Context, workspaceID, conversationID string) (string, string, string, error) {
	filter := "c.workspace_id=? AND c.agent_depth=0 AND NOT EXISTS(SELECT 1 FROM conversation_launches l WHERE l.child_id=c.id)"
	id := workspaceID
	if conversationID != "" {
		filter, id = "c.id=?", conversationID
	}
	rows, err := c.reader().QueryContext(ctx, `SELECT m.id,m.text,COALESCE(m.created_at,''),a.spans_json,c.id
 FROM conversations c JOIN messages m INDEXED BY messages_conversation_idx ON m.conversation_id=c.id
 LEFT JOIN message_authorship a ON a.message_id=m.id
 WHERE `+filter+` AND `+currentGroupConversation("c")+` AND +m.role='user' AND m.kind='message' AND m.text<>''
 ORDER BY m.created_at,m.id`, id)
	if err != nil {
		return "", "", "", err
	}
	defer rows.Close()
	classifier := newClassifier(nil)
	classifier.quick = true
	for rows.Next() {
		var messageID, body, at, conversation string
		var stored *string
		if err := rows.Scan(&messageID, &body, &at, &stored, &conversation); err != nil {
			return "", "", "", err
		}
		var spans []authorSpan
		if stored == nil || json.Unmarshal([]byte(*stored), &spans) != nil || len(spans) == 0 {
			sent, _ := parseTime(at)
			spans = classifier.classify(authorInput{ID: messageID, ConversationID: conversation, WorkspaceID: workspaceID, Text: body, SentAt: sent})
		}
		var pieces []string
		for _, span := range spans {
			if span.Category == spanTyped && span.Start >= 0 && span.End >= span.Start && span.End <= len(body) {
				if piece := strings.TrimSpace(body[span.Start:span.End]); piece != "" {
					pieces = append(pieces, piece)
				}
			}
		}
		if len(pieces) > 0 {
			return messageID, clipText(strings.Join(pieces, " "), 600), at, nil
		}
	}
	return "", "", "", rows.Err()
}

package archive

import "context"

// Conversation activity keeps human messages and output usage as separate
// events. Joining a message onto every usage row would multiply its input.
// Human text uses the existing authorship classification; no transcript text
// is returned, and missing classification is not invented as human input.
func (c *Catalog) conversationActivityRows(ctx context.Context) ([]map[string]any, error) {
	return cachedValue(ctx, c, "conversation-activity:"+c.clock().Format("2006-01-02"), func(ctx context.Context) ([]map[string]any, error) {
		usage, err := c.localUsageRows(ctx)
		if err != nil {
			return nil, err
		}
		ledger, err := c.representativeLedger()
		if err != nil {
			return nil, err
		}
		lineage, err := c.usageConversationRoots(ledger)
		if err != nil {
			return nil, err
		}
		messages, err := queryMapsContext(ctx, c.reader(), `SELECT message_id,conversation_id,sent_at,
			MAX(total_chars-harness_chars-automated_chars,0) human_chars
			FROM message_authorship WHERE total_chars>harness_chars+automated_chars ORDER BY sent_at,message_id`)
		if err != nil {
			return nil, err
		}
		rows := make([]map[string]any, 0, len(usage)+len(messages))
		base := func(id string, root map[string]any, at any, kind string) map[string]any {
			return map[string]any{"id": id, "root_conversation_id": root["id"], "root_provider": root["provider"],
				"root_model_family": modelFamily(firstString(root["model"])), "root_effort": nilIfEmpty(firstString(root["effort"])),
				"workspace_id": root["workspace_id"], "event_at": at, "event_kind": kind,
				"human_input_tokens_estimate": int64(0), "human_messages": int64(0), "output_tokens": int64(0)}
		}
		for _, row := range usage {
			root := lineage.roots[firstString(row["conversation_id"])]
			if root == nil {
				continue
			}
			event := base("usage:"+firstString(row["id"]), root, row["first_usage_at"], "usage")
			event["output_tokens"] = row["output_tokens"]
			rows = append(rows, event)
		}
		for _, message := range messages {
			root := lineage.roots[firstString(message["conversation_id"])]
			if root == nil {
				continue
			}
			event := base("human:"+firstString(message["message_id"]), root, message["sent_at"], "human")
			// This is a byte/4 estimate, not a tokenizer result. Count supplied
			// text (including pastes/quotes/attachment references) once and omit
			// harness/automated spans. Image and unretained file bytes are absent.
			event["human_input_tokens_estimate"] = (integer(message["human_chars"]) + 3) / 4
			event["human_messages"] = int64(1)
			rows = append(rows, event)
		}
		return rows, nil
	})
}

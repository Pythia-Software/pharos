package archive

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// What a conversation holds beside its words is stored a row a kind in
// conversation_content, so a search can ask for the conversations with
// voice, uploads or artifacts without reading their messages. The rows are
// derived from message_assets, message_sources, message_notes and the calls
// that published an artifact, and rewritten whenever those are.

// contentFeature is one kind of content a search can ask for.
type contentFeature struct{ Name, Meaning string }

// contentFeatures is the vocabulary, in the order it is described.
var contentFeatures = []contentFeature{
	{"voice", "a voice conversation: a recording, or a reply marked as spoken"},
	{"voice_recordings", "the audio of a voice conversation is in the library"},
	{"uploads", "files the person attached"},
	{"uploaded_images", "images the person attached"},
	{"generated_images", "images the agent made"},
	{"generated_files", "documents or code the agent handed over"},
	{"artifacts", "artifacts the conversation made or published"},
	{"widgets", "inline widgets and maps in a reply"},
	{"web_sources", "web pages the agent read"},
	{"citations", "replies with inline citations"},
	{"missing_files", "files referred to that the export did not include"},
}

func contentFeatureNames() []string {
	names := make([]string, len(contentFeatures))
	for index, feature := range contentFeatures {
		names[index] = feature.Name
	}
	return names
}

// contentFeatureHelp describes the vocabulary to a caller choosing from it.
func contentFeatureHelp() string {
	parts := make([]string, len(contentFeatures))
	for index, feature := range contentFeatures {
		parts[index] = feature.Name + " (" + feature.Meaning + ")"
	}
	return strings.Join(parts, "; ")
}

// contentWidget is true of an asset drawn inside a reply, not handed over.
const contentWidget = `(COALESCE(json_extract(a.details,'$.fragment'),0)=1 OR json_extract(a.details,'$.widget') IS NOT NULL)`

// contentCounts counts each kind from what is stored for one conversation.
// A picture kept beside an exported artifact's pages is part of the
// artifact, not an image the agent made.
const contentCounts = `
 SELECT 'voice',(SELECT COUNT(*) FROM message_assets a WHERE a.conversation_id=?1 AND a.relation='recording')
  +(SELECT COUNT(*) FROM message_notes n WHERE n.conversation_id=?1 AND json_extract(n.details,'$.spoken_chars') IS NOT NULL)
 UNION ALL SELECT 'voice_recordings',COUNT(*) FROM message_assets a WHERE a.conversation_id=?1 AND a.relation='recording' AND a.availability='stored'
 UNION ALL SELECT 'uploads',COUNT(*) FROM message_assets a WHERE a.conversation_id=?1 AND a.relation='uploaded'
 UNION ALL SELECT 'uploaded_images',COUNT(*) FROM message_assets a WHERE a.conversation_id=?1 AND a.relation='uploaded' AND a.kind='image'
 UNION ALL SELECT 'generated_images',COUNT(*) FROM message_assets a WHERE a.conversation_id=?1 AND a.relation='generated' AND a.kind='image' AND a.asset_key NOT LIKE 'claude-export-file:%'
 UNION ALL SELECT 'generated_files',COUNT(*) FROM message_assets a WHERE a.conversation_id=?1 AND a.relation='generated' AND a.kind IN ('document','code','file') AND NOT ` + contentWidget + `
 UNION ALL SELECT 'artifacts',COUNT(*) FROM message_assets a WHERE a.conversation_id=?1 AND a.kind='artifact'
 UNION ALL SELECT 'widgets',COUNT(*) FROM message_assets a WHERE a.conversation_id=?1 AND ` + contentWidget + `
 UNION ALL SELECT 'web_sources',COUNT(*) FROM message_sources s WHERE s.conversation_id=?1
 UNION ALL SELECT 'citations',COUNT(*) FROM message_notes n WHERE n.conversation_id=?1 AND (json_extract(n.details,'$.citations') IS NOT NULL OR json_extract(n.details,'$.marks') IS NOT NULL)
 UNION ALL SELECT 'missing_files',COUNT(*) FROM message_assets a WHERE a.conversation_id=?1 AND a.availability='not_exported'`

// refreshConversationContent rewrites one conversation's rows, in the
// transaction that changed what they are derived from.
func refreshConversationContent(tx *sql.Tx, conversationID string) error {
	if _, err := tx.Exec("DELETE FROM conversation_content WHERE conversation_id=?", conversationID); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO conversation_content(conversation_id,feature,count) SELECT ?1,feature,count FROM (SELECT '' feature,0 count WHERE 0 UNION ALL"+contentCounts+") WHERE count>0", conversationID); err != nil {
		return err
	}
	// A coding agent's session records an artifact only as the call that
	// published it. A conversation whose messages already refer to its
	// artifacts has them counted above.
	rows, err := tx.Query(`SELECT m.text FROM tool_calls t JOIN messages m ON m.id=t.result_message_id WHERE t.conversation_id=? AND t.tool_name='Artifact'
		AND NOT EXISTS(SELECT 1 FROM conversation_content c WHERE c.conversation_id=t.conversation_id AND c.feature='artifacts')`, conversationID)
	if err != nil {
		return err
	}
	published := map[string]bool{}
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			rows.Close()
			return err
		}
		if match := artifactPublished.FindStringSubmatch(text); match != nil {
			// One artifact, however many versions of it were published.
			published[strings.SplitN(match[0], " ", 2)[0]] = true
		}
	}
	if err := rows.Close(); err != nil || len(published) == 0 {
		return err
	}
	_, err = tx.Exec("INSERT INTO conversation_content(conversation_id,feature,count) VALUES(?,'artifacts',?)", conversationID, len(published))
	return err
}

// conversationContentVersion names how the rows are derived. A catalog
// with another version, or from before the table, has them derived again
// from what it already stores: nothing is read from a source again.
const conversationContentVersion = "1"

func (c *Catalog) ensureConversationContent() error {
	var current bool
	if err := c.DB.QueryRow("SELECT EXISTS(SELECT 1 FROM meta WHERE key='conversation_content_version' AND value=?)", conversationContentVersion).Scan(&current); err != nil || current {
		return err
	}
	tx, finish, err := c.beginTrackedWrite(context.Background(), "conversation-content")
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(); finish() }()
	rows, err := tx.Query(`SELECT conversation_id FROM message_assets UNION SELECT conversation_id FROM message_sources UNION SELECT conversation_id FROM message_notes
		UNION SELECT conversation_id FROM tool_calls WHERE tool_name='Artifact' UNION SELECT conversation_id FROM conversation_content`)
	if err != nil {
		return err
	}
	var conversations []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		conversations = append(conversations, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range conversations {
		if err := refreshConversationContent(tx, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('conversation_content_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, conversationContentVersion); err != nil {
		return err
	}
	return tx.Commit()
}

// contentFilter reads the kinds a caller asked for: a list, or names
// separated by commas. An unknown name is an error that lists the known.
func contentFilter(value any) ([]string, error) {
	var names []string
	switch typed := value.(type) {
	case nil:
	case string:
		names = strings.Split(typed, ",")
	case []string:
		names = typed
	case []any:
		for _, item := range typed {
			name, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("has must name kinds of content: %s", strings.Join(contentFeatureNames(), ", "))
			}
			names = append(names, name)
		}
	default:
		return nil, fmt.Errorf("has must name kinds of content: %s", strings.Join(contentFeatureNames(), ", "))
	}
	var wanted []string
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || slices.Contains(wanted, name) {
			continue
		}
		if !slices.Contains(contentFeatureNames(), name) {
			return nil, fmt.Errorf("unknown kind of content %q; has takes: %s", name, strings.Join(contentFeatureNames(), ", "))
		}
		wanted = append(wanted, name)
	}
	return wanted, nil
}

// contentConversationSQL is true of a conversation that holds every kind
// wanted; contentWorkspaceSQL of a workspace with one conversation that does.
func contentConversationSQL(conversation string, wanted []string) (string, []any) {
	clauses, values := make([]string, len(wanted)), make([]any, len(wanted))
	for index, name := range wanted {
		clauses[index] = "EXISTS(SELECT 1 FROM conversation_content cc WHERE cc.conversation_id=" + conversation + " AND cc.feature=?)"
		values[index] = name
	}
	return strings.Join(clauses, " AND "), values
}

func contentWorkspaceSQL(workspace string, wanted []string) (string, []any) {
	inner, values := contentConversationSQL("hc.id", wanted)
	return "EXISTS(SELECT 1 FROM conversations hc WHERE hc.workspace_id=" + workspace + " AND " + inner + ")", values
}

// conversationContents is what each of these conversations holds: kind to
// count. A conversation with nothing beside its words is absent.
func (c *Catalog) conversationContents(ctx context.Context, conversations []string) (map[string]map[string]int64, error) {
	held := map[string]map[string]int64{}
	if len(conversations) == 0 {
		return held, nil
	}
	rows, err := queryMapsContext(ctx, c.reader(), "SELECT conversation_id,feature,count FROM conversation_content WHERE conversation_id IN (SELECT value FROM json_each(?))", jsonText(conversations))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		id := firstString(row["conversation_id"])
		if held[id] == nil {
			held[id] = map[string]int64{}
		}
		held[id][firstString(row["feature"])] = integer(row["count"])
	}
	return held, nil
}

// contentFilterText is a caller's kinds as SearchOptions carries them. A
// value that is not names is passed on as one, for contentFilter to refuse.
func contentFilterText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []any:
		names := make([]string, len(typed))
		for index, item := range typed {
			names[index] = fmt.Sprint(item)
		}
		return strings.Join(names, ",")
	}
	return fmt.Sprint(value)
}

// contentFilterSchema is the has argument of the search tools.
func contentFilterSchema() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": contentFeatureNames()},
		"description": "Only conversations that hold every one of these kinds of content beside their words: " + contentFeatureHelp() + ". Most kinds are recorded for ChatGPT and Claude.ai exports; artifacts also for coding sessions that published one."}
}

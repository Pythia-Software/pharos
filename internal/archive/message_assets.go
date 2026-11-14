package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

// Files and web sources a message refers to. A source that records them
// (today the ChatGPT and Claude.ai exports) hands them to ingest beside its
// messages; message rows are unchanged, and a conversation without any
// stores nothing here.
//
// An asset is one object whatever it is: an uploaded image, a recording, a
// document the agent wrote, a published artifact. It has a current version
// and may list earlier ones; a version is one file, or with Member naming a
// directory several. Availability says what the source kept of it, so a
// file the export only names is never mistaken for no file.

// Asset availability.
const (
	assetStored        = "stored"        // the bytes are in the captured source
	assetTextOnly      = "text_only"     // the source kept extracted text, not the file
	assetReconstructed = "reconstructed" // rebuilt from the tool calls that wrote it
	assetNotExported   = "not_exported"  // the source names the file without its content
	assetRemote        = "remote"        // the source holds a web address, not yet fetched
)

// AssetVersion is one recorded version of an asset.
type AssetVersion struct {
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// AssetRecord is an asset as one message refers to it.
type AssetRecord struct {
	// MessageNativeID is the message the asset belongs with in the reader.
	MessageNativeID string
	// AttachmentNativeID, when set, is the attachment message that already
	// carries this file's source descriptor, which the asset now presents.
	AttachmentNativeID string `json:",omitempty"`
	// Also are other attachment messages that name the same object, when a
	// source says it more than once.
	Also []string `json:",omitempty"`
	// Ref is the source's own identity for the object (a file or artifact
	// ID), the same wherever the object is referred to.
	Ref  string
	Kind string // image, audio, video, document, code, artifact or file
	Name string
	// Relation is what the message did with it: uploaded, generated,
	// recording, tool_output or library.
	Relation string
	// AgentAuthored marks content an agent wrote, which the reader may
	// render without asking; anything else is a person's or the web's.
	AgentAuthored bool   `json:",omitempty"`
	Availability  string // see the asset availability constants
	MimeType      string `json:",omitempty"`
	SizeBytes     int64  `json:",omitempty"`
	Width         int64  `json:",omitempty"`
	Height        int64  `json:",omitempty"`
	// DurationMS is a recording's length; OffsetMS is where it starts in
	// the conversation's own clock, when the source records one.
	DurationMS int64          `json:",omitempty"`
	OffsetMS   *int64         `json:",omitempty"`
	Version    string         `json:",omitempty"`
	Versions   []AssetVersion `json:",omitempty"`
	// SourceFile and Member locate stored bytes inside the captured source,
	// as the adapter read them.
	SourceFile string `json:",omitempty"`
	Member     string `json:",omitempty"`
	// Content is the text of a text-only or reconstructed asset.
	Content string         `json:",omitempty"`
	Details map[string]any `json:",omitempty"`
}

// SourceRecord is a web page or other reference a message drew on. Cited is
// false for one the agent read without citing.
type SourceRecord struct {
	MessageNativeID string
	URL             string
	Title           string `json:",omitempty"`
	Site            string `json:",omitempty"`
	Snippet         string `json:",omitempty"`
	Kind            string `json:",omitempty"` // web (default) or past_chat
	Cited           bool   `json:",omitempty"`
}

// replaceConversationAssets stores what a read of a conversation recorded,
// in the transaction that wrote its messages. A reference to a message the
// read does not hold is dropped.
func replaceConversationAssets(tx *sql.Tx, conversationID string, assets []AssetRecord, sources []SourceRecord) error {
	for _, table := range []string{"message_assets", "message_sources"} {
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE conversation_id=?", conversationID); err != nil {
			return err
		}
	}
	if len(assets) == 0 && len(sources) == 0 {
		return nil
	}
	known := map[string]bool{}
	rows, err := tx.Query("SELECT id FROM messages WHERE conversation_id=?", conversationID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		known[id] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	ordinals := map[string]int{}
	for _, asset := range assets {
		messageID := stableID("message", conversationID, asset.MessageNativeID)
		if !known[messageID] {
			continue
		}
		ordinal := ordinals[messageID]
		ordinals[messageID]++
		var attachment, versions, details, content, offset any
		if id := stableID("message", conversationID, asset.AttachmentNativeID); asset.AttachmentNativeID != "" && known[id] {
			attachment = id
		}
		if len(asset.Versions) > 0 {
			versions = jsonText(asset.Versions)
		}
		recorded := asset.Details
		if len(asset.Also) > 0 {
			var also []string
			for _, native := range asset.Also {
				if id := stableID("message", conversationID, native); known[id] {
					also = append(also, id)
				}
			}
			if len(also) > 0 {
				recorded = map[string]any{"also": also}
				for key, value := range asset.Details {
					recorded[key] = value
				}
			}
		}
		if len(recorded) > 0 {
			details = jsonText(recorded)
		}
		if asset.Content != "" {
			content = []byte(asset.Content)
		}
		if asset.OffsetMS != nil {
			offset = *asset.OffsetMS
		}
		if _, err := tx.Exec(`INSERT INTO message_assets(id,conversation_id,message_id,ordinal,attachment_message_id,asset_key,kind,name,relation,agent_authored,availability,
			mime_type,size_bytes,width,height,duration_ms,offset_ms,version,versions,source_file,member,content,details) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			stableID("message-asset", messageID, ordinal), conversationID, messageID, ordinal, attachment, asset.Ref, defaultString(asset.Kind, "file"), asset.Name, asset.Relation, asset.AgentAuthored,
			defaultString(asset.Availability, assetNotExported), nullIfEmpty(asset.MimeType), nullIfZero(asset.SizeBytes), nullIfZero(asset.Width), nullIfZero(asset.Height), nullIfZero(asset.DurationMS), offset,
			nullIfEmpty(asset.Version), versions, nullIfEmpty(asset.SourceFile), nullIfEmpty(asset.Member), content, details); err != nil {
			return err
		}
	}
	ordinals = map[string]int{}
	for _, source := range sources {
		messageID := stableID("message", conversationID, source.MessageNativeID)
		if !known[messageID] || source.URL == "" {
			continue
		}
		ordinal := ordinals[messageID]
		ordinals[messageID]++
		if _, err := tx.Exec(`INSERT INTO message_sources(conversation_id,message_id,ordinal,url,title,site,snippet,kind,cited) VALUES(?,?,?,?,?,?,?,?,?)`,
			conversationID, messageID, ordinal, source.URL, source.Title, source.Site, source.Snippet, defaultString(source.Kind, "web"), source.Cited); err != nil {
			return err
		}
	}
	return nil
}

func nullIfZero(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

// messageAssetsAttacher gives each message the assets and sources stored
// for it. Content is left out: the reader asks for a file when it shows it.
func (c *Catalog) messageAssetsAttacher(ctx context.Context, conversationID string) (func(map[string]any), error) {
	assets, err := queryMapsContext(ctx, c.reader(), `SELECT id,message_id,attachment_message_id,asset_key,kind,name,relation,agent_authored,availability,mime_type,size_bytes,width,height,
		duration_ms,offset_ms,version,versions,member,details,content IS NOT NULL AS has_content FROM message_assets WHERE conversation_id=? ORDER BY message_id,ordinal`, conversationID)
	if err != nil {
		return nil, err
	}
	sources, err := queryMapsContext(ctx, c.reader(), `SELECT message_id,url,title,site,snippet,kind,cited FROM message_sources WHERE conversation_id=? ORDER BY message_id,ordinal`, conversationID)
	if err != nil {
		return nil, err
	}
	if len(assets) == 0 && len(sources) == 0 {
		return func(map[string]any) {}, nil
	}
	byMessage, sourcesByMessage := map[string][]map[string]any{}, map[string][]map[string]any{}
	var fetched map[string]bool
	for _, asset := range assets {
		if asset["availability"] == assetRemote {
			if fetched, err = fetchedRemoteAssets(ctx, c.reader(), conversationID); err != nil {
				return nil, err
			}
			break
		}
	}
	for _, asset := range assets {
		messageID := firstString(asset["message_id"])
		delete(asset, "message_id")
		for _, key := range []string{"versions", "details"} {
			var value any
			if text := firstString(asset[key]); text != "" && json.Unmarshal([]byte(text), &value) == nil {
				asset[key] = value
			} else {
				delete(asset, key)
			}
		}
		for key, value := range asset {
			if value == nil {
				delete(asset, key)
			}
		}
		asset["agent_authored"], asset["has_content"] = integer(asset["agent_authored"]) != 0, integer(asset["has_content"]) != 0
		// A stored file with a directory for a member is several files:
		// the reader opens its index.
		if details, ok := asset["details"].(map[string]any); ok && asset["availability"] == assetRemote && fetched[firstString(details["url"])] {
			// A web image fetched after import reads as any stored file.
			asset["availability"] = assetStored
		}
		if asset["availability"] != assetNotExported && asset["availability"] != assetRemote {
			asset["url"] = "/api/assets/" + firstString(asset["id"])
		}
		delete(asset, "member")
		byMessage[messageID] = append(byMessage[messageID], asset)
	}
	for _, source := range sources {
		messageID := firstString(source["message_id"])
		delete(source, "message_id")
		source["cited"] = integer(source["cited"]) != 0
		if parsed, err := url.Parse(firstString(source["url"])); err == nil {
			source["domain"] = strings.TrimPrefix(parsed.Hostname(), "www.")
		}
		sourcesByMessage[messageID] = append(sourcesByMessage[messageID], source)
	}
	if err := c.resolveArtifactReferences(ctx, conversationID, assets); err != nil {
		return nil, err
	}
	return func(message map[string]any) {
		id := firstString(message["id"])
		if list := byMessage[id]; list != nil {
			message["assets"] = list
		}
		if list := sourcesByMessage[id]; list != nil {
			message["sources"] = list
		}
	}, nil
}

// assetContentTypes are the types an asset may be shown as in the page.
// Anything else downloads.
func assetContentType(declared, name string, data []byte) string {
	sniffed := http.DetectContentType(data)
	base, _, _ := mime.ParseMediaType(sniffed)
	switch {
	case strings.HasPrefix(base, "image/"), strings.HasPrefix(base, "audio/"), base == "video/mp4", base == "video/webm", base == "application/pdf":
		return sniffed
	}
	extension := strings.ToLower(path.Ext(name))
	declared, _, _ = mime.ParseMediaType(declared)
	switch {
	case declared == "text/html" || extension == ".html" || extension == ".htm" || base == "text/html":
		return "text/html; charset=utf-8"
	case extension == ".svg" || declared == "image/svg+xml":
		return "image/svg+xml"
	case strings.HasPrefix(base, "text/"):
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

// asset serves one stored asset by its ID, never by a path a request
// supplied. HTML is always sandboxed away from Pharos's origin; HTML an
// agent did not write is served as HTML only when the request says the
// person consented to opening it.
func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	id, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/assets/"), "/")
	rows, err := queryMapsContext(r.Context(), s.Catalog.reader(), `SELECT a.name,a.kind,COALESCE(r.mime_type,a.mime_type) AS mime_type,a.availability,a.agent_authored,a.source_file,a.member,COALESCE(a.content,r.content) AS content,a.attachment_message_id,a.details,c.workspace_id
		FROM message_assets a JOIN conversations c ON c.id=a.conversation_id
		LEFT JOIN remote_assets r ON a.availability='remote' AND r.url=json_extract(a.details,'$.url') WHERE a.id=?`, id)
	if err != nil || len(rows) != 1 {
		http.NotFound(w, r)
		return
	}
	row := rows[0]
	var data []byte
	switch content := row["content"].(type) {
	case []byte:
		data = content
	case string:
		data = []byte(content)
	}
	if attachment := firstString(row["attachment_message_id"]); data == nil && row["availability"] != assetNotExported && firstString(row["source_file"]) == "" && attachment != "" {
		// A text file an export document's message already holds whole.
		var text string
		var held map[string]any
		if s.Catalog.reader().QueryRowContext(r.Context(), "SELECT text FROM messages WHERE id=?", attachment).Scan(&text) == nil && json.Unmarshal([]byte(text), &held) == nil {
			if content, ok := held["extracted_content"].(string); ok {
				data = []byte(content)
			}
		}
	}
	if data == nil {
		original, member := firstString(row["source_file"]), firstString(row["member"])
		if row["availability"] != assetStored || original == "" || member != "" && !claudeExportPath(member) {
			http.NotFound(w, r)
			return
		}
		data, err = s.exportMemberBytes(r.Context(), firstString(row["workspace_id"]), []string{"chatgpt", "chatgpt-export"}, original, member)
		if err != nil || len(data) > 64<<20 {
			http.NotFound(w, r)
			return
		}
	}
	name := firstString(row["name"])
	contentType := assetContentType(firstString(row["mime_type"]), name, data)
	disposition := "attachment"
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	switch {
	case strings.HasPrefix(contentType, "text/html"):
		if integer(row["agent_authored"]) == 0 && r.URL.Query().Get("consent") != "1" {
			contentType = "text/plain; charset=utf-8"
			break
		}
		// Scripts run in an opaque origin: no cookies, no Pharos API, and
		// nothing loaded from the network.
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; media-src data: blob:; font-src data:")
		disposition = "inline"
		if r.URL.Query().Get("download") != "1" {
			data = widgetLibraries(data)
		}
		var details map[string]any
		if json.Unmarshal([]byte(firstString(row["details"])), &details) == nil && details["fragment"] == true && r.URL.Query().Get("download") != "1" {
			// A widget is a piece of a page, written for the host that drew
			// it: it is shown inside a page that supplies what that host did.
			data = []byte(widgetPage(r.URL.Query().Get("theme") == "light", data))
		}
	case contentType == "image/svg+xml":
		// An SVG is shown as an image only; opened directly it stays inert.
		disposition = "inline"
	case strings.HasPrefix(contentType, "image/"), strings.HasPrefix(contentType, "audio/"), strings.HasPrefix(contentType, "video/"), strings.HasPrefix(contentType, "text/plain"), strings.HasPrefix(contentType, "application/pdf"):
		disposition = "inline"
	}
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": path.Base(strings.ReplaceAll(name, "\\", "/"))}))
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// resolveArtifactReferences completes each artifact a conversation refers
// to from the export's own document of it, which holds the versions and,
// when the export included it, the content. The reference names the
// artifact by ID, or when its tool call gave none by title or description.
func (c *Catalog) resolveArtifactReferences(ctx context.Context, conversationID string, assets []map[string]any) error {
	var open []map[string]any
	for _, asset := range assets {
		if asset["kind"] == "artifact" && asset["availability"] != assetStored {
			open = append(open, asset)
		}
	}
	if len(open) == 0 {
		return nil
	}
	held, err := queryMapsContext(ctx, c.reader(), `SELECT a.id,a.asset_key,a.name,a.availability,a.version,a.versions,a.details,a.conversation_id,c.workspace_id FROM message_assets a
		JOIN conversations c ON c.id=a.conversation_id WHERE a.asset_key>='claude-artifact:' AND a.asset_key<'claude-artifact;' AND a.kind='artifact' AND a.conversation_id<>?
		AND c.harness='claude-export-document'`, conversationID)
	if err != nil {
		return err
	}
	for _, asset := range open {
		key := firstString(asset["asset_key"])
		wanted := strings.TrimPrefix(key, "claude-artifact-title:")
		description := strings.ToLower(strings.TrimSpace(firstString(mapValue(asset["details"])["description"])))
		for _, document := range held {
			var versions []AssetVersion
			_ = json.Unmarshal([]byte(firstString(document["versions"])), &versions)
			match := document["asset_key"] == key
			for _, version := range versions {
				title, about := strings.ToLower(strings.TrimSpace(version.Title)), strings.ToLower(strings.TrimSpace(version.Description))
				match = match || wanted != key && (title == wanted || about == wanted) || description != "" && about == description
			}
			if !match {
				continue
			}
			details := mapValueDefault(asset["details"])
			var documentDetails map[string]any
			_ = json.Unmarshal([]byte(firstString(document["details"])), &documentDetails)
			for name, value := range documentDetails {
				details[name] = value
			}
			asset["details"], asset["name"], asset["versions"] = details, document["name"], versions
			if document["availability"] == assetStored || asset["availability"] == assetNotExported {
				// A copy rebuilt from the conversation's calls stands until the export's own is held.
				asset["availability"] = document["availability"]
			}
			if version := firstString(document["version"]); version != "" {
				asset["version"] = version
			}
			asset["document"] = map[string]any{"workspace_id": document["workspace_id"], "conversation_id": document["conversation_id"]}
			if document["availability"] == assetStored {
				asset["url"] = "/api/assets/" + firstString(document["id"])
			}
			break
		}
	}
	return nil
}

// widgetShell stands in for the chat application a widget was written to
// be drawn inside: its theme variables in both modes, the helper classes
// its guidance names, and the functions it may call. Nothing in it loads
// from the network; a widget that asks a CDN for a library runs without it.
const widgetShell = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><style>
:root{color-scheme:%[1]s;--font-sans:-apple-system,BlinkMacSystemFont,"Segoe UI",system-ui,sans-serif;--font-voice:Georgia,"Times New Roman",serif;--font-mono:ui-monospace,SFMono-Regular,Menlo,monospace;
--radius:8px;--pad-sm:8px;--pad-md:12px;--pad-lg:16px;--pad-xl:24px;--gap-xs:4px;--gap-sm:8px;--gap-md:12px;--gap-lg:16px;--gap-xl:24px;--h-control:32px;%[2]s}
html,body{margin:0;background:var(--page-bg);color:var(--text-primary);font:14px/1.5 var(--font-sans)}
body{padding:16px;max-width:760px;margin:0 auto}
h1{font-size:22px}h2{font-size:18px}h3{font-size:16px}h1,h2,h3{font-weight:500;margin:0 0 8px}
.sr-only{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
svg{max-width:100%%;height:auto}svg text{font-family:var(--font-sans)}
svg .t,svg .th{fill:var(--text-primary)}svg .ts{fill:var(--text-secondary)}svg .th{font-weight:500}
button,input,select{font:inherit;color:inherit}
button{background:var(--surface-1);border:1px solid var(--border);border-radius:var(--radius);padding:4px 10px;cursor:pointer}
a{color:var(--text-accent)}
</style><script>window.sendPrompt=function(){};window.openLink=function(){};</script></head><body>
%[3]s
<script>(function(){var tell=function(){parent.postMessage({pharosWidgetHeight:document.documentElement.scrollHeight},'*')};new ResizeObserver(tell).observe(document.body);addEventListener('load',tell);tell()})()</script>
</body></html>`

const (
	widgetLight = `--page-bg:#faf9f5;--surface-0:#faf9f5;--surface-1:#f3f1ea;--surface-2:#ffffff;--surface-3:#e9e6dc;--surface-panel:#ffffff;--surface-popover:#ffffff;--text-primary:#1f1e1d;--text-secondary:#5e5d59;--text-muted:#87867f;--text-accent:#2a78d6;--text-danger:#b53333;--text-success:#2f7d4f;--text-warning:#946200;--border:#e3e0d5;--border-strong:#cfccc0;--border-stronger:#b0aea5;--border-accent:#2a78d6;--bg-accent:#2a78d6;--bg-accent-muted:#e3eefb;--fill-accent:#2a78d6;--fill-brand:#d97757;--fill-danger:#e34948;--fill-success:#3f9b65;--fill-warning:#eda100;--bg-danger:#fbe9e9;--bg-success:#e6f4ec;--bg-warning:#fdf3d7;--border-danger:#e9b4b4;--border-success:#a9d6bb;--border-warning:#ecd592;--on-brand:#ffffff;--shadow-color:rgba(0,0,0,.12)`
	widgetDark  = `--page-bg:#1f1e1d;--surface-0:#1f1e1d;--surface-1:#262624;--surface-2:#30302e;--surface-3:#3a3a37;--surface-panel:#262624;--surface-popover:#30302e;--text-primary:#f3f1ea;--text-secondary:#c2c0b6;--text-muted:#9c9a92;--text-accent:#6ea8ef;--text-danger:#f08a89;--text-success:#7cc79b;--text-warning:#f0c55a;--border:#3d3d3a;--border-strong:#53524e;--border-stronger:#6b6a65;--border-accent:#6ea8ef;--bg-accent:#2a78d6;--bg-accent-muted:#1e3350;--fill-accent:#4a90e2;--fill-brand:#d97757;--fill-danger:#e34948;--fill-success:#3f9b65;--fill-warning:#eda100;--bg-danger:#4a2222;--bg-success:#1f3b2b;--bg-warning:#453710;--border-danger:#7d3a3a;--border-success:#35684c;--border-warning:#76601f;--on-brand:#ffffff;--shadow-color:rgba(0,0,0,.5)`
)

// widgetChartScript matches a page's request to a CDN for the Chart.js
// library, in the forms the chat applications' widget guidance gives.
var widgetChartScript = regexp.MustCompile(`(?i)<script\b[^>]*\bsrc=["']https://(?:cdnjs\.cloudflare\.com/ajax/libs/Chart\.js/[^"'/]+/chart(?:\.umd)?(?:\.min)?\.js|cdn\.jsdelivr\.net/npm/chart\.js(?:@[^"'/]+)?(?:/dist/chart(?:\.umd)?(?:\.min)?\.js)?)["'][^>]*>\s*</script>`)

// widgetLibraries gives a page the copy of Chart.js built into Pharos where
// it asks a CDN for one: the frame it is shown in reaches no network.
func widgetLibraries(page []byte) []byte {
	if !widgetChartScript.Match(page) {
		return page
	}
	library, err := assets.ReadFile("assets/widget-chart.js")
	if err != nil {
		return page
	}
	script := append(append([]byte("<script>"), bytes.ReplaceAll(library, []byte("</script"), []byte("<\\/script"))...), []byte("</script>")...)
	return widgetChartScript.ReplaceAllLiteral(page, script)
}

func widgetPage(light bool, fragment []byte) string {
	if light {
		return fmt.Sprintf(widgetShell, "light", widgetLight, fragment)
	}
	return fmt.Sprintf(widgetShell, "dark", widgetDark, fragment)
}

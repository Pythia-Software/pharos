package archive

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// claudeAssetReader collects, across one exported conversation, the files
// and sources its messages refer to. files holds the text of each file the
// conversation's tools wrote, so a file Claude presented can be shown as it
// stood then: the export keeps the calls, not the file.
type claudeAssetReader struct {
	assets  []AssetRecord
	sources []SourceRecord
	files   map[string]string
	edits   map[string]int
	// shown is where each artifact's card stands, and named the identity a
	// title was seen with: an artifact worked on over many calls is shown
	// once, with the last message that touched it.
	shown map[string]int
	named map[string]string
	// published is, by file name, the card of the artifact a file was
	// published as.
	published map[string]int
}

// written is the text of the one file the conversation wrote under a name.
func (c *claudeAssetReader) written(name string) (string, string, bool) {
	found, text := "", ""
	for file, content := range c.files {
		if path.Base(file) == name {
			if found != "" {
				return "", "", false
			}
			found, text = file, content
		}
	}
	return found, text, found != ""
}

// claudeFileType is the type a written file's name gives it.
func claudeFileType(file string) string {
	switch strings.ToLower(path.Ext(file)) {
	case ".html", ".htm":
		return "text/html"
	case ".md", ".markdown":
		return "text/markdown"
	case ".svg":
		return "image/svg+xml"
	}
	return ""
}

// found is the assets read, without the cards a later one replaced.
func (c *claudeAssetReader) found() []AssetRecord {
	// A file published as an artifact is that artifact: its card stands for
	// the file's lines in the lists of files.
	for at, asset := range c.assets {
		card, ok := c.published[asset.Name]
		if !ok || card == at || asset.Ref == "" || c.assets[card].Ref == "" || asset.Kind == "artifact" || !strings.HasSuffix(asset.AttachmentNativeID, ":files") {
			continue
		}
		c.assets[card].Also = append(append(c.assets[card].Also, asset.AttachmentNativeID), asset.Also...)
		c.assets[at].Ref = ""
	}
	kept := c.assets[:0:0]
	for _, asset := range c.assets {
		if asset.Ref != "" {
			kept = append(kept, asset)
		}
	}
	return kept
}

var claudeArtifactLink = regexp.MustCompile(`claude\.ai/(?:code/)?artifact/([0-9a-f]{8}-[0-9a-f-]{27})`)

// claudeArtifactRef is the identity an artifact has in a conversation and
// in the export's own copy of it.
func claudeArtifactRef(id string) string { return "claude-artifact:" + id }

// message reads one exported message, whose records were just read: each
// asset belongs with the text before it, and the sources with the last.
func (c *claudeAssetReader) message(message map[string]any, id, role string, records []MessageRecord) {
	if len(records) == 0 {
		return
	}
	stored := map[string]bool{}
	for _, record := range records {
		stored[record.NativeID] = true
	}
	blocks, _ := message["content"].([]any)
	// The record each block was read as: the first text is the message's
	// own, by its bare ID.
	blockID := func(index int) string {
		if native := fmt.Sprintf("%s:block:%d", id, index); stored[native] {
			return native
		}
		return id
	}
	owner, lastText := "", ""
	var waiting []int
	names, used, paired, placed := map[string]string{}, map[string]bool{}, map[string]int{}, map[string]int{}
	// A file placed where the conversation shows it, as a screenshot is on
	// its tool result, is not placed again from the message's list of files.
	place := func(asset AssetRecord) {
		asset.MessageNativeID = owner
		used[asset.Ref], placed[asset.Ref] = true, len(c.assets)
		c.assets = append(c.assets, asset)
		if owner == "" {
			waiting = append(waiting, len(c.assets)-1)
		}
	}
	files, _ := message["files"].([]any)
	for _, raw := range files {
		file := mapValue(raw)
		names[firstString(file["file_uuid"])] = firstString(file["file_name"])
	}
	uploaded := "uploaded"
	if role != "user" {
		uploaded = "generated"
	}
	if role == "user" && stored[id] {
		owner = id
	}
	// An upload whose text the export extracted. The same upload is also
	// listed among the files, where alone it has its name.
	attachments, _ := message["attachments"].([]any)
	for index, raw := range attachments {
		attachment := mapValue(raw)
		if attachment == nil {
			continue
		}
		name := firstString(attachment["file_name"])
		for _, rawFile := range files {
			file := mapValue(rawFile)
			uuid, fileName := firstString(file["file_uuid"]), firstString(file["file_name"])
			if used[uuid] || fileName == "" || name != "" && name != fileName {
				continue
			}
			name, used[uuid] = fileName, true
			paired[uuid] = len(c.assets)
			break
		}
		native := fmt.Sprintf("%s:attachment:%d", id, index)
		place(AssetRecord{AttachmentNativeID: native, Ref: native, Name: defaultString(name, "Pasted text"), Kind: assetKind(firstString(attachment["file_type"]), defaultString(name, "pasted.txt")),
			MimeType: firstString(attachment["file_type"]), SizeBytes: integer(attachment["file_size"]), Relation: "uploaded", Availability: assetTextOnly})
	}
	results := map[string]map[string]any{}
	for _, raw := range blocks {
		if block := mapValue(raw); firstString(block["type"]) == "tool_result" {
			results[firstString(block["tool_use_id"])] = block
		}
	}
	index := map[string]int{}
	var found []SourceRecord
	source := func(link, title, site, snippet string, cited bool) {
		if !strings.HasPrefix(link, "http://") && !strings.HasPrefix(link, "https://") {
			return
		}
		if at, ok := index[link]; ok {
			found[at].Cited = found[at].Cited || cited
			found[at].Title, found[at].Site, found[at].Snippet = defaultString(found[at].Title, title), defaultString(found[at].Site, site), defaultString(found[at].Snippet, snippet)
			return
		}
		index[link] = len(found)
		found = append(found, SourceRecord{URL: link, Title: title, Site: site, Snippet: snippet, Cited: cited})
	}
	artifacts := map[string]bool{}
	for blockIndex, raw := range blocks {
		block := mapValue(raw)
		native := blockID(blockIndex)
		switch firstString(block["type"]) {
		case "text", "voice_note":
			if !stored[native] || strings.TrimSpace(asString(block["text"])) == "" {
				continue
			}
			owner, lastText = native, native
			for _, at := range waiting {
				c.assets[at].MessageNativeID = owner
			}
			waiting = nil
			for _, rawCitation := range reasoningList(block["citations"]) {
				source(firstString(mapValue(mapValue(rawCitation)["details"])["url"]), "", "", "", true)
			}
		case "image", "document":
			uuid := firstString(block["file_uuid"])
			if at, ok := paired[uuid]; ok && native != id {
				// The block names the upload whose text the attachment holds.
				c.assets[at].Also = append(c.assets[at].Also, native)
			}
			if used[uuid] {
				continue
			}
			used[uuid] = true
			kind := assetKind("", names[uuid])
			if firstString(block["type"]) == "image" {
				kind = "image"
			}
			place(AssetRecord{AttachmentNativeID: native, Ref: uuid, Name: firstString(block["title"], names[uuid], block["type"]), Kind: kind, Relation: uploaded, Availability: assetNotExported})
		case "tool_use":
			c.tool(block, results[firstString(block["id"])], native, place, source, artifacts)
		}
	}
	// A file the agent kept rewriting is listed once for each time; the
	// export holds none of them, so the listings are one card.
	listed := map[string]int{}
	for _, raw := range files {
		file := mapValue(raw)
		uuid := firstString(file["file_uuid"])
		if at, ok := placed[uuid]; ok && c.assets[at].AttachmentNativeID == "" {
			// Its card stands for its line in the list of files too.
			c.assets[at].Also = append(c.assets[at].Also, id+":files")
		}
		if !used[uuid] {
			used[uuid] = true
			name := defaultString(file["file_name"], "File")
			if at, ok := listed[name]; ok && firstString(file["file_name"]) != "" {
				c.assets[at].Details["listed"] = integer(c.assets[at].Details["listed"]) + 1
				continue
			}
			listed[name] = len(c.assets)
			record := AssetRecord{AttachmentNativeID: id + ":files", Ref: uuid, Name: name, Kind: assetKind("", name), Relation: uploaded, Availability: assetNotExported, Details: map[string]any{"listed": 1}}
			if file, text, ok := c.written(name); ok && role != "user" {
				// The export does not hold the file, but holds every call that wrote it.
				record.Content, record.SizeBytes, record.MimeType, record.Availability, record.AgentAuthored = text, int64(len(text)), claudeFileType(file), assetReconstructed, true
				record.Details["edits"] = c.edits[file]
			}
			place(record)
		}
	}
	for _, at := range paired {
		c.assets[at].Also = append(c.assets[at].Also, id+":files")
	}
	for _, at := range waiting {
		c.assets[at].MessageNativeID = records[0].NativeID
	}
	for _, item := range found {
		item.MessageNativeID = firstString(lastText, records[0].NativeID)
		if len(item.Snippet) > 400 {
			item.Snippet = strings.ToValidUTF8(item.Snippet[:400], "")
		}
		c.sources = append(c.sources, item)
	}
}

// tool reads what one tool call made or drew on.
func (c *claudeAssetReader) tool(block, result map[string]any, native string, place func(AssetRecord), source func(string, string, string, string, bool), artifacts map[string]bool) {
	name, input := firstString(block["name"]), mapValue(block["input"])
	generated := func(ref, title, mimeType, content, availability string) {
		place(AssetRecord{AttachmentNativeID: native, Ref: ref, Name: title, Kind: assetKind(mimeType, title), MimeType: mimeType, SizeBytes: int64(len(content)),
			Relation: "generated", AgentAuthored: true, Availability: availability, Content: content})
	}
	for _, raw := range reasoningList(result["content"]) {
		item := mapValue(raw)
		switch firstString(item["type"]) {
		case "knowledge":
			meta := mapValue(item["metadata"])
			source(firstString(item["url"]), firstString(item["title"]), firstString(meta["site_name"], meta["site_domain"]), firstString(item["text"]), false)
		case "image":
			if uuid := firstString(item["file_uuid"]); uuid != "" {
				place(AssetRecord{Ref: uuid, Name: "Image", Kind: "image", Relation: "tool_output", Availability: assetNotExported})
			}
		case "image_gallery":
			// Web images a search showed: the export holds their addresses.
			for _, rawImage := range reasoningList(item["images"]) {
				image := mapValue(rawImage)
				address := firstString(image["url"], image["thumbnail_url"])
				if address == "" {
					continue
				}
				details := map[string]any{"url": address}
				for key, field := range map[string]string{"thumbnail_url": "thumbnail_url", "page_url": "page_url", "site": "source"} {
					if value := firstString(image[field]); value != "" {
						details[key] = value
					}
				}
				place(AssetRecord{AttachmentNativeID: native, Ref: address, Name: defaultString(image["title"], "Image"), Kind: "image", Relation: "web", Availability: assetRemote,
					Width: integer(image["width"]), Height: integer(image["height"]), Details: details})
			}
		}
	}
	if name == "link_preview_display_v0" {
		// Links the reply set out as cards.
		for _, raw := range reasoningList(input["links"]) {
			link := mapValue(raw)
			source(firstString(link["url"]), firstString(link["title"]), firstString(link["domain"]), firstString(link["snippet"]), true)
		}
	}
	for _, raw := range reasoningList(input["md_citations"]) {
		citation := mapValue(raw)
		source(firstString(citation["url"]), firstString(citation["title"]), "", "", true)
	}
	if link := mapValue(mapValue(result["display_content"])["link"]); link != nil {
		source(firstString(link["url"]), firstString(link["title"]), "", "", false)
	}
	file := firstString(input["path"], input["file_path"])
	switch {
	case name == "artifacts" && firstString(input["content"]) != "":
		// A document written straight into the conversation.
		title := defaultString(input["title"], "Artifact")
		mimeType := firstString(input["type"])
		if path.Ext(title) == "" {
			switch mimeType {
			case "text/markdown":
				title += ".md"
			case "text/html":
				title += ".html"
			}
		}
		generated(firstString(input["id"], block["id"]), title, mimeType, firstString(input["content"]), assetStored)
	case (name == "create_file" || name == "Write") && file != "":
		c.files[file] = firstString(input["file_text"], input["content"])
		c.edits[file] = 0
	case (name == "str_replace" || name == "Edit") && file != "":
		if text, ok := c.files[file]; ok {
			count := 1
			if input["replace_all"] == true {
				count = -1
			}
			c.files[file] = strings.Replace(text, firstString(input["old_str"], input["old_string"]), firstString(input["new_str"], input["new_string"]), count)
			c.edits[file]++
		}
	case name == "present_files":
		for _, raw := range reasoningList(input["filepaths"]) {
			file := firstString(raw)
			text, ok := c.files[file]
			if !ok {
				place(AssetRecord{AttachmentNativeID: native, Ref: file, Name: path.Base(file), Kind: assetKind("", file), Relation: "generated", AgentAuthored: true, Availability: assetNotExported})
				continue
			}
			mimeType := ""
			if extension := strings.ToLower(path.Ext(file)); extension == ".html" || extension == ".htm" {
				mimeType = "text/html"
			}
			generated(file, path.Base(file), mimeType, text, assetReconstructed)
			c.assets[len(c.assets)-1].Details = map[string]any{"edits": c.edits[file]}
		}
	case name == "visualize:show_widget" && firstString(input["widget_code"]) != "":
		generated(firstString(block["id"]), strings.ReplaceAll(defaultString(input["title"], "widget"), "_", " ")+".html", "text/html", firstString(input["widget_code"]), assetStored)
		c.assets[len(c.assets)-1].Details = map[string]any{"fragment": true}
	case name == "Artifact" || strings.HasPrefix(name, "mcp__Claude_Docs__"):
		// A published artifact or living doc: the export holds its content,
		// when it holds it at all, as a document of its own, which a
		// reader finds by this identity or else by the title.
		structured := mapValue(result["structured_content"])
		id := firstString(structured["artifact_id"])
		if container := mapValue(input["container"]); id == "" && firstString(container["kind"]) == "project" {
			id = firstString(container["id"])
		}
		if match := claudeArtifactLink.FindStringSubmatch(firstString(input["url"])); id == "" && match != nil {
			id = match[1]
		}
		title := firstString(structured["title"], input["title"])
		if create := mapValue(mapValue(input["container"])["create"]); title == "" && create != nil {
			title = firstString(create["name"])
		}
		description := firstString(input["description"])
		if c.shown == nil {
			c.shown, c.named = map[string]int{}, map[string]string{}
		}
		// An earlier card of this artifact gives way to the one placed now,
		// which takes its name, what was published as it, and the file lines
		// it stands for.
		var before *AssetRecord
		retire := func(at int) {
			title = firstString(title, c.assets[at].Name)
			c.assets[at].Ref = ""
			if before == nil || before.Content == "" {
				before = &c.assets[at]
			}
			for name, card := range c.published {
				if card == at {
					c.published[name] = len(c.assets)
				}
			}
		}
		label := strings.ToLower(strings.TrimSpace(firstString(title, description)))
		if label != "" && id == "" {
			id = c.named[label]
		}
		key := firstString(id, title, description)
		if key == "" || artifacts[key] || firstString(input["action"]) == "list" {
			return
		}
		if label != "" && id != "" {
			c.named[label] = id
			// The card placed before the artifact had its identity.
			if at, ok := c.shown["claude-artifact-title:"+label]; ok {
				retire(at)
				delete(c.shown, "claude-artifact-title:"+label)
			}
		}
		artifacts[key] = true
		ref := "claude-artifact-title:" + label
		if id != "" {
			ref = claudeArtifactRef(id)
		}
		details := map[string]any{}
		if description != "" {
			details["description"] = description
		}
		if at, ok := c.shown[ref]; ok {
			retire(at)
		}
		c.shown[ref] = len(c.assets)
		record := AssetRecord{AttachmentNativeID: native, Ref: ref, Name: defaultString(title, "Artifact"), Kind: "artifact", Relation: "generated", AgentAuthored: true, Availability: assetNotExported, Details: details}
		if before != nil && before.Content != "" {
			// A later call that names no file leaves what was published.
			record.Content, record.SizeBytes, record.MimeType, record.Availability = before.Content, before.SizeBytes, before.MimeType, before.Availability
			for _, key := range []string{"file", "edits"} {
				if value, ok := before.Details[key]; ok {
					details[key] = value
				}
			}
		}
		if file != "" {
			// The call names the file it published: the artifact is that
			// file as the conversation's calls left it, until the export's
			// own copy of the artifact is found.
			if c.published == nil {
				c.published = map[string]int{}
			}
			c.published[path.Base(file)] = len(c.assets)
			details["file"] = path.Base(file)
			if text, ok := c.files[file]; ok {
				record.Content, record.SizeBytes, record.MimeType, record.Availability = text, int64(len(text)), claudeFileType(file), assetReconstructed
				details["edits"] = c.edits[file]
			}
		}
		place(record)
	}
}

// claudeArtifactAsset is an exported artifact as its own document holds
// it: meta is its artifact.json, and members the files beside it.
func claudeArtifactAsset(group string, meta map[string]any, members map[string]bool) (AssetRecord, bool) {
	id := firstString(meta["id"], path.Base(group))
	current := firstString(meta["active_version"])
	record := AssetRecord{Ref: claudeArtifactRef(id), Kind: "artifact", Name: "Artifact", Relation: "generated", AgentAuthored: true, Availability: assetNotExported, Version: current,
		MessageNativeID: group + "/artifact.json", Details: map[string]any{"artifact_kind": firstString(meta["kind"])}}
	for _, raw := range reasoningList(meta["versions"]) {
		version := mapValue(raw)
		item := AssetVersion{ID: firstString(version["id"]), Title: firstString(version["title"]), Description: firstString(version["description"]), CreatedAt: iso(version["created_at"])}
		record.Versions = append(record.Versions, item)
		if item.ID == current || current == "" && record.Name == "Artifact" {
			record.Name = defaultString(item.Title, record.Name)
		}
	}
	if !members[record.MessageNativeID] {
		return record, false
	}
	files := 0
	for name := range members {
		if strings.HasPrefix(name, group+"/versions/"+current+"/") {
			files++
		}
	}
	for _, index := range []string{group + "/versions/" + current + "/index.html", group + "/versions/" + current + ".html"} {
		if current != "" && members[index] {
			record.MessageNativeID, record.AttachmentNativeID, record.Availability, record.MimeType = index, index, assetStored, "text/html"
			record.Details["files"] = max(files, 1)
			break
		}
	}
	return record, true
}

package archive

import (
	"net/url"
	"path"
	"strings"
)

// assetKind names what a file is from its declared type, else its name.
func assetKind(mimeType, name string) string {
	for _, kind := range []string{"image", "audio", "video"} {
		if strings.HasPrefix(mimeType, kind+"/") {
			return kind
		}
	}
	switch strings.ToLower(strings.TrimPrefix(path.Ext(name), ".")) {
	case "png", "jpg", "jpeg", "gif", "webp", "svg", "heic", "bmp":
		return "image"
	case "wav", "mp3", "m4a", "aac", "ogg", "flac":
		return "audio"
	case "mp4", "mov", "webm":
		return "video"
	case "js", "ts", "py", "go", "rs", "java", "c", "cpp", "h", "sh", "json", "css", "sql", "rb", "swift":
		return "code"
	case "pdf", "md", "txt", "html", "htm", "doc", "docx", "xls", "xlsx", "csv", "ppt", "pptx", "rtf":
		return "document"
	}
	if strings.HasPrefix(mimeType, "text/") {
		return "document"
	}
	return "file"
}

// chatGPTAssetRecord is the asset a resolved export attachment describes:
// resolved is the bundle's descriptor, holding the message part or upload
// it came from under source_attachment.
func chatGPTAssetRecord(resolved map[string]any, role, owner, attachmentID string) AssetRecord {
	part := mapValue(resolved["source_attachment"])
	audio := mapValue(part["audio_asset_pointer"])
	if audio == nil && strings.Contains(firstString(part["content_type"]), "audio") {
		audio = part
	}
	name := firstString(part["name"], resolved["name"])
	mimeType := firstString(resolved["mime_type"], part["mime_type"], part["mimeType"])
	record := AssetRecord{MessageNativeID: owner, AttachmentNativeID: attachmentID, Name: path.Base(strings.ReplaceAll(name, "\\", "/")), MimeType: mimeType,
		SizeBytes: integer(firstNonNil(resolved["size_bytes"], part["size_bytes"], part["size"], audio["size_bytes"])), Width: integer(part["width"]), Height: integer(part["height"]),
		SourceFile: firstString(resolved["source_file"]), Member: firstString(resolved["member"]), Availability: assetNotExported}
	_, record.Ref, _ = strings.Cut(firstString(part["asset_pointer"], audio["asset_pointer"]), "://")
	record.Ref = firstString(record.Ref, part["id"], resolved["library_file_id"], record.Member, record.Name)
	if resolved["available"] == true {
		record.Availability = assetStored
	}
	record.Kind = assetKind(mimeType, record.Name)
	switch media := firstString(resolved["media_kind"]); {
	case audio != nil:
		record.Kind = "audio"
		timing := mapValue(audio["metadata"])
		end, _ := number(timing["end"])
		if start, _ := number(timing["start"]); end > start {
			record.DurationMS = int64((end - start) * 1000)
		}
		if at, ok := number(part["audio_start_timestamp"]); ok {
			offset := int64(at * 1000)
			record.OffsetMS = &offset
		}
	case media != "" && record.Kind == "file":
		record.Kind = media
	case firstString(part["content_type"]) == "image_asset_pointer":
		record.Kind = "image"
	}
	library := mapValue(resolved["library_metadata"])
	switch {
	case record.Kind == "audio" && role == "user":
		record.Relation = "recording"
	case firstString(library["image_gen_generation_id"]) != "" || role == "assistant" || role == "tool":
		record.Relation, record.AgentAuthored = "generated", true
	case role == "user":
		record.Relation = "uploaded"
	default:
		record.Relation = "library"
	}
	return record
}

// chatGPTSourceURL drops the marker ChatGPT adds to every link it shows.
func chatGPTSourceURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	query := parsed.Query()
	if query.Get("utm_source") == "chatgpt.com" {
		query.Del("utm_source")
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}

// chatGPTSources reads the pages a message cites, then the search results
// it was given without citing, each once.
func chatGPTSources(metadata map[string]any, owner string) (sources []SourceRecord, images []AssetRecord) {
	index := map[string]int{}
	add := func(raw, title, site, snippet, kind string, cited bool) {
		link := chatGPTSourceURL(raw)
		if kind == "past_chat" {
			link = raw
		}
		if link == "" {
			return
		}
		if at, ok := index[link]; ok {
			sources[at].Cited = sources[at].Cited || cited
			sources[at].Title, sources[at].Snippet = defaultString(sources[at].Title, title), defaultString(sources[at].Snippet, snippet)
			return
		}
		index[link] = len(sources)
		if len(snippet) > 400 {
			snippet = strings.ToValidUTF8(snippet[:400], "")
		}
		sources = append(sources, SourceRecord{MessageNativeID: owner, URL: link, Title: title, Site: site, Snippet: snippet, Kind: kind, Cited: cited})
	}
	for _, raw := range reasoningList(metadata["content_references"]) {
		reference := mapValue(raw)
		switch firstString(reference["type"]) {
		case "grouped_webpages":
			for _, item := range reasoningList(reference["items"]) {
				page := mapValue(item)
				add(firstString(page["url"]), firstString(page["title"]), firstString(page["attribution"]), firstString(page["snippet"]), "web", true)
			}
		case "image_group":
			// Images ChatGPT showed from the web: the export holds their
			// addresses only.
			for _, item := range reasoningList(reference["images"]) {
				image := mapValue(mapValue(item)["image_result"])
				remote := firstString(image["content_url"], image["thumbnail_url"])
				if remote == "" {
					continue
				}
				size := mapValue(image["content_size"])
				images = append(images, AssetRecord{MessageNativeID: owner, Ref: remote, Kind: "image", Name: defaultString(image["title"], "Web image"), Relation: "web", Availability: assetRemote,
					Width: integer(size["width"]), Height: integer(size["height"]),
					Details: map[string]any{"url": remote, "thumbnail_url": firstString(image["thumbnail_url"]), "page_url": chatGPTSourceURL(firstString(image["url"])), "original_url": firstString(image["original_content_url"])}})
			}
		}
	}
	for _, raw := range reasoningList(metadata["conversation_context_citation_metadata"]) {
		citation := mapValue(mapValue(raw)["citation"])
		if link := firstString(citation["url"]); link != "" {
			add("https://chatgpt.com"+link, firstString(citation["title"], citation["conversation_title"]), "Past chat", firstString(citation["snippet"]), "past_chat", true)
		}
	}
	for _, raw := range reasoningList(metadata["search_result_groups"]) {
		for _, item := range reasoningList(mapValue(raw)["entries"]) {
			entry := mapValue(item)
			add(firstString(entry["url"]), firstString(entry["title"]), firstString(entry["attribution"]), firstString(entry["snippet"]), "web", false)
		}
	}
	return sources, images
}

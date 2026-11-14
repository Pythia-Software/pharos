package archive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"mime"
	"path"
	"regexp"
	"strings"
)

// An exported artifact is a document of its own: one conversation whose
// messages are the export's files of it, each version under
// artifacts/<id>/versions/<version>. A version is one page, or a directory
// of pages, stylesheets, scripts and pictures. A page is shown in a frame
// that can ask for nothing, so what it would load from beside it is put
// into it here, and its links are handed to the reader, which follows them.

// artifactFiles reads the files of one exported artifact.
type artifactFiles struct {
	ctx          context.Context
	catalog      *Catalog
	conversation string
}

// file is one member of the export, by its name there.
func (f artifactFiles) file(name string) ([]byte, bool) {
	id := stableID("message", f.conversation, name)
	var text string
	var held map[string]any
	if f.catalog.reader().QueryRowContext(f.ctx, "SELECT text FROM messages WHERE id=? AND conversation_id=?", id, f.conversation).Scan(&text) != nil || json.Unmarshal([]byte(text), &held) != nil {
		return nil, false
	}
	if content, ok := held["extracted_content"].(string); ok {
		return []byte(content), true
	}
	var data []byte
	if f.catalog.reader().QueryRowContext(f.ctx, "SELECT content FROM message_assets WHERE message_id=? AND content IS NOT NULL", id).Scan(&data) != nil {
		return nil, false
	}
	return data, true
}

// artifactVersionRoot splits the name of a version's opening page into the
// artifact's directory and the version.
func artifactVersionRoot(native string) (group, version string, ok bool) {
	parts := strings.Split(native, "/")
	if len(parts) < 4 || parts[0] != "artifacts" || parts[2] != "versions" {
		return "", "", false
	}
	return parts[0] + "/" + parts[1], strings.TrimSuffix(parts[3], ".html"), true
}

// artifactRelative is the member a page's reference names, or empty for a
// reference that leaves the version or the export.
func artifactRelative(page, reference string) string {
	reference, _, _ = strings.Cut(reference, "#")
	reference, _, _ = strings.Cut(reference, "?")
	if reference == "" || strings.Contains(reference, ":") || strings.HasPrefix(reference, "/") {
		return ""
	}
	joined := path.Join(path.Dir(page), reference)
	if joined == "." || joined == ".." || strings.HasPrefix(joined, "../") {
		return ""
	}
	return joined
}

var (
	artifactTag       = regexp.MustCompile(`(?is)<link\b[^>]*>|<script\b[^>]*>\s*</script>|<img\b[^>]*>`)
	artifactAttribute = regexp.MustCompile(`(?is)\b(href|src|rel)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	artifactStyleURL  = regexp.MustCompile(`(?i)url\(\s*["']?([^"')]+)["']?\s*\)`)
)

// artifactInlineLimit is the most one page is given of the files beside it.
var artifactInlineLimit = 24 << 20

// artifactLinks tells the reader which link a person followed: the frame
// may not navigate itself or open a window.
const artifactLinks = `<script>(function(){document.addEventListener('click',function(event){var link=event.target&&event.target.closest&&event.target.closest('a[href]');if(!link)return;var href=link.getAttribute('href');if(!href||href.charAt(0)==='#')return;event.preventDefault();parent.postMessage({pharosAssetLink:href},'*')},true)})()</script>`

// page is one page of a version made whole: page is its name within the
// version at root.
func (f artifactFiles) page(root, page string, data []byte) []byte {
	budget := artifactInlineLimit
	take := func(reference, from string) ([]byte, string, bool) {
		name := artifactRelative(from, reference)
		if name == "" {
			return nil, "", false
		}
		held, ok := f.file(root + "/" + name)
		if !ok || len(held) > budget {
			return nil, "", false
		}
		budget -= len(held)
		return held, name, true
	}
	picture := func(reference, from string) string {
		held, name, ok := take(reference, from)
		kind := mime.TypeByExtension(path.Ext(name))
		if !ok || !strings.HasPrefix(kind, "image/") {
			return ""
		}
		return "data:" + kind + ";base64," + base64.StdEncoding.EncodeToString(held)
	}
	shown := artifactTag.ReplaceAllFunc(data, func(tag []byte) []byte {
		attributes := map[string]string{}
		for _, match := range artifactAttribute.FindAllSubmatch(tag, -1) {
			attributes[strings.ToLower(string(match[1]))] = html.UnescapeString(string(match[2]) + string(match[3]) + string(match[4]))
		}
		switch lower := strings.ToLower(string(tag[:4])); {
		case lower == "<lin" && strings.Contains(strings.ToLower(attributes["rel"]), "stylesheet"):
			if sheet, name, ok := take(attributes["href"], page); ok {
				// A stylesheet's own pictures are named from where it is.
				sheet = artifactStyleURL.ReplaceAllFunc(sheet, func(use []byte) []byte {
					if address := picture(string(artifactStyleURL.FindSubmatch(use)[1]), name); address != "" {
						return []byte("url(" + address + ")")
					}
					return use
				})
				return []byte("<style>" + strings.ReplaceAll(string(sheet), "</style", "<\\/style") + "</style>")
			}
		case lower == "<scr":
			if script, _, ok := take(attributes["src"], page); ok {
				return []byte("<script>" + strings.ReplaceAll(string(script), "</script", "<\\/script") + "</script>")
			}
		case lower == "<img":
			if address := picture(attributes["src"], page); address != "" {
				return artifactAttribute.ReplaceAllFunc(tag, func(attribute []byte) []byte {
					if strings.EqualFold(string(artifactAttribute.FindSubmatch(attribute)[1]), "src") {
						return []byte(`src="` + address + `"`)
					}
					return attribute
				})
			}
		}
		return tag
	})
	return append(shown, artifactLinks...)
}

// deck is a slide deck's slides, one under another in the deck's order:
// the application that plays a deck loads its slides as it runs, which the
// frame does not allow.
func (f artifactFiles) deck(root string) ([]byte, bool) {
	held, ok := f.file(root + "/project/deck.json")
	var deck map[string]any
	if !ok || json.Unmarshal(held, &deck) != nil {
		return nil, false
	}
	var slides strings.Builder
	for _, raw := range reasoningList(deck["order"]) {
		name := firstString(raw)
		if name == "" || strings.ContainsAny(name, "/\\") {
			continue
		}
		if slide, ok := f.file(root + "/project/slides/" + name + ".html"); ok {
			slides.WriteString(`<div class="frame"><div class="slide">` + string(slide) + "</div></div>\n")
		}
	}
	if slides.Len() == 0 {
		return nil, false
	}
	return []byte(fmt.Sprintf(artifactDeck, html.EscapeString(firstString(deck["title"], "Slides")), slides.String())), true
}

const artifactDeck = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title><style>
html,body{margin:0;background:#18181a}
.deck{display:flex;flex-direction:column;align-items:center;gap:16px;padding:16px}
.frame{flex:none;overflow:hidden;border-radius:4px}
.slide{width:1920px;height:1080px;overflow:hidden;transform-origin:0 0;background:#fff;color:#111}
.slide>section{width:100%%;height:100%%;box-sizing:border-box;margin:0}
</style></head><body><div class="deck">
%s</div><script>(function(){function fit(){var width=Math.max(160,Math.min(document.documentElement.clientWidth-32,1920)),scale=width/1920;document.querySelectorAll('.frame').forEach(function(frame){frame.style.width=width+'px';frame.style.height=(1080*scale)+'px';frame.firstElementChild.style.transform='scale('+scale+')'})}addEventListener('resize',fit);fit()})()</script></body></html>`

// artifactRequest answers a request for an artifact's opening page as one
// of its versions or files: the content, and the name it is served under.
// held is false when the asset is not an exported artifact's page.
func (s *Server) artifactRequest(ctx context.Context, conversation, attachment, version, file string) (data []byte, name string, held, found bool) {
	var native string
	if attachment == "" || s.Catalog.reader().QueryRowContext(ctx, "SELECT native_id FROM messages WHERE id=?", attachment).Scan(&native) != nil {
		return nil, "", false, false
	}
	group, current, ok := artifactVersionRoot(native)
	if !ok {
		return nil, "", false, false
	}
	files := artifactFiles{ctx, s.Catalog, conversation}
	if version == "" {
		version = current
	}
	if strings.ContainsAny(version, "/\\") || file != "" && artifactRelative("index.html", file) != path.Clean(file) {
		return nil, "", true, false
	}
	root := group + "/versions/" + version
	if file == "" {
		if deck, ok := files.deck(root); ok {
			return deck, "index.html", true, true
		}
		for _, candidate := range []string{root + "/index.html", root + ".html"} {
			if data, ok := files.file(candidate); ok {
				return files.page(root, "index.html", data), "index.html", true, true
			}
		}
		return nil, "", true, false
	}
	file = path.Clean(file)
	data, ok = files.file(root + "/" + file)
	if !ok {
		return nil, "", true, false
	}
	if extension := strings.ToLower(path.Ext(file)); extension == ".html" || extension == ".htm" {
		data = files.page(root, file, data)
	}
	return data, path.Base(file), true, true
}

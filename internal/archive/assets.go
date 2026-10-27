package archive

import (
	"embed"
	"fmt"
	"regexp"
	"strings"

	"github.com/gbdubs/pharos/schemas"
)

// assets contains the existing catalog contract and the existing Pharos UI.
// Keeping both embedded makes the service a single, relocatable executable.
// Name the generated bundles explicitly so a fresh checkout cannot compile a
// service without its frontend. Run tools/setup.sh before Go builds or tests.
//
//go:embed assets assets/query-tables.js assets/query-tables.css
var assets embed.FS

func schemaSQL() string {
	value, _ := assets.ReadFile("assets/schema.sql")
	return string(value)
}

func appHTML() string {
	value, _ := assets.ReadFile("assets/ui.py")
	return uiHTML(value)
}

var shellScripts = regexp.MustCompile(`(?s)<script\b[^>]*>.*?</script>`)

// Library setup uses the app's real chrome and styles before a library exists.
// Its background is intentionally idle: none of the library scripts may query
// a catalog, start source discovery or load another library's preferences.
func applicationHTML() string {
	shell := shellScripts.ReplaceAllString(appHTML(), "")
	shell = strings.Replace(shell, "<body>", `<body data-application-setup>`, 1)
	backdrop, _ := assets.ReadFile("assets/application-home.html")
	shell = strings.Replace(shell, `<div id="homePage"></div>`, `<div id="homePage">`+string(backdrop)+`</div>`, 1)
	setup, _ := assets.ReadFile("assets/application.html")
	return strings.Replace(shell, "</body>", string(setup)+"</body>", 1)
}

// uiHTML extracts the page from ui.py, which wraps it in a Python string.
func uiHTML(source []byte) string {
	text := string(source)
	text = strings.TrimPrefix(text, "APP_HTML = r'''")
	text = strings.TrimSuffix(text, "'''\n")
	return text
}

func querySchemaDocument(name string) ([]byte, error) {
	value, err := schemas.Documents.ReadFile(name + ".schema.json")
	if err != nil {
		return nil, fmt.Errorf("query-table schema not found: %s", name)
	}
	return value, nil
}

func queryTableAsset(name string) ([]byte, error) {
	return assets.ReadFile("assets/" + name)
}

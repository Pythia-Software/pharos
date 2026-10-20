package archive

import (
	"embed"
	"fmt"
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

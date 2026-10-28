package archive

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"time"
)

// servedPages reads the paths isUIPage serves from its source, so the test
// follows the function rather than a copy of its list.
func servedPages(t *testing.T) (paths []string, prefixes []string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "api.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "isUIPage" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.CaseClause:
				for _, expression := range node.List {
					if literal, ok := expression.(*ast.BasicLit); ok {
						path, _ := strconv.Unquote(literal.Value)
						paths = append(paths, path)
					}
				}
			case *ast.CallExpr:
				if selector, ok := node.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "HasPrefix" {
					if literal, ok := node.Args[1].(*ast.BasicLit); ok {
						prefix, _ := strconv.Unquote(literal.Value)
						prefixes = append(prefixes, prefix)
					}
				}
			}
			return true
		})
	}
	if len(paths) < 10 || len(prefixes) == 0 {
		t.Fatalf("read %d paths and %d prefixes from isUIPage", len(paths), len(prefixes))
	}
	return paths, prefixes
}

// browseText calls browse_site through the MCP server, as an agent does,
// and returns the answer and its text.
func browseText(t *testing.T, catalog *Catalog, args map[string]any) (map[string]any, string) {
	t.Helper()
	response := handleMCP(catalog, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "browse_site", "arguments": args}})
	result := response["result"].(map[string]any)
	text := result["content"].([]map[string]any)[0]["text"].(string)
	if result["isError"] == true {
		t.Fatalf("browse_site %v: %s", args, text)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatal(err)
	}
	return value, text
}

func TestBrowseSiteReachesEveryServedPageWithinItsBudget(t *testing.T) {
	catalog, _ := testCatalog(t)
	site := testSiteMap(t)
	// Walk the tree from the top, as an agent would, through every path an
	// answer lists, except the cards and filter fields themselves.
	reached, queue := map[string]bool{}, []string{""}
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		if reached[path] {
			continue
		}
		reached[path] = true
		offset := 0
		for {
			value, text := browseText(t, catalog, map[string]any{"path": path, "offset": offset})
			if len(text) > 3000*3 {
				t.Errorf("%s: %d bytes over the default budget", path, len(text))
			}
			items, _ := value["items"].([]any)
			for _, raw := range items {
				item := raw.(map[string]any)
				child, _ := item["path"].(string)
				if child != "" && !strings.HasPrefix(child, "cards/") && !strings.Contains(child, "/filters/") {
					queue = append(queue, child)
				}
			}
			next, ok := value["next_offset"].(float64)
			if !ok {
				break
			}
			offset = int(next)
		}
	}
	routes := map[string]bool{}
	for path := range reached {
		if node := site.Nodes[path]; node != nil {
			routes[node.Route] = true
		}
	}
	for path := range site.Nodes {
		if !reached[path] {
			t.Errorf("browsing never reaches %s", path)
		}
	}
	paths, prefixes := servedPages(t)
	for _, path := range paths {
		if _, legacy := site.Excluded.LegacyRoutes[path]; !legacy && !routes[path] {
			t.Errorf("isUIPage serves %s, which browse_site never reaches", path)
		}
	}
	for _, prefix := range prefixes {
		found := false
		for route := range routes {
			found = found || strings.HasPrefix(route, prefix) && strings.Contains(route, "{")
		}
		if !found {
			t.Errorf("isUIPage serves %s…, which browse_site never reaches", prefix)
		}
	}
	for route := range routes {
		if !isUIPage(strings.ReplaceAll(route, "{work_id}", "work-1")) {
			t.Errorf("browse_site offers %s, which isUIPage does not serve", route)
		}
	}
	// A small budget sheds list items and pages, and never overruns.
	value, text := browseText(t, catalog, map[string]any{"path": "cards", "max_output_tokens": 1000})
	if len(text) > 1000*3 || value["next_offset"] == nil {
		t.Errorf("cards at 1000 tokens: %d bytes, next_offset %v", len(text), value["next_offset"])
	}
	response := handleMCP(catalog, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "browse_site", "arguments": map[string]any{"path": "cards", "max_output_tokens": 200}}})
	if result := response["result"].(map[string]any); result["isError"] != true || !strings.Contains(compactJSON(result["content"]), "too small") {
		t.Errorf("cards at 200 tokens: %v", result)
	}
}

func TestBrowseSiteListsAFieldsMostFrequentValues(t *testing.T) {
	catalog, _ := testCatalog(t)
	call := func(name string, args map[string]any) {
		handleMCP(catalog, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	}
	for range 3 {
		call("site_link", map[string]any{"page": "analyze/mcp"})
	}
	call("browse_site", map[string]any{"path": "analyze"})
	if err := catalog.flushMCPHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	value, _ := browseText(t, catalog, map[string]any{"path": "analyze/mcp/filters/tool_name"})
	items := value["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["value"] != "site_link" || items[0].(map[string]any)["count"] != float64(3) {
		t.Fatalf("tool_name values %v", items)
	}
	if operators := value["operators"].([]any); len(operators) == 0 || operators[0] != "=" {
		t.Errorf("operators %v", operators)
	}
	// An enum lists its own values; a field whose values take too long to
	// count lists none and says so.
	value, _ = browseText(t, catalog, map[string]any{"path": "analyze/tokens/filters/provider"})
	if !strings.Contains(compactJSON(value["items"]), `"claude"`) {
		t.Errorf("provider values %v", value["items"])
	}
}

// Frequent values come only from rows already computed: a cold or
// in-flight derived value lists none at once, starts nothing, and never
// waits for a build.
func TestBrowseSiteFrequentValuesNeverComputeDerivedRows(t *testing.T) {
	catalog, _ := testCatalog(t)
	release, started := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = cachedValue(context.Background(), catalog, "site-test", func(context.Context) (int, error) {
			close(started)
			<-release
			return 1, nil
		})
	}()
	<-started
	defer close(release)
	begun := time.Now()
	if _, err := catalog.derivedValue(warmOnly(context.Background()), "site-test", func(context.Context) (any, error) { t.Error("computed"); return nil, nil }, true); !errors.Is(err, errDerivedCold) {
		t.Fatalf("warm-only read of a building value: %v", err)
	}
	value, _ := browseText(t, catalog, map[string]any{"path": "analyze/tokens/filters/model_family"})
	if len(value["items"].([]any)) != 0 || !strings.Contains(value["values"].(string), "not computed") || time.Since(begun) > time.Second {
		t.Errorf("cold usage: %v after %s", value, time.Since(begun))
	}
	catalog.derived.mu.Lock()
	for key := range catalog.derived.entries {
		if key != "site-test" {
			t.Errorf("browse_site started computing %s", key)
		}
	}
	catalog.derived.mu.Unlock()
	// Once the page has computed the rows, they are counted.
	if _, _, err := catalog.datasetRows(context.Background(), "usage", libraryFields{"model_family": true}); err != nil {
		t.Fatal(err)
	}
	value, _ = browseText(t, catalog, map[string]any{"path": "analyze/tokens/filters/model_family"})
	if strings.Contains(value["values"].(string), "not computed") {
		t.Errorf("warm usage: %v", value)
	}
}

func TestSiteLinkAnswersThroughMCP(t *testing.T) {
	catalog, _ := testCatalog(t)
	listed := map[string]bool{}
	for _, tool := range handleMCP(catalog, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})["result"].(map[string]any)["tools"].([]map[string]any) {
		listed[tool["name"].(string)] = true
	}
	if !listed["browse_site"] || !listed["site_link"] {
		t.Fatalf("tools/list: %v", listed)
	}
	call := func(args map[string]any) (map[string]any, bool) {
		response := handleMCP(catalog, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "site_link", "arguments": args}})
		result := response["result"].(map[string]any)
		var value map[string]any
		_ = json.Unmarshal([]byte(result["content"].([]map[string]any)[0]["text"].(string)), &value)
		return value, result["isError"] == true
	}
	value, failed := call(map[string]any{"page": "findings", "options": map[string]any{"list": "wins", "repo": "pharos"}})
	if failed || value["url"] != "/findings?list=wins&repo=pharos" || value["title"] != "Findings" {
		t.Fatalf("findings link: %v", value)
	}
	if again, failed := call(map[string]any{"url": value["url"]}); failed || again["url"] != value["url"] || again["canonical"] != true {
		t.Errorf("checked link: %v", again)
	}
	if _, failed := call(map[string]any{"page": "findings", "options": map[string]any{"list": "everything"}}); !failed {
		t.Error("an invalid list was accepted")
	}
}

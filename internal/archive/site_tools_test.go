package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
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
	// A field computed from the stored columns lists none.
	if value, _ := browseText(t, catalog, map[string]any{"path": "analyze/mcp/filters/day"}); len(value["items"].([]any)) != 0 || !strings.Contains(value["values"].(string), "computed") {
		t.Errorf("computed field: %v", value)
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
		text := result["content"].([]map[string]any)[0]["text"].(string)
		if strings.Contains(text, `\u0026`) {
			t.Errorf("site_link query separators need to be directly copyable: %s", text)
		}
		var value map[string]any
		_ = json.Unmarshal([]byte(text), &value)
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

func TestSiteLinkLibrarySnapshotsAndAsk(t *testing.T) {
	catalog, _ := testCatalog(t)
	site := testSiteMap(t)
	terms := []any{}
	for i := 0; i < 70; i++ {
		terms = append(terms, map[string]any{"field": "model", "op": "!=", "value": fmt.Sprintf("model-%d-%s", i, strings.Repeat(fmt.Sprint(i), 40))})
	}
	args := map[string]any{"page": "analyze/query/usage", "query": map[string]any{"select": []any{map[string]any{"field": "model"}}, "where": terms, "limit": 5}, "reveal": "row:test"}
	built, err := site.linkTool(args)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise actual library storage even if this synthetic query compresses
	// well: portable promotion uses the same endpoint store as the browser.
	built["url"] = built["url"].(string) + "&highlight=text:" + strings.Repeat("x", 2500)
	inline := built["url"].(string)
	if _, err := askSiteCard(t.Context(), inline, catalog); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := callSiteMCP(t.Context(), catalog, "site_link", map[string]any{"url": inline}); err != nil || !ok {
		t.Fatal(err)
	}
	refused := inline
	for i := 0; i < 10; i++ {
		refused += "&highlight=text:" + fmt.Sprint(i) + strings.Repeat("x", 2000)
	}
	if _, err := askSiteCard(t.Context(), refused, catalog); err == nil {
		t.Fatal("oversized Ask destination accepted")
	}
	var count int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM query_table_query_snapshots").Scan(&count); err != nil || count != 0 {
		t.Fatal("agent link validation wrote snapshots", count, err)
	}
	// The browser's explicit snapshot endpoint still persists portable definitions.
	address, _ := url.Parse(inline)
	params := address.Query()
	code := "abcdefghijklmnopqrstuv"
	snapshot := qt.QuerySnapshot{Version: 1, Code: code, Token: params.Get("q_usage"), CreatedAt: time.Now().UnixMilli()}
	if _, err := (pharosQuerySnapshotStore{catalog}).Save(t.Context(), queryTableScope, "pharos_usage", snapshot); err != nil {
		t.Fatal(err)
	}
	params.Set("q_usage", "@"+code)
	address.RawQuery = params.Encode()
	built["url"] = address.String()
	resolved, err := site.resolveViewLink(t.Context(), catalog, map[string]any{"url": built["url"]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := site.linkTool(resolved); err != nil {
		t.Fatal(err)
	}
	card, err := askSiteCard(t.Context(), built["url"].(string), catalog)
	if err != nil || !strings.Contains(card.Href, "%40") {
		t.Fatal(card, err)
	}
	value, ok, err := callSiteMCP(t.Context(), catalog, "site_link", map[string]any{"url": built["url"]})
	if err != nil || !ok {
		t.Fatal(value, err)
	}
	view, _ := site.browseTool(t.Context(), catalog, map[string]any{"path": "work/view"})
	if len(view) == 0 {
		t.Fatal("view contract is not discoverable")
	}
	for _, path := range []string{"work/view/reader_search", "analyze/parallelism/view/parallelism", "analyze/query/usage/query/types", "analyze/query/usage/query/types/QueryState", "analyze/query/usage/query/types/GroupingDefinition", "analyze/query/usage/query/types/SummaryColumn", "analyze/query/usage/query/types/GroupKey", "analyze/query/usage/query/types/RepresentativeRow"} {
		help, err := site.browseTool(t.Context(), catalog, map[string]any{"path": path})
		if err != nil || len(help) == 0 {
			t.Fatalf("missing detailed agent help at %s: %v", path, err)
		}
	}
}

func TestSiteMCPViewDiscoveryAndValidation(t *testing.T) {
	catalog, _ := testCatalog(t)
	_, err := (qt.SQLiteComputedColumnStore{DB: catalog.DB}).Save(t.Context(), queryTableScope, "pharos_usage", qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{ID: "scaled", Label: "Scaled", Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: "[total_tokens]/1000"}}})
	if err != nil {
		t.Fatal(err)
	}
	built, ok, err := callSiteMCP(t.Context(), catalog, "site_link", linkArgs(t, `{"page":"analyze/query/usage","query":{"select":[{"field":"@computed/scaled"},{"field":"usage_drill_source"},{"field":"__qt_item_details__"}],"where":[{"field":"@computed/scaled","op":">","value":"1"}]},"days":7}`))
	if err != nil || !ok {
		t.Fatal("saved/virtual columns rejected", built, err)
	}
	if _, err := askSiteCard(t.Context(), built.(map[string]any)["url"].(string), catalog); err != nil {
		t.Fatal("Ask rejected a verified query", err)
	}
	for _, input := range []string{
		`{"page":"library","query":{"select":[{"field":"title"}],"limit":5,"hostState":{"version":1,"params":{}}},"view":{"search_state":{"match":"literal","text":"failure"}}}`,
		`{"page":"analyze/query/usage","query":{"select":[{"field":"model"}],"limit":5,"hostState":{"version":1,"params":{}}},"view":{"record_keyword":{"field":"model","draft":{"match":"literal","text":"failure"}}}}`,
	} {
		built, ok, err := callSiteMCP(t.Context(), catalog, "site_link", linkArgs(t, input))
		if err != nil || !ok {
			t.Fatal("host search composition failed", built, err)
		}
		link := built.(map[string]any)["url"].(string)
		if _, ok, err := callSiteMCP(t.Context(), catalog, "site_link", map[string]any{"url": link}); err != nil || !ok {
			t.Fatal("MCP rejected its own search link", err)
		}
		if _, err := askSiteCard(t.Context(), link, catalog); err != nil {
			t.Fatal("Ask rejected a composed search link", err)
		}
	}
	browseText(t, catalog, map[string]any{"path": "analyze/query/usage/filters/@computed/scaled"})
	_, targetHelp := browseText(t, catalog, map[string]any{"path": "analyze/query/usage/targets"})
	if !strings.Contains(targetHelp, "control:q_usage") {
		t.Fatal("native query control is not discoverable", targetHelp)
	}

	for _, tool := range siteMCPTools {
		description := tool["description"].(string)
		for _, term := range []string{"/view", "/query", "/targets"} {
			if !strings.Contains(description, term) {
				t.Errorf("%s omits %s", tool["name"], term)
			}
		}
	}
	view, _ := browseText(t, catalog, map[string]any{"path": "cards/view"})
	for _, raw := range view["items"].([]any) {
		item := raw.(map[string]any)
		if field, ok := item["field"].(map[string]any); ok {
			if field["pages"] != nil || field["details"] != nil {
				t.Error("view list repeats detail metadata", field)
			}
			if strings.HasPrefix(siteString(item["name"]), "nav_") {
				t.Error("global field in page controls", item)
			}
		}
	}
	browseText(t, catalog, map[string]any{"path": "cards/view/global"})
	help, _ := browseText(t, catalog, map[string]any{"path": "analyze/query/usage/query"})
	if help["time_window"] == nil {
		t.Fatal("missing native time help")
	}
	library, _ := browseText(t, catalog, map[string]any{"path": "library/query"})
	site := testSiteMap(t)
	if compactJSON(library["default"]) == compactJSON(json.RawMessage(site.EmptyQuery)) {
		t.Fatal("library default is generic empty query")
	}
	targets, _ := browseText(t, catalog, map[string]any{"path": "cards/targets"})
	if !strings.Contains(compactJSON(targets["sections"]), "card-library") {
		t.Fatal("source sections are undiscoverable", targets)
	}
	for _, node := range site.Nodes {
		if option := site.optionOfKind(node, "where"); option != nil && strings.HasPrefix(option.Param, "q_") {
			example := site.nativeWindowHelp(node)["example"].(map[string]any)
			if _, err := site.linkTool(example); err != nil {
				t.Errorf("advertised example for %s: %v", node.Path, err)
			}
		}
	}
	response := handleMCP(catalog, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "site_link", "arguments": linkArgs(t, `{"page":"analyze/query/usage","query":{"aggregations":[{"id":"total","op":"total","groupBy":[]}]}}`)}})
	result := response["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatal("MCP reports an invalid metric as success", response)
	}
}

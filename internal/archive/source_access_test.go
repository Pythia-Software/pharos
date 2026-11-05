package archive

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// An adapter reaches its source only through sourceFiles, so it reads a
// capture as captured (decoded, mapped, materialized) and never the live
// source in its place. The functions liveSourceAccess names, with a reason,
// are the only ones that read the OS directly.
func TestAdaptersReadThroughTheAccessLayer(t *testing.T) {
	direct := map[string][]string{
		"os":       {"Open", "OpenFile", "ReadFile", "Stat", "Lstat", "ReadDir"},
		"filepath": {"Walk", "WalkDir", "Glob"},
		"sql":      {"Open"},
		"exec":     {"Command", "CommandContext"},
	}
	openers := []string{"openSourceSQLite", "openReadOnlySQLite"}
	files, err := filepath.Glob("adapter*.go")
	if err != nil {
		t.Fatal(err)
	}
	set := token.NewFileSet()
	bypassing := map[string]bool{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(set, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			// sourceFiles' own methods are the access layer.
			if function.Recv != nil && strings.Contains(types(function.Recv.List[0].Type), "sourceFiles") {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				reason := ""
				switch value := node.(type) {
				case *ast.SelectorExpr:
					if pkg, ok := value.X.(*ast.Ident); ok && slices.Contains(direct[pkg.Name], value.Sel.Name) {
						reason = pkg.Name + "." + value.Sel.Name
					}
				case *ast.Ident:
					if slices.Contains(openers, value.Name) {
						reason = value.Name
					}
				case *ast.CompositeLit:
					if ident, ok := value.Type.(*ast.Ident); ok && ident.Name == "sourceFiles" {
						reason = "a sourceFiles without its adapter's view"
					}
				}
				if reason != "" {
					bypassing[function.Name.Name] = true
					if liveSourceAccess[function.Name.Name] == "" {
						t.Errorf("%s: %s uses %s; read the source through a.files(), or declare why in liveSourceAccess", set.Position(node.Pos()), function.Name.Name, reason)
					}
				}
				return true
			})
		}
	}
	for name, reason := range liveSourceAccess {
		if !bypassing[name] || reason == "" {
			t.Errorf("liveSourceAccess declares %s, which no longer reads the OS directly, or gives no reason", name)
		}
	}
}

// types is a receiver's type as written.
func types(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.StarExpr:
		return "*" + types(value.X)
	case *ast.Ident:
		return value.Name
	}
	return ""
}

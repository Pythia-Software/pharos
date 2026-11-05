package archive

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// An adapter reaches its source only through sourceFiles, so it reads a
// capture as captured (decoded, mapped, materialized) and never the live
// source in its place. The functions liveSourceAccess names, with a reason,
// are the only ones that read the OS directly.
func TestAdaptersReadThroughTheAccessLayer(t *testing.T) {
	// By import path, so a renamed import is caught too.
	direct := map[string][]string{
		"os":            {"Open", "OpenFile", "ReadFile", "Stat", "Lstat", "ReadDir", "DirFS"},
		"io/ioutil":     {"ReadFile", "ReadDir"},
		"path/filepath": {"Walk", "WalkDir", "Glob"},
		"database/sql":  {"Open"},
		"os/exec":       {"Command", "CommandContext"},
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
		imports := map[string]string{}
		for _, spec := range parsed.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			name := filepath.Base(path)
			if spec.Name != nil {
				name = spec.Name.Name
			}
			imports[name] = path
		}
		check := func(scope string, root ast.Node) {
			ast.Inspect(root, func(node ast.Node) bool {
				reason := ""
				switch value := node.(type) {
				case *ast.SelectorExpr:
					if pkg, ok := value.X.(*ast.Ident); ok && pkg.Obj == nil && slices.Contains(direct[imports[pkg.Name]], value.Sel.Name) {
						reason = imports[pkg.Name] + "." + value.Sel.Name
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
					bypassing[scope] = true
					if liveSourceAccess[scope] == "" {
						t.Errorf("%s: %s uses %s; read the source through a.files(), or declare why in liveSourceAccess", set.Position(node.Pos()), scope, reason)
					}
				}
				return true
			})
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				// A package-level value (var read = os.ReadFile) belongs to no
				// function, so it can't be declared.
				check("package scope of "+file, declaration)
				continue
			}
			if function.Body == nil {
				continue
			}
			// sourceFiles' own methods are the access layer.
			if function.Recv != nil && strings.Contains(types(function.Recv.List[0].Type), "sourceFiles") {
				continue
			}
			check(function.Name.Name, function.Body)
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

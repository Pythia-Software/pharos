// Package architecture checks source boundaries without importing the service,
// loading its catalog, or compiling another operating system's implementation.
package architecture

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type finding struct {
	file, owner, operation string
	line                   int
}

func (f finding) String() string {
	return fmt.Sprintf("%s:%d: %s in %s belongs behind a reviewed native boundary", f.file, f.line, f.operation, f.owner)
}

func guardedImport(name string) bool {
	return name == "C" || name == "syscall" || name == "syscall/js" || name == "golang.org/x/sys/unix" || name == "golang.org/x/sys/windows" || name == "golang.org/x/sys/plan9"
}

func portableSyscall(name string) bool {
	// Error/signal values and signal registration are different from native
	// observations, ABI structures, process attributes and raw system calls.
	return name == "Errno" || name == "Signal" || strings.HasPrefix(name, "SIG") ||
		len(name) > 1 && name[0] == 'E' && name[1] >= 'A' && name[1] <= 'Z'
}

var nativeCommands = map[string]bool{
	"diskutil": true, "hdiutil": true, "mdutil": true, "ioreg": true,
	"scutil": true, "osascript": true, "pmset": true, "plutil": true,
	"launchctl": true, "sysctl": true, "sw_vers": true, "system_profiler": true,
	"open": true, "xdg-open": true, "powershell": true, "powershell.exe": true,
	"cmd.exe": true, "taskkill": true, "ps": true, "sh": true, "bash": true, "zsh": true,
}

func nativePath(value string) bool {
	for _, prefix := range []string{"/proc", "/sys", "/Volumes", "/Applications", "/System/Library", "/Library/Application Support", "~/Library/"} {
		if value == prefix || strings.HasPrefix(value, strings.TrimSuffix(prefix, "/")+"/") {
			return true
		}
	}
	return false
}

func initializer(id *ast.Ident) ast.Expr {
	if id.Obj == nil {
		return nil
	}
	var names []ast.Expr
	var values []ast.Expr
	switch decl := id.Obj.Decl.(type) {
	case *ast.ValueSpec:
		for _, name := range decl.Names {
			names = append(names, name)
		}
		values = decl.Values
	case *ast.AssignStmt:
		names, values = decl.Lhs, decl.Rhs
	}
	for i, name := range names {
		if name, ok := name.(*ast.Ident); ok && name.Name == id.Name && i < len(values) {
			return values[i]
		}
	}
	return nil
}

// inspectGo parses every platform's source. Import aliases and selector values
// count too: assigning syscall.Kill to a variable is still native access.
func inspectGo(name string, source []byte) ([]finding, bool, error) {
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, name, source, parser.ParseComments)
	if err != nil {
		return nil, false, err
	}
	imports := map[string]string{}
	var found []finding
	add := func(at token.Pos, owner, op string) {
		found = append(found, finding{name, owner, op, set.Position(at).Line})
	}
	for _, spec := range file.Imports {
		pkg, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, false, err
		}
		alias := path.Base(pkg)
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		if alias == "." && (guardedImport(pkg) || pkg == "os/exec") {
			add(spec.Pos(), "<imports>", "dot-import:"+pkg)
		}
		imports[alias] = pkg
		if guardedImport(pkg) && pkg != "syscall" {
			add(spec.Pos(), "<imports>", "import:"+pkg)
		}
	}
	// Simple bindings are resolved from the parser's lexical objects, so renamed
	// imports, shadowed locals and simple command/path aliases stay distinct.
	var text func(ast.Expr, map[*ast.Object]bool) (string, bool)
	text = func(expr ast.Expr, seen map[*ast.Object]bool) (string, bool) {
		switch node := expr.(type) {
		case *ast.BasicLit:
			if node.Kind == token.STRING {
				value, err := strconv.Unquote(node.Value)
				return value, err == nil
			}
		case *ast.ParenExpr:
			return text(node.X, seen)
		case *ast.BinaryExpr:
			if node.Op == token.ADD {
				a, okA := text(node.X, seen)
				b, okB := text(node.Y, seen)
				return a + b, okA && okB
			}
		case *ast.Ident:
			if node.Obj == nil || seen[node.Obj] {
				break
			}
			if expr := initializer(node); expr != nil {
				seen[node.Obj] = true
				value, ok := text(expr, seen)
				delete(seen, node.Obj)
				return value, ok
			}
		}
		return "", false
	}
	var visit func(ast.Node, string)
	var commandKind func(ast.Expr, map[*ast.Object]bool) string
	commandKind = func(expr ast.Expr, seen map[*ast.Object]bool) string {
		switch n := expr.(type) {
		case *ast.ParenExpr:
			return commandKind(n.X, seen)
		case *ast.SelectorExpr:
			if id, ok := n.X.(*ast.Ident); ok && id.Obj == nil && imports[id.Name] == "os/exec" {
				return n.Sel.Name
			}
		case *ast.Ident:
			if n.Obj != nil && !seen[n.Obj] {
				if expr := initializer(n); expr != nil {
					seen[n.Obj] = true
					return commandKind(expr, seen)
				}
			}
		}
		return ""
	}
	visit = func(node ast.Node, owner string) {
		switch n := node.(type) {
		case *ast.FuncDecl:
			owner = n.Name.Name
		case *ast.ValueSpec:
			if owner == "<package>" && len(n.Names) == 1 {
				owner = n.Names[0].Name
			}
		case *ast.SelectorExpr:
			if id, ok := n.X.(*ast.Ident); ok && id.Obj == nil {
				pkg := imports[id.Name]
				if guardedImport(pkg) && !(pkg == "syscall" && portableSyscall(n.Sel.Name)) {
					add(n.Pos(), owner, pkg+"."+n.Sel.Name)
				}
				if pkg == "os" && (n.Sel.Name == "Getuid" || n.Sel.Name == "Geteuid" || n.Sel.Name == "Getgid" || n.Sel.Name == "Getegid" || n.Sel.Name == "Getgroups" || n.Sel.Name == "Chown" || n.Sel.Name == "Lchown") {
					add(n.Pos(), owner, "os."+n.Sel.Name)
				}
			}
			if n.Sel.Name == "SysProcAttr" {
				if _, ok := n.X.(*ast.Ident); !ok || imports[n.X.(*ast.Ident).Name] == "" {
					add(n.Pos(), owner, "process.SysProcAttr")
				}
			}
		case *ast.CallExpr:
			commandArg := -1
			switch commandKind(n.Fun, map[*ast.Object]bool{}) {
			case "Command":
				commandArg = 0
			case "CommandContext":
				commandArg = 1
			}
			if id, ok := n.Fun.(*ast.Ident); ok && commandArg < 0 {
				switch id.Name {
				case "driveTool":
					commandArg = 1
				case "commandOutput":
					commandArg = 0
				}
			}
			if commandArg >= 0 && commandArg < len(n.Args) {
				if value, ok := text(n.Args[commandArg], map[*ast.Object]bool{}); ok && nativeCommands[path.Base(strings.ReplaceAll(value, "\\", "/"))] {
					add(n.Pos(), owner, "command:"+path.Base(value))
				}
			}
			for _, arg := range n.Args {
				if _, literal := arg.(*ast.BasicLit); !literal {
					if value, ok := text(arg, map[*ast.Object]bool{}); ok && nativePath(value) {
						add(arg.Pos(), owner, "native-path:"+value)
					}
				}
			}
		case *ast.BasicLit:
			if n.Kind == token.STRING {
				value, _ := strconv.Unquote(n.Value)
				if nativePath(value) {
					add(n.Pos(), owner, "native-path:"+value)
				}
			}
		}
		ast.Inspect(node, func(child ast.Node) bool {
			if child == nil {
				return false
			}
			if child == node {
				return true
			}
			visit(child, owner)
			return false
		})
	}
	visit(file, "<package>")
	gated := false
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.HasPrefix(comment.Text, "//go:build ") {
				expr, err := constraint.Parse(comment.Text)
				if err != nil {
					return nil, false, err
				}
				// An OS/unix/cgo condition must limit the build, even when
				// architecture tags match. An architecture-only gate is insufficient.
				for _, target := range []string{"darwin", "linux", "windows"} {
					if expr.Eval(func(value string) bool {
						return value == target || value == "cgo" || value == "arm64" || value == "amd64" || value == "unix" && target != "windows"
					}) && !expr.Eval(func(value string) bool { return value == "arm64" || value == "amd64" }) {
						gated = true
					}
				}
			}
		}
	}
	base := strings.TrimSuffix(filepath.Base(name), ".go")
	parts := strings.Split(base, "_")
	last := parts[len(parts)-1]
	if (last == "arm64" || last == "amd64" || last == "386" || last == "arm") && len(parts) > 1 {
		last = parts[len(parts)-2]
	}
	gated = gated || last == "darwin" || last == "linux" || last == "windows"
	return found, gated, nil
}

func goSources(root fs.FS) ([]string, error) {
	var names []string
	for _, dir := range []string{"internal", "cmd"} {
		err := fs.WalkDir(root, dir, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() && (entry.Name() == "vendor" || entry.Name() == "testdata" || name == "internal/architecture") {
				return fs.SkipDir
			}
			if !entry.IsDir() && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") && (strings.HasPrefix(name, "internal/") || strings.HasPrefix(name, "cmd/")) {
				names = append(names, name)
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	sort.Strings(names)
	return names, nil
}

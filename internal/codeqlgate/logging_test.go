package codeqlgate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var escapingHandlers = map[string]bool{"NewTextHandler": true, "NewJSONHandler": true, "DiscardHandler": true}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test")
		}
		dir = parent
	}
}

func loggingProblems(path string, file *ast.File) []string {
	var problems []string
	for _, spec := range file.Imports {
		if spec.Path.Value == `"log"` {
			problems = append(problems, path+": imports the standard log package, which writes values unescaped")
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.SelectorExpr:
			if pkg, ok := n.X.(*ast.Ident); ok && pkg.Name == "slog" && n.Sel.Name == "Record" {
				problems = append(problems, path+": implements a slog handler, which may write values unescaped")
			}
		case *ast.CallExpr:
			selector, ok := n.Fun.(*ast.SelectorExpr)
			if !ok || len(n.Args) != 1 {
				return true
			}
			if pkg, ok := selector.X.(*ast.Ident); !ok || pkg.Name != "slog" || selector.Sel.Name != "New" {
				return true
			}
			handler := n.Args[0]
			if call, ok := handler.(*ast.CallExpr); ok {
				handler = call.Fun
			}
			if inner, ok := handler.(*ast.SelectorExpr); !ok || !escapingHandlers[inner.Sel.Name] {
				problems = append(problems, path+": builds a logger on a handler other than slog's text, JSON or discard handler")
			}
		}
		return true
	})
	return problems
}

func TestEveryLoggerEscapesTheValuesItWrites(t *testing.T) {
	root := repositoryRoot(t)
	fset := token.NewFileSet()
	var problems []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == "testdata") {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly|parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		if len(file.Imports) == 0 {
			return nil
		}
		file, err = parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		problems = append(problems, loggingProblems(relative, file)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range problems {
		t.Error(problem)
	}
}

func TestTheLoggingRuleCatchesWhatItForbids(t *testing.T) {
	for name, source := range map[string]string{
		"standard log":   "package p\nimport \"log\"\nfunc f(s string) { log.Print(s) }\n",
		"custom handler": "package p\nimport (\"context\"; \"log/slog\")\ntype h struct{}\nfunc (h) Handle(context.Context, slog.Record) error { return nil }\n",
		"other handler":  "package p\nimport \"log/slog\"\nvar l = slog.New(custom())\n",
	} {
		t.Run(name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "p.go", source, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(loggingProblems("p.go", file)) == 0 {
				t.Fatal("accepted, want refused")
			}
		})
	}
	file, err := parser.ParseFile(token.NewFileSet(), "p.go", "package p\nimport (\"log/slog\"; \"os\")\nvar a = slog.New(slog.NewTextHandler(os.Stderr, nil))\nvar b = slog.New(slog.DiscardHandler)\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	if problems := loggingProblems("p.go", file); len(problems) != 0 {
		t.Fatalf("refused slog's own handlers: %v", problems)
	}
}

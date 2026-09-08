package sqlitestore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSQLiteCompatibilityConstructorsHaveDeprecationDirectives(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	for _, target := range []struct {
		file, function string
	}{
		{file: "pkg/session/jsonl_backend.go", function: "NewJSONLBackend"},
		{file: "pkg/workflows/store.go", function: "NewFileRunStore"},
	} {
		t.Run(target.function, func(t *testing.T) {
			path := filepath.Join(repositoryRoot, filepath.FromSlash(target.file))
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			for _, declaration := range parsed.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Name.Name != target.function {
					continue
				}
				if function.Doc == nil || !strings.Contains(function.Doc.Text(), "Deprecated:") {
					t.Fatalf("%s lacks an attached Go deprecation directive", target.function)
				}
				return
			}
			t.Fatalf("function %s is missing", target.function)
		})
	}
}

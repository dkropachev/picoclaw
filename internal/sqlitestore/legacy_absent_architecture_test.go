package sqlitestore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSealedAbsentLegacyRootPolicyIsConfinedToOfflineStorageBoundary(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve sealed absent policy architecture source")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	allowed := map[string]struct{}{
		"internal/sqlitestore/legacy.go":    {},
		"internal/sqlitestore/open.go":      {},
		"pkg/repoaudit/database_adapter.go": {},
	}
	guarded := map[string]struct{}{
		"LegacySourceRootPolicy":       {},
		"LegacySourceRootSealedAbsent": {},
		"SourceRootPolicy":             {},
	}

	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".cache", "build", "node_modules", "vendor":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(repositoryRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if _, ok := allowed[relative]; ok {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		var found string
		ast.Inspect(parsed, func(node ast.Node) bool {
			identifier, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			if _, guarded := guarded[identifier.Name]; guarded {
				found = identifier.Name
				return false
			}
			return true
		})
		if found != "" {
			t.Errorf("%s references sealed absent legacy policy boundary %s", relative, found)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

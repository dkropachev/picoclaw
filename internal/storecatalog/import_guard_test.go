package storecatalog

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const storeCatalogImportPath = "github.com/sipeed/picoclaw/internal/storecatalog"

func TestStoreCatalogHasNoProductionImporters(t *testing.T) {
	t.Parallel()

	repositoryRoot := storeCatalogRepositoryRoot(t)
	allowedImporters := map[string]struct{}{
		"internal/databaseclaims/claims.go":            {},
		"internal/databasemigration/backup.go":         {},
		"internal/databasemigration/backup_archive.go": {},
		"internal/databasemigration/backup_parent.go":  {},
		"internal/databasemigration/migration.go":      {},
		"internal/databasereadiness/readiness.go":      {},
		"pkg/database/catalog/catalog.go":              {},
	}
	var violations []string
	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != repositoryRoot && storeCatalogImportGuardSkipsDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		relative, err := filepath.Rel(repositoryRoot, path)
		if err != nil {
			return fmt.Errorf("resolve repository-relative path for %q: %w", path, err)
		}
		relative = filepath.ToSlash(relative)

		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse production Go file %s: %w", relative, err)
		}
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return fmt.Errorf("decode import in %s: %w", relative, err)
			}
			if importPath != storeCatalogImportPath {
				continue
			}
			if _, allowed := allowedImporters[relative]; allowed {
				continue
			}
			violations = append(violations, fmt.Sprintf("%s:%d", relative, fileSet.Position(imported.Pos()).Line))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan production imports: %v", err)
	}
	if len(violations) == 0 {
		return
	}
	sort.Strings(violations)
	t.Fatalf(
		"dormant store catalog imported by production code; add only reviewed integration files to the exact allowlist:\n%s",
		strings.Join(violations, "\n"),
	)
}

func storeCatalogRepositoryRoot(t *testing.T) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve import-guard source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func storeCatalogImportGuardSkipsDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".cache", "cache", "generated", "gen", "node_modules", "testdata", "vendor":
		return true
	default:
		return false
	}
}

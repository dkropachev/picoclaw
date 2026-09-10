package databaseproviderlease

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const providerLeaseImportPath = "github.com/sipeed/picoclaw/internal/databaseproviderlease"

func TestProviderLeaseProductionBoundaryIsExact(t *testing.T) {
	repositoryRoot := providerLeaseRepositoryRoot(t)
	violations, err := providerLeaseProductionBoundaryViolations(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("database provider lease boundary mismatch:\n%s", strings.Join(violations, "\n"))
	}
}

func providerLeaseProductionBoundaryViolations(repositoryRoot string) ([]string, error) {
	roles := map[string]string{
		"internal/databaseclaims/provider_lease.go":   "claims",
		"internal/sqliteprovider/provider_offline.go": "provider",
	}
	allowedReferences := map[string]map[string]bool{
		"claims": {
			"New": true, "Revoke": true, "Wait": true, "Lease": true, "Hooks": true,
		},
		"provider": {
			"Consume": true, "Lease": true, "Access": true,
		},
	}
	var violations []string
	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != repositoryRoot && providerLeaseGuardSkipsDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") ||
			filepath.Clean(filepath.Dir(path)) == filepath.Join(
				repositoryRoot, "internal", "databaseproviderlease",
			) {
			return nil
		}
		relative, err := filepath.Rel(repositoryRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.AllErrors)
		if err != nil {
			return fmt.Errorf("parse %s: %w", relative, err)
		}
		aliases := make(map[string]struct{})
		for _, imported := range parsed.Imports {
			importPath, unquoteErr := strconv.Unquote(imported.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			if importPath != providerLeaseImportPath {
				continue
			}
			role, allowed := roles[relative]
			if !allowed {
				violations = append(violations, fmt.Sprintf(
					"%s:%d is not an approved provider-lease importer",
					relative, fileSet.Position(imported.Pos()).Line,
				))
				continue
			}
			_ = role
			alias := "databaseproviderlease"
			if imported.Name != nil {
				alias = imported.Name.Name
			}
			if alias == "." || alias == "_" {
				violations = append(violations, fmt.Sprintf(
					"%s:%d cannot use a dot or blank provider-lease import",
					relative, fileSet.Position(imported.Pos()).Line,
				))
				continue
			}
			aliases[alias] = struct{}{}
		}
		if len(aliases) == 0 {
			return nil
		}
		role := roles[relative]
		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			identifier, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if _, imported := aliases[identifier.Name]; !imported {
				return true
			}
			if !allowedReferences[role][selector.Sel.Name] {
				violations = append(violations, fmt.Sprintf(
					"%s:%d role %s cannot reference databaseproviderlease.%s",
					relative, fileSet.Position(selector.Pos()).Line, role, selector.Sel.Name,
				))
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(violations)
	return violations, nil
}

func TestProviderLeaseProductionBoundaryRejectsUnauthorizedReferences(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"internal/databaseclaims/provider_lease.go": `package databaseclaims
import lease "github.com/sipeed/picoclaw/internal/databaseproviderlease"
var _ = lease.Consume
`,
		"internal/sqliteprovider/provider_offline.go": `package sqliteprovider
import _ "github.com/sipeed/picoclaw/internal/databaseproviderlease"
`,
		"internal/other/other.go": `package other
import lease "github.com/sipeed/picoclaw/internal/databaseproviderlease"
var _ = lease.New
`,
		"internal/generated/escape.go": `package generated
import . "github.com/sipeed/picoclaw/internal/databaseproviderlease"
`,
	}
	for relative, source := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	violations, err := providerLeaseProductionBoundaryViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(violations, "\n")
	for _, wanted := range []string{
		"internal/databaseclaims/provider_lease.go:3 role claims cannot reference databaseproviderlease.Consume",
		"internal/sqliteprovider/provider_offline.go:2 cannot use a dot or blank provider-lease import",
		"internal/other/other.go:2 is not an approved provider-lease importer",
		"internal/generated/escape.go:2 is not an approved provider-lease importer",
	} {
		if !strings.Contains(joined, wanted) {
			t.Errorf("provider lease violations missing %q:\n%s", wanted, joined)
		}
	}
}

func providerLeaseRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve provider-lease guard source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}

func providerLeaseGuardSkipsDirectory(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return true
	}
	switch strings.ToLower(name) {
	case "node_modules", "testdata", "vendor":
		return true
	default:
		return false
	}
}

package sqliteprovider

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const transactionBoundaryConsumer = "internal/sqlitestore/open.go"

func TestTransactionBoundaryConsumptionAndHooksAreNarrow(t *testing.T) {
	t.Parallel()
	violations, err := transactionBoundaryArchitectureViolations(sqliteProviderRepositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("SQLite transaction boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestTransactionBoundaryArchitectureRejectsUnauthorizedForms(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		transactionBoundaryConsumer: `package sqlitestore
import (
    "database/sql"
    provider "github.com/sipeed/picoclaw/internal/sqliteprovider"
)
func allowed(conn *sql.Conn) { _, _ = provider.NewTransactionBoundary(conn) }
`,
		"internal/rogue/direct.go": `package rogue
import provider "github.com/sipeed/picoclaw/internal/sqliteprovider"
type copied = provider.TransactionBoundary
var _ = provider.NewTransactionBoundary
`,
		"internal/rogue/dot.go": `package rogue
import . "github.com/sipeed/picoclaw/internal/sqliteprovider"
var _ TransactionBoundary
`,
		"internal/rogue/hooks.go": `package rogue
type registrar interface {
    RegisterCommitHook(func() int32)
    RegisterRollbackHook(func())
}
func replaceHooks(value registrar) {
    value.RegisterCommitHook(nil)
    value.RegisterRollbackHook(nil)
}
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
	violations, err := transactionBoundaryArchitectureViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(violations, "\n")
	if strings.Contains(joined, transactionBoundaryConsumer) {
		t.Fatalf("approved transaction consumer was rejected:\n%s", joined)
	}
	for _, expected := range []string{
		"cannot consume sqliteprovider.TransactionBoundary",
		"cannot consume sqliteprovider.NewTransactionBoundary",
		"cannot consume dot-imported sqliteprovider.TransactionBoundary",
		"cannot control SQLite transaction hooks",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("transaction architecture guard missing %q:\n%s", expected, joined)
		}
	}
}

func transactionBoundaryArchitectureViolations(repositoryRoot string) ([]string, error) {
	protectedProviderNames := map[string]struct{}{
		"TransactionBoundary":    {},
		"NewTransactionBoundary": {},
	}
	var violations []string
	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != repositoryRoot && sqliteProviderImportGuardSkipsDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
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
			return fmt.Errorf("parse production Go file %s: %w", relative, err)
		}
		providerAliases := make(map[string]struct{})
		dotProvider := false
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if importPath == "modernc.org/sqlite" {
				alias := "sqlite"
				if imported.Name != nil {
					alias = imported.Name.Name
				}
				if alias != "_" && filepath.Dir(relative) != "internal/sqliteprovider" {
					violations = append(violations, fmt.Sprintf(
						"%s:%d cannot import SQLite hook types",
						relative,
						fileSet.Position(imported.Pos()).Line,
					))
				}
			}
			if importPath != sqliteProviderImportPath {
				continue
			}
			alias := "sqliteprovider"
			if imported.Name != nil {
				alias = imported.Name.Name
			}
			switch alias {
			case ".":
				dotProvider = true
			case "_":
			default:
				providerAliases[alias] = struct{}{}
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.SelectorExpr:
				if value.Sel.Name == "RegisterCommitHook" ||
					value.Sel.Name == "RegisterRollbackHook" {
					if relative != "internal/sqliteprovider/transaction_boundary.go" {
						violations = append(violations, fmt.Sprintf(
							"%s:%d cannot control SQLite transaction hooks",
							relative,
							fileSet.Position(value.Pos()).Line,
						))
					}
				}
				receiver, ok := value.X.(*ast.Ident)
				if !ok {
					return true
				}
				if _, imported := providerAliases[receiver.Name]; !imported {
					return true
				}
				if _, protected := protectedProviderNames[value.Sel.Name]; protected &&
					relative != transactionBoundaryConsumer {
					violations = append(violations, fmt.Sprintf(
						"%s:%d cannot consume sqliteprovider.%s",
						relative,
						fileSet.Position(value.Pos()).Line,
						value.Sel.Name,
					))
				}
			case *ast.Ident:
				if !dotProvider {
					return true
				}
				if _, protected := protectedProviderNames[value.Name]; protected &&
					relative != transactionBoundaryConsumer {
					violations = append(violations, fmt.Sprintf(
						"%s:%d cannot consume dot-imported sqliteprovider.%s",
						relative,
						fileSet.Position(value.Pos()).Line,
						value.Name,
					))
				}
			}
			return true
		})
		return nil
	})
	sort.Strings(violations)
	return violations, err
}

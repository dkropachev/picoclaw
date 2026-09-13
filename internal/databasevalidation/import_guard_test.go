package databasevalidation

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const databaseValidationImportPath = "github.com/sipeed/picoclaw/internal/databasevalidation"

func TestExactDatabaseValidationCapabilityImportersAreNarrow(t *testing.T) {
	t.Parallel()
	violations, err := databaseValidationImportBoundaryViolations(
		databaseValidationRepositoryRoot(t),
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("exact database-validation import boundary changed:\n%s", strings.Join(violations, "\n"))
	}
}

func databaseValidationImportBoundaryViolations(
	repositoryRoot string,
	requireExpected bool,
) ([]string, error) {
	allowed := map[string]bool{
		"internal/databaseadapter/registry.go":             false,
		"internal/sqliteprovider/inspection_validation.go": false,
	}
	var violations []string
	err := filepath.WalkDir(repositoryRoot, func(
		path string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != repositoryRoot && databaseValidationGuardSkipsDir(entry.Name()) {
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
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse production Go file %s: %w", relative, err)
		}
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if importPath != databaseValidationImportPath {
				continue
			}
			if _, ok := allowed[relative]; !ok || imported.Name != nil &&
				imported.Name.Name == "." {
				violations = append(violations, fmt.Sprintf(
					"%s:%d imports the exact database-validation capability",
					relative,
					fileSet.Position(imported.Pos()).Line,
				))
			} else {
				allowed[relative] = true
			}
		}
		return nil
	})
	if requireExpected {
		for path, found := range allowed {
			if !found {
				violations = append(violations, path+": expected capability importer is missing")
			}
		}
	}
	sort.Strings(violations)
	return violations, err
}

func TestExactDatabaseValidationImportGuardRejectsSkippedNameBypasses(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rogue := filepath.Join(root, "internal", "cache", "rogue.go")
	if err := os.MkdirAll(filepath.Dir(rogue), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rogue, []byte(`package cache
import "github.com/sipeed/picoclaw/internal/databasevalidation"
var _ databasevalidation.Generation
`), 0o600); err != nil {
		t.Fatal(err)
	}
	violations, err := databaseValidationImportBoundaryViolations(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || !strings.Contains(violations[0], "internal/cache/rogue.go") {
		t.Fatalf("guard bypass violations = %#v", violations)
	}
}

func TestExactDatabaseValidationAPIIsScalarOnly(t *testing.T) {
	t.Parallel()
	generation := reflect.TypeOf((*Generation)(nil)).Elem()
	if generation.NumMethod() != 3 {
		t.Fatalf("Generation method count = %d, want 3", generation.NumMethod())
	}
	read, ok := generation.MethodByName("ReadScalar")
	if !ok || !read.Type.IsVariadic() || read.Type.NumIn() != 2 ||
		read.Type.In(0).Kind() != reflect.String ||
		read.Type.In(1) != reflect.TypeOf([]string{}) ||
		read.Type.NumOut() != 2 || read.Type.Out(0) != reflect.TypeOf(Scalar{}) ||
		read.Type.Out(1) != reflect.TypeOf((*error)(nil)).Elem() {
		t.Fatalf("ReadScalar signature = %v", read.Type)
	}
	if _, ok := generation.MethodByName("StoreID"); !ok {
		t.Fatal("Generation.StoreID is missing")
	}
	if _, ok := generation.MethodByName("Domain"); !ok {
		t.Fatal("Generation.Domain is missing")
	}
	if scalar := reflect.TypeOf(Scalar{}); scalar.NumField() != 3 ||
		scalar.Field(0).Name != "Kind" || scalar.Field(1).Name != "Int64" ||
		scalar.Field(2).Name != "Text" {
		t.Fatalf("Scalar surface = %v", scalar)
	}
}

func databaseValidationRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve database-validation guard path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func databaseValidationGuardSkipsDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".cache", "node_modules", "testdata", "vendor":
		return true
	default:
		return false
	}
}

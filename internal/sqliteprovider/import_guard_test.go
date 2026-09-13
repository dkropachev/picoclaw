package sqliteprovider

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

const sqliteProviderImportPath = "github.com/sipeed/picoclaw/internal/sqliteprovider"

const immutableGenerationSourceMinter = "internal/databasemigration/backup_prepare.go"

var validatedReplacementConsumers = map[string]map[string]bool{
	"MigrateStagedOfflineFromWithLiveVerification": {
		"internal/databasemigration/migration.go": true,
	},
	"StagedLiveVerification": {
		"internal/databasemigration/migration.go": true,
	},
	"ValidatedReplacement": {
		"internal/databasemigration/backup_archive.go": true,
		"internal/databasemigration/migration.go":      true,
	},
	"ValidatedReplacementCheck": {
		"internal/databasemigration/backup_archive.go": true,
	},
	"ValidatedTargetParentCheck": {
		"internal/databasemigration/backup_archive.go": true,
	},
}

const validatedReplacementUseConsumer = "internal/databasemigration/backup_archive.go"

var inspectionDomainValidationConsumers = map[string]bool{
	"internal/databasemigration/migration.go": true,
	"internal/databasereadiness/readiness.go": true,
}

var inspectionDomainValidationExpectedCalls = map[string]int{
	"internal/databasemigration/migration.go": 2,
	"internal/databasereadiness/readiness.go": 1,
}

func TestSQLiteProviderBoundaryVersionIsStable(t *testing.T) {
	if providerBoundaryVersion != "picoclaw/sqlite-provider-boundary/v1" {
		t.Fatalf("provider boundary version = %q", providerBoundaryVersion)
	}
}

func TestSQLiteProviderProductionImportersAreExplicit(t *testing.T) {
	t.Parallel()

	repositoryRoot := sqliteProviderRepositoryRoot(t)
	allowed := map[string]bool{
		"internal/databasemigration/backup.go":         true,
		"internal/databasemigration/backup_archive.go": true,
		"internal/databasemigration/migration.go":      true,
		"internal/databasereadiness/readiness.go":      true,
		"internal/sqlitestore/open.go":                 false,
		"internal/sqlitestore/schema.go":               false,
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
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") ||
			filepath.Clean(filepath.Dir(path)) == filepath.Join(repositoryRoot, "internal", "sqliteprovider") {
			return nil
		}
		relative, relativeErr := filepath.Rel(repositoryRoot, path)
		if relativeErr != nil {
			return relativeErr
		}
		relative = filepath.ToSlash(relative)
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.AllErrors)
		if err != nil {
			return fmt.Errorf("parse production Go file %s: %w", relative, err)
		}
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if importPath != sqliteProviderImportPath {
				continue
			}
			if _, ok := allowed[relative]; !ok && relative != immutableGenerationSourceMinter {
				violations = append(violations, fmt.Sprintf(
					"%s:%d", relative, fileSet.Position(imported.Pos()).Line,
				))
			} else if ok {
				allowed[relative] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan production imports: %v", err)
	}
	mintViolations, mintErr := immutableGenerationSourceMintViolations(repositoryRoot)
	if mintErr != nil {
		t.Fatalf("scan immutable source mints: %v", mintErr)
	}
	violations = append(violations, mintViolations...)
	for path, found := range allowed {
		if !found {
			violations = append(violations, path+": expected provider importer is missing")
		}
	}
	if len(violations) != 0 {
		sort.Strings(violations)
		t.Fatalf("SQLite provider importer allowlist mismatch:\n%s", strings.Join(violations, "\n"))
	}
}

func TestSQLiteProviderImmutableSourceMintBoundaryRejectsUnauthorizedCallers(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	files := map[string]string{
		immutableGenerationSourceMinter: `package databasemigration
import provider "github.com/sipeed/picoclaw/internal/sqliteprovider"
var _ = provider.NewImmutableGenerationSource
`,
		"internal/databasemigration/migration.go": `package databasemigration
import provider "github.com/sipeed/picoclaw/internal/sqliteprovider"
var _ = provider.NewImmutableGenerationSource
`,
		"internal/databasereadiness/readiness.go": `package databasereadiness
import . "github.com/sipeed/picoclaw/internal/sqliteprovider"
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
	violations, err := immutableGenerationSourceMintViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(violations, "\n")
	if strings.Contains(joined, immutableGenerationSourceMinter) {
		t.Fatalf("approved immutable-source minter was rejected:\n%s", joined)
	}
	for _, expected := range []string{
		"internal/databasemigration/migration.go:3 cannot mint",
		"internal/databasereadiness/readiness.go:2 cannot use a dot",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("mint violations missing %q:\n%s", expected, joined)
		}
	}
}

func TestSQLiteProviderValidatedReplacementBoundaryIsNarrow(t *testing.T) {
	t.Parallel()
	violations, err := validatedReplacementBoundaryViolations(sqliteProviderRepositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf(
			"validated SQLite replacement boundary violations:\n%s",
			strings.Join(violations, "\n"),
		)
	}
}

func TestSQLiteProviderInspectionDomainValidationConsumersAreNarrow(t *testing.T) {
	t.Parallel()
	violations, err := inspectionDomainValidationBoundaryViolations(
		sqliteProviderRepositoryRoot(t),
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("SQLite inspection domain-validation boundary changed:\n%s", strings.Join(violations, "\n"))
	}
}

func TestSQLiteProviderInspectionDomainValidationGuardRejectsEscapeForms(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	files := map[string]string{
		"internal/databasemigration/migration.go": `package databasemigration
func allowed(inspection interface{ ValidateDomain() error }) error {
    if err := inspection.ValidateDomain(); err != nil { return err }
    return inspection.ValidateDomain()
}
`,
		"internal/databasereadiness/readiness.go": `package databasereadiness
func allowed(inspection interface{ ValidateDomain() error }) error {
    return inspection.ValidateDomain()
}
`,
		"internal/rogue/direct.go": `package rogue
func denied(inspection interface{ ValidateDomain() error }) error {
    return inspection.ValidateDomain()
}
`,
		"internal/cache/direct.go": `package cache
func denied(inspection interface{ ValidateDomain() error }) error {
    return inspection.ValidateDomain()
}
`,
		"internal/gen/direct.go": `package gen
func denied(inspection interface{ ValidateDomain() error }) error {
    return inspection.ValidateDomain()
}
`,
		"internal/generated/direct.go": `package generated
func denied(inspection interface{ ValidateDomain() error }) error {
    return inspection.ValidateDomain()
}
`,
		"internal/databasemigration/alias.go": `package databasemigration
func deniedAlias(inspection interface{ ValidateDomain() error }) {
    retained := inspection.ValidateDomain
    _ = retained
}
`,
		"internal/databasereadiness/async.go": `package databasereadiness
func deniedAsync(inspection interface{ ValidateDomain() error }) {
    go inspection.ValidateDomain()
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
	violations, err := inspectionDomainValidationBoundaryViolations(root, true)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(violations, "\n")
	for _, expected := range []string{
		"internal/rogue/direct.go",
		"internal/cache/direct.go",
		"internal/gen/direct.go",
		"internal/generated/direct.go",
		"internal/databasemigration/alias.go",
		"internal/databasereadiness/async.go",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("inspection validation guard missing %q:\n%s", expected, joined)
		}
	}
}

func TestSQLiteProviderValidatedReplacementBoundaryRejectsUnauthorizedConsumers(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	files := map[string]string{
		validatedReplacementUseConsumer: `package databasemigration
import (
    "context"
    provider "github.com/sipeed/picoclaw/internal/sqliteprovider"
)
func allowed(ctx context.Context, replacement provider.ValidatedReplacement) error {
    return replacement.Use(ctx, "global/auth", "/tmp/auth.db", func(context.Context, string) error { return nil })
}
`,
		"internal/rogue/rogue.go": `package rogue
import (
    "context"
    provider "github.com/sipeed/picoclaw/internal/sqliteprovider"
)
func denied(ctx context.Context, replacement provider.ValidatedReplacement) error {
    return replacement.Use(ctx, "global/auth", "/tmp/auth.db", func(context.Context, string) error { return nil })
}
var _ = provider.MigrateStagedOfflineFromWithLiveVerification
var _ provider.ValidatedReplacementCheck
var _ provider.ValidatedTargetParentCheck
`,
		"internal/rogue/domain_validation.go": `package rogue
import (
    "context"
    provider "github.com/sipeed/picoclaw/internal/sqliteprovider"
)
func deniedDomainValidation(ctx context.Context, inspection provider.Inspection) error {
    return inspection.ValidateDomain(ctx, "global/auth", "auth", nil)
}
`,
		"internal/rogue/dot_import.go": `package rogue
import . "github.com/sipeed/picoclaw/internal/sqliteprovider"
var _ ValidatedReplacement
`,
		"internal/rogue/type_alias.go": `package rogue
import (
    "context"
    provider "github.com/sipeed/picoclaw/internal/sqliteprovider"
)
type localReplacement = provider.ValidatedReplacement
func deniedAlias(ctx context.Context, replacement localReplacement) error {
    return replacement.Use(ctx, "global/auth", "/tmp/auth.db", func(context.Context, string) error { return nil })
}
`,
		"internal/rogue/variable.go": `package rogue
import (
    "context"
    provider "github.com/sipeed/picoclaw/internal/sqliteprovider"
)
var replacement provider.ValidatedReplacement
func deniedVariable(ctx context.Context) error {
    return replacement.Use(ctx, "global/auth", "/tmp/auth.db", func(context.Context, string) error { return nil })
}
`,
		"internal/rogue/inferred.go": `package rogue
import (
    "context"
    provider "github.com/sipeed/picoclaw/internal/sqliteprovider"
)
func deniedInferred(ctx context.Context, replacement provider.ValidatedReplacement) error {
    inferred := replacement
    return inferred.Use(ctx, "global/auth", "/tmp/auth.db", func(context.Context, string) error { return nil })
}
`,
		"internal/rogue/interface.go": `package rogue
import (
    "context"
    provider "github.com/sipeed/picoclaw/internal/sqliteprovider"
)
type replacementUser interface {
    Use(context.Context, string, string, func(context.Context, string) error) error
}
func deniedInterface(ctx context.Context, replacement provider.ValidatedReplacement) error {
    var user replacementUser = replacement
    return user.Use(ctx, "global/auth", "/tmp/auth.db", func(context.Context, string) error { return nil })
}
`,
		"internal/rogue/cross_file.go": `package rogue
import "context"
func deniedCrossFile(ctx context.Context) error {
    return replacement.Use(ctx, "global/auth", "/tmp/auth.db", nil)
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
	violations, err := validatedReplacementBoundaryViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(violations, "\n")
	if strings.Contains(joined, validatedReplacementUseConsumer) {
		t.Fatalf("approved validated-replacement consumer was rejected:\n%s", joined)
	}
	for _, expected := range []struct {
		path    string
		message string
	}{
		{"internal/rogue/rogue.go", "cannot reference sqliteprovider.ValidatedReplacement"},
		{"internal/rogue/rogue.go", "cannot call sqliteprovider.MigrateStagedOfflineFromWithLiveVerification"},
		{"internal/rogue/rogue.go", "cannot reference sqliteprovider.ValidatedReplacementCheck"},
		{"internal/rogue/rogue.go", "cannot reference sqliteprovider.ValidatedTargetParentCheck"},
		{"internal/rogue/rogue.go", "cannot consume ValidatedReplacement.Use"},
		{"internal/rogue/dot_import.go", "cannot use a dot SQLite-provider import"},
		{"internal/rogue/type_alias.go", "cannot consume ValidatedReplacement.Use"},
		{"internal/rogue/variable.go", "cannot consume ValidatedReplacement.Use"},
		{"internal/rogue/inferred.go", "cannot consume ValidatedReplacement.Use"},
		{"internal/rogue/interface.go", "cannot consume ValidatedReplacement.Use"},
		{"internal/rogue/cross_file.go", "cannot consume ValidatedReplacement.Use"},
		{"internal/rogue/domain_validation.go", "cannot invoke Inspection.ValidateDomain"},
	} {
		found := false
		for _, violation := range violations {
			if strings.HasPrefix(violation, expected.path+":") &&
				strings.Contains(violation, expected.message) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf(
				"boundary violations missing %s containing %q:\n%s",
				expected.path,
				expected.message,
				joined,
			)
		}
	}
}

func immutableGenerationSourceMintViolations(repositoryRoot string) ([]string, error) {
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
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") ||
			filepath.Clean(filepath.Dir(path)) == filepath.Join(repositoryRoot, "internal", "sqliteprovider") {
			return nil
		}
		relative, relativeErr := filepath.Rel(repositoryRoot, path)
		if relativeErr != nil {
			return relativeErr
		}
		relative = filepath.ToSlash(relative)
		fileSet := token.NewFileSet()
		parsed, parseErr := parser.ParseFile(fileSet, path, nil, parser.AllErrors)
		if parseErr != nil {
			return fmt.Errorf("parse production Go file %s: %w", relative, parseErr)
		}
		aliases := make(map[string]struct{})
		for _, imported := range parsed.Imports {
			importPath, unquoteErr := strconv.Unquote(imported.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			if importPath != sqliteProviderImportPath {
				continue
			}
			alias := "sqliteprovider"
			if imported.Name != nil {
				alias = imported.Name.Name
			}
			if alias == "." {
				violations = append(violations, fmt.Sprintf(
					"%s:%d cannot use a dot SQLite-provider import",
					relative, fileSet.Position(imported.Pos()).Line,
				))
				continue
			}
			if alias != "_" {
				aliases[alias] = struct{}{}
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "NewImmutableGenerationSource" {
				return true
			}
			identifier, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if _, imported := aliases[identifier.Name]; imported &&
				relative != immutableGenerationSourceMinter {
				violations = append(violations, fmt.Sprintf(
					"%s:%d cannot mint an immutable SQLite generation source",
					relative, fileSet.Position(selector.Pos()).Line,
				))
			}
			return true
		})
		return nil
	})
	sort.Strings(violations)
	return violations, err
}

func validatedReplacementBoundaryViolations(repositoryRoot string) ([]string, error) {
	providerDirectories, err := sqliteProviderProductionImportDirectories(repositoryRoot)
	if err != nil {
		return nil, err
	}
	var violations []string
	err = filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != repositoryRoot && sqliteProviderImportGuardSkipsDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") ||
			filepath.Clean(filepath.Dir(path)) == filepath.Join(repositoryRoot, "internal", "sqliteprovider") {
			return nil
		}
		relative, relativeErr := filepath.Rel(repositoryRoot, path)
		if relativeErr != nil {
			return relativeErr
		}
		relative = filepath.ToSlash(relative)
		fileSet := token.NewFileSet()
		parsed, parseErr := parser.ParseFile(fileSet, path, nil, parser.AllErrors)
		if parseErr != nil {
			return fmt.Errorf("parse production Go file %s: %w", relative, parseErr)
		}
		aliases := make(map[string]struct{})
		hasUsableProviderImport := false
		for _, imported := range parsed.Imports {
			importPath, unquoteErr := strconv.Unquote(imported.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
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
				hasUsableProviderImport = true
				violations = append(violations, fmt.Sprintf(
					"%s:%d: cannot use a dot SQLite-provider import at the validated-replacement boundary",
					relative, fileSet.Position(imported.Pos()).Line,
				))
			case "_":
				// A blank import cannot name any protected provider capability.
			default:
				hasUsableProviderImport = true
				aliases[alias] = struct{}{}
			}
		}
		_, packageUsesProvider := providerDirectories[filepath.Clean(filepath.Dir(path))]
		if !hasUsableProviderImport && !packageUsesProvider {
			return nil
		}

		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if identifier, identifierReceiver := selector.X.(*ast.Ident); identifierReceiver {
				_, imported := aliases[identifier.Name]
				if imported {
					allowed, protected := validatedReplacementConsumers[selector.Sel.Name]
					if protected && !allowed[relative] {
						verb := "reference"
						if selector.Sel.Name == "MigrateStagedOfflineFromWithLiveVerification" {
							verb = "call"
						}
						violations = append(violations, fmt.Sprintf(
							"%s:%d: cannot %s sqliteprovider.%s",
							relative, fileSet.Position(selector.Pos()).Line, verb, selector.Sel.Name,
						))
					}
				}
			}

			// ValidatedReplacement.Use and UseWithTargetParent can be reached through type aliases,
			// package variables, inferred locals, or interface receivers. An
			// AST-only receiver-name allowlist cannot distinguish those safely,
			// so every Use selector in a provider-importing production file is
			// denied outside the single approved synchronous consumer.
			if selector.Sel.Name == "Use" || selector.Sel.Name == "UseWithTargetParent" {
				if relative != validatedReplacementUseConsumer {
					violations = append(violations, fmt.Sprintf(
						"%s:%d: cannot consume ValidatedReplacement.Use",
						relative, fileSet.Position(selector.Pos()).Line,
					))
				}
			}
			if selector.Sel.Name == "ValidateDomain" &&
				!inspectionDomainValidationConsumers[relative] {
				violations = append(violations, fmt.Sprintf(
					"%s:%d: cannot invoke Inspection.ValidateDomain",
					relative, fileSet.Position(selector.Pos()).Line,
				))
			}
			return true
		})
		return nil
	})
	sort.Strings(violations)
	return violations, err
}

func inspectionDomainValidationBoundaryViolations(
	repositoryRoot string,
	requireExpected bool,
) ([]string, error) {
	actual := make(map[string]int)
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
		direct := make(map[token.Pos]bool)
		asynchronous := make(map[token.Pos]bool)
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.CallExpr:
				if selector, ok := value.Fun.(*ast.SelectorExpr); ok &&
					selector.Sel.Name == "ValidateDomain" {
					direct[selector.Pos()] = true
				}
			case *ast.GoStmt:
				ast.Inspect(value.Call, func(child ast.Node) bool {
					if selector, ok := child.(*ast.SelectorExpr); ok &&
						selector.Sel.Name == "ValidateDomain" {
						asynchronous[selector.Pos()] = true
					}
					return true
				})
			case *ast.DeferStmt:
				ast.Inspect(value.Call, func(child ast.Node) bool {
					if selector, ok := child.(*ast.SelectorExpr); ok &&
						selector.Sel.Name == "ValidateDomain" {
						asynchronous[selector.Pos()] = true
					}
					return true
				})
			}
			return true
		})
		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "ValidateDomain" {
				return true
			}
			if !inspectionDomainValidationConsumers[relative] || !direct[selector.Pos()] ||
				asynchronous[selector.Pos()] {
				violations = append(violations, fmt.Sprintf(
					"%s:%d cannot retain or asynchronously invoke Inspection.ValidateDomain",
					relative,
					fileSet.Position(selector.Pos()).Line,
				))
				return true
			}
			actual[relative]++
			return true
		})
		return nil
	})
	if requireExpected {
		for relative, expected := range inspectionDomainValidationExpectedCalls {
			if actual[relative] != expected {
				violations = append(violations, fmt.Sprintf(
					"%s direct Inspection.ValidateDomain calls = %d, want %d",
					relative,
					actual[relative],
					expected,
				))
			}
		}
	}
	sort.Strings(violations)
	return violations, err
}

func sqliteProviderProductionImportDirectories(repositoryRoot string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
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
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") ||
			filepath.Clean(filepath.Dir(path)) == filepath.Join(repositoryRoot, "internal", "sqliteprovider") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if importPath == sqliteProviderImportPath &&
				(imported.Name == nil || imported.Name.Name != "_") {
				result[filepath.Clean(filepath.Dir(path))] = struct{}{}
				break
			}
		}
		return nil
	})
	return result, err
}

func TestSQLiteProviderOwnsDriverOpen(t *testing.T) {
	t.Parallel()

	root := filepath.Join(sqliteProviderRepositoryRoot(t), "internal", "sqliteprovider")
	moderncUsers := map[string]bool{
		"maintenance.go":          true,
		"provider.go":             true,
		"staged_migration.go":     true,
		"transaction_boundary.go": true,
	}
	var violations []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.AllErrors)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		sqlNames := map[string]bool{}
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if importPath == "modernc.org/sqlite" && !moderncUsers[entry.Name()] {
				violations = append(violations, entry.Name()+": unexpected SQLite driver import")
			}
			if importPath == "database/sql" {
				name := "sql"
				if imported.Name != nil {
					name = imported.Name.Name
				}
				sqlNames[name] = true
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Open" {
				return true
			}
			identifier, ok := selector.X.(*ast.Ident)
			if ok && sqlNames[identifier.Name] && entry.Name() != "provider.go" {
				violations = append(violations, fmt.Sprintf(
					"%s:%d calls database/sql.Open", entry.Name(), fileSet.Position(call.Pos()).Line,
				))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("inspect SQLite provider: %v", err)
	}
	if len(violations) != 0 {
		sort.Strings(violations)
		t.Fatalf("SQLite provider driver boundary mismatch:\n%s", strings.Join(violations, "\n"))
	}
}

func sqliteProviderRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve SQLite provider guard source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func sqliteProviderImportGuardSkipsDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".cache", "node_modules", "testdata", "vendor":
		return true
	default:
		return false
	}
}

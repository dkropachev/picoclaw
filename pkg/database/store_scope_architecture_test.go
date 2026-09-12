package database

import (
	"fmt"
	"go/ast"
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

const (
	scopedTargetTagStoreID          = "tag:store_id"
	scopedTargetLiteralStoreID      = "literal:store_id"
	scopedTargetLiteralWorkspace    = "literal:workspace_selector"
	scopedTargetLiteralResolveStore = "literal:resolve-store"
	scopedTargetRequestEnvelope     = "constructor:request_envelope"
	scopedTargetExternalStoreID     = "database:StoreID"
	scopedTargetExternalParseID     = "database:ParseStoreID"
	scopedTargetExternalEnvelope    = "database:RequestEnvelope"
	scopedTargetInternalEnvelope    = "database-internal:RequestEnvelope"
	scopedTargetUnscopedCall        = "selector:Call"
	scopedTargetUnscopedCallOptions = "selector:CallWithOptions"
	scopedTargetDatabaseImportPath  = "github.com/sipeed/picoclaw/pkg/database"
	scopedTargetCatalogImportPath   = "github.com/sipeed/picoclaw/pkg/database/catalog"
	scopedTargetProtocolPath        = "pkg/database/protocol.go"
	scopedTargetClientPath          = "pkg/database/client.go"
	scopedTargetServerPath          = "pkg/database/server.go"
	scopedTargetBackupModelPath     = "internal/databasemigration/backup_model.go"
	scopedTargetBackupManifestPath  = "internal/databasemigration/backup_manifest_json.go"
)

func TestScopedStoreTargetHasOneWireAuthority(t *testing.T) {
	t.Parallel()

	expected := map[string]map[string]int{
		scopedTargetProtocolPath: {
			scopedTargetTagStoreID:       1,
			scopedTargetInternalEnvelope: 2,
		},
		scopedTargetServerPath: {
			scopedTargetLiteralStoreID:      2,
			scopedTargetLiteralWorkspace:    2,
			scopedTargetLiteralResolveStore: 2,
			scopedTargetInternalEnvelope:    4,
		},
		scopedTargetBackupModelPath: {
			scopedTargetTagStoreID: 2,
		},
		scopedTargetBackupManifestPath: {
			scopedTargetLiteralStoreID: 2,
		},
		"cmd/picoclaw/internal/database/command.go": {
			scopedTargetExternalStoreID: 2,
		},
		"internal/databaseadapter/registry.go": {
			scopedTargetExternalStoreID: 1,
		},
		"internal/databaseclaims/claims.go": {
			scopedTargetExternalStoreID: 35,
		},
		"internal/databaseclaims/provider_lease.go": {
			scopedTargetExternalStoreID: 1,
		},
		"internal/databaseclaims/scoped_claims.go": {
			scopedTargetExternalStoreID: 8,
		},
		"internal/databasemigration/backup_archive.go": {
			scopedTargetExternalStoreID: 4,
		},
		"internal/databasemigration/backup_prepare.go": {
			scopedTargetExternalStoreID: 2,
		},
		"internal/databasemigration/manifest_validation.go": {
			scopedTargetExternalStoreID: 3,
			scopedTargetExternalParseID: 3,
		},
		"internal/databasemigration/migration.go": {
			scopedTargetExternalStoreID: 8,
		},
		"internal/databaseproviderlease/lease.go": {
			scopedTargetExternalStoreID: 3,
		},
		"internal/databasereadiness/readiness.go": {
			scopedTargetExternalStoreID: 7,
		},
		"internal/sqliteprovider/provider_offline.go": {
			scopedTargetExternalStoreID: 2,
		},
		"internal/sqliteprovider/staged_live_verification.go": {
			scopedTargetExternalStoreID: 3,
		},
		"internal/storecatalog/catalog.go": {
			scopedTargetExternalStoreID: 12,
			scopedTargetExternalParseID: 1,
		},
		"internal/storecatalog/review_scope.go": {
			scopedTargetExternalStoreID: 13,
		},
		"pkg/database/catalog/catalog.go": {
			scopedTargetExternalStoreID: 1,
			scopedTargetExternalParseID: 1,
		},
		"internal/databasemigration/backup_parent_create_windows.go": {
			scopedTargetUnscopedCall: 1,
		},
		"internal/databasemigration/backup_remove_platform_windows.go": {
			scopedTargetUnscopedCall: 1,
		},
		"internal/databasemigration/backup_status_exchange_windows.go": {
			scopedTargetUnscopedCall: 1,
		},
		"internal/sqliteprovider/staged_retirement_windows.go": {
			scopedTargetUnscopedCall: 1,
		},
		"pkg/agent/hook_process.go": {
			scopedTargetUnscopedCall: 4,
		},
		"pkg/config/security.go": {
			scopedTargetUnscopedCall: 3,
		},
		"pkg/database/client.go": {
			scopedTargetRequestEnvelope:     1,
			scopedTargetInternalEnvelope:    1,
			scopedTargetUnscopedCall:        2,
			scopedTargetUnscopedCallOptions: 4,
		},
		"pkg/database/idempotency.go": {
			scopedTargetInternalEnvelope: 3,
		},
		"pkg/isolation/platform_windows.go": {
			scopedTargetUnscopedCall: 1,
		},
		"pkg/pid/pidfile_windows.go": {
			scopedTargetUnscopedCall: 6,
		},
		"pkg/repoaudit/purge_root_sync_windows.go": {
			scopedTargetUnscopedCall: 1,
		},
		"pkg/tools/hardware/serial_windows.go": {
			scopedTargetUnscopedCall: 4,
		},
	}
	actual := make(map[string]map[string]int)
	var violations []string
	repositoryRoot := scopedTargetRepositoryRoot(t)
	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != repositoryRoot && entry.Type()&os.ModeSymlink != 0 {
			relative, relativeErr := filepath.Rel(repositoryRoot, path)
			if relativeErr != nil {
				return relativeErr
			}
			violations = append(
				violations,
				filepath.ToSlash(relative)+": repository entry is a symlink",
			)
			return nil
		}
		if entry.IsDir() {
			if path != repositoryRoot && scopedTargetSkipsDirectory(entry.Name()) {
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
		parsed, err := parser.ParseFile(
			fileSet, path, nil, parser.ParseComments|parser.AllErrors,
		)
		if err != nil {
			return fmt.Errorf("parse production Go file %s: %w", relative, err)
		}
		fileOccurrences, fileViolations := scopedTargetOccurrences(relative, parsed, fileSet, expected)
		violations = append(violations, fileViolations...)
		for kind, count := range fileOccurrences {
			if actual[relative] == nil {
				actual[relative] = make(map[string]int)
			}
			actual[relative][kind] += count
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan scoped StoreID authority: %v", err)
	}
	for path, kinds := range expected {
		for kind, count := range kinds {
			if actual[path][kind] != count {
				violations = append(violations, fmt.Sprintf(
					"%s %s occurrences = %d, want %d",
					path, kind, actual[path][kind], count,
				))
			}
		}
	}
	if len(violations) != 0 {
		sort.Strings(violations)
		t.Fatalf("scoped StoreID wire authority changed:\n%s", strings.Join(violations, "\n"))
	}
}

func scopedTargetOccurrences(
	relative string,
	parsed *ast.File,
	fileSet *token.FileSet,
	expected map[string]map[string]int,
) (map[string]int, []string) {
	occurrences := make(map[string]int)
	var violations []string
	databaseAliases := make(map[string]string)
	for _, imported := range parsed.Imports {
		importPath, err := strconv.Unquote(imported.Path.Value)
		if err != nil || importPath != scopedTargetDatabaseImportPath &&
			importPath != scopedTargetCatalogImportPath {
			continue
		}
		alias := filepath.Base(importPath)
		if imported.Name != nil {
			alias = imported.Name.Name
		}
		if alias == "." {
			violations = append(violations, fmt.Sprintf(
				"%s:%d dot-imports guarded database protocol %s",
				relative, fileSet.Position(imported.Pos()).Line, importPath,
			))
		}
		databaseAliases[alias] = importPath
	}
	for _, group := range parsed.Comments {
		for _, comment := range group.List {
			if strings.Contains(comment.Text, "go:linkname") &&
				strings.Contains(comment.Text, scopedTargetDatabaseImportPath) {
				violations = append(violations, fmt.Sprintf(
					"%s:%d linknames scoped StoreID authority",
					relative, fileSet.Position(comment.Pos()).Line,
				))
			}
		}
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		if identifier, ok := node.(*ast.Ident); ok && parsed.Name.Name == "database" &&
			identifier.Name == "RequestEnvelope" {
			occurrences[scopedTargetInternalEnvelope]++
			if expected[relative][scopedTargetInternalEnvelope] == 0 {
				violations = append(violations, fmt.Sprintf(
					"%s:%d references RequestEnvelope outside its exact protocol core",
					relative, fileSet.Position(identifier.Pos()).Line,
				))
			}
		}
		if composite, ok := node.(*ast.CompositeLit); ok &&
			scopedTargetIsRequestEnvelope(composite.Type, parsed.Name.Name, databaseAliases) {
			occurrences[scopedTargetRequestEnvelope]++
			if expected[relative][scopedTargetRequestEnvelope] == 0 {
				violations = append(violations, fmt.Sprintf(
					"%s:%d constructs a request envelope outside the canonical client",
					relative, fileSet.Position(composite.Pos()).Line,
				))
			}
		}
		if selector, ok := node.(*ast.SelectorExpr); ok {
			kind := ""
			switch selector.Sel.Name {
			case "Call":
				kind = scopedTargetUnscopedCall
			case "CallWithOptions":
				kind = scopedTargetUnscopedCallOptions
			}
			if qualifier, ok := selector.X.(*ast.Ident); ok {
				switch databaseAliases[qualifier.Name] {
				case scopedTargetDatabaseImportPath:
					switch selector.Sel.Name {
					case "StoreID":
						kind = scopedTargetExternalStoreID
					case "ParseStoreID":
						kind = scopedTargetExternalParseID
					case "RequestEnvelope":
						kind = scopedTargetExternalEnvelope
					}
				case scopedTargetCatalogImportPath:
					if selector.Sel.Name == "StoreID" {
						kind = scopedTargetExternalStoreID
					}
				}
			}
			if kind != "" {
				occurrences[kind]++
				if expected[relative][kind] == 0 {
					violations = append(violations, fmt.Sprintf(
						"%s:%d uses unapproved scoped target surface %s",
						relative, fileSet.Position(selector.Sel.Pos()).Line, selector.Sel.Name,
					))
				}
			}
		}
		if structure, ok := node.(*ast.StructType); ok && scopedTargetProtectedAdapter(relative) {
			for _, field := range structure.Fields.List {
				for _, fieldName := range field.Names {
					key := scopedTargetKey(fieldName.Name)
					if ast.IsExported(fieldName.Name) &&
						(key == "storeid" || key == "workspaceselector") {
						violations = append(violations, fmt.Sprintf(
							"%s:%d exports forbidden scoped target payload field %s",
							relative, fileSet.Position(fieldName.Pos()).Line, fieldName.Name,
						))
					}
				}
			}
		}
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			return true
		}
		kind := scopedTargetLiteralKind(value)
		if kind == "" {
			return true
		}
		occurrences[kind]++
		if expected[relative][kind] == 0 {
			violations = append(violations, fmt.Sprintf(
				"%s:%d declares forbidden scoped targeting value %q",
				relative, fileSet.Position(literal.Pos()).Line, value,
			))
		}
		return true
	})
	return occurrences, violations
}

func scopedTargetIsRequestEnvelope(
	expression ast.Expr,
	packageName string,
	databaseAliases map[string]string,
) bool {
	if identifier, ok := expression.(*ast.Ident); ok {
		return packageName == "database" && identifier.Name == "RequestEnvelope"
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "RequestEnvelope" {
		return false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	return ok && databaseAliases[qualifier.Name] == scopedTargetDatabaseImportPath
}

func scopedTargetProtectedAdapter(relative string) bool {
	for _, prefix := range []string{
		"cmd/picoclaw/",
		"pkg/gitworkspace/",
		"pkg/gateway/",
		"pkg/prworkspace/",
		"pkg/repoaudit/",
		"pkg/repoeval/",
	} {
		if strings.HasPrefix(relative, prefix) {
			return true
		}
	}
	return false
}

func scopedTargetLiteralKind(value string) string {
	if strings.EqualFold(value, "Call") {
		return scopedTargetUnscopedCall
	}
	if strings.EqualFold(value, "CallWithOptions") {
		return scopedTargetUnscopedCallOptions
	}
	if jsonTag, ok := reflect.StructTag(value).Lookup("json"); ok {
		name, _, _ := strings.Cut(jsonTag, ",")
		switch scopedTargetKey(name) {
		case "storeid":
			return scopedTargetTagStoreID
		case "workspaceselector":
			return scopedTargetLiteralWorkspace
		}
	}
	switch scopedTargetKey(value) {
	case "storeid":
		return scopedTargetLiteralStoreID
	case "workspaceselector":
		return scopedTargetLiteralWorkspace
	case "resolvestore":
		return scopedTargetLiteralResolveStore
	default:
		return ""
	}
}

func TestScopedStoreTargetGuardRejectsAdapterPayloadAndResolverFixtures(t *testing.T) {
	t.Parallel()

	for _, fixture := range []struct {
		name   string
		source string
		want   string
	}{
		{
			name: "payload field",
			source: `package adapter
type request struct { StoreID string ` + "`json:\"store_id\"`" + ` }
`,
			want: "store_id",
		},
		{
			name: "uppercase payload tag",
			source: `package adapter
type request struct { StoreID string ` + "`json:\"STORE_ID\"`" + ` }
`,
			want: "STORE_ID",
		},
		{
			name: "default payload field",
			source: `package repoaudit
type request struct { StoreID string }
`,
			want: "forbidden scoped target payload field StoreID",
		},
		{
			name: "alternate typed target",
			source: `package repoaudit
import db "github.com/sipeed/picoclaw/pkg/database"
type request struct { Target db.StoreID ` + "`json:\"target\"`" + ` }
`,
			want: "unapproved scoped target surface StoreID",
		},
		{
			name: "unaliased catalog typed target",
			source: `package repoaudit
import "github.com/sipeed/picoclaw/pkg/database/catalog"
type request struct { Target catalog.StoreID ` + "`json:\"target\"`" + ` }
`,
			want: "unapproved scoped target surface StoreID",
		},
		{
			name: "aliased typed target",
			source: `package repoaudit
import db "github.com/sipeed/picoclaw/pkg/database"
type targetID = db.StoreID
type request struct { Target targetID ` + "`json:\"target\"`" + ` }
`,
			want: "unapproved scoped target surface StoreID",
		},
		{
			name: "payload map key",
			source: `package adapter
var request = map[string]string{"store_id": "workspace/reviews"}
`,
			want: "store_id",
		},
		{
			name: "workspace resolver",
			source: `package adapter
const operation = "resolve-store"
const key = "workspace_selector"
`,
			want: "resolve-store",
		},
		{
			name: "unscoped adapter client",
			source: `package repoaudit
import "github.com/sipeed/picoclaw/pkg/database"
func call(client *database.Client) { _ = client.Call(nil, "reviews", 1, "read", nil, nil) }
`,
			want: "unapproved scoped target surface Call",
		},
		{
			name: "manual envelope",
			source: `package repoaudit
import db "github.com/sipeed/picoclaw/pkg/database"
var request = db.RequestEnvelope{}
`,
			want: "constructs a request envelope",
		},
		{
			name: "aliased envelope allocation",
			source: `package repoaudit
import db "github.com/sipeed/picoclaw/pkg/database"
type wire = db.RequestEnvelope
var request = new(wire)
`,
			want: "unapproved scoped target surface RequestEnvelope",
		},
		{
			name: "envelope zero value",
			source: `package repoaudit
import db "github.com/sipeed/picoclaw/pkg/database"
var request db.RequestEnvelope
`,
			want: "unapproved scoped target surface RequestEnvelope",
		},
		{
			name: "gateway unscoped wrapper",
			source: `package gateway
type legacy interface { Call(any) error }
func invoke(value legacy) { _ = value.Call(nil) }
`,
			want: "unapproved scoped target surface Call",
		},
		{
			name: "reflective unscoped call",
			source: `package gateway
const method = "CallWithOptions"
`,
			want: "CallWithOptions",
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			fileSet := token.NewFileSet()
			parsed, err := parser.ParseFile(fileSet, "adapter.go", fixture.source, parser.AllErrors)
			if err != nil {
				t.Fatal(err)
			}
			_, violations := scopedTargetOccurrences(
				"pkg/repoaudit/adapter.go", parsed, fileSet, nil,
			)
			if !strings.Contains(strings.Join(violations, "\n"), fixture.want) {
				t.Fatalf("guard violations = %v, want %q", violations, fixture.want)
			}
		})
	}
}

func scopedTargetRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve scoped StoreID architecture source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func scopedTargetSkipsDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".cache", "node_modules", "testdata", "vendor":
		return true
	default:
		return false
	}
}

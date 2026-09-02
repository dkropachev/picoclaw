package database_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// sanctionedPublicDatabaseBoundaries are maintenance/import tools that operate
// outside the live broker contract. They are declaration-specific so another
// exported locator in the same package is still rejected.
var sanctionedPublicDatabaseBoundaries = map[string]string{
	"cmd/membench/ingest.go: exported function IngestSeahorse exposes physical database locator":                              "development-only benchmark fixture importer",
	"pkg/channels/matrix/database_migration.go: exported function MigrateCryptoDatabase exposes physical database locator":    "offline Matrix library migration adapter",
	"pkg/channels/whatsapp_native/whatsapp_native.go: exported function MigrateDatabase exposes physical database locator":    "offline WhatsApp library migration adapter",
	"pkg/cron/service.go: exported function NewOfflineService exposes physical database locator":                              "offline cron migration adapter",
	"pkg/eventing/database_migration.go: exported function RunOfflineDatabaseMigration exposes physical database locator":     "offline domain migration adapter",
	"pkg/evolution/database_migration.go: exported function RunOfflineDatabaseMigration exposes physical database locator":    "offline domain migration adapter",
	"pkg/memory/database_migration.go: exported function RunOfflineDatabaseMigration exposes physical database locator":       "offline domain migration adapter",
	"pkg/seahorse/short_engine.go: exported type OfflineConfig exposes physical database locator":                             "explicit offline benchmark and migration boundary",
	"pkg/seahorse/database_migration.go: exported function RunOfflineDatabaseMigration exposes physical database locator":     "offline domain migration adapter",
	"web/backend/dashboardauth/broker.go: exported function RunOfflineDatabaseMigration exposes physical database locator":    "offline domain migration adapter",
	"pkg/migrate/sources/openclaw/openclaw_config.go: exported type OpenClawIMessageConfig exposes physical database locator": "external legacy configuration schema, not a PicoClaw store API",
	"pkg/config/config.go: exported type MatrixSettings exposes physical database locator":                                    "trusted broker-loaded temporary Matrix bridge configuration",
	"pkg/config/config.go: exported type WhatsAppSettings exposes physical database locator":                                  "trusted broker-loaded temporary WhatsApp bridge configuration",
	"pkg/config/events.go: exported type EventIngressConfig exposes physical database locator":                                "trusted broker-loaded dynamic event catalog configuration",
}

// knownPublicDatabaseAPIDebt is intentionally declaration-specific. These
// compatibility surfaces remain until their callers move to logical StoreIDs.
// Keeping them here makes the cutover debt reviewable without granting a broad
// package exemption.
var knownPublicDatabaseAPIDebt = map[string]string{}

func TestApplicationPublicAPIsAreProviderNeutral(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	detected := make(map[string]struct{})
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if relative != "." && shouldSkipArchitectureDirectory(relative, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		directory := filepath.ToSlash(filepath.Dir(relative))
		if directory == "internal" || strings.HasPrefix(directory, "internal/") ||
			strings.HasPrefix(directory, "pkg/internal/") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, name := range exposedDatabaseBoundaryDeclarations(raw) {
			detected[filepath.ToSlash(relative)+": "+name] = struct{}{}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	unexpected := make([]string, 0)
	for violation := range detected {
		_, sanctioned := sanctionedPublicDatabaseBoundaries[violation]
		_, debt := knownPublicDatabaseAPIDebt[violation]
		if !sanctioned && !debt {
			unexpected = append(unexpected, violation)
		}
	}
	stale := make([]string, 0)
	for category, allowances := range map[string]map[string]string{
		"sanctioned": sanctionedPublicDatabaseBoundaries,
		"debt":       knownPublicDatabaseAPIDebt,
	} {
		for allowance := range allowances {
			if _, stillPresent := detected[allowance]; !stillPresent {
				stale = append(stale, category+": "+allowance)
			}
		}
	}
	sort.Strings(unexpected)
	sort.Strings(stale)
	if len(unexpected) != 0 || len(stale) != 0 {
		var sections []string
		if len(unexpected) != 0 {
			sections = append(
				sections,
				"unexpected application database API exposure:\n"+strings.Join(unexpected, "\n"),
			)
		}
		if len(stale) != 0 {
			sections = append(
				sections,
				"stale public database API debt allowances (remove them):\n"+strings.Join(stale, "\n"),
			)
		}
		t.Fatal(strings.Join(sections, "\n"))
	}
}

func TestPublicAPIDetectorRejectsSQLHandlesCallbacksAndDriverErrors(t *testing.T) {
	t.Parallel()

	source := []byte(`package bad
import (
	storage "database/sql"
	"database/sql/driver"
	backend "modernc.org/sqlite"
)
type Public struct { DB *storage.DB }
func Open() *storage.Conn { return nil }
func With(fn func(*storage.Tx) error) {}
type txCallback func(*storage.Tx) error
func WithAlias(fn txCallback) {}
func Execute() error { return driver.ErrBadConn }
func LeakError() backend.Error { panic("unreachable") }`)
	violations := exposedDatabaseBoundaryDeclarations(source)
	if len(violations) != 6 {
		t.Fatalf("detector violations = %v, want six exported declarations", violations)
	}
}

func TestPublicAPIDetectorRejectsPhysicalDatabaseLocators(t *testing.T) {
	t.Parallel()

	source := []byte(`package bad
type Options struct { DBPath string }
type SQLiteConfig struct { Path string }
type Tagged struct { Location string ` + "`json:\"database_path\"`" + ` }
const DatabaseFilename = "state.db"
const DefaultFile = "hidden.db"
func OpenDSN(dsn string) {}
func NewSQLiteStore(dir string) any { return nil }`)
	violations := exposedDatabaseBoundaryDeclarations(source)
	if len(violations) != 7 {
		t.Fatalf("detector violations = %v, want seven exported declarations (SQLiteConfig=%v NewSQLiteStore=%v)",
			violations, identifierWords("SQLiteConfig"), identifierWords("NewSQLiteStore"))
	}
}

func TestPublicAPIDetectorAvoidsGenericPaths(t *testing.T) {
	t.Parallel()

	source := []byte(`package good
type Artifact struct { Path string }
func OpenDocument(path string) error { return nil }
func DriverNameForBrowser() string { return "" }`)
	if violations := exposedDatabaseBoundaryDeclarations(source); len(violations) != 0 {
		t.Fatalf("generic public API rejected: %v", violations)
	}
}

func exposedDatabaseBoundaryDeclarations(source []byte) []string {
	file, err := parser.ParseFile(token.NewFileSet(), "source.go", source, parser.SkipObjectResolution)
	if err != nil {
		return []string{"unparseable source"}
	}

	backendAliases := make(map[string]struct{})
	driverAliases := make(map[string]struct{})
	for _, imported := range file.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			continue
		}
		if path != "database/sql" && path != "database/sql/driver" && path != "modernc.org/sqlite" {
			continue
		}
		name := filepath.Base(path)
		if path == "database/sql" {
			name = "sql"
		}
		if imported.Name != nil {
			name = imported.Name.Name
		}
		if name == "_" || name == "." {
			continue
		}
		backendAliases[name] = struct{}{}
		if path == "database/sql/driver" || path == "modernc.org/sqlite" {
			driverAliases[name] = struct{}{}
		}
	}

	backendTypeNames := collectBackendTypeNames(file, backendAliases)
	containsBackendType := func(node ast.Node) bool {
		return containsBackendTypeReference(node, backendAliases, backendTypeNames)
	}
	containsReturnedDriverError := func(body *ast.BlockStmt) bool {
		if body == nil || len(driverAliases) == 0 {
			return false
		}
		found := false
		ast.Inspect(body, func(candidate ast.Node) bool {
			if found {
				return false
			}
			statement, ok := candidate.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			for _, result := range statement.Results {
				if containsSelectorFromAliases(result, driverAliases, func(name string) bool {
					return name == "Error" || strings.HasPrefix(name, "Err")
				}) {
					found = true
					return false
				}
			}
			return true
		})
		return found
	}

	var violations []string
	addViolation := func(message string) {
		for _, existing := range violations {
			if existing == message {
				return
			}
		}
		violations = append(violations, message)
	}
	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			if !declaration.Name.IsExported() {
				continue
			}
			label := "exported function " + declaration.Name.Name
			contextName := declaration.Name.Name + receiverTypeName(declaration.Recv)
			if containsBackendType(declaration.Type) {
				addViolation(label + " exposes backend type")
			}
			if isPhysicalDatabaseIdentifier(declaration.Name.Name) ||
				fieldListExposesPhysicalLocator(declaration.Type.Params, contextName) ||
				fieldListExposesPhysicalLocator(declaration.Type.Results, contextName) {
				addViolation(label + " exposes physical database locator")
			}
			if containsReturnedDriverError(declaration.Body) {
				addViolation(label + " returns backend driver error")
			}
		case *ast.GenDecl:
			switch declaration.Tok {
			case token.TYPE:
				for _, rawSpec := range declaration.Specs {
					spec, ok := rawSpec.(*ast.TypeSpec)
					if !ok || !spec.Name.IsExported() {
						continue
					}
					label := "exported type " + spec.Name.Name
					if exportedTypeContainsBackend(spec.Type, containsBackendType) {
						addViolation(label + " exposes backend type")
					}
					if isPhysicalDatabaseIdentifier(spec.Name.Name) ||
						exportedTypeExposesPhysicalLocator(spec.Name.Name, spec.Type) {
						addViolation(label + " exposes physical database locator")
					}
				}
			case token.CONST, token.VAR:
				for _, rawSpec := range declaration.Specs {
					spec, ok := rawSpec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for index, name := range spec.Names {
						valueExposesGeneration := index < len(spec.Values) &&
							expressionContainsDatabaseGenerationLiteral(spec.Values[index])
						if name.IsExported() && (isPhysicalDatabaseIdentifier(name.Name) || valueExposesGeneration) {
							addViolation(
								"exported " + strings.ToLower(
									declaration.Tok.String(),
								) + " " + name.Name + " exposes physical database locator",
							)
						}
					}
				}
			}
		}
	}
	sort.Strings(violations)
	return violations
}

func expressionContainsDatabaseGenerationLiteral(expression ast.Expr) bool {
	found := false
	ast.Inspect(expression, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err == nil && isDatabaseGenerationLiteral(value) {
			found = true
			return false
		}
		return true
	})
	return found
}

func containsSelectorFromAliases(node ast.Node, aliases map[string]struct{}, selectorAllowed func(string) bool) bool {
	if node == nil || len(aliases) == 0 {
		return false
	}
	found := false
	ast.Inspect(node, func(candidate ast.Node) bool {
		selector, ok := candidate.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if _, imported := aliases[identifier.Name]; imported &&
			(selectorAllowed == nil || selectorAllowed(selector.Sel.Name)) {
			found = true
			return false
		}
		return true
	})
	return found
}

func collectBackendTypeNames(file *ast.File, aliases map[string]struct{}) map[string]struct{} {
	names := make(map[string]struct{})
	for changed := true; changed; {
		changed = false
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, rawSpec := range general.Specs {
				spec, ok := rawSpec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if _, exists := names[spec.Name.Name]; exists {
					continue
				}
				containsBackend := func(node ast.Node) bool {
					return containsBackendTypeReference(node, aliases, names)
				}
				if exportedTypeContainsBackend(spec.Type, containsBackend) {
					names[spec.Name.Name] = struct{}{}
					changed = true
				}
			}
		}
	}
	return names
}

func containsBackendTypeReference(
	node ast.Node,
	aliases map[string]struct{},
	backendTypeNames map[string]struct{},
) bool {
	if node == nil {
		return false
	}
	found := false
	ast.Inspect(node, func(candidate ast.Node) bool {
		if found {
			return false
		}
		switch candidate := candidate.(type) {
		case *ast.SelectorExpr:
			identifier, ok := candidate.X.(*ast.Ident)
			if ok {
				_, found = aliases[identifier.Name]
			}
		case *ast.Ident:
			_, found = backendTypeNames[candidate.Name]
		}
		return !found
	})
	return found
}

func exportedTypeContainsBackend(expression ast.Expr, containsBackend func(ast.Node) bool) bool {
	switch expression := expression.(type) {
	case *ast.StructType:
		for _, field := range expression.Fields.List {
			if fieldIsExported(field) && containsBackend(field.Type) {
				return true
			}
		}
		return false
	case *ast.InterfaceType:
		for _, method := range expression.Methods.List {
			if fieldIsExported(method) && containsBackend(method.Type) {
				return true
			}
		}
		return false
	default:
		return containsBackend(expression)
	}
}

func exportedTypeExposesPhysicalLocator(typeName string, expression ast.Expr) bool {
	switch expression := expression.(type) {
	case *ast.StructType:
		for _, field := range expression.Fields.List {
			if !fieldIsExported(field) {
				continue
			}
			for _, name := range field.Names {
				if isPhysicalDatabaseIdentifier(name.Name) ||
					(isDatabaseContextIdentifier(typeName) && isPhysicalLocatorIdentifier(name.Name)) {
					return true
				}
			}
			if structTagExposesPhysicalDatabaseLocator(field.Tag) {
				return true
			}
		}
	case *ast.InterfaceType:
		for _, method := range expression.Methods.List {
			if !fieldIsExported(method) {
				continue
			}
			for _, name := range method.Names {
				if isPhysicalDatabaseIdentifier(name.Name) {
					return true
				}
				function, ok := method.Type.(*ast.FuncType)
				if ok && (fieldListExposesPhysicalLocator(function.Params, typeName+name.Name) ||
					fieldListExposesPhysicalLocator(function.Results, typeName+name.Name)) {
					return true
				}
			}
		}
	}
	return false
}

func structTagExposesPhysicalDatabaseLocator(literal *ast.BasicLit) bool {
	if literal == nil || literal.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return false
	}
	tag := reflect.StructTag(value)
	for _, key := range []string{"json", "yaml", "toml", "mapstructure", "env"} {
		name, _, _ := strings.Cut(tag.Get(key), ",")
		if isPhysicalDatabaseIdentifier(name) {
			return true
		}
	}
	return false
}

func fieldListExposesPhysicalLocator(fields *ast.FieldList, contextName string) bool {
	if fields == nil {
		return false
	}
	databaseContext := isDatabaseContextIdentifier(contextName)
	for _, field := range fields.List {
		for _, name := range field.Names {
			if isPhysicalDatabaseIdentifier(name.Name) ||
				(databaseContext && isPhysicalLocatorIdentifier(name.Name)) {
				return true
			}
		}
	}
	return false
}

func fieldIsExported(field *ast.Field) bool {
	if len(field.Names) == 0 {
		return true
	}
	for _, name := range field.Names {
		if name.IsExported() {
			return true
		}
	}
	return false
}

func receiverTypeName(receivers *ast.FieldList) string {
	if receivers == nil || len(receivers.List) == 0 {
		return ""
	}
	expression := receivers.List[0].Type
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = pointer.X
	}
	if name, ok := expression.(*ast.Ident); ok {
		return name.Name
	}
	return ""
}

func isPhysicalDatabaseIdentifier(name string) bool {
	words := identifierWords(name)
	contains := func(target string) bool {
		for _, word := range words {
			if word == target {
				return true
			}
		}
		return false
	}
	if contains("dsn") || contains("pragma") || contains("wal") || contains("shm") {
		return true
	}
	physical := contains("path") || contains("file") || contains("filename") ||
		contains("directory") || contains("dir") || contains("uri") || contains("url")
	if contains("store") && contains("path") {
		return true
	}
	if physical && (contains("database") || contains("db")) {
		return true
	}
	if contains("journal") && contains("mode") {
		return true
	}
	return contains("driver") && (contains("sqlite") || contains("database") || contains("db"))
}

func isDatabaseContextIdentifier(name string) bool {
	for _, word := range identifierWords(name) {
		if word == "database" || word == "db" || word == "sqlite" {
			return true
		}
	}
	return false
}

func isPhysicalLocatorIdentifier(name string) bool {
	for _, word := range identifierWords(name) {
		switch word {
		case "path", "file", "filename", "directory", "dir", "uri", "url", "dsn":
			return true
		}
	}
	return false
}

func identifierWords(name string) []string {
	runes := []rune(name)
	var words []string
	start := -1
	flush := func(end int) {
		if start >= 0 && end > start {
			words = append(words, strings.ToLower(string(runes[start:end])))
		}
		start = -1
	}
	for index, current := range runes {
		if !unicode.IsLetter(current) && !unicode.IsDigit(current) {
			flush(index)
			continue
		}
		if start < 0 {
			start = index
			continue
		}
		previous := runes[index-1]
		nextLower := index+1 < len(runes) && unicode.IsLower(runes[index+1])
		if unicode.IsUpper(current) && (unicode.IsLower(previous) || unicode.IsDigit(previous) ||
			(unicode.IsUpper(previous) && nextLower)) {
			flush(index)
			start = index
		}
	}
	flush(len(runes))
	merged := make([]string, 0, len(words))
	for index := 0; index < len(words); index++ {
		if index+1 < len(words) && (words[index] == "sql" || words[index] == "sq") && words[index+1] == "lite" {
			merged = append(merged, "sqlite")
			index++
			continue
		}
		merged = append(merged, words[index])
	}
	return merged
}

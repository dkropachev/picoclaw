package databaseclaims

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
)

const databaseClaimsImportPath = "github.com/sipeed/picoclaw/internal/databaseclaims"

func TestPhysicalClaimTestingAPIsAreUsedOnlyByTests(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path is unavailable")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	err := filepath.WalkDir(repository, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".cache", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		if !isGoTestPath(path) {
			for _, reference := range testingAPIProductionReferences(parsed) {
				t.Errorf("test-only physical claim API %s used by production source %s", reference, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSuccessfulProductionClaimWrappersAreNotUsedByTests(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path is unavailable")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	allowed := map[string]map[string]map[string]struct{}{
		"claims_test.go": {
			"TestAcquireRequiresExistingStorageFence":        {"Acquire": {}},
			"TestAcquireProjectedRevalidatesDetachedCatalog": {"AcquireProjected": {}},
		},
		"lease_safety_test.go": {
			"TestAcquireProjectedRejectsUnavailableInventory": {"AcquireProjected": {}},
		},
		"scoped_claims_test.go": {
			"TestAcquireReviewScopeRejectsInvalidAuthorityWithoutStableMutation": {"AcquireReviewScope": {}},
		},
	}
	err := filepath.WalkDir(repository, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".cache", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !isGoTestPath(path) {
			return nil
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		for _, reference := range productionClaimWrapperReferences(parsed) {
			fileAllowed := allowed[filepath.Base(path)]
			functionAllowed := fileAllowed[reference.function]
			_, explicitlyAllowed := functionAllowed[reference.name]
			if !explicitlyAllowed || !reference.directCall {
				t.Errorf(
					"test %s in %s references production-root %s (direct call: %t)",
					reference.function, path, reference.name, reference.directCall,
				)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func databaseClaimsImportAliases(file *ast.File) map[string]struct{} {
	aliases := make(map[string]struct{})
	for _, imported := range file.Imports {
		if imported.Path.Value != `"`+databaseClaimsImportPath+`"` {
			continue
		}
		name := "databaseclaims"
		if imported.Name != nil {
			name = imported.Name.Name
		}
		aliases[name] = struct{}{}
	}
	return aliases
}

type claimWrapperReference struct {
	name       string
	function   string
	directCall bool
}

func testingAPIProductionReferences(file *ast.File) []string {
	parents := astParentMap(file)
	aliases := databaseClaimsImportAliases(file)
	var references []string
	ast.Inspect(file, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.SelectorExpr:
			qualifier, ok := value.X.(*ast.Ident)
			_, imported := aliases[qualifierName(qualifier, ok)]
			if imported && (value.Sel.Name == "AcquireForTesting" ||
				value.Sel.Name == "AcquireReviewScopeForTesting" ||
				value.Sel.Name == "PrepareRootForTesting") {
				references = append(references, value.Sel.Name)
			}
		case *ast.Ident:
			if selector, selected := parents[value].(*ast.SelectorExpr); selected && selector.Sel == value {
				return true
			}
			_, dotImported := aliases["."]
			if file.Name.Name != "databaseclaims" && !dotImported ||
				!testingOnlyClaimIdentifier(value.Name) ||
				functionDeclarationName(parents[value], value) {
				return true
			}
			references = append(references, value.Name)
		}
		return true
	})
	return references
}

func testingOnlyClaimIdentifier(name string) bool {
	return name == "PrepareRootForTesting" || name == "AcquireForTesting" ||
		name == "AcquireReviewScopeForTesting"
}

func productionClaimWrapperReferences(file *ast.File) []claimWrapperReference {
	aliases := databaseClaimsImportAliases(file)
	parents := astParentMap(file)
	owners := astFunctionOwners(file)
	var references []claimWrapperReference
	ast.Inspect(file, func(node ast.Node) bool {
		name := ""
		referenceNode := node
		switch value := node.(type) {
		case *ast.SelectorExpr:
			qualifier, ok := value.X.(*ast.Ident)
			_, imported := aliases[qualifierName(qualifier, ok)]
			if imported && (value.Sel.Name == "Acquire" || value.Sel.Name == "AcquireProjected" ||
				value.Sel.Name == "AcquireReviewScope") {
				name = value.Sel.Name
			}
		case *ast.Ident:
			if selector, selected := parents[value].(*ast.SelectorExpr); selected && selector.Sel == value {
				return true
			}
			_, dotImported := aliases["."]
			if (file.Name.Name == "databaseclaims" || dotImported) &&
				(value.Name == "Acquire" || value.Name == "AcquireProjected" ||
					value.Name == "AcquireReviewScope") &&
				!functionDeclarationName(parents[value], value) {
				name = value.Name
			}
		}
		if name == "" {
			return true
		}
		call, called := parents[referenceNode].(*ast.CallExpr)
		references = append(references, claimWrapperReference{
			name: name, function: owners[referenceNode], directCall: called && call.Fun == referenceNode,
		})
		return true
	})
	return references
}

func astParentMap(file *ast.File) map[ast.Node]ast.Node {
	parents := make(map[ast.Node]ast.Node)
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			return true
		}
		ast.Inspect(node, func(child ast.Node) bool {
			if child != nil && child != node {
				if _, exists := parents[child]; !exists {
					parents[child] = node
				}
				return false
			}
			return true
		})
		return true
	})
	return parents
}

func astFunctionOwners(file *ast.File) map[ast.Node]string {
	owners := make(map[ast.Node]string)
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if node != nil {
				owners[node] = function.Name.Name
			}
			return true
		})
	}
	return owners
}

func qualifierName(identifier *ast.Ident, ok bool) string {
	if !ok || identifier == nil {
		return ""
	}
	return identifier.Name
}

func functionDeclarationName(parent ast.Node, identifier *ast.Ident) bool {
	function, ok := parent.(*ast.FuncDecl)
	return ok && function.Name == identifier
}

func TestProductionClaimWrapperReferenceDetectionRejectsAliases(t *testing.T) {
	source := `package fixture
import claims "github.com/sipeed/picoclaw/internal/databaseclaims"
var escaped = claims.Acquire
func direct() { claims.AcquireProjected(nil, nil) }
func scoped() { claims.AcquireReviewScope(nil, nil) }
`
	file, err := parser.ParseFile(token.NewFileSet(), "fixture_test.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	references := productionClaimWrapperReferences(file)
	if len(references) != 3 || references[0].name != "Acquire" || references[0].directCall ||
		references[1].name != "AcquireProjected" || !references[1].directCall ||
		references[2].name != "AcquireReviewScope" || !references[2].directCall {
		t.Fatalf("wrapper references = %#v", references)
	}
}

func TestTestingAPIReferenceDetectionRejectsDotImport(t *testing.T) {
	source := `package fixture
import . "github.com/sipeed/picoclaw/internal/databaseclaims"
var escaped = AcquireReviewScopeForTesting
`
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	references := testingAPIProductionReferences(file)
	if len(references) != 1 || references[0] != "AcquireReviewScopeForTesting" {
		t.Fatalf("testing API references = %#v", references)
	}
}

func TestPhysicalClaimTestingAPIsRejectNonTestContext(t *testing.T) {
	root := filepath.Join(t.TempDir(), "must-not-exist")
	if err := requireTestingProcess(false); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("non-test physical claim gate = %v", err)
	}
	if err := requireTestingProcess(true); err != nil {
		t.Fatalf("test physical claim gate = %v", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("non-test gate helper mutated root: %v", err)
	}
}

func isGoTestPath(path string) bool {
	base := filepath.Base(path)
	return len(base) > len("_test.go") && base[len(base)-len("_test.go"):] == "_test.go"
}

func TestAcquireForTestingRejectsUnsafeExplicitRoot(t *testing.T) {
	home := secureTestDir(t)
	root := filepath.Join(t.TempDir(), "missing")
	if lease, err := AcquireForTesting(
		testOptions(t, home, nil), nil, nil, root,
	); lease != nil || err == nil {
		t.Fatalf("AcquireForTesting(unsafe root) = %#v, %v", lease, err)
	}
}

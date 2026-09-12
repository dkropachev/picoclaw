package sqlitestore

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

func TestSQLConnectionCallbackParametersRejectDirectEscapeForms(t *testing.T) {
	t.Parallel()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve SQLite transaction callback architecture source")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	violations, err := sqlConnectionCallbackEscapeViolations(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("SQLite callback connection escapes:\n%s", strings.Join(violations, "\n"))
	}
}

func TestSQLConnectionCallbackEscapeGuardRejectsDirectForms(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pkg", "rogue", "rogue.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	source := `package rogue
import (
    "database/sql"
)
var retained *sql.Conn
type alias = sql.Conn
func closeDirect(conn *sql.Conn) { _ = conn.Close() }
func closeAlias(conn *sql.Conn) { other := conn; _ = other.Close() }
func raw(conn *sql.Conn) { _ = conn.Raw(func(any) error { return nil }) }
func retain(conn *sql.Conn) { retained = conn }
func send(conn *sql.Conn, out chan *sql.Conn) { out <- conn }
func returnIt(conn *sql.Conn) *sql.Conn { return conn }
func escape(conn *sql.Conn) { go func() { _ = conn.Close() }() }
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	violations, err := sqlConnectionCallbackEscapeViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(violations, "\n")
	for _, expected := range []string{
		"cannot alias database/sql.Conn",
		"cannot call or retain Close",
		"cannot call or retain Raw",
		"cannot be retained",
		"cannot be sent",
		"cannot be returned",
		"cannot escape to a goroutine",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("escape guard missing %q:\n%s", expected, joined)
		}
	}
}

func sqlConnectionCallbackEscapeViolations(repositoryRoot string) ([]string, error) {
	var violations []string
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
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") ||
			filepath.Clean(filepath.Dir(path)) == filepath.Join(repositoryRoot, "internal", "sqliteprovider") {
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
		sqlAliases := make(map[string]struct{})
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if importPath != "database/sql" {
				continue
			}
			alias := "sql"
			if imported.Name != nil {
				alias = imported.Name.Name
			}
			if alias == "." {
				sqlAliases[alias] = struct{}{}
			} else if alias != "_" {
				sqlAliases[alias] = struct{}{}
			}
		}
		if len(sqlAliases) == 0 {
			return nil
		}
		inspect := func(parameters *ast.FieldList, body *ast.BlockStmt) {
			if parameters == nil || body == nil {
				return
			}
			tainted := make(map[string]struct{})
			for _, field := range parameters.List {
				if !sqlConnectionPointerType(field.Type, sqlAliases) {
					continue
				}
				for _, name := range field.Names {
					if name.Name != "_" {
						tainted[name.Name] = struct{}{}
					}
				}
			}
			if len(tainted) == 0 {
				return
			}
			changed := true
			for changed {
				changed = false
				ast.Inspect(body, func(node ast.Node) bool {
					switch value := node.(type) {
					case *ast.AssignStmt:
						for index, right := range value.Rhs {
							if !expressionIsTaintedSQLConnection(right, tainted) {
								continue
							}
							if index < len(value.Lhs) {
								if name, ok := value.Lhs[index].(*ast.Ident); ok && name.Name != "_" {
									if _, exists := tainted[name.Name]; !exists {
										tainted[name.Name] = struct{}{}
										changed = true
									}
								}
							}
						}
					case *ast.ValueSpec:
						for index, right := range value.Values {
							if index >= len(value.Names) ||
								!expressionIsTaintedSQLConnection(right, tainted) {
								continue
							}
							name := value.Names[index].Name
							if name != "_" {
								if _, exists := tainted[name]; !exists {
									tainted[name] = struct{}{}
									changed = true
								}
							}
						}
					}
					return true
				})
			}
			ast.Inspect(body, func(node ast.Node) bool {
				switch value := node.(type) {
				case *ast.AssignStmt:
					for index, right := range value.Rhs {
						if !expressionIsTaintedSQLConnection(right, tainted) || index >= len(value.Lhs) {
							continue
						}
						_, localAlias := value.Lhs[index].(*ast.Ident)
						if value.Tok == token.ASSIGN || !localAlias {
							violations = append(violations, fmt.Sprintf(
								"%s:%d callback-accessible sql.Conn cannot be retained",
								relative,
								fileSet.Position(value.Pos()).Line,
							))
						}
					}
				case *ast.SelectorExpr:
					receiver, ok := value.X.(*ast.Ident)
					if !ok {
						return true
					}
					if _, isConnection := tainted[receiver.Name]; isConnection &&
						(value.Sel.Name == "Raw" || value.Sel.Name == "Close") {
						violations = append(violations, fmt.Sprintf(
							"%s:%d callback-accessible sql.Conn cannot call or retain %s",
							relative,
							fileSet.Position(value.Pos()).Line,
							value.Sel.Name,
						))
					}
				case *ast.GoStmt:
					if expressionUsesTaintedSQLConnection(value.Call, tainted) {
						violations = append(violations, fmt.Sprintf(
							"%s:%d callback-accessible sql.Conn cannot escape to a goroutine",
							relative,
							fileSet.Position(value.Pos()).Line,
						))
					}
				case *ast.ReturnStmt:
					for _, result := range value.Results {
						if expressionIsTaintedSQLConnection(result, tainted) {
							violations = append(violations, fmt.Sprintf(
								"%s:%d callback-accessible sql.Conn cannot be returned",
								relative,
								fileSet.Position(value.Pos()).Line,
							))
						}
					}
				case *ast.SendStmt:
					if expressionIsTaintedSQLConnection(value.Value, tainted) {
						violations = append(violations, fmt.Sprintf(
							"%s:%d callback-accessible sql.Conn cannot be sent",
							relative,
							fileSet.Position(value.Pos()).Line,
						))
					}
				}
				return true
			})
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok {
				inspect(function.Type.Params, function.Body)
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			typeSpec, ok := node.(*ast.TypeSpec)
			if ok && typeSpec.Assign.IsValid() && sqlConnectionType(typeSpec.Type, sqlAliases) {
				violations = append(violations, fmt.Sprintf(
					"%s:%d cannot alias database/sql.Conn across the callback boundary",
					relative,
					fileSet.Position(typeSpec.Pos()).Line,
				))
			}
			return true
		})
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.FuncLit)
			if ok {
				inspect(literal.Type.Params, literal.Body)
			}
			return true
		})
		return nil
	})
	sort.Strings(violations)
	return violations, err
}

func sqlConnectionPointerType(expression ast.Expr, aliases map[string]struct{}) bool {
	pointer, ok := expression.(*ast.StarExpr)
	if !ok {
		return false
	}
	return sqlConnectionType(pointer.X, aliases)
}

func sqlConnectionType(expression ast.Expr, aliases map[string]struct{}) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Conn" {
		identifier, identifierOK := expression.(*ast.Ident)
		_, dotImported := aliases["."]
		return identifierOK && dotImported && identifier.Name == "Conn"
	}
	name, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = aliases[name.Name]
	return ok
}

func expressionUsesTaintedSQLConnection(expression ast.Expr, tainted map[string]struct{}) bool {
	used := false
	ast.Inspect(expression, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if ok && !used {
			_, used = tainted[identifier.Name]
		}
		return true
	})
	return used
}

func expressionIsTaintedSQLConnection(expression ast.Expr, tainted map[string]struct{}) bool {
	switch value := expression.(type) {
	case *ast.Ident:
		_, ok := tainted[value.Name]
		return ok
	case *ast.ParenExpr:
		return expressionIsTaintedSQLConnection(value.X, tainted)
	default:
		return false
	}
}

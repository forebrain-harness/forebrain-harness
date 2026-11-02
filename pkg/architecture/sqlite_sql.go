// The "SQLite statements never change" contract (plan 209 §3.5).
package architecture

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// queryMethods are the methods a SQL statement reaches the database through.
// A call to one of them with a string literal argument is a statement the
// snapshot holds.
var queryMethods = map[string]bool{
	"Exec":            true,
	"ExecContext":     true,
	"Prepare":         true,
	"PrepareContext":  true,
	"Query":           true,
	"QueryContext":    true,
	"QueryRow":        true,
	"QueryRowContext": true,
}

// SQLiteStatements extracts every SQL statement the source under root carries
// as a literal, sorted and deduplicated:
//
//   - the literal handed to a query method (Exec, Query, ExecContext, …), and
//   - the SQLite field of a dbx.SQL{…} three-dialect statement.
//
// The set is the contract of multi-database support (plan 209): the SQLite
// text existed before MySQL and PostgreSQL were added and is never edited, so
// a statement that moves into a dbx.SQL leaves the set unchanged and a
// statement whose text moves at all fails TestSQLiteStatementsUnchanged.
//
// Only literals are visible to a syntax-tree walk. A statement assembled by
// concatenation or fmt.Sprintf is not in the set; the test reports how many
// such call sites exist so the plans that translate them can check them by
// hand.
func SQLiteStatements(root string) ([]string, error) {
	stmts, _, err := sqliteStatementScan(root)
	return stmts, err
}

// DynamicSQLSites reports the "file:line" of every query call whose statement
// is not a literal — built with fmt.Sprintf or concatenation — so the plans
// that translate those statements can check them by hand. The snapshot cannot
// see them, and this is how their number and position get reported instead of
// being silently missing (plan 209 §3.5).
func DynamicSQLSites(root string) ([]string, error) {
	_, sites, err := sqliteStatementScan(root)
	return sites, err
}

// dialectLayer is the directory whose statements are by definition not
// SQLite's: pkg/state/dbx picks a statement per dialect at run time, and the
// literals it carries are the MySQL and PostgreSQL halves of a statement plus
// the lock queries only those engines run. It holds no SQLite text to protect,
// so it is left out of the snapshot.
var dialectLayer = filepath.Join("pkg", "state", "dbx")

func sqliteStatementScan(root string) (stmts []string, dynamic []string, err error) {
	fset := token.NewFileSet()
	seen := map[string]bool{}
	pkgRoot := filepath.Join(root, "pkg")
	walkErr := filepath.WalkDir(pkgRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel, relErr := filepath.Rel(root, path); relErr == nil && rel == dialectLayer {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CompositeLit:
				if isDBXSQLType(x.Type) {
					literal, ok := dbxSQLiteField(x)
					if !ok {
						return true
					}
					seen[literal] = true
				}
			case *ast.CallExpr:
				if !isQueryCall(x) {
					return true
				}
				// The statement is the first string literal in the argument
				// list: the first argument of Exec/Query/Prepare, and the one
				// after the context on the *Context forms.
				for _, arg := range x.Args {
					lit, ok := arg.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					if v, uerr := unquoteLiteral(lit.Value); uerr == nil {
						seen[v] = true
					}
					return true
				}
				if rel, relErr := filepath.Rel(root, path); relErr == nil {
					dynamic = append(dynamic, fmt.Sprintf("%s:%d", filepath.ToSlash(rel), fset.Position(x.Pos()).Line))
				}
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		return nil, nil, walkErr
	}
	sort.Strings(dynamic)
	stmts = make([]string, 0, len(seen))
	for s := range seen {
		stmts = append(stmts, s)
	}
	sort.Strings(stmts)
	return stmts, dynamic, nil
}

func isQueryCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return queryMethods[sel.Sel.Name]
}

func isDBXSQLType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel.Name == "SQL"
}

// dbxSQLiteField returns the SQLite field's literal from a dbx.SQL composite
// literal.
func dbxSQLiteField(lit *ast.CompositeLit) (string, bool) {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "SQLite" {
			continue
		}
		if value, ok := kv.Value.(*ast.BasicLit); ok && value.Kind == token.STRING {
			v, err := unquoteLiteral(value.Value)
			if err == nil {
				return v, true
			}
		}
	}
	return "", false
}

// unquoteLiteral reads a Go string literal (raw or interpreted) as its text.
func unquoteLiteral(v string) (string, error) {
	return strconv.Unquote(v)
}

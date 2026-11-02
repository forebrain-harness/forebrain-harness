// Command sqlite-sql-snapshot writes the set of SQL statements the source
// carries as literals (plan 209 §3.5). Compare it with the committed snapshot
// to prove the SQLite statements did not change while MySQL and PostgreSQL
// translations were added:
//
//	go run ./scripts/sqlite-sql-snapshot -out pkg/architecture/testdata/sqlite_sql_snapshot.json
//
// The file is the same set TestSQLiteStatementsUnchanged checks, so it is
// written with the repository root as the default.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/forebrain-harness/forebrain-harness/pkg/architecture"
)

func main() {
	root := flag.String("root", ".", "repository root to extract from")
	out := flag.String("out", "pkg/architecture/testdata/sqlite_sql_snapshot.json", "snapshot file to write")
	flag.Parse()

	stmts, err := architecture.SQLiteStatements(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	body, err := json.MarshalIndent(stmts, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	body = append(body, '\n')
	if dir := filepath.Dir(*out); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %d statements to %s\n", len(stmts), *out)
}

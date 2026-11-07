package store

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two invariants that only make sense together.
//
// Every database the Worker opens allows exactly one connection. Nesting is what
// that setting punishes: a query issued on a pooled handle while that handle's rows
// are still open waits for the only connection, and the connection is released only
// when those rows close. With a request context that has a deadline the wait ends as
// `context deadline exceeded`; with `context.Background()` it never ends. Nothing
// catches this at compile time, and nothing fails at runtime.
//
// Cross-handle contention is a different failure and is not this guard's question:
// two handles on one file get SQLITE_BUSY immediately, because the DSNs in this
// package set no busy handler that takes effect (see the note on `_busy_timeout` in
// db.go — modernc ignores unknown query parameters).
//
// What the guard reads, and what it cannot:
//
//   - It works on syntax, not types: there is no go/types pass here because the rule
//     is local and the cost of loading the world for it is not worth it.
//   - It only sees a nested call written in the same function. Rows produced by a
//     helper (`rows := load(db)`) and iterated here, or a query issued through a
//     helper called from the loop body, are invisible to it.
//   - It does not look into function literals, so `defer func(){ db.Exec(...) }()`
//     inside a loop is not reported.
//   - A transaction owns its connection outright (`tx.dc`, never the pool), so a
//     statement issued inside a transaction's own rows does not wait at all. That is
//     a different rule — a read-then-write on the same table is SQLITE_LOCKED — and
//     is deliberately out of scope here.
//
// Test files are skipped: they open their own handles against temp databases, and
// the production invariant is not theirs to keep.

type scanKind int

const (
	kindNestedQuery scanKind = iota
	kindOpenCap
)

type scanHit struct {
	kind    scanKind
	path    string
	line    int
	message string
}

func (h scanHit) String() string {
	return h.path + ":" + strconv.Itoa(h.line) + " " + h.message
}

type sourceScanner struct {
	fset *token.FileSet
	path string
	hits []scanHit
}

func (s *sourceScanner) text(node ast.Node) string {
	var printed strings.Builder
	_ = printer.Fprint(&printed, s.fset, node)
	return printed.String()
}

func (s *sourceScanner) report(kind scanKind, node ast.Node, message string) {
	s.hits = append(s.hits, scanHit{
		kind: kind, path: s.path, line: s.fset.Position(node.Pos()).Line, message: message,
	})
}

// databaseCall is the selector set that waits for a connection.
func databaseCall(name string) bool {
	switch name {
	case "Query", "QueryContext", "QueryRow", "QueryRowContext",
		"Exec", "ExecContext", "Begin", "BeginTx", "Prepare", "PrepareContext",
		"Ping", "PingContext", "Conn":
		return true
	}
	return false
}

// advancesRows reports whether the expression calls `.Next()` anywhere inside it.
func advancesRows(node ast.Node) bool {
	found := false
	if node == nil {
		return false
	}
	ast.Inspect(node, func(inner ast.Node) bool {
		if call, ok := inner.(*ast.CallExpr); ok {
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Next" {
				found = true
			}
		}
		return true
	})
	return found
}

// iteratesRows reports whether this loop advances a rows handle *itself* — in its
// condition or its post statement (`for rows.Next()`, `for ok := rows.Next(); ok; …`),
// or in its body outside a nested loop. Without that distinction an outer
// `for _, table := range tables` looks like a row loop because its body happens to
// contain one, and the producer query before it reads as a nested call.
func iteratesRows(loop ast.Node) bool {
	var body *ast.BlockStmt
	if statement, ok := loop.(*ast.ForStmt); ok {
		if advancesRows(statement.Init) || advancesRows(statement.Cond) || advancesRows(statement.Post) {
			return true
		}
		body = statement.Body
	} else if statement, ok := loop.(*ast.RangeStmt); ok {
		body = statement.Body
	}
	if body == nil {
		return false
	}
	for _, statement := range body.List {
		found := false
		ast.Inspect(statement, func(node ast.Node) bool {
			switch node.(type) {
			case *ast.ForStmt, *ast.RangeStmt, *ast.FuncLit:
				return false
			}
			if call, ok := node.(*ast.CallExpr); ok {
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Next" {
					found = true
				}
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// scanNestedQueries reports every database call issued on a pooled handle whose rows
// are being iterated at that point.
func (s *sourceScanner) scanNestedQueries(decl *ast.FuncDecl) {
	if decl.Body == nil {
		return
	}

	// rows variable -> the expression it was produced from, e.g. "rows" -> "r.DB".
	// Keyed by bare name with no scope tracking: two variables of the same name in
	// one function, or a closure reusing the name, would confuse it. That is a
	// limitation of reading syntax without types, and it fails in both directions.
	producer := map[string]string{}
	transactions := map[string]bool{}
	// A transaction handed in as a parameter is still a transaction.
	for _, field := range decl.Type.Params.List {
		if s.text(field.Type) != "*sql.Tx" {
			continue
		}
		for _, name := range field.Names {
			transactions[name.Name] = true
		}
	}
	ast.Inspect(decl.Body, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || len(assignment.Lhs) == 0 || len(assignment.Rhs) != 1 {
			return true
		}
		call, ok := assignment.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		name, ok := assignment.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		if selector.Sel.Name == "Begin" || selector.Sel.Name == "BeginTx" {
			transactions[name.Name] = true
			return true
		}
		if strings.HasPrefix(selector.Sel.Name, "Query") {
			producer[name.Name] = s.text(selector.X)
		}
		return true
	})

	ast.Inspect(decl.Body, func(node ast.Node) bool {
		switch node.(type) {
		case *ast.ForStmt, *ast.RangeStmt:
		default:
			return true
		}
		if !iteratesRows(node) {
			return true
		}

		open := ""
		ast.Inspect(node, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Next" {
				return true
			}
			if name, ok := selector.X.(*ast.Ident); ok {
				open = producer[name.Name]
			}
			return true
		})
		if open == "" || transactions[open] {
			return true
		}

		ast.Inspect(node, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !databaseCall(selector.Sel.Name) {
				return true
			}
			if s.text(selector.X) == open {
				s.report(kindNestedQuery, call, selector.Sel.Name+" on "+open+
					" while its rows are open (collect the rows, close them, then query)")
			}
			return true
		})
		return true
	})
}

// scanSingleConnection reports every sql.Open, and every opened handle, that does not
// come with a one-connection cap. The cap is paired with the opened variable rather
// than counted per function, because one cap does not cover another handle.
//
// sql.OpenDB is not covered: it takes a connector, and there is no literal to pair.
func (s *sourceScanner) scanSingleConnection(decl *ast.FuncDecl) {
	if decl.Body == nil {
		return
	}
	opened := map[string]bool{}
	capped := map[string]bool{}
	ast.Inspect(decl.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if name, ok := selector.X.(*ast.Ident); ok && name.Name == "sql" && selector.Sel.Name == "Open" {
			// The handle this call defines. `db, err := sql.Open(...)` is the normal
			// form, so the first name is taken regardless of how many results follow.
			if assignment, ok := enclosingAssignment(decl, call); ok && len(assignment.Lhs) >= 1 {
				if ident, ok := assignment.Lhs[0].(*ast.Ident); ok {
					opened[ident.Name] = true
				}
			}
			return true
		}
		if selector.Sel.Name != "SetMaxOpenConns" {
			return true
		}
		target, ok := selector.X.(*ast.Ident)
		if ok {
			capped[target.Name] = true
		}
		if len(call.Args) != 1 {
			s.report(kindOpenCap, call, "SetMaxOpenConns takes one argument")
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || strings.TrimSpace(literal.Value) != "1" {
			s.report(kindOpenCap, call, "SetMaxOpenConns must be 1")
		}
		return true
	})
	for name := range opened {
		if !capped[name] {
			s.report(kindOpenCap, decl, "sql.Open("+name+") without SetMaxOpenConns(1)")
		}
	}
}

// enclosingAssignment finds the assignment a call is the right-hand side of.
func enclosingAssignment(decl *ast.FuncDecl, call *ast.CallExpr) (*ast.AssignStmt, bool) {
	var found *ast.AssignStmt
	ast.Inspect(decl.Body, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || len(assignment.Rhs) != 1 {
			return true
		}
		if inner, ok := assignment.Rhs[0].(*ast.CallExpr); ok && inner == call {
			found = assignment
		}
		return true
	})
	return found, found != nil
}

// scanSource runs both guards over one file.
func scanSource(t *testing.T, path string, source []byte) []scanHit {
	t.Helper()
	fset := token.NewFileSet()
	// Object resolution is irrelevant here and costs most of the parse.
	file, err := parser.ParseFile(fset, path, source, parser.SkipObjectResolution)
	require.NoError(t, err, path)
	scanner := &sourceScanner{fset: fset, path: path}
	ast.Inspect(file, func(node ast.Node) bool {
		decl, ok := node.(*ast.FuncDecl)
		if !ok {
			return true
		}
		scanner.scanNestedQueries(decl)
		scanner.scanSingleConnection(decl)
		return true
	})
	return scanner.hits
}

// moduleScan is the whole-tree result, computed once for both tests below.
type moduleScan struct {
	hits  []scanHit
	files int
	err   error
}

var scanModuleOnce = sync.OnceValue(func() moduleScan {
	var result moduleScan
	walkErr := filepath.WalkDir("../..", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == "testdata" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, source, parser.SkipObjectResolution)
		if parseErr != nil {
			// A file the guard cannot read is a failure of the guard, not a
			// finding: say which file and why instead of reporting a violation.
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		result.files++
		scanner := &sourceScanner{fset: fset, path: path}
		ast.Inspect(file, func(node ast.Node) bool {
			decl, ok := node.(*ast.FuncDecl)
			if !ok {
				return true
			}
			scanner.scanNestedQueries(decl)
			scanner.scanSingleConnection(decl)
			return true
		})
		result.hits = append(result.hits, scanner.hits...)
		return nil
	})
	if walkErr != nil {
		result.err = walkErr
	}
	return result
})

// scanModuleTree returns the module-wide findings, refusing to pass on an empty scan:
// a walk that finds nothing would make both tests below succeed forever.
func scanModuleTree(t *testing.T) []scanHit {
	t.Helper()
	result := scanModuleOnce()
	require.NoError(t, result.err)
	require.Greater(t, result.files, 100, "the guard scanned almost nothing; its root path is wrong")
	return result.hits
}

func hitsOfKind(hits []scanHit, kind scanKind) []string {
	var found []string
	for _, hit := range hits {
		if hit.kind == kind {
			found = append(found, hit.String())
		}
	}
	return found
}

func TestWorkerDatabasesAllowOneConnection(t *testing.T) {
	// A second connection would break the write serialization the stores assume, and
	// raising the cap is the tempting way to "fix" a hang that is really a nested
	// query — so both halves are asserted together.
	assert.Empty(t, hitsOfKind(scanModuleTree(t), kindOpenCap),
		"every Worker database allows exactly one connection")
}

func TestNoQueryWhileRowsAreOpen(t *testing.T) {
	assert.Empty(t, hitsOfKind(scanModuleTree(t), kindNestedQuery),
		"no database call is issued on a pooled handle while its rows are open")
}

func TestTheGuardsFireOnAViolation(t *testing.T) {
	// A guard that silently matches nothing would pass the tests above forever, so
	// each rule is shown to reject a violation and to accept its neighbour.
	cases := []struct {
		name   string
		source string
		kind   scanKind
		want   int
	}{
		{
			name: "query nested inside the iteration",
			source: `package p
func f(db *T) {
	rows, err := db.Query("SELECT 1")
	if err != nil { return }
	for rows.Next() {
		if _, err := db.Exec("INSERT"); err != nil { return }
	}
	rows.Close()
}`,
			kind: kindNestedQuery, want: 1,
		},
		{
			name: "iteration with the break form",
			source: `package p
func f(db *T) {
	rows, err := db.Query("SELECT 1")
	if err != nil { return }
	for {
		if !rows.Next() { break }
		_, _ = db.Exec("INSERT")
	}
	rows.Close()
}`,
			kind: kindNestedQuery, want: 1,
		},
		{
			name: "iteration with a post statement",
			source: `package p
func f(db *T) {
	rows, err := db.Query("SELECT 1")
	if err != nil { return }
	for ok := rows.Next(); ok; ok = rows.Next() {
		_, _ = db.Exec("INSERT")
	}
	rows.Close()
}`,
			kind: kindNestedQuery, want: 1,
		},
		{
			name: "query after the rows are closed is fine",
			source: `package p
func f(db *T) {
	rows, err := db.Query("SELECT 1")
	if err != nil { return }
	for rows.Next() {
		_ = rows.Scan()
	}
	rows.Close()
	_, _ = db.Exec("INSERT")
}`,
			kind: kindNestedQuery, want: 0,
		},
		{
			name: "a different handle in the loop is fine",
			source: `package p
func f(sessionDB *T, tx *U) {
	rows, err := sessionDB.Query("SELECT 1")
	if err != nil { return }
	for rows.Next() {
		if _, err := tx.Exec("INSERT"); err != nil { return }
	}
	rows.Close()
}`,
			kind: kindNestedQuery, want: 0,
		},
		{
			name: "an outer range loop over tables is not a row loop",
			source: `package p
func f(db *T, tx *U) {
	for _, table := range tables {
		rows, err := db.Query("SELECT 1 FROM " + table)
		if err != nil { return }
		for rows.Next() {
			if _, err := tx.Exec("INSERT"); err != nil { return }
		}
		rows.Close()
	}
}`,
			kind: kindNestedQuery, want: 0,
		},
		{
			name: "a transaction taken as a parameter is not a pooled handle",
			source: `package p
func f(ctx context.Context, tx *sql.Tx) {
	rows, err := tx.Query("SELECT 1")
	if err != nil { return }
	for rows.Next() {
		_ = tx.QueryRow("SELECT 1").Scan()
	}
	rows.Close()
}`,
			kind: kindNestedQuery, want: 0,
		},
		{
			name: "a transaction begun here is not a pooled handle either",
			source: `package p
func f(db *T) {
	tx, err := db.Begin()
	if err != nil { return }
	rows, err := tx.Query("SELECT 1")
	if err != nil { return }
	for rows.Next() {
		_ = tx.QueryRow("SELECT 1").Scan()
	}
	rows.Close()
}`,
			kind: kindNestedQuery, want: 0,
		},
		{
			name: "a database opened without the cap",
			source: `package p
func f() {
	db, err := sql.Open("sqlite", path)
	if err != nil { return }
	_ = db
}`,
			kind: kindOpenCap, want: 1,
		},
		{
			name: "a cap above one",
			source: `package p
func f() {
	db, err := sql.Open("sqlite", path)
	if err != nil { return }
	db.SetMaxOpenConns(4)
	_ = db
}`,
			kind: kindOpenCap, want: 1,
		},
		{
			name: "a cap that is not a literal",
			source: `package p
func f(limit int) {
	db, err := sql.Open("sqlite", path)
	if err != nil { return }
	db.SetMaxOpenConns(limit)
	_ = db
}`,
			kind: kindOpenCap, want: 1,
		},
		{
			name: "a cap with no argument",
			source: `package p
func f() {
	db, err := sql.Open("sqlite", path)
	if err != nil { return }
	db.SetMaxOpenConns()
	_ = db
}`,
			kind: kindOpenCap, want: 1,
		},
		{
			name: "one cap does not cover a second handle",
			source: `package p
func f() {
	first, err := sql.Open("sqlite", path)
	if err != nil { return }
	first.SetMaxOpenConns(1)
	second, err := sql.Open("sqlite", path)
	_ = first
	_ = second
}`,
			kind: kindOpenCap, want: 1,
		},
		{
			name: "the shape the Worker actually uses",
			source: `package p
func f() {
	db, err := sql.Open("sqlite", path)
	if err != nil { return }
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
}`,
			kind: kindOpenCap, want: 0,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Len(t, hitsOfKind(scanSource(t, "fixture.go", []byte(testCase.source)), testCase.kind),
				testCase.want)
		})
	}
}

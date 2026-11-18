package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settlementWriter is one call site that writes a tool call's terminal state.
type settlementWriter struct {
	file     string
	function string
	writer   string
}

// The tool-settlement rule is that an outcome is recorded in exactly one place
// per branch, so a transcript cannot hold two endings for one call and no branch
// can forget one. Until now that rule lived in a comment above settleToolPlan and
// a new writer could be added without any test noticing.
//
// This scans the package's own source instead of trusting the comment: a fourth
// writer, or a second writer in a function that already has one, fails here.
func TestToolSettlementHasNoUnlistedWriter(t *testing.T) {
	expected := []string{
		"dispatcher.go *Loop.completePrecomputedToolCall recordToolCompleted",
		"dispatcher.go *Loop.settleToolPlan recordToolCompleted",
		"dispatcher.go *Loop.settleToolPlan recordToolFailed",
		"dispatcher.go *Loop.settleToolPlan recordToolFailed",
		"loop.go *Loop.Run recordToolCompleted",
	}

	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	var found []string
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		// Walk each declaration with its own enclosing function name: a call site
		// is only meaningful together with the branch it settles in.
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			enclosing := functionName(fn)
			ast.Inspect(fn, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch selector.Sel.Name {
				case "recordToolCompleted", "recordToolFailed":
					found = append(found, name+" "+enclosing+" "+selector.Sel.Name)
				}
				return true
			})
		}
	}

	sort.Strings(found)
	sort.Strings(expected)
	assert.Equal(t, expected, found,
		"tool settlement writers changed: a new one must be justified, not discovered")
}

// functionName is the name the package calls the function by: a method reports
// its receiver so two types with the same method name stay distinguishable.
func functionName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	receiver := fn.Recv.List[0].Type
	if star, ok := receiver.(*ast.StarExpr); ok {
		receiver = star.X
	}
	name := ""
	if ident, ok := receiver.(*ast.Ident); ok {
		name = ident.Name
	}
	if name == "" {
		return fn.Name.Name
	}
	return "*" + name + "." + fn.Name.Name
}

// A scan that silently reads nothing would pass the assertion above forever, so
// pin that it sees the package it claims to.
func TestToolSettlementScanReadsThePackage(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", "dispatcher.go"), nil, 0)
	require.NoError(t, err)
	require.NotNil(t, file)
}

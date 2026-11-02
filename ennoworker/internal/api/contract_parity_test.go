package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contractOpenAPIPath is the authoritative Worker contract, three levels above
// this module (ennoworker/internal/api -> repository root).
const contractOpenAPIPath = "../../../contracts/openapi.yaml"

// TestRouterCoversTheContractPaths is the Go half of the contract drift guard.
// The TS half (ci.yml: `npm run generate:api` plus `git diff --exit-code`)
// proves the generated types match the YAML. Neither half notices a route the
// mux serves but the contract never declares, so this test extracts the
// `mux.HandleFunc` patterns from server.go with go/ast and compares the path
// set against the contract's `paths` block in both directions: an undeclared
// route leaves every client without a type, and a declared route nothing serves
// is a promise the Worker cannot keep.
func TestRouterCoversTheContractPaths(t *testing.T) {
	served := routerContractPaths(t)
	declared := contractPaths(t)

	// A parser regression that returned nothing would make both diffs empty and
	// the comparison green, so pin the shape of what was read.
	require.NotEmpty(t, served, "no mux.HandleFunc pattern was extracted from server.go")
	require.NotEmpty(t, declared, "no path was extracted from the contract")

	assert.Empty(t, missingFromContract(served, declared),
		"routes served by the mux but absent from contracts/openapi.yaml")
	assert.Empty(t, missingFromRouter(served, declared),
		"contract paths the mux does not serve")
}

// TestNormalizeContractPathTreatsWildcardsAsParameters pins the normalization
// the comparison depends on: the Go mux spells a catch-all parameter
// `{relPath...}` where the contract spells it `{relPath}`.
func TestNormalizeContractPathTreatsWildcardsAsParameters(t *testing.T) {
	cases := map[string]string{
		"/v1/runtime":                             "/v1/runtime",
		"/v1/sessions/{sessionID}/events":         "/v1/sessions/{param}/events",
		"/v1/skills/disabled/{relPath...}":        "/v1/skills/disabled/{param}",
		"/v1/attention/{attentionID}/dismiss":     "/v1/attention/{param}/dismiss",
		"/v1/projects/{projectID}/graph-bindings": "/v1/projects/{param}/graph-bindings",
		"/v1/skills/remove/{a}/nested/{b...}":     "/v1/skills/remove/{param}/nested/{param}",
	}
	for input, expected := range cases {
		assert.Equal(t, expected, normalizeContractPath(input), input)
	}
}

var contractPathParameter = regexp.MustCompile(`\{[^}]*\}`)

// normalizeContractPath replaces every path parameter with one placeholder so a
// parameter rename on either side is not reported as a missing route.
func normalizeContractPath(path string) string {
	return contractPathParameter.ReplaceAllString(path, "{param}")
}

// routerContractPaths reads the route patterns registered on the mux. The
// patterns are string literals, so parsing is enough: no Go dependency on the
// handler bodies. Registrations inside `if` blocks are included because the
// contract describes the route surface, not which optional dependencies happen
// to be wired in one process.
func routerContractPaths(t *testing.T) map[string]struct{} {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), "server.go", nil, 0)
	require.NoError(t, err)

	paths := make(map[string]struct{})
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "HandleFunc" {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		pattern, err := strconv.Unquote(literal.Value)
		require.NoError(t, err, literal.Value)
		_, route, found := strings.Cut(pattern, " ")
		require.True(t, found, "mux pattern %q carries no method", pattern)
		paths[normalizeContractPath(route)] = struct{}{}
		return true
	})
	return paths
}

// contractPaths reads the `paths` block line by line. The Worker has no YAML
// dependency and this block is one two-space-indented key per line, so a
// dedicated parser would be new surface for no gain. Limitations, deliberate:
// only the block that starts at the top-level `paths:` key is read, aliases and
// multi-line keys are not resolved, and HTTP methods are not compared (the
// method set per path is out of scope for this guard). A path item nested
// anywhere else — for instance appended after `components:` — is invisible here,
// which is exactly the drift this test must fail on: openapi-typescript ignores
// it too, so the generated types would be missing the operation.
func contractPaths(t *testing.T) map[string]struct{} {
	t.Helper()
	contents, err := os.ReadFile(contractOpenAPIPath)
	require.NoError(t, err)

	paths := make(map[string]struct{})
	inPaths := false
	for _, line := range strings.Split(string(contents), "\n") {
		if !inPaths {
			inPaths = line == "paths:"
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			// The next top-level key ends the block.
			inPaths = false
			continue
		}
		if strings.HasPrefix(line, "   ") {
			// Operation, parameter, or response under a path item.
			continue
		}
		key, ok := strings.CutSuffix(strings.TrimPrefix(line, "  "), ":")
		require.True(t, ok && strings.HasPrefix(key, "/"),
			"unexpected line in the contract paths block: %q", line)
		paths[normalizeContractPath(key)] = struct{}{}
	}
	return paths
}

func missingFromContract(served, declared map[string]struct{}) []string {
	return sortedDifference(served, declared)
}

func missingFromRouter(served, declared map[string]struct{}) []string {
	return sortedDifference(declared, served)
}

func sortedDifference(left, right map[string]struct{}) []string {
	var difference []string
	for key := range left {
		if _, ok := right[key]; !ok {
			difference = append(difference, key)
		}
	}
	sort.Strings(difference)
	return difference
}

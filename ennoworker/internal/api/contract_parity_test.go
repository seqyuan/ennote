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
// `mux.HandleFunc` patterns from server.go with go/ast and compares the
// operation set against the contract's `paths` block in both directions: an
// undeclared route leaves every client without a type, and a declared route
// nothing serves is a promise the Worker cannot keep.
//
// It compares methods, not just paths. A handler registered under a second verb
// on an already-declared path is invisible to both halves otherwise — the TS half
// only proves the types match the YAML, and the path comparison cannot see the
// missing verb.
//
// It reads the contract as a data file, so Go's test cache can serve a stale pass
// when the only change is the contract itself. CI's matrix includes a -count=1
// leg, which bypasses the cache; run this with -count=1 after editing the YAML.
func TestRouterCoversTheContractPaths(t *testing.T) {
	served := routerContractOperations(t)
	declared := contractOperations(t)

	// A parser regression that returned nothing would make both diffs empty and
	// the comparison green, so pin the shape of what was read.
	require.NotEmpty(t, served, "no mux.HandleFunc pattern was extracted from server.go")
	require.NotEmpty(t, declared, "no path was extracted from the contract")

	assert.Empty(t, missingFromContract(served, declared),
		"operations served by the mux but absent from contracts/openapi.yaml")
	assert.Empty(t, missingFromRouter(served, declared),
		"operations the contract declares but the mux does not serve")

}

// TestContractOperationsReadsMethodsPerPath pins the parser's shape on a fixture
// rather than on the live contract: a regression that kept one method per path,
// or read a nested key as an operation, would otherwise leave both drift diffs
// empty on any contract where every path happens to have a single verb.
func TestContractOperationsReadsMethodsPerPath(t *testing.T) {
	operations := parseContractOperations(t, `paths:
  /v1/one:
    get:
      operationId: getOne
      responses:
        "200":
          description: ok
  /v1/two:
    get:
      operationId: getTwo
      security: []
    delete:
      operationId: deleteTwo
components:
  schemas:
    Placeholder:
      type: object
`)

	assert.Equal(t, map[string]map[string]struct{}{
		"/v1/one": {"get": {}},
		"/v1/two": {"get": {}, "delete": {}},
	}, operations)
}

// TestContractOperationsKeepsMethodsFromCollidingPaths pins the normalization the
// comparison depends on: two paths that differ only in a parameter name normalize
// to one key, and neither path's methods may overwrite the other's.
func TestContractOperationsKeepsMethodsFromCollidingPaths(t *testing.T) {
	operations := parseContractOperations(t, `paths:
  /v1/skills/remove/{a}:
    get:
      operationId: getOne
  /v1/skills/remove/{b}:
    post:
      operationId: postAnother
`)

	assert.Equal(t, map[string]map[string]struct{}{
		"/v1/skills/remove/{param}": {"get": {}, "post": {}},
	}, operations)
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

// routerContractOperations reads the route patterns registered on the mux. The
// patterns are string literals, so parsing is enough: no Go dependency on the
// handler bodies. Registrations inside `if` blocks are included because the
// contract describes the route surface, not which optional dependencies happen
// to be wired in one process.
func routerContractOperations(t *testing.T) map[string]map[string]struct{} {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), "server.go", nil, 0)
	require.NoError(t, err)

	operations := make(map[string]map[string]struct{})
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
		method, route, found := strings.Cut(pattern, " ")
		require.True(t, found,
			"mux pattern %q has no method prefix; the contract cannot express an all-method route, so register the verbs explicitly",
			pattern)
		path := normalizeContractPath(route)
		if operations[path] == nil {
			operations[path] = make(map[string]struct{})
		}
		operations[path][strings.ToLower(method)] = struct{}{}
		return true
	})
	return operations
}

// httpMethods is the set of OAS path-item keys that name an operation.
var httpMethods = map[string]struct{}{
	"get": {}, "put": {}, "post": {}, "delete": {}, "patch": {}, "head": {}, "options": {}, "trace": {},
}

// contractOperations reads the `paths` block line by line. The Worker has no YAML
// dependency and this block is one key per line, so a dedicated parser would be
// new surface for no gain. Limitations, deliberate: only the block that starts at
// the top-level `paths:` key is read, aliases and multi-line keys are not
// resolved, and a path item nested anywhere else — for instance appended after
// `components:` — is invisible here, which is exactly the drift this test must
// fail on: openapi-typescript ignores it too, so the generated types would be
// missing the operation.
func contractOperations(t *testing.T) map[string]map[string]struct{} {
	t.Helper()
	contents, err := os.ReadFile(contractOpenAPIPath)
	require.NoError(t, err)
	return parseContractOperations(t, string(contents))
}

func parseContractOperations(t *testing.T, contents string) map[string]map[string]struct{} {
	t.Helper()

	operations := make(map[string]map[string]struct{})
	inPaths := false
	current := ""
	for _, line := range strings.Split(contents, "\n") {
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
		if strings.HasPrefix(line, "     ") {
			// Under an operation: parameters, responses, security.
			continue
		}
		if strings.HasPrefix(line, "    ") {
			// Under a path item: an operation, a parameter, or a summary.
			key := strings.TrimSuffix(strings.TrimPrefix(line, "    "), ":")
			if _, isMethod := httpMethods[strings.ToLower(key)]; !isMethod {
				continue
			}
			require.NotEmpty(t, current, "operation %q appears before any path", key)
			operations[current][strings.ToLower(key)] = struct{}{}
			continue
		}
		if strings.HasPrefix(line, "   ") {
			continue
		}
		key, ok := strings.CutSuffix(strings.TrimPrefix(line, "  "), ":")
		require.True(t, ok && strings.HasPrefix(key, "/"),
			"unexpected line in the contract paths block: %q", line)
		current = normalizeContractPath(key)
		if operations[current] == nil {
			operations[current] = make(map[string]struct{})
		}
	}
	return operations
}

// missingFromContract lists the operations the mux serves but the contract never
// declares. Neither half of the drift guard saw these before: the TS half only
// proves the generated types match the YAML, and the path comparison ignored
// methods, so a handler registered under a second verb on a declared path was
// invisible to both.
func missingFromContract(served, declared map[string]map[string]struct{}) []string {
	return sortedOperationDifference(served, declared)
}

func missingFromRouter(served, declared map[string]map[string]struct{}) []string {
	return sortedOperationDifference(declared, served)
}

func sortedOperationDifference(left, right map[string]map[string]struct{}) []string {
	var difference []string
	for path, methods := range left {
		for method := range methods {
			if _, ok := right[path][method]; ok {
				continue
			}
			difference = append(difference, strings.ToUpper(method)+" "+path)
		}
	}
	sort.Strings(difference)
	return difference
}

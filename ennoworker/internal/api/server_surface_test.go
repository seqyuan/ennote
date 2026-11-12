package api

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The dependency surface of `Server`, per file, recorded as a ratchet.
//
// `Server` is one struct with 45 fields and every handler in the package hanging off
// it, which is the shape this repository has chosen: files group the handlers, the
// struct carries the dependencies. The cost is that no file declares what it needs —
// any handler can reach any field — and the struct can grow one plausible-looking
// field at a time without anyone noticing, until a test fixture has to wire 45
// dependencies to exercise a handler that uses two.
//
// This table is that declaration. It grows only by an explicit edit: a file that
// reaches for a field it never used before fails until the table says so, and a field
// no file reads fails on its own. The shrink rule is weaker by nature — a file with
// several handlers keeps an entry as long as one of them still reads the field — so it
// catches a file that stopped being used, not a handler that did.
//
// It is deliberately per file and not per method: per method would be the accurate
// answer to "what does this endpoint need", but it is also the answer the sub-struct
// split would produce, and this table is what a split would be reviewed against.
//
// What this table does not do is shrink `Server`, and it is not evidence against
// splitting it. The groups are already narrow — mcp.go reads two fields, and there are
// tests (`packs_inspect_test.go`, `mcp_test.go`) that build a Server from a handful of
// fields — so the cheapest real step is a route-registration seam per group
// (`func (s *Server) mcpRoutes(mux)`), which is what narrows a fixture.
var serverSurface = map[string][]string{
	// artifacts.go owns the artifact routes, including the ones that read across a
	// Project's Session databases: `Artifacts` is the service it scopes per Session,
	// `SessionStores` the manager it fans out through.
	"artifacts.go":              {"Artifacts", "SessionStores"},
	"approvals.go":              {"Approvals", "Control", "Runs", "Sessions", "StandingApprovals"},
	"attention.go":              {"Attention", "DB", "SessionStores", "Sessions"},
	"delegation_approvals.go":   {"DelegationApprovals"},
	"delegation_follow_up.go":   {"Delegations"},
	"delegations.go":            {"Delegations", "Runs"},
	"delivery_events.go":        {"Delegations", "Sessions"},
	"events.go":                 {"Events", "Hub", "Runs"},
	"flow_sources.go":           {"CheckAllowlist", "CheckAllowlistAllowsAll", "FlowRuns", "Models", "Sessions", "SkillCatalog"},
	"graph_builder.go":          {"GlobalSources", "GraphBuilder"},
	"graph_check_approvals.go":  {"CheckRunner"},
	"graph_runs.go":             {"Control", "GraphRunResume", "SessionStores"},
	"graph_sources.go":          {"GlobalSources", "GraphRuns", "Models", "Sessions", "SkillCatalog"},
	"host_directories.go":       {"HostHome", "HostRoots"},
	"mcp.go":                    {"MCP", "Projects"},
	"packs_install.go":          {"Packs"},
	"project_active_runs.go":    {"Runs", "SessionStores"},
	"project_graph_bindings.go": {"GlobalSources", "Projects"},
	"prompt_templates.go":       {"Prompts"},
	"provider_doctor.go":        {"Doctor"},
	"provider_models.go":        {"Doctor", "Providers"},
	"hero_sources.go":           {"Models"},
	"run_mcp.go":                {"DB"},
	"run_messages.go":           {"DB"},
	"run_prompt.go":             {"DB"},
	"session_events.go":         {"Approvals", "Compactions", "DB", "Events", "InstanceID", "Queue", "Runs", "Sessions"},
	"session_skills.go":         {"GlobalSources", "SkillCatalog"},
	"session_scope.go": {
		"Approvals", "Artifacts", "Attention", "Branches", "Compactions",
		"DelegationApprovals", "Delegations", "Events", "MCP", "Messages", "Queue",
		"Runs", "SessionStores", "StandingApprovals",
	},
	"skills.go":          {"Projects", "SkillRoots", "Skills", "Trust"},
	"workspace_files.go": {"Projects"},
	// server.go is the widest row for two different reasons, and only the first is a
	// handler dependency: SessionStores, GraphBuilder, MCP and SkillRoots are nil
	// guards in routes(), not fields a handler reads.
	"server.go": {
		"Artifacts", "Branches", "CatalogDB", "Compactions", "Control", "DB",
		"GraphBuilder", "InstanceID", "MCP", "Messages", "Models", "Policies",
		"Projection", "Projects", "PromptGate", "Providers", "Queue", "Runs",
		"Sandbox", "SessionStores", "Sessions", "SkillRoots", "Token", "UsageDB",
	},
}

// receiverOf returns the receiver name of a `func (x *Server)` declaration, or "" for
// any other function. The scan below matches `s.<field>`, so a handler using a
// different receiver name would be invisible to it — which would make this guard
// silently weaker rather than fail.
func receiverOf(decl *ast.FuncDecl) string {
	if decl.Recv == nil || len(decl.Recv.List) != 1 {
		return ""
	}
	// Both receivers count. Recognising only `*Server` would let `func (x Server)`
	// read fields under a name the scan does not match, which is the same silent
	// weakening the name rule exists to prevent.
	typeOf := decl.Recv.List[0].Type
	if pointer, ok := typeOf.(*ast.StarExpr); ok {
		typeOf = pointer.X
	}
	identifier, ok := typeOf.(*ast.Ident)
	if !ok || identifier.Name != "Server" {
		return ""
	}
	if len(decl.Recv.List[0].Names) != 1 {
		return ""
	}
	return decl.Recv.List[0].Names[0].Name
}

// usedFields reports which of the Server fields a source file reads. A Server method
// with any receiver name other than `s` is an error rather than a silent blind spot.
func usedFields(t *testing.T, path string, source []byte, fields []string) ([]string, error) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, parser.SkipObjectResolution)
	require.NoError(t, err, path)

	var receiverProblem error
	ast.Inspect(file, func(node ast.Node) bool {
		decl, ok := node.(*ast.FuncDecl)
		if !ok {
			return true
		}
		if receiver := receiverOf(decl); receiver != "" && receiver != "s" {
			receiverProblem = fmt.Errorf("%s: %s uses receiver %q; the surface scan matches `s.`, so name every Server method's receiver `s`",
				path, decl.Name.Name, receiver)
		}
		return true
	})
	if receiverProblem != nil {
		return nil, receiverProblem
	}

	// Matching the text is enough here: the receiver name is enforced above, and a
	// field name is unique within the struct. The word boundary is what keeps a
	// `sources.Runs` or a `params.DB` out of the count.
	used := map[string]bool{}
	if len(fields) == 0 {
		return nil, nil
	}
	reads := regexp.MustCompile(`\bs\.(` + strings.Join(fields, "|") + `)\b`)
	for _, match := range reads.FindAllStringSubmatch(string(source), -1) {
		used[match[1]] = true
	}
	names := make([]string, 0, len(used))
	for name := range used {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// compareSurface reports every difference between the recorded and the actual
// dependency surface, plus any field no file needs.
func compareSurface(recorded, actual map[string][]string, fields []string) []string {
	var problems []string
	for file, used := range actual {
		declared := map[string]bool{}
		for _, field := range recorded[file] {
			declared[field] = true
		}
		for _, field := range used {
			if !declared[field] {
				problems = append(problems, file+" now reads "+field+"; add it to serverSurface if that is intended")
			}
		}
	}
	for file, declared := range recorded {
		used := map[string]bool{}
		for _, field := range actual[file] {
			used[field] = true
		}
		for _, field := range declared {
			if !used[field] {
				problems = append(problems, file+" no longer reads "+field+"; remove it from serverSurface")
			}
		}
	}
	readAnywhere := map[string]bool{}
	for _, used := range actual {
		for _, field := range used {
			readAnywhere[field] = true
		}
	}
	for _, field := range fields {
		if !readAnywhere[field] {
			problems = append(problems, "Server."+field+" is read by nothing; delete the field")
		}
	}
	sort.Strings(problems)
	return problems
}

func TestServerDependencySurfaceIsUnchanged(t *testing.T) {
	fields := make([]string, 0, 45)
	structType := reflect.TypeOf(Server{})
	for index := 0; index < structType.NumField(); index++ {
		fields = append(fields, structType.Field(index).Name)
	}

	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	actual := map[string][]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, readErr := os.ReadFile(name)
		require.NoError(t, readErr)
		// A wrong working directory would otherwise scan another package's files and
		// report dependencies that make no sense here.
		require.Contains(t, string(source), "package api", name)
		used, usedErr := usedFields(t, name, source, fields)
		require.NoError(t, usedErr)
		if len(used) > 0 {
			actual[name] = used
		}
	}
	require.NotEmpty(t, actual, "the scan found no handlers; its working directory is wrong")

	assert.Empty(t, compareSurface(serverSurface, actual, fields))
}

func TestTheSurfaceGuardFiresOnAViolation(t *testing.T) {
	// Same reasoning as the other guards: one that matches nothing passes forever.
	cases := []struct {
		name     string
		fields   []string
		recorded map[string][]string
		actual   map[string][]string
		want     []string
	}{
		{
			name:     "a new dependency",
			fields:   []string{"MCP", "Runs"},
			recorded: map[string][]string{"mcp.go": {"MCP"}},
			actual:   map[string][]string{"mcp.go": {"MCP", "Runs"}},
			want:     []string{"mcp.go now reads Runs; add it to serverSurface if that is intended"},
		},
		{
			name:     "a removed dependency",
			fields:   []string{"MCP", "Runs"},
			recorded: map[string][]string{"mcp.go": {"MCP", "Runs"}},
			// Runs still being a struct field is part of the fixture, so the removal
			// shows up twice: once for the table, once for the field nobody reads.
			actual: map[string][]string{"mcp.go": {"MCP"}},
			want: []string{
				"Server.Runs is read by nothing; delete the field",
				"mcp.go no longer reads Runs; remove it from serverSurface",
			},
		},
		{
			// The dead-field rule on its own: a new field nobody reads, with the table
			// untouched. This is the case a real change produces.
			name:     "a field nobody reads",
			fields:   []string{"MCP", "Doctor"},
			recorded: map[string][]string{"mcp.go": {"MCP"}},
			actual:   map[string][]string{"mcp.go": {"MCP"}},
			want:     []string{"Server.Doctor is read by nothing; delete the field"},
		},
		{
			name:     "a file that stopped existing",
			fields:   []string{"Doctor"},
			recorded: map[string][]string{"gone.go": {"Doctor"}},
			actual:   map[string][]string{"mcp.go": {"Doctor"}},
			want: []string{
				"gone.go no longer reads Doctor; remove it from serverSurface",
				"mcp.go now reads Doctor; add it to serverSurface if that is intended",
			},
		},
		{
			name:     "matching surface",
			fields:   []string{"MCP", "Runs"},
			recorded: map[string][]string{"mcp.go": {"MCP", "Runs"}},
			actual:   map[string][]string{"mcp.go": {"MCP", "Runs"}},
			want:     nil,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.want, compareSurface(testCase.recorded, testCase.actual, testCase.fields))
		})
	}
}

func TestUsedFieldsReadsOnlyTheServersOwnFields(t *testing.T) {
	source := []byte(`package api
func (s *Server) handler(sources *sources, params params) {
	s.Runs.Get()
	other.MCP.Do()
	local := struct{ Runs int }{}
	_ = local.Runs
	sources.Runs.List()
	params.DB.Query()
}`)
	// A same-named field on another value must not count, and neither must a receiver
	// whose name merely ends in `s`: only the word `s.` is the Server's.
	used, err := usedFields(t, "fixture.go", source, []string{"Runs", "MCP", "DB"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Runs"}, used)
}

func TestReceiverNameIsEnforcedSoTheScanStaysSound(t *testing.T) {
	// The scan matches `s.`; a handler on `srv` would be invisible to it, which would
	// quietly weaken the guard instead of failing. So the receiver name is part of the
	// rule.
	source := []byte(`package api
func (srv *Server) handler() {}`)
	_, err := usedFields(t, "fixture.go", source, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `uses receiver "srv"`)

	// A value receiver takes the same rule. Recognising only `*Server` would let this
	// one read fields under a name the scan does not match.
	valueReceiver := []byte(`package api
func (other Server) handler() {}`)
	_, err = usedFields(t, "fixture.go", valueReceiver, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `uses receiver "other"`)
}

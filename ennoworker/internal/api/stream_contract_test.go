package api

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The streaming endpoints used to describe their response body as a bare
// `type: string`, which left the UI's primary transport outside the generated
// types: a frame shape could change in Go and no type-checker would notice.
// These tests are the Go half of that guard. The TS half is structural — the
// client imports the generated frame unions — so what remains is proving the
// contract still describes what the wire actually carries.
//
// Both sides are extracted mechanically (contract text, Go AST or struct tags)
// rather than hand-copied, because a guard that repeats the value it checks
// passes even when both are wrong.

const domainRunSourcePath = "../domain/run.go"

// TestLiveEventTypeEnumMatchesDomain pins the live delta names. RunEvent and
// RunLiveEventFrame both spell `text_delta`, so the enum cannot be derived from
// the durable one; it has to be checked against domain/run.go, which is where
// the publisher picks the string.
func TestLiveEventTypeEnumMatchesDomain(t *testing.T) {
	declared := contractEnumValues(t, "RunLiveEventType")
	require.NotEmpty(t, declared, "no enum value was read for RunLiveEventType")

	published := domainLiveEventTypes(t)
	require.NotEmpty(t, published, "no Live* constant was found in domain/run.go")

	assert.Equal(t, published, declared,
		"RunLiveEventType must list exactly the live delta kinds the Worker publishes")
}

// TestStreamFrameSchemasMatchTheWire compares each frame schema against the Go
// value that produces it: the live frame against domain.LiveRunEvent's json
// tags, the durable frame against the map eventJSON actually marshals, and the
// transient snapshot against sessionSnapshot. A field added on one side only is
// the drift this catches.
func TestStreamFrameSchemasMatchTheWire(t *testing.T) {
	t.Run("live frame follows LiveRunEvent tags", func(t *testing.T) {
		assert.Equal(t,
			jsonFieldNames(reflect.TypeOf(domain.LiveRunEvent{})),
			contractSchemaProperties(t, "RunLiveEventFrame"))
	})

	t.Run("durable frame follows eventJSON", func(t *testing.T) {
		encoded := eventJSON(domain.RunEvent{
			EventID:   1,
			RunID:     "run-1",
			Seq:       1,
			EventType: "run_started",
			Payload:   json.RawMessage(`{}`),
		})
		var wire map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(encoded, &wire))

		keys := make([]string, 0, len(wire))
		for key := range wire {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		assert.Equal(t, keys, contractSchemaProperties(t, "RunEvent"),
			"RunEvent must describe exactly the keys eventJSON writes")
	})

	t.Run("session snapshot follows sessionSnapshot tags", func(t *testing.T) {
		assert.Equal(t,
			jsonFieldNames(reflect.TypeOf(sessionSnapshot{})),
			contractSchemaProperties(t, "SessionRunSnapshot"))
	})
}

// TestSessionMessageCommittedFrameDocumentsItsAsymmetry guards the one frame
// that breaks the durable shape. sessionEventFrame drops eventId/seq for
// message_committed and adds a committed seq range; the schema is only useful
// if it says so, and it is only correct while the code says the same.
func TestSessionMessageCommittedFrameDocumentsItsAsymmetry(t *testing.T) {
	properties := contractSchemaProperties(t, "SessionMessageCommittedFrame")
	require.NotEmpty(t, properties)

	assert.Contains(t, properties, "firstSeq")
	assert.Contains(t, properties, "lastSeq")
	assert.NotContains(t, properties, "eventId",
		"message_committed frames carry the cursor on the SSE id line, not in the body")
	assert.NotContains(t, properties, "seq")
}

// jsonFieldNames returns the sorted json key of every field on a struct type.
func jsonFieldNames(structType reflect.Type) []string {
	names := make([]string, 0, structType.NumField())
	for i := 0; i < structType.NumField(); i++ {
		tag := structType.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// domainLiveEventTypes reads the `Live* = "..."` constants out of domain/run.go.
// Reading the source instead of a hand-maintained slice means a new constant is
// picked up without anyone remembering to register it.
func domainLiveEventTypes(t *testing.T) []string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), domainRunSourcePath, nil, 0)
	require.NoError(t, err)

	var types []string
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range value.Names {
				if !strings.HasPrefix(name.Name, "Live") || i >= len(value.Values) {
					continue
				}
				literal, ok := value.Values[i].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				text, err := strconv.Unquote(literal.Value)
				require.NoError(t, err, literal.Value)
				types = append(types, text)
			}
		}
	}
	sort.Strings(types)
	return types
}

// contractSchemaProperties returns the sorted property names of one schema in
// components/schemas. The Worker has no YAML dependency (see
// contract_parity_test.go for the same trade-off), and this block is one
// four-space key per line with one six-space `properties:` header.
func contractSchemaProperties(t *testing.T, schema string) []string {
	t.Helper()
	var properties []string
	inProperties := false
	for _, line := range contractSchemaBlock(t, schema) {
		if line == "      properties:" {
			inProperties = true
			continue
		}
		if !inProperties {
			continue
		}
		if !strings.HasPrefix(line, "        ") || strings.HasPrefix(line, "         ") {
			// Deeper lines are a property's own body; shallower ones end the block.
			if !strings.HasPrefix(line, "        ") {
				inProperties = false
			}
			continue
		}
		name, _, ok := strings.Cut(strings.TrimPrefix(line, "        "), ":")
		name = strings.TrimSpace(name)
		require.True(t, ok && name != "", "unexpected property line in %s: %q", schema, line)
		properties = append(properties, name)
	}
	sort.Strings(properties)
	return properties
}

// contractEnumValues returns the `- value` entries of a schema's top-level enum.
func contractEnumValues(t *testing.T, schema string) []string {
	t.Helper()
	var values []string
	inEnum := false
	for _, line := range contractSchemaBlock(t, schema) {
		switch {
		case line == "      enum:":
			inEnum = true
		case inEnum && strings.HasPrefix(line, "        - "):
			values = append(values, strings.TrimPrefix(line, "        - "))
		case inEnum:
			inEnum = false
		}
	}
	sort.Strings(values)
	return values
}

// contractSchemaBlock returns the lines of one schema definition, excluding the
// key line itself. The block ends at the next four-space key.
func contractSchemaBlock(t *testing.T, schema string) []string {
	t.Helper()
	contents, err := os.ReadFile(contractOpenAPIPath)
	require.NoError(t, err)

	lines := strings.Split(string(contents), "\n")
	header := "    " + schema + ":"
	start := -1
	for i, line := range lines {
		if line == header {
			start = i + 1
			break
		}
	}
	require.NotEqual(t, -1, start, "schema %s is not declared in the contract", schema)

	var block []string
	for _, line := range lines[start:] {
		if line == "" || strings.HasPrefix(line, "      ") {
			block = append(block, line)
			continue
		}
		break
	}
	return block
}

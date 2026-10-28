package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agentpack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// packFixtureFiles is the review fixture pack used by the API tests: one graph
// flow, one flow-scoped role, one vendored skill, one external skill ref.
func packFixtureFiles() map[string]string {
	return map[string]string{
		"pack.yaml": `schema_version: 1
id: seqyuan/cell-qc-review
name: Cell QC Review
version: 1.3.0
license: Apache-2.0
model_requirements:
  defaults:
    capabilities: [tool_use]
flows:
  - id: qc-review
    path: flows/qc-review/graph.yaml
    format: graph
    roles: [qc-reviewer]
roles:
  - handle: qc-reviewer
    path: roles/qc-reviewer/role.md
    scope: flow
skills:
  - id: cell-qc-tables
    path: skills/cell-qc-tables/
`,
		"flows/qc-review/graph.yaml": `schema_version: 1
id: qc-review
name: QC Review
tasks:
  load:
    name: Load
    role: local/qc-reviewer
    goal: Load the samplesheet
graph:
  load: []
`,
		"roles/qc-reviewer/role.md": `---
schemaVersion: 1
handle: qc-reviewer
name: QC Reviewer
model:
  ref: anthropic/claude-sonnet-4
authority: read_only
---
Review the QC metrics.
`,
		"skills/cell-qc-tables/skill.json": `{"id":"cell-qc-tables","prompt":"SKILL.md"}`,
		"skills/cell-qc-tables/SKILL.md":   "# Cell QC Tables\n",
	}
}

func writePackFixture(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	for name, body := range files {
		target := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
		require.NoError(t, os.WriteFile(target, []byte(body), 0o644))
	}
	return root
}

func writePackArchive(t *testing.T, target string, files map[string]string) string {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressed)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	// Deterministic order keeps the fixture stable across runs.
	for left := 0; left < len(names); left++ {
		for right := left + 1; right < len(names); right++ {
			if names[right] < names[left] {
				names[left], names[right] = names[right], names[left]
			}
		}
	}
	for _, name := range names {
		body := []byte(files[name])
		require.NoError(t, writer.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}))
		_, err := writer.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	require.NoError(t, compressed.Close())
	require.NoError(t, os.WriteFile(target, buffer.Bytes(), 0o644))
	return target
}

func packServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	home := t.TempDir()
	// hostHomeDir canonicalizes the home, so use the resolved path for fixtures
	// to keep the confinement check identical on symlinked temp roots (macOS).
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	server := &Server{Token: "test-token", HostHome: home}
	return server, server.Handler()
}

func inspectRequest(t *testing.T, handler http.Handler, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return request(t, handler, http.MethodPost, "/v1/packs/inspect", body, true)
}

func TestInspectPackRequiresAuthAndPathSource(t *testing.T) {
	server, handler := packServer(t)

	unauthorized := request(t, handler, http.MethodPost, "/v1/packs/inspect",
		map[string]any{"source": map[string]any{"kind": "path", "path": server.HostHome}}, false)
	assert.Equal(t, http.StatusUnauthorized, unauthorized.Code)

	relative := inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": "packs/x.enpack"}})
	assert.Equal(t, http.StatusBadRequest, relative.Code)
	assert.Contains(t, relative.Body.String(), agentpack.CodePackPathInvalid)

	unsupported := inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "url", "path": "https://example.com/pack.enpack"}})
	assert.Equal(t, http.StatusBadRequest, unsupported.Code)
	assert.Contains(t, unsupported.Body.String(), agentpack.CodePackFormatUnsupported)

	missingKind := inspectRequest(t, handler, map[string]any{"source": map[string]any{"path": server.HostHome}})
	assert.Equal(t, http.StatusBadRequest, missingKind.Code)

	invalid := inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": server.HostHome}, "bogus": true})
	assert.Equal(t, http.StatusBadRequest, invalid.Code)
	assert.Contains(t, invalid.Body.String(), "invalid_json")
}

func TestInspectPackRejectsPathsOutsideHostHome(t *testing.T) {
	server, handler := packServer(t)
	outside := t.TempDir()
	writePackFixture(t, outside, packFixtureFiles())

	forbidden := inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": outside}})
	assert.Equal(t, http.StatusForbidden, forbidden.Code)
	assert.Contains(t, forbidden.Body.String(), agentpack.CodePackPathForbidden)

	escape := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.MkdirAll(escape, 0o755))
	require.NoError(t, os.Symlink(escape, filepath.Join(server.HostHome, "escape")))
	link := inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": filepath.Join(server.HostHome, "escape")}})
	assert.Equal(t, http.StatusForbidden, link.Code)
}

func TestInspectPackNoticesMissingAndUnsupportedTargets(t *testing.T) {
	server, handler := packServer(t)

	missing := inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": filepath.Join(server.HostHome, "nope.enpack")}})
	assert.Equal(t, http.StatusNotFound, missing.Code)
	assert.Contains(t, missing.Body.String(), agentpack.CodePackNotFound)

	zipFile := filepath.Join(server.HostHome, "pack.zip")
	require.NoError(t, os.WriteFile(zipFile, []byte("PK"), 0o644))
	unsupported := inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": zipFile}})
	assert.Equal(t, http.StatusBadRequest, unsupported.Code)
	assert.Contains(t, unsupported.Body.String(), agentpack.CodePackFormatUnsupported)

	notGzip := filepath.Join(server.HostHome, "pack.enpack")
	require.NoError(t, os.WriteFile(notGzip, []byte("not a gzip stream"), 0o644))
	container := inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": notGzip}})
	assert.Equal(t, http.StatusUnprocessableEntity, container.Code)
	assert.Contains(t, container.Body.String(), agentpack.CodePackUnsupportedContainer)

	emptyDir := filepath.Join(server.HostHome, "empty-pack")
	require.NoError(t, os.MkdirAll(emptyDir, 0o755))
	noManifest := inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": emptyDir}})
	assert.Equal(t, http.StatusUnprocessableEntity, noManifest.Code)
	assert.Contains(t, noManifest.Body.String(), agentpack.CodePackManifestMissing)
}

func TestInspectPackRejectsTraversalInsideArchive(t *testing.T) {
	server, handler := packServer(t)
	files := packFixtureFiles()
	files["../escape.yaml"] = "id: escape\n"
	archive := writePackArchive(t, filepath.Join(server.HostHome, "traversal.enpack"), files)

	response := inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": archive}})
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code)
	assert.Contains(t, response.Body.String(), agentpack.CodePackPathInvalid)
}

func TestInspectPackDirectory(t *testing.T) {
	server, handler := packServer(t)
	root := writePackFixture(t, filepath.Join(server.HostHome, "cell-qc"), packFixtureFiles())

	response := inspectRequest(t, handler, map[string]any{
		"source":           map[string]any{"kind": "path", "path": root},
		"includeDocuments": true,
	})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var result agentpack.Result
	decodeData(t, response, &result)
	assert.Equal(t, "seqyuan/cell-qc-review", result.Pack.ID)
	assert.Equal(t, agentpack.ContainerDirectory, result.Container.Kind)
	assert.False(t, result.Signed)
	assert.Equal(t, root, result.Source.Path)
	require.Len(t, result.Flows, 1)
	assert.Equal(t, agentpack.FlowFormatGraph, result.Flows[0].Format)
	require.Len(t, result.Documents, 3)
	assert.False(t, result.DocumentsTruncated)
}

func TestInspectPackArchiveWithExpectedDigest(t *testing.T) {
	server, handler := packServer(t)
	archive := writePackArchive(t, filepath.Join(server.HostHome, "cell-qc.enpack"), packFixtureFiles())

	first := inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": archive}})
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	var result agentpack.Result
	decodeData(t, first, &result)
	assert.Equal(t, agentpack.ContainerArchive, result.Container.Kind)
	require.NotEmpty(t, result.PackDigest)

	matched := inspectRequest(t, handler, map[string]any{
		"source":         map[string]any{"kind": "path", "path": archive},
		"expectedDigest": result.PackDigest,
	})
	assert.Equal(t, http.StatusOK, matched.Code, matched.Body.String())

	mismatched := inspectRequest(t, handler, map[string]any{
		"source":         map[string]any{"kind": "path", "path": archive},
		"expectedDigest": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
	})
	assert.Equal(t, http.StatusUnprocessableEntity, mismatched.Code)
	assert.Contains(t, mismatched.Body.String(), agentpack.CodePackDigestMismatch)
}

func TestInspectPackRoleOnlyAndFlowOnly(t *testing.T) {
	server, handler := packServer(t)

	roleOnly := map[string]string{
		"pack.yaml": `schema_version: 1
id: seqyuan/reviewer
name: Reviewer
version: 0.1.0
roles:
  - handle: reviewer
    path: roles/reviewer/role.md
    scope: global
`,
		"roles/reviewer/role.md": `---
schemaVersion: 1
handle: reviewer
name: Reviewer
model:
  ref: anthropic/claude-sonnet-4
authority: read_only
---
Review the incoming artifact.
`,
	}
	roleRoot := writePackFixture(t, filepath.Join(server.HostHome, "role-only"), roleOnly)
	response := inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": roleRoot}})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var roleResult agentpack.Result
	decodeData(t, response, &roleResult)
	assert.Empty(t, roleResult.Flows)
	require.Len(t, roleResult.Roles, 1)
	assert.Equal(t, agentpack.RoleScopeGlobal, roleResult.Roles[0].Scope)

	flowOnly := map[string]string{
		"pack.yaml": `schema_version: 1
id: seqyuan/qc-flow
name: QC Flow
version: 0.1.0
flows:
  - id: qc-review
    path: flows/qc-review/flow.yaml
    format: flow
`,
		"flows/qc-review/flow.yaml": `schemaVersion: 1
id: qc-review
budget:
  max_total_tokens: 1000
tasks:
  load:
    role: qc-reviewer
    goal: Load the samplesheet
`,
	}
	flowRoot := writePackFixture(t, filepath.Join(server.HostHome, "flow-only"), flowOnly)
	response = inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": flowRoot}})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var flowResult agentpack.Result
	decodeData(t, response, &flowResult)
	assert.Empty(t, flowResult.Roles)
	require.Len(t, flowResult.Flows, 1)
	assert.Equal(t, agentpack.FlowFormatFlow, flowResult.Flows[0].Format)
	assert.Equal(t, int64(1000), flowResult.Flows[0].MaxTotalTokens)
}

func TestInspectPackValidationFailures(t *testing.T) {
	server, handler := packServer(t)
	schema2 := packFixtureFiles()
	schema2["pack.yaml"] = "schema_version: 2\nid: seqyuan/cell-qc-review\nname: Cell QC\nversion: 1.0.0\nflows: []\nroles: []\n"
	root := writePackFixture(t, filepath.Join(server.HostHome, "schema-2"), schema2)
	response := inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": root}})
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code)
	assert.Contains(t, response.Body.String(), agentpack.CodePackSchemaUnsupported)

	duplicate := packFixtureFiles()
	duplicate["pack.yaml"] = `schema_version: 1
id: seqyuan/cell-qc-review
name: Cell QC
version: 1.0.0
flows:
  - id: qc-review
    path: flows/qc-review/graph.yaml
    format: graph
  - id: qc-review
    path: flows/qc-review/graph.yaml
    format: graph
`
	root = writePackFixture(t, filepath.Join(server.HostHome, "duplicate"), duplicate)
	response = inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": root}})
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code)
	assert.Contains(t, response.Body.String(), agentpack.CodePackDuplicateID)
}

// TestInspectPackDeclaresExternalMCPRefs covers the review surface for packs
// that declare external MCP profiles: mixed and Role-only packs inspect
// successfully, and the refs show up in both the result and the envelope.
func TestInspectPackDeclaresExternalMCPRefs(t *testing.T) {
	server, handler := packServer(t)

	mixed := packFixtureFiles()
	mixed["pack.yaml"] += `mcp:
  - ref: pubmed@1
  - ref: bio-tools@0.2.0
  - ref: pubmed@1
`
	mixedRoot := writePackFixture(t, filepath.Join(server.HostHome, "mixed-mcp"), mixed)
	response := inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": mixedRoot}})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var mixedResult agentpack.Result
	decodeData(t, response, &mixedResult)
	assert.Equal(t, []agentpack.MCPSummary{{Ref: "bio-tools@0.2.0"}, {Ref: "pubmed@1"}}, mixedResult.MCP)
	assert.Equal(t, []string{"bio-tools@0.2.0", "pubmed@1"}, mixedResult.CapabilityEnvelope.RequiresMCP)
	assert.Contains(t, response.Body.String(), agentpack.WarningExternalMCPRef)

	roleOnly := map[string]string{
		"pack.yaml": `schema_version: 1
id: seqyuan/reviewer
name: Reviewer
version: 0.1.0
roles:
  - handle: reviewer
    path: roles/reviewer/role.md
    scope: global
mcp:
  - ref: pubmed@1
`,
		"roles/reviewer/role.md": `---
schemaVersion: 1
handle: reviewer
name: Reviewer
model:
  ref: anthropic/claude-sonnet-4
authority: read_only
---
Review the incoming artifact.
`,
	}
	roleRoot := writePackFixture(t, filepath.Join(server.HostHome, "role-only-mcp"), roleOnly)
	response = inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": roleRoot}})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var roleResult agentpack.Result
	decodeData(t, response, &roleResult)
	assert.Equal(t, []agentpack.MCPSummary{{Ref: "pubmed@1"}}, roleResult.MCP)
	assert.Equal(t, []string{"pubmed@1"}, roleResult.CapabilityEnvelope.RequiresMCP)

	// Inline MCP declarations are still rejected, not silently dropped.
	inline := packFixtureFiles()
	inline["pack.yaml"] += "mcp:\n  - ref: pubmed@1\n    command: npx\n"
	inlineRoot := writePackFixture(t, filepath.Join(server.HostHome, "inline-mcp"), inline)
	response = inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": inlineRoot}})
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code)
	assert.Contains(t, response.Body.String(), agentpack.CodePackManifestUnknownField)
}

// TestInspectPackWorksWithoutStores pins the S1 constraint that inspect has no
// store dependency: the server in packServer has every store field nil.
func TestInspectPackWorksWithoutStores(t *testing.T) {
	server, handler := packServer(t)
	require.Nil(t, server.DB)
	require.Nil(t, server.SessionStores)
	root := writePackFixture(t, filepath.Join(server.HostHome, "storeless"), packFixtureFiles())
	response := inspectRequest(t, handler, map[string]any{"source": map[string]any{"kind": "path", "path": root}})
	assert.Equal(t, http.StatusOK, response.Code, response.Body.String())
}

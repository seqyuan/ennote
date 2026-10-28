package agentpack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// entry is one fixture pack file. A pack is described as an ordered list of
// files so directory and archive containers can be built from the same source.
type entry struct {
	path string
	body string
}

func writeDirectoryPack(t *testing.T, files []entry) string {
	t.Helper()
	root := t.TempDir()
	for _, file := range files {
		target := filepath.Join(root, filepath.FromSlash(file.path))
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
		require.NoError(t, os.WriteFile(target, []byte(file.body), 0o644))
	}
	return root
}

type archiveEntry struct {
	name     string
	body     string
	typeflag byte
	linkname string
}

func filesToArchive(files []entry) []archiveEntry {
	entries := make([]archiveEntry, 0, len(files))
	for _, file := range files {
		entries = append(entries, archiveEntry{name: file.path, body: file.body, typeflag: tar.TypeReg})
	}
	return entries
}

func writeArchiveFile(t *testing.T, entries []archiveEntry) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "pack.enpack")
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressed)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{
			Name: entry.name, Mode: 0o644, Size: int64(len(entry.body)),
			Typeflag: typeflag, Linkname: entry.linkname,
		}
		if typeflag == tar.TypeDir {
			header.Size = 0
		}
		require.NoError(t, writer.WriteHeader(header))
		if header.Size > 0 {
			_, err := writer.Write([]byte(entry.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, writer.Close())
	require.NoError(t, compressed.Close())
	require.NoError(t, os.WriteFile(target, buffer.Bytes(), 0o644))
	return target
}

const mixedManifest = `schema_version: 1
id: seqyuan/cell-qc-review
name: Cell QC Review
version: 1.3.0
description: Multi-sample scRNA QC.
license: Apache-2.0
requires_ennote: ">=0.5.0"
model_requirements:
  defaults:
    capabilities: [tool_use]
    min_context_window: 128000
  roles:
    qc-reviewer:
      capabilities: [tool_use, thinking]
inputs:
  samplesheet:
    type: path
    required: true
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
  - ref: bio-workspace@0.1.0
requires: []
docs:
  readme: README.md
`

const graphFlow = `schema_version: 1
id: qc-review
name: QC Review
tasks:
  load:
    name: Load
    role: local/qc-reviewer
    goal: Load the samplesheet
  review:
    name: Review
    role: local/qc-reviewer
    goal: Review QC metrics
    writes: [results/qc.csv]
graph:
  load: []
  review: [load]
`

const reviewerRole = `---
schemaVersion: 1
handle: qc-reviewer
name: QC Reviewer
model:
  ref: anthropic/claude-sonnet-4
authority: read_only
permissionCeiling: discuss
allowedTools: [read, grep]
skills:
  - id: cell-qc-tables
    mode: preload
---
Review the QC metrics table by table.
`

const skillManifestJSON = `{"id":"cell-qc-tables","prompt":"SKILL.md"}`

func mixedPackFiles() []entry {
	return []entry{
		{path: "pack.yaml", body: mixedManifest},
		{path: "README.md", body: "# Cell QC Review\n"},
		{path: "flows/qc-review/graph.yaml", body: graphFlow},
		{path: "roles/qc-reviewer/role.md", body: reviewerRole},
		{path: "skills/cell-qc-tables/skill.json", body: skillManifestJSON},
		{path: "skills/cell-qc-tables/SKILL.md", body: "# Cell QC Tables\n"},
	}
}

func mustInspect(t *testing.T, request Request) *Result {
	t.Helper()
	result, err := Inspect(request)
	require.NoError(t, err)
	require.NotNil(t, result)
	return result
}

func inspectFiles(t *testing.T, files []entry) *Result {
	t.Helper()
	return mustInspect(t, Request{Source: Source{Kind: SourceKindPath, Path: writeDirectoryPack(t, files)}})
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, code, Code(err), "unexpected error: %v", err)
}

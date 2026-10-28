package agentpack

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInspectDirectoryPack(t *testing.T) {
	result := inspectFiles(t, mixedPackFiles())

	assert.Equal(t, InspectVersion, result.InspectVersion)
	assert.Equal(t, SourceKindPath, result.Source.Kind)
	assert.Equal(t, ContainerDirectory, result.Container.Kind)
	assert.Equal(t, 1, result.SchemaVersion)
	assert.Equal(t, "seqyuan/cell-qc-review", result.Pack.ID)
	assert.Equal(t, "Cell QC Review", result.Pack.Name)
	assert.Equal(t, "1.3.0", result.Pack.Version)
	assert.Equal(t, "Apache-2.0", result.Pack.License)
	assert.Equal(t, ">=0.5.0", result.Pack.RequiresEnnote)
	assert.Equal(t, mixedManifest, result.Manifest)
	assert.True(t, strings.HasPrefix(result.ManifestDigest, "sha256:"))
	assert.Len(t, result.ManifestDigest, len("sha256:")+64)
	assert.True(t, strings.HasPrefix(result.PackDigest, "sha256:"))
	assert.False(t, result.Signed)
	assert.False(t, result.Signature.Present)
	assert.Nil(t, result.Signature.Algorithm)
	assert.False(t, result.Signature.Verified)

	require.Len(t, result.Flows, 1)
	flow := result.Flows[0]
	assert.Equal(t, "qc-review", flow.ID)
	assert.Equal(t, "flows/qc-review/graph.yaml", flow.Path)
	assert.Equal(t, FlowFormatGraph, flow.Format)
	assert.Equal(t, 2, flow.TaskCount)
	assert.Equal(t, "load", flow.EntryTask)
	assert.Equal(t, []string{"qc-reviewer"}, flow.Roles)
	assert.Zero(t, flow.CheckTasks)
	assert.Zero(t, flow.FanOutTasks)
	assert.Zero(t, flow.MaxTotalTokens)
	assert.True(t, strings.HasPrefix(flow.PathDigest, "sha256:"))
	assert.True(t, strings.HasPrefix(flow.SourceDigest, "sha256:"))

	require.Len(t, result.Roles, 1)
	role := result.Roles[0]
	assert.Equal(t, "qc-reviewer", role.Handle)
	assert.Equal(t, "roles/qc-reviewer/role.md", role.Path)
	assert.Equal(t, RoleScopeFlow, role.Scope)
	assert.Equal(t, "QC Reviewer", role.Name)
	assert.Equal(t, "read_only", role.Authority)
	assert.Equal(t, "discuss", role.PermissionCeiling)
	assert.Equal(t, []string{"grep", "read"}, role.AllowedTools)
	assert.Equal(t, []string{"cell-qc-tables"}, role.Skills)
	assert.True(t, strings.HasPrefix(role.SourceDigest, "sha256:"))

	require.Len(t, result.Skills, 2)
	assert.Equal(t, SkillKindVendored, result.Skills[0].Kind)
	assert.Equal(t, "cell-qc-tables", result.Skills[0].ID)
	assert.Equal(t, "skills/cell-qc-tables", result.Skills[0].Path)
	assert.Equal(t, int64(len("# Cell QC Tables\n")), result.Skills[0].PromptBytes)
	assert.Equal(t, SkillKindExternal, result.Skills[1].Kind)
	assert.Equal(t, "bio-workspace@0.1.0", result.Skills[1].Ref)

	assert.Empty(t, result.Requires)
	assert.Equal(t, len(mixedPackFiles()), result.FilesTotal)
	assert.False(t, result.FilesTruncated)
	assert.Equal(t, "README.md", result.Files[0].Path)
	for _, file := range result.Files {
		assert.True(t, strings.HasPrefix(file.Digest, "sha256:"), file.Path)
	}

	envelope := result.CapabilityEnvelope
	assert.Equal(t, 1, envelope.FlowCount)
	assert.Equal(t, 1, envelope.RoleCount)
	assert.Equal(t, 2, envelope.SkillCount)
	assert.Equal(t, 2, envelope.TaskCount)
	assert.Equal(t, []string{"qc-reviewer"}, envelope.Roles)
	assert.Equal(t, []string{"bio-workspace@0.1.0", "cell-qc-tables"}, envelope.Skills)
	assert.Equal(t, []string{"tool_use"}, envelope.ModelRequirements.Capabilities)
	assert.Equal(t, int64(128000), envelope.ModelRequirements.MinContextWindow)
	assert.Equal(t, []string{"thinking", "tool_use"}, envelope.ModelRequirements.PerRole["qc-reviewer"])
	assert.Equal(t, []string{"grep", "read"}, envelope.Tools)
	assert.Equal(t, []string{"results/qc.csv"}, envelope.Writes)
	assert.Empty(t, envelope.CheckCommands)
	assert.Zero(t, envelope.FlowMaxTotalTokens)

	codes := warningCodes(result.Warnings)
	assert.Contains(t, codes, WarningUnsignedPack)
	assert.Contains(t, codes, WarningExternalSkillRef)
	assert.NotContains(t, codes, WarningRoleOnlyPack)
	assert.Empty(t, result.Documents)
	assert.False(t, result.DocumentsTruncated)
}

func TestInspectArchiveMatchesDirectoryDigests(t *testing.T) {
	files := mixedPackFiles()
	directoryResult := inspectFiles(t, files)

	archivePath := writeArchiveFile(t, filesToArchive(files))
	archiveResult := mustInspect(t, Request{Source: Source{Path: archivePath}})

	assert.Equal(t, ContainerArchive, archiveResult.Container.Kind)
	info, err := os.Stat(archivePath)
	require.NoError(t, err)
	assert.Equal(t, info.Size(), archiveResult.Container.Bytes)
	assert.Equal(t, directoryResult.ManifestDigest, archiveResult.ManifestDigest)
	assert.Equal(t, directoryResult.PackDigest, archiveResult.PackDigest)
	assert.Equal(t, directoryResult.FilesTotal, archiveResult.FilesTotal)
}

func TestInspectToleratesArchiveDotPrefix(t *testing.T) {
	entries := make([]archiveEntry, 0)
	for _, file := range mixedPackFiles() {
		entries = append(entries, archiveEntry{name: "./" + file.path, body: file.body})
	}
	entries = append(entries, archiveEntry{name: "./", typeflag: 0x35})

	archivePath := writeArchiveFile(t, entries)
	result := mustInspect(t, Request{Source: Source{Path: archivePath}})
	assert.Equal(t, inspectDirectoryPackDigest(t, mixedPackFiles()), result.PackDigest)
}

func inspectDirectoryPackDigest(t *testing.T, files []entry) string {
	t.Helper()
	return inspectFiles(t, files).PackDigest
}

func TestInspectIncludeDocuments(t *testing.T) {
	path := writeDirectoryPack(t, mixedPackFiles())

	without := mustInspect(t, Request{Source: Source{Path: path}})
	assert.Empty(t, without.Documents)

	with := mustInspect(t, Request{Source: Source{Path: path}, IncludeDocuments: true})
	require.Len(t, with.Documents, 3)
	assert.Equal(t, DocumentFlow, with.Documents[0].Kind)
	assert.Equal(t, graphFlow, with.Documents[0].Content)
	assert.Equal(t, DocumentRole, with.Documents[1].Kind)
	assert.Equal(t, reviewerRole, with.Documents[1].Content)
	assert.Equal(t, DocumentSkill, with.Documents[2].Kind)
	assert.Equal(t, "# Cell QC Tables\n", with.Documents[2].Content)
	for _, document := range with.Documents {
		assert.False(t, document.Truncated)
		assert.NotZero(t, document.Bytes)
	}
	assert.False(t, with.DocumentsTruncated)
}

func TestInspectTruncatesOversizedDocuments(t *testing.T) {
	files := mixedPackFiles()
	huge := strings.Repeat("x", MaxDocumentBytes+16)
	files[2] = entry{path: "flows/qc-review/graph.yaml", body: graphFlow + "\n# " + huge + "\n"}
	path := writeDirectoryPack(t, files)

	result := mustInspect(t, Request{Source: Source{Path: path}, IncludeDocuments: true})
	require.Len(t, result.Documents, 3)
	assert.True(t, result.Documents[0].Truncated)
	assert.True(t, result.DocumentsTruncated)
	assert.Len(t, result.Documents[0].Content, MaxDocumentBytes)
}

func TestInspectArchiveWithoutManifest(t *testing.T) {
	archivePath := writeArchiveFile(t, filesToArchive([]entry{{path: "README.md", body: "hi"}}))
	_, err := Inspect(Request{Source: Source{Path: archivePath}})
	requireCode(t, err, CodePackManifestMissing)
}

func TestInspectArchiveRejectsNonGzipAndTruncatedGzip(t *testing.T) {
	dir := t.TempDir()
	notGzip := filepath.Join(dir, "not-gzip.enpack")
	require.NoError(t, os.WriteFile(notGzip, []byte("PK\x03\x04 this is a zip"), 0o644))
	_, err := Inspect(Request{Source: Source{Path: notGzip}})
	requireCode(t, err, CodePackUnsupportedContainer)

	archive := writeArchiveFile(t, filesToArchive(mixedPackFiles()))
	raw, err := os.ReadFile(archive)
	require.NoError(t, err)
	truncated := filepath.Join(dir, "truncated.enpack")
	require.NoError(t, os.WriteFile(truncated, raw[:len(raw)/2], 0o644))
	_, err = Inspect(Request{Source: Source{Path: truncated}})
	requireCode(t, err, CodePackUnsupportedContainer)
}

func TestInspectRejectsUnsupportedContainers(t *testing.T) {
	dir := t.TempDir()
	zipFile := filepath.Join(dir, "pack.zip")
	require.NoError(t, os.WriteFile(zipFile, []byte("PK"), 0o644))
	_, err := Inspect(Request{Source: Source{Path: zipFile}})
	requireCode(t, err, CodePackFormatUnsupported)

	missing := filepath.Join(dir, "missing.enpack")
	_, err = Inspect(Request{Source: Source{Path: missing}})
	requireCode(t, err, CodePackNotFound)

	noManifest := t.TempDir()
	_, err = Inspect(Request{Source: Source{Path: noManifest}})
	requireCode(t, err, CodePackManifestMissing)

	_, err = Inspect(Request{Source: Source{Path: ""}})
	requireCode(t, err, CodePackPathInvalid)

	_, err = Inspect(Request{Source: Source{Kind: "url", Path: noManifest}})
	requireCode(t, err, CodePackFormatUnsupported)
}

func TestInspectRejectsSymlinks(t *testing.T) {
	files := mixedPackFiles()
	root := writeDirectoryPack(t, files)
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(root, "leak.txt")))
	_, err := Inspect(Request{Source: Source{Path: root}})
	requireCode(t, err, CodePackSymlinkRejected)

	// The pack path itself must not be a symlink either.
	link := filepath.Join(t.TempDir(), "pack.enpack")
	require.NoError(t, os.Symlink(writeArchiveFile(t, filesToArchive(files)), link))
	_, err = Inspect(Request{Source: Source{Path: link}})
	requireCode(t, err, CodePackSymlinkRejected)
}

func TestInspectRejectsArchiveLinksAndDevices(t *testing.T) {
	files := mixedPackFiles()
	for name, entry := range map[string]archiveEntry{
		"symlink":  {name: "leak", typeflag: 0x32, linkname: "/etc/passwd"},
		"hardlink": {name: "leak", typeflag: 0x31, linkname: "pack.yaml"},
		"fifo":     {name: "pipe", typeflag: 0x36},
	} {
		t.Run(name, func(t *testing.T) {
			entries := append(filesToArchive(files), entry)
			_, err := Inspect(Request{Source: Source{Path: writeArchiveFile(t, entries)}})
			assert.Contains(t, []string{CodePackSymlinkRejected, CodePackPathKindUnsupported, CodePackUnsupportedContainer}, Code(err), err)
		})
	}
}

func TestInspectRejectsDuplicateArchiveEntries(t *testing.T) {
	entries := append(filesToArchive(mixedPackFiles()), archiveEntry{name: "README.md", body: "again"})
	_, err := Inspect(Request{Source: Source{Path: writeArchiveFile(t, entries)}})
	requireCode(t, err, CodePackPathInvalid)
}

func TestInspectRejectsUnsafePaths(t *testing.T) {
	cases := map[string]string{
		"traversal":      "../outside.yaml",
		"nested":         "flows/../../outside.yaml",
		"absolute":       "/etc/passwd",
		"backslash":      `flows\qc.yaml`,
		"too-deep":       "a/b/c/d/e/f/g/h/i.yaml",
		"dot-segment":    "./.",
		"empty-segments": "flows//qc.yaml",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			files := mixedPackFiles()
			files[0] = entry{path: "pack.yaml", body: strings.Replace(mixedManifest, "path: flows/qc-review/graph.yaml", "path: "+quoteYAML(path), 1)}
			_, err := Inspect(Request{Source: Source{Path: writeDirectoryPack(t, files)}})
			requireCode(t, err, CodePackPathInvalid)
		})
	}
}

func quoteYAML(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

func TestInspectPathKindAndPresenceChecks(t *testing.T) {
	files := mixedPackFiles()
	files[0] = entry{path: "pack.yaml", body: strings.Replace(mixedManifest, "path: flows/qc-review/graph.yaml", "path: flows/qc-review/missing.yaml", 1)}
	_, err := Inspect(Request{Source: Source{Path: writeDirectoryPack(t, files)}})
	requireCode(t, err, CodePackPathMissing)

	files = mixedPackFiles()
	files[0] = entry{path: "pack.yaml", body: strings.Replace(mixedManifest, "path: flows/qc-review/graph.yaml", "path: roles/qc-reviewer", 1)}
	_, err = Inspect(Request{Source: Source{Path: writeDirectoryPack(t, files)}})
	requireCode(t, err, CodePackPathKindUnsupported)

	files = mixedPackFiles()
	files[0] = entry{path: "pack.yaml", body: strings.Replace(mixedManifest, "path: skills/cell-qc-tables/", "path: skills/cell-qc-tables", 1)}
	// A vendored skill path may omit the trailing slash.
	result := mustInspect(t, Request{Source: Source{Path: writeDirectoryPack(t, files)}})
	assert.Equal(t, "skills/cell-qc-tables", result.Skills[0].Path)

	files = mixedPackFiles()
	files[0] = entry{path: "pack.yaml", body: strings.Replace(mixedManifest, "path: skills/cell-qc-tables/", "path: skills/cell-qc-tables/SKILL.md", 1)}
	_, err = Inspect(Request{Source: Source{Path: writeDirectoryPack(t, files)}})
	requireCode(t, err, CodePackPathKindUnsupported)
}

func TestInspectLimits(t *testing.T) {
	t.Run("manifest", func(t *testing.T) {
		files := mixedPackFiles()
		files[0] = entry{path: "pack.yaml", body: mixedManifest + "\n# " + strings.Repeat("x", maxManifestBytes) + "\n"}
		_, err := Inspect(Request{Source: Source{Path: writeDirectoryPack(t, files)}})
		requireCode(t, err, CodePackTooLarge)
	})

	t.Run("file", func(t *testing.T) {
		files := append(mixedPackFiles(), entry{path: "big.bin", body: strings.Repeat("x", MaxFileBytes+1)})
		_, err := Inspect(Request{Source: Source{Path: writeDirectoryPack(t, files)}})
		requireCode(t, err, CodePackTooLarge)
	})

	t.Run("files", func(t *testing.T) {
		files := mixedPackFiles()
		for index := 0; index < MaxFiles; index++ {
			files = append(files, entry{path: "extra/" + strconv.Itoa(index) + ".txt", body: "x"})
		}
		_, err := Inspect(Request{Source: Source{Path: writeDirectoryPack(t, files)}})
		requireCode(t, err, CodePackTooManyFiles)
	})

	t.Run("payload", func(t *testing.T) {
		files := mixedPackFiles()
		for index := 0; index < 5; index++ {
			files = append(files, entry{path: "big/" + strconv.Itoa(index) + ".bin", body: strings.Repeat("x", MaxFileBytes)})
		}
		_, err := Inspect(Request{Source: Source{Path: writeDirectoryPack(t, files)}})
		requireCode(t, err, CodePackTooLarge)
	})

	t.Run("archive-payload", func(t *testing.T) {
		entries := filesToArchive(mixedPackFiles())
		for index := 0; index < 5; index++ {
			entries = append(entries, archiveEntry{
				name: "big/" + strconv.Itoa(index) + ".bin", body: strings.Repeat("x", MaxFileBytes),
			})
		}
		_, err := Inspect(Request{Source: Source{Path: writeArchiveFile(t, entries)}})
		requireCode(t, err, CodePackTooLarge)
	})
}

func warningCodes(warnings []Warning) []string {
	codes := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		codes = append(codes, warning.Code)
	}
	return codes
}

// A bare Agent Flow task role is the flow-local spelling. It must count as a
// reference to a declared flow-scoped role even when flows[].roles is omitted.
func TestInspectBareRoleRefCountsAsFlowReference(t *testing.T) {
	files := mixedPackFiles()
	files[0] = entry{path: "pack.yaml", body: `schema_version: 1
id: seqyuan/bare-role
name: Bare Role
version: 1.0.0
flows:
  - id: qc-review
    path: flows/qc-review/flow.yaml
    format: flow
roles:
  - handle: qc-reviewer
    path: roles/qc-reviewer/role.md
    scope: flow
`}
	files[2] = entry{path: "flows/qc-review/flow.yaml", body: `schemaVersion: 1
id: qc-review
budget: {max_total_tokens: 1000}
tasks:
  review:
    role: qc-reviewer
    goal: Review the sample
`}
	result := inspectFiles(t, files)
	require.Len(t, result.Flows, 1)
	assert.Equal(t, FlowFormatFlow, result.Flows[0].Format)
	require.Len(t, result.Roles, 1)
	assert.Equal(t, RoleScopeFlow, result.Roles[0].Scope)
	assert.NotContains(t, warningCodes(result.Warnings), WarningFlowRefsGlobalRole)
}

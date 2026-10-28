package agentpack

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPackDigestChangesWithPayloadOnly(t *testing.T) {
	base := inspectFiles(t, mixedPackFiles())

	files := mixedPackFiles()
	files[1] = entry{path: "README.md", body: "# Cell QC Review (edited)\n"}
	edited := inspectFiles(t, files)
	assert.Equal(t, base.ManifestDigest, edited.ManifestDigest, "README bytes must not change the manifest digest")
	assert.NotEqual(t, base.PackDigest, edited.PackDigest, "README bytes must change the pack digest")

	files = mixedPackFiles()
	files[0] = entry{path: "pack.yaml", body: strings.Replace(mixedManifest, "license: Apache-2.0", "license: Apache-2.0\n# reviewed by hand", 1)}
	commented := inspectFiles(t, files)
	assert.NotEqual(t, base.ManifestDigest, commented.ManifestDigest, "manifest comments must change the manifest digest")
	assert.NotEqual(t, base.PackDigest, commented.PackDigest)
}

func TestPackDigestExcludesSignatureFile(t *testing.T) {
	base := inspectFiles(t, mixedPackFiles())
	files := append(mixedPackFiles(), entry{path: SignatureName, body: "detached signature bytes"})
	withSignature := inspectFiles(t, files)
	assert.Equal(t, base.PackDigest, withSignature.PackDigest, "pack.sig must not change the payload digest")
	assert.Equal(t, base.ManifestDigest, withSignature.ManifestDigest)
}

func TestInspectExpectedDigest(t *testing.T) {
	path := writeDirectoryPack(t, mixedPackFiles())
	expected := inspectFiles(t, mixedPackFiles())

	accepted := []string{
		expected.PackDigest,
		expected.ManifestDigest,
		strings.TrimPrefix(expected.PackDigest, "sha256:"),
		strings.ToUpper(expected.ManifestDigest),
	}
	for _, digest := range accepted {
		result, err := Inspect(Request{Source: Source{Path: path}, ExpectedDigest: digest})
		require.NoError(t, err, digest)
		assert.Equal(t, expected.PackDigest, result.PackDigest)
	}

	_, err := Inspect(Request{Source: Source{Path: path}, ExpectedDigest: "sha256:" + strings.Repeat("0", 64)})
	requireCode(t, err, CodePackDigestMismatch)
}

func TestPackDigestIsOrderIndependent(t *testing.T) {
	forward := inspectFiles(t, mixedPackFiles())
	reversed := mixedPackFiles()
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	reordered := inspectFiles(t, reversed)
	assert.Equal(t, forward.PackDigest, reordered.PackDigest)
	assert.Equal(t, forward.ManifestDigest, reordered.ManifestDigest)
}

func TestNormalizePackPath(t *testing.T) {
	accepted := map[string]string{
		"pack.yaml":       "pack.yaml",
		"./pack.yaml":     "pack.yaml",
		"skills/a/":       "skills/a",
		"a/b/c.txt":       "a/b/c.txt",
		"a/b/c/d/e/f/g/h": "a/b/c/d/e/f/g/h",
	}
	for input, expected := range accepted {
		got, err := normalizePackPath(input)
		require.NoError(t, err, input)
		assert.Equal(t, expected, got, input)
	}

	rejected := []string{
		"", " ", "/abs/path", "../escape", "a/../../b", `a\b`, "a\x00b",
		"a//b", "./.", "a/b/c/d/e/f/g/h/i",
	}
	for _, input := range rejected {
		_, err := normalizePackPath(input)
		require.Error(t, err, input)
		assert.Equal(t, CodePackPathInvalid, Code(err), input)
	}

	_, err := normalizePackPath(strings.Repeat("a", MaxPathBytes+1))
	requireCode(t, err, CodePackPathInvalid)
}

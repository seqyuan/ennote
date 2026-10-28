package agentpack

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Digest tags domain-separate the pack payload digest and the vendored skill
// directory digest from every other sha256 use in the Worker.
const (
	payloadDigestTag   = "agentpack-payload-v1"
	directoryDigestTag = "agentpack-directory-v1"
)

// fileDigest hashes the exact bytes of one payload file.
func fileDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// digestLines computes tag \0 line... over sorted "path \0 digest \n" lines.
func digestLines(tag string, lines []string) string {
	sorted := append([]string(nil), lines...)
	sort.Strings(sorted)
	hash := sha256.New()
	hash.Write([]byte(tag))
	hash.Write([]byte{0})
	hash.Write([]byte(strings.Join(sorted, "")))
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// ManifestDigest hashes the raw pack.yaml bytes. No YAML/JSON canonicalization
// happens on purpose: editing a comment or reordering keys changes the digest,
// because the reviewer approved those exact bytes ("what you reviewed is what
// you signed").
func ManifestDigest(pkg *Package) (string, error) {
	manifest, ok := pkg.Lookup(ManifestName)
	if !ok {
		return "", newError(CodePackManifestMissing, "pack root is missing pack.yaml")
	}
	return fileDigest(manifest.Data), nil
}

// PackDigest hashes every payload file (including pack.yaml, documentation, and
// vendored skill files) keyed by its sorted relative POSIX path.
//
// The top-level pack.sig is excluded so a detached signature can be added or
// rotated without changing the payload identity. Directory packs and .enpack
// archives that hold the same file set produce the same digest: tar headers,
// mtimes, uid/gid, and gzip level never contribute.
func PackDigest(pkg *Package) string {
	lines := make([]string, 0, len(pkg.Files))
	for _, file := range pkg.Files {
		if file.Path == SignatureName {
			continue
		}
		lines = append(lines, file.Path+"\x00"+fileDigest(file.Data)+"\n")
	}
	return digestLines(payloadDigestTag, lines)
}

// directoryDigest identifies a vendored directory by its content, relative to
// the directory itself, so moving the skill inside the pack does not change the
// skill identity.
func directoryDigest(prefix string, files []File) string {
	lines := make([]string, 0, len(files))
	for _, file := range files {
		relative := strings.TrimPrefix(file.Path, prefix+"/")
		lines = append(lines, relative+"\x00"+fileDigest(file.Data)+"\n")
	}
	return digestLines(directoryDigestTag, lines)
}

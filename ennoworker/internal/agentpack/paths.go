package agentpack

import (
	"sort"
	"strings"
)

const (
	// MaxPathBytes bounds one relative pack path.
	MaxPathBytes = 255
	// MaxPathDepth bounds the number of path segments below the pack root.
	MaxPathDepth = 8
)

// normalizePackPath validates a manifest-declared or archive-declared path and
// returns its canonical relative POSIX form (no leading "./", no trailing
// slash).
//
// A pack path must be relative, must not escape the pack root, must not use
// Windows separators, must not contain NUL, must be at most MaxPathBytes and
// at most MaxPathDepth segments. No pack path is ever resolved against the host
// filesystem: the same rules apply to directory packs and to archive entries so
// both containers accept exactly the same payload.
func normalizePackPath(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", newError(CodePackPathInvalid, "path must not be blank")
	}
	if strings.ContainsRune(raw, 0) {
		return "", pathError(CodePackPathInvalid, raw, "path must not contain NUL")
	}
	if strings.Contains(raw, `\`) {
		return "", pathError(CodePackPathInvalid, raw, "path must use forward slashes")
	}
	if strings.HasPrefix(raw, "/") {
		return "", pathError(CodePackPathInvalid, raw, "path must be relative to the pack root")
	}
	trimmed := strings.TrimPrefix(raw, "./")
	trimmed = strings.TrimSuffix(trimmed, "/")
	if trimmed == "" || trimmed == "." {
		return "", pathError(CodePackPathInvalid, raw, "path must name a file or directory below the pack root")
	}
	segments := strings.Split(trimmed, "/")
	for _, segment := range segments {
		switch segment {
		case "":
			return "", pathError(CodePackPathInvalid, raw, "path must not contain empty segments")
		case ".", "..":
			return "", pathError(CodePackPathInvalid, raw, "path must not contain %q segments", segment)
		}
	}
	if len(segments) > MaxPathDepth {
		return "", pathError(CodePackPathInvalid, raw,
			"path depth %d exceeds the limit of %d", len(segments), MaxPathDepth)
	}
	if len(trimmed) > MaxPathBytes {
		return "", pathError(CodePackPathInvalid, raw,
			"path is %d bytes (limit %d)", len(trimmed), MaxPathBytes)
	}
	return trimmed, nil
}

func sortedStrings(values []string) []string {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	return sorted
}

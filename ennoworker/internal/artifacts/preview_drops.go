package artifacts

import (
	"bytes"
	"regexp"
	"strings"
)

const (
	// previewDropScanBytes bounds how much of a document is scanned. The file is
	// already in memory by classification time, so this saves regular-expression
	// work rather than I/O — but a 50 MiB single-line export would still mean a
	// megabyte-scale regex sweep to produce a one-line notice, and 256 KiB covers
	// the structure, chart markup and inline handlers of anything a person reads.
	previewDropScanBytes = 256 << 10
	// maxPreviewDrops keeps the notice to one readable banner and one line of a
	// tool result. Truncation happens after sorting, so the same document always
	// reports the same subset.
	maxPreviewDrops = 6
	// previewDropsMetadataKey is the artifact metadata field this hint is stored
	// in. GET /artifacts/{id} does not expose metadata, so the key lives only in
	// SQLite and is re-read by GetForSession.
	previewDropsMetadataKey = "previewDrops"
	// previewDropsTotalKey is how many kinds were found before the list was capped.
	previewDropsTotalKey = "previewDropsTotal"
)

// previewDropTags are the elements the static preview cannot keep. None of them
// is in sanitizeStaticHTML's allowlist and the preview's CSP is
// default-src 'none', so today they vanish without a trace.
var previewDropTags = []string{
	"audio", "button", "canvas", "embed", "form", "iframe", "input",
	"math", "object", "script", "select", "style", "svg", "textarea", "video",
}

// previewDropOrder is how the notice ranks what it found, which is not the same
// question as what it looks for. The cap is small, so alphabetical order would let
// a document with eight kinds of removed content report "audio, button, canvas,
// embed, form, iframe" and stay silent about the svg it is actually missing — the
// one thing the reader's eye goes to first. Ranked here, truncated from the end, and
// a test keeps this list and previewDropTags covering the same set.
var previewDropOrder = []string{
	"style", "svg", "canvas", "script", "on*", "video", "audio", "iframe",
	"object", "embed", "form", "input", "button", "select", "textarea", "math",
}

var (
	// The scanner is a heuristic on purpose, and it is not the sanitizer's
	// mirror: it does not parse HTML, so it cannot disagree with bluemonday about
	// a document's tree, and it stays correct if the allowlist is ever tightened
	// for a reason unrelated to this hint.
	//
	// Case-insensitive, because the sanitizer is: `<SVG>` is removed just like
	// `<svg>`, and "Save as web page" exports from office suites emit upper-case
	// tags. Matching only lower case would have kept exactly those reports silent.
	//
	// It still under-reports, and the misses are known: `< svg` (no name directly
	// followed by `<`), and anything past the scanned window. The name must end at
	// a space, `/` or `>` so `<form-group>` is not mistaken for `<form>`; the window
	// is trimmed back to the last `<`, so a token cut by the boundary is not
	// guessed at (a `<svg` that begins exactly at the cut could be `<svg-chart>`).
	previewDropTagPattern = regexp.MustCompile(`(?i)<(` + strings.Join(previewDropTags, "|") + `)([\s/>])`)
	// An inline event handler: whitespace, then `on` plus letters, then `=`.
	// It over-reports prose that looks like an assignment to a word beginning with
	// "on" (`<td> only = 0</td>`), and it reports tags inside comments, attribute
	// values and script strings, since nothing here parses. Those cost the reader a
	// sentence of doubt; a wrong "we kept everything" would cost them the chart.
	previewDropAttrPattern = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
)

// scanPreviewDrops returns the ranked, de-duplicated, bounded list of content
// kinds the static preview will strip from the first 64 KiB of data. An empty
// slice means "nothing worth warning about", not "nothing was removed".
func scanPreviewDrops(data []byte) (drops []string, total int) {
	if len(data) > previewDropScanBytes {
		// Trim back to the last `<` so no token is decided by where the window
		// happened to end. A few bytes of content are lost, no tag is invented.
		cut := bytes.LastIndexByte(data[:previewDropScanBytes], '<')
		if cut < 0 {
			cut = previewDropScanBytes
		}
		data = data[:cut]
	}
	window := string(data)
	found := make(map[string]struct{})
	for _, match := range previewDropTagPattern.FindAllStringSubmatch(window, -1) {
		// `(?i)` makes the match case-insensitive but leaves the capture as written,
		// so the rank lookup would miss `<SVG>`: the names are compared lower-cased.
		found[strings.ToLower(match[1])] = struct{}{}
	}
	if previewDropAttrPattern.MatchString(window) {
		found["on*"] = struct{}{}
	}
	total = len(found)
	drops = make([]string, 0, total)
	for _, name := range previewDropOrder {
		if _, ok := found[name]; ok {
			drops = append(drops, name)
		}
	}
	if len(drops) > maxPreviewDrops {
		drops = drops[:maxPreviewDrops]
	}
	return drops, total
}

// previewDropsFromMetadata reads the hint back out of a classification result so
// a freshly published artifact carries it without a second trip to SQLite.
func previewDropsFromMetadata(metadata map[string]any) ([]string, int) {
	drops := stringSlice(metadata[previewDropsMetadataKey])
	if len(drops) == 0 {
		return nil, 0
	}
	// The count is stored beside the list because the list is capped: without it the
	// notice would state a total that is only the part it could fit.
	total := len(drops)
	if stored, ok := metadata[previewDropsTotalKey].(float64); ok && int(stored) > total {
		total = int(stored)
	}
	return drops, total
}

// stringSlice accepts both shapes a JSON round trip can produce, because losing the
// hint silently is the exact failure this whole file exists to prevent.
func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			if name, ok := entry.(string); ok {
				out = append(out, name)
			}
		}
		return out
	default:
		return nil
	}
}

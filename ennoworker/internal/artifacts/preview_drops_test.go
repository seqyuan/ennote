package artifacts

import (
	"context"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The whole point of the hint: a report whose chart is an SVG is previewed as a
// report with no chart, and until now nothing anywhere said so.
// The banner element only. The class name also lives in the document's style
// block, so it cannot be used to prove the banner is absent.
const previewNoticeMarkup = `<p class="ennote-preview-notice"`

func TestScanPreviewDropsFindsWhatThePreviewDeletes(t *testing.T) {
	drops, _ := scanPreviewDrops([]byte(`<div><svg viewBox="0 0 1 1"></svg>` +
		`<p onclick="go()">text</p><script>void 0</script></div>`))
	assert.Equal(t, []string{"svg", "script", "on*"}, drops)
}

// The two lists answer different questions — what to look for, and in what order to
// say it — and a tag that appears in only one of them is silently invisible. This is
// the only thing keeping them in step.
func TestPreviewDropListsCoverTheSameSet(t *testing.T) {
	order := make(map[string]struct{}, len(previewDropOrder))
	for _, name := range previewDropOrder {
		order[name] = struct{}{}
	}
	for _, tag := range previewDropTags {
		_, ok := order[tag]
		assert.True(t, ok, "%q is scanned for but has no rank, so it can never be reported", tag)
	}
	assert.Len(t, previewDropOrder, len(previewDropTags)+1, "the extra entry is on*")
}

func TestScanPreviewDropsStaysQuietOnADocumentItCanKeep(t *testing.T) {
	drops, _ := scanPreviewDrops([]byte(`<table><caption>genes</caption><tr><th>gene</th></tr>` +
		`<tr><td>A</td></tr></table><p style="color:#111">body</p>`))
	assert.Empty(t, drops)
}

// A custom element is not the tag its name starts with, or every `<svg-icon>` in a
// design system would claim its chart was removed. Upper case, by contrast, is a
// real removal: the sanitizer is case-insensitive and office-suite exports are
// upper-case, so a lower-case-only scanner would have left those reports silent.
func TestScanPreviewDropsKnowsACustomElementFromATag(t *testing.T) {
	drops, _ := scanPreviewDrops([]byte(`<form-group><svg-chart></svg-chart></form-group>`))
	assert.Empty(t, drops)

	upper, _ := scanPreviewDrops([]byte(`<SVG viewBox="0 0 1 1"></SVG><P onClick="x()">upper</P>`))
	assert.Equal(t, []string{"svg", "on*"}, upper)
}

// The notice states a total, and the list is capped: without the count it would
// claim that six kinds is everything when ten were found.
func TestScanPreviewDropsReportsTheTotalBeyondTheCap(t *testing.T) {
	body := "<audio></audio><button></button><canvas></canvas><embed><form></form><iframe></iframe>" +
		"<input><math></math><object></object><script></script><select></select><svg></svg>" +
		"<textarea></textarea><video></video>"
	drops, total := scanPreviewDrops([]byte(body))
	assert.Len(t, drops, maxPreviewDrops)
	assert.Greater(t, total, maxPreviewDrops, "the count is what lets the notice say \"and N more\"")
}

// The scan is bounded by design: a tag past the window is not reported, and the
// cut itself must not damage the preview of the document it belongs to.
func TestScanPreviewDropsStopsAtTheScanWindow(t *testing.T) {
	beyond := "<p>" + strings.Repeat("a", previewDropScanBytes) + "</p><svg></svg>"
	beyondDrops, _ := scanPreviewDrops([]byte(beyond))
	assert.Empty(t, beyondDrops)

	// A tag that begins exactly at the cut is not guessed at: the bytes needed to
	// tell `<svg` from `<svg-chart>` are past the window, so it is reported as
	// nothing rather than as a false positive.
	atEdge := strings.Repeat("a", previewDropScanBytes-len("<svg")) + "<svg"
	atEdgeDrops, _ := scanPreviewDrops([]byte(atEdge))
	assert.Empty(t, atEdgeDrops)

	service := setupArtifactService(t)
	artifact, err := service.Store(context.Background(),
		PublishInput{ProjectID: "p", SessionID: "s", Name: "long.html"}, strings.NewReader(beyond))
	require.NoError(t, err)
	assert.Empty(t, artifact.PreviewDrops)
	_, document, err := service.PreviewHTML(context.Background(), artifact.ID, "s")
	require.NoError(t, err)
	assert.Contains(t, document, strings.Repeat("a", 1024), "the document still renders")
	assert.NotContains(t, document, previewNoticeMarkup)
}

func TestScanPreviewDropsIsRankedAndBounded(t *testing.T) {
	body := "<audio></audio><button></button><canvas></canvas><embed><form></form><iframe></iframe>" +
		"<input><math></math><object></object><script></script><select></select><svg></svg>" +
		"<textarea></textarea><video></video>"
	drops, total := scanPreviewDrops([]byte(body))
	// Ranked, then cut to the ceiling: the same bytes always report the same subset,
	// which is what makes the notice quotable in a test. Every tag here is dropped,
	// so the six that survive are the six the reader is most likely looking for —
	// svg and script first, never "audio, button, canvas" while the chart is missing.
	assert.Equal(t, []string{"svg", "canvas", "script", "video", "audio", "iframe"}, drops)
	assert.Equal(t, 14, total)
	again, againTotal := scanPreviewDrops([]byte(body))
	assert.Equal(t, drops, again)
	assert.Equal(t, total, againTotal)
}

// The hint has to survive the row it is written to, or the preview route (which
// only reads it back) would never see it.
func TestStaticHTMLPreviewDropsSurviveTheStoreRoundTrip(t *testing.T) {
	service := setupArtifactService(t)
	ctx := context.Background()
	artifact, err := service.Store(ctx, PublishInput{ProjectID: "p", SessionID: "s", Name: "report.html"},
		strings.NewReader(`<svg></svg><script>void 0</script><p onclick="x()">Report</p>`))
	require.NoError(t, err)
	// Set on the value handed back by Store, so publish_artifact can warn without
	// a second query.
	// Ranked, not alphabetical: svg leads because it is the one the reader's
	// eye goes to first, and the cap must never drop it for an earlier name.
	assert.Equal(t, []string{"svg", "script", "on*"}, artifact.PreviewDrops)

	stored, err := service.GetForSession(ctx, artifact.ID, "s")
	require.NoError(t, err)
	assert.Equal(t, []string{"svg", "script", "on*"}, stored.PreviewDrops)

	_, document, err := service.PreviewHTML(ctx, artifact.ID, "s")
	require.NoError(t, err)
	assert.Contains(t, document, `class="ennote-preview-notice" role="note"`)
	assert.Contains(t, document, "This preview drops 3 kinds of content: svg, script, on*.")
	assert.Contains(t, document, "The published file is unchanged.")
	// The reader is not the one who can republish: the widget advice belongs to the
	// tool result, and the notice only states what this preview does.
	assert.NotContains(t, document, "kind \"widget\"")
	assert.Contains(t, document, "Report", "sanitized content still follows the notice")
	assert.NotContains(t, document, "<svg")
}

func TestStaticHTMLPreviewWithoutDropsCarriesNoNotice(t *testing.T) {
	service := setupArtifactService(t)
	ctx := context.Background()
	artifact, err := service.Store(ctx, PublishInput{ProjectID: "p", SessionID: "s", Name: "clean.html"},
		strings.NewReader(`<table><tr><th>gene</th></tr><tr><td>A</td></tr></table>`))
	require.NoError(t, err)
	assert.Empty(t, artifact.PreviewDrops)
	_, document, err := service.PreviewHTML(ctx, artifact.ID, "s")
	require.NoError(t, err)
	assert.NotContains(t, document, previewNoticeMarkup)
	assert.NotContains(t, document, "This preview drops")
}

// singular/plural, the capped total, and the one place a drop name meets a
// document: it is escaped there, even though the names come from a fixed list today.
func TestPreviewDropNoticeWordingAndEscaping(t *testing.T) {
	assert.Contains(t, previewDropNotice([]string{"svg"}, 1), "This preview drops 1 kind of content: svg.")
	assert.Contains(t, previewDropNotice([]string{"canvas", "svg"}, 2), "2 kinds of content: canvas, svg.")
	// The list can be capped short of the truth, and the notice must not state the
	// cap as the total.
	assert.Contains(t, previewDropNotice([]string{"svg", "canvas"}, 9), "9 kinds of content: svg, canvas (and 7 more).")
	assert.Contains(t, previewDropNotice([]string{"a<b"}, 1), "a&lt;b")
	assert.Empty(t, previewDropNotice(nil, 0))
}

// A widget keeps its SVG because it is served verbatim, so it must never carry a
// notice about content being removed.
func TestWidgetArtifactRecordsNoPreviewDrops(t *testing.T) {
	service, _ := widgetService(t)
	artifact, err := publishWidget(t, service, "canvas.html",
		`<svg viewBox="0 0 1 1"></svg><script>void 0</script>`)
	require.NoError(t, err)
	assert.Equal(t, domain.ArtifactKindWidget, artifact.Kind)
	assert.Empty(t, artifact.PreviewDrops)
	_, document, err := service.PreviewWidget(context.Background(), artifact.ID, "s")
	require.NoError(t, err)
	assert.NotContains(t, document, previewNoticeMarkup)
}

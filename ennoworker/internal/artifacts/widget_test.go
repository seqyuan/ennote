package artifacts

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func widgetService(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.MigrateFixtureSchema(db))
	now := "2026-11-11T00:00:00Z"
	_, err = db.Exec(`INSERT INTO sessions(id,project_id,created_at,updated_at) VALUES('s','p',?,?)`, now, now)
	require.NoError(t, err)
	return &Service{DB: db, Root: t.TempDir()}, db
}

func publishWidget(t *testing.T, service *Service, name, body string) (*domain.Artifact, error) {
	t.Helper()
	return service.Store(context.Background(), PublishInput{
		ProjectID: "p", SessionID: "s", Name: name, Kind: domain.ArtifactKindWidget,
	}, strings.NewReader(body))
}

// An .html file that nobody asked to make a widget stays static_html. If
// classification ever started inferring `widget`, every existing published page
// would begin executing scripts without a single line of diff saying so.
func TestStoreNeverInfersWidgetFromHTML(t *testing.T) {
	service, _ := widgetService(t)
	artifact, err := service.Store(context.Background(), PublishInput{ProjectID: "p", SessionID: "s", Name: "report.html"},
		strings.NewReader("<!doctype html><p>report</p><script>alert(1)</script>"))
	require.NoError(t, err)
	assert.Equal(t, domain.ArtifactKindStaticHTML, artifact.Kind)
}

func TestStoreAcceptsAnExplicitWidget(t *testing.T) {
	service, db := widgetService(t)
	body := "<!doctype html><svg viewBox=\"0 0 10 10\"><circle cx=\"5\" cy=\"5\" r=\"4\"/></svg>"
	artifact, err := publishWidget(t, service, "canvas.html", body)
	require.NoError(t, err)
	assert.Equal(t, domain.ArtifactKindWidget, artifact.Kind)
	assert.Equal(t, "text/html; charset=utf-8", artifact.MIMEType)
	assert.Equal(t, int64(len(body)), artifact.SizeBytes)

	var metadata string
	require.NoError(t, db.QueryRow(`SELECT metadata_json FROM artifacts WHERE id=?`, artifact.ID).Scan(&metadata))
	assert.Contains(t, metadata, `"widgetPolicyVersion":1`)

	// The stored bytes are the published bytes: the envelope is added when the
	// document is served, never baked into the artifact.
	_, data, err := service.ReadForSession(context.Background(), artifact.ID, "s")
	require.NoError(t, err)
	assert.Equal(t, body, string(data))
}

func TestStoreRejectsMalformedWidgets(t *testing.T) {
	service, db := widgetService(t)
	cases := []struct {
		name string
		file string
		body string
		want string
	}{
		{"wrong extension", "canvas.txt", "<p>x</p>", "must be an .html file"},
		{"no tag", "canvas.html", "  stray text", "must start with a tag"},
		{"not utf8", "canvas.html", "<p>\xff\xfe</p>", "must be UTF-8 text"},
		{"nul byte", "canvas.html", "<p>\x00</p>", "must be UTF-8 text"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := publishWidget(t, service, testCase.file, testCase.body)
			require.Error(t, err)
			assert.Contains(t, err.Error(), testCase.want)
			var count int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM artifacts`).Scan(&count))
			assert.Zero(t, count, "a rejected widget leaves no row behind")
		})
	}
}

// A BOM or a vertical tab before the doctype is still a tag; only genuinely textless
// documents should be refused.
func TestStoreAcceptsWhitespaceAndBOMBeforeTheTag(t *testing.T) {
	service, _ := widgetService(t)
	for _, body := range []string{"\xef\xbb\xbf<!doctype html><p>x</p>", "\n\t\v\f<!doctype html><p>x</p>"} {
		artifact, err := publishWidget(t, service, "canvas.html", body)
		require.NoError(t, err, "%q", body)
		assert.Equal(t, domain.ArtifactKindWidget, artifact.Kind)
	}
}

// The ceiling exists for the client that has to render the result through the
// remote transport's bounded JSON envelope, not for the disk (that ceiling is 400x
// higher). Both sides of the boundary are pinned: one byte over is refused, exact
// size is accepted.
func TestStoreEnforcesTheWidgetCeiling(t *testing.T) {
	service, db := widgetService(t)
	atLimit := "<html>" + strings.Repeat("a", int(defaultMaxWidgetBytes)-len("<html>"))
	require.Equal(t, int(defaultMaxWidgetBytes), len(atLimit))
	artifact, err := publishWidget(t, service, "canvas.html", atLimit)
	require.NoError(t, err)
	assert.Equal(t, int64(len(atLimit)), artifact.SizeBytes)

	_, err = publishWidget(t, service, "oversize.html", atLimit+"!")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrArtifactTooLarge)
	assert.Contains(t, err.Error(), "131072")

	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM artifacts`).Scan(&count))
	assert.Equal(t, 1, count, "the refused publish added no row")

	pending, readErr := os.ReadDir(filepath.Join(service.Root, ".pending"))
	require.NoError(t, readErr)
	assert.Empty(t, pending, "the refused publish left no temporary file")
}

// The envelope is the isolation policy. These strings are the policy, so they are
// asserted literally: a well-meaning "simplification" of the CSP is a security
// regression that no other test would notice.
func TestWidgetDocumentCarriesTheEnvelopeAndTheModelBytes(t *testing.T) {
	body := "<div id=\"chart\"><script>draw()</script></div>"
	document := WidgetDocument(body)

	assert.Contains(t, document, "<meta http-equiv=\"Content-Security-Policy\"")
	assert.Contains(t, document, "connect-src 'none'")
	assert.Contains(t, document, "img-src data: blob:")
	assert.Contains(t, document, "script-src 'unsafe-inline' https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com https://esm.sh")
	assert.NotContains(t, document, "img-src *")
	// sandbox and frame-ancestors are ignored in a meta element; the header carries
	// them, and the srcdoc path relies on the client's frame attribute.
	assert.NotContains(t, document, "frame-ancestors")
	assert.Contains(t, document, body, "model markup is inserted verbatim")
	assert.Contains(t, document, "id=\"__ennote_widget\"")
	assert.Contains(t, document, "ennote:widget:height")
	assert.Contains(t, document, "ennote:widget:error")
	assert.Contains(t, document, "ennote:widget:theme")
	// Nothing a widget can call: the reference implementation exposed sendMessage,
	// cross-widget publish and a parent-side fetch proxy, and every client would have
	// had to reimplement them.
	assert.Contains(t, document, "event.source!==parent", "the theme channel must check its sender")
	// One field, one authority: the first-report transition is the client's business,
	// so the protocol must not also carry a flag that means the same thing.
	assert.Contains(t, document, "send('ennote:widget:height',{height:h});")
	// Content that escapes the wrapper (a stray closing tag in published markup) still
	// has to be measured, or the reader gets a 40px slit and no error.
	assert.Contains(t, document, "document.body.scrollHeight")
	// Margin collapse would make every frame shorter than its content.
	assert.Contains(t, document, "display:flow-root")
	assert.NotContains(t, document, "__widgetSendMessage")
	assert.NotContains(t, document, "__widgetPublish")
	assert.NotContains(t, document, "hatch-buddy")
}

func TestWidgetContentSecurityPolicyPinsSandbox(t *testing.T) {
	policy := WidgetContentSecurityPolicy
	assert.Contains(t, policy, "sandbox allow-scripts")
	// allow-same-origin next to allow-scripts lets the frame reach parent.document
	// and remove its own sandbox, so it must never appear here or in the attribute.
	assert.NotContains(t, policy, "allow-same-origin")
	assert.Contains(t, policy, "frame-ancestors 'self'")
	assert.Contains(t, policy, "default-src 'none'")
	assert.NotContains(t, policy, "'unsafe-eval'")
}

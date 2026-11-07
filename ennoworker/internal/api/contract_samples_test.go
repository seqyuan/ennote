package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/stretchr/testify/require"
)

// Contract samples: the JSON the Worker answers for a route, stored per
// (operation, status) and checked against the contract's declared response type by
// `tsc` (scripts/generate-contract-samples.mjs writes the assertions).
//
// Why this exists: `generate:api` proves the generated types match the YAML, and
// the router parity test proves the mux serves the declared operations. Neither
// proves a handler's JSON matches the type it promised, so a renamed or dropped
// field is invisible until a client reads `undefined` from it.
//
// What is checked: required properties, property types, and any property the
// schema does not declare — `openapi-typescript` emits no index signature, so the
// fresh-literal excess-property check applies wherever the object is closed with
// additionalProperties: false.
//
// Deliberately not covered:
//
//   - format, pattern, minimum and cross-field constraints. TypeScript has no
//     notion of them, and re-implementing a validator is a new liability.
//   - status codes, response headers and Content-Type as a property of the
//     handler. The sample is named after the status that was observed; nothing
//     proves the handler could not have answered another one.
//   - any operation without a sample. The list below is an explicit act: adding an
//     endpoint is a decision, and the sample is what makes its shape reviewable.
//     A change to a sampled handler shows up as a diff in this test.
//
// Some sample fields come from the fixture rather than from the handler under
// test, and a diff in them is noise: `name: "api"` (seeded project), `version`
// ("dev" without build info), `sandbox: "none"` and `degraded: true` (the fixture
// runs without isolation). Everything else is produced by the route.
const contractSampleUpdateEnv = "ENNOTE_UPDATE_CONTRACT_SAMPLES"

const contractSampleDirectory = "testdata/contract-samples"

// contractSampleRequest names the route by its contract spelling and lets record
// perform the request, so a sample cannot be attributed to an operation the
// request never reached: the route, the parameter values and the operation name
// come from one place.
type contractSampleRequest struct {
	method string
	// route is the contract's path template, e.g. "/v1/sessions/{sessionID}/messages".
	route  string
	values map[string]string
	body   any
	header map[string]string
	auth   bool
	status int
}

type contractSamples struct {
	t       *testing.T
	handler http.Handler
	// operations is the contract's route → method → operationId, read by the same
	// parser the parity test uses.
	operations map[string]map[string]string
	seen       map[string]struct{}
	volatile   normalizer
}

// record performs the request, checks the status the operation declares, and
// compares the body against the stored sample — or rewrites the sample when the
// update variable is set. It returns the recorder so the caller can read ids out
// of a response it just created.
func (s *contractSamples) record(request contractSampleRequest) *httptest.ResponseRecorder {
	s.t.Helper()

	operation, ok := s.operations[normalizeContractPath(request.route)][strings.ToLower(request.method)]
	require.True(s.t, ok, "contracts/openapi.yaml declares no %s %s", request.method, request.route)
	require.NotEmpty(s.t, operation, "%s %s declares no operationId", request.method, request.route)

	path := request.route
	for _, parameter := range contractPathParameter.FindAllString(path, -1) {
		value, provided := request.values[strings.Trim(parameter, "{}")]
		require.True(s.t, provided, "%s %s: no value for %s", request.method, request.route, parameter)
		path = strings.ReplaceAll(path, parameter, value)
	}
	require.NotContains(s.t, path, "{", "%s %s: unsubstituted parameter", request.method, request.route)

	var reader *bytes.Reader
	if request.body != nil {
		encoded, err := json.Marshal(request.body)
		require.NoError(s.t, err)
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	httpRequest := httptest.NewRequest(request.method, path, reader)
	if request.body != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	if request.auth {
		httpRequest.Header.Set("Authorization", "Bearer test-token")
	}
	for name, value := range request.header {
		httpRequest.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	s.handler.ServeHTTP(recorder, httpRequest)

	require.Equal(s.t, request.status, recorder.Code,
		"%s %s answered %d: %s", request.method, path, recorder.Code, recorder.Body.String())

	var decoded any
	require.NoError(s.t, json.Unmarshal(recorder.Body.Bytes(), &decoded))

	// Envelope.data is optional in the contract, so tsc cannot catch a handler that
	// stops sending it — and a sample without it would still satisfy the declared
	// type. Every success answer is required to carry one.
	if request.status >= 200 && request.status < 300 {
		envelope, isObject := decoded.(map[string]any)
		require.True(s.t, isObject, "%s %s: success body is not an object", request.method, path)
		require.Contains(s.t, envelope, "data", "%s %s: success body carries no data", request.method, path)
	}

	s.compare(operation, request.status, recorder.Body.Bytes())
	return recorder
}

func (s *contractSamples) compare(operation string, status int, body []byte) {
	s.t.Helper()
	name := fmt.Sprintf("%s-%d.json", operation, status)
	path := filepath.Join(contractSampleDirectory, name)

	// A duplicate would silently replace the first sample and hide an assertion.
	require.NotContains(s.t, s.seen, name, "%s was recorded twice", name)

	var decoded any
	require.NoError(s.t, json.Unmarshal(body, &decoded))
	normalized := s.volatile.normalize(decoded)

	// No HTML escaping: a sample is read by people, and "\u003chost-path\u003e" is
	// noise in a diff.
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	require.NoError(s.t, encoder.Encode(normalized))
	pretty := encoded.Bytes()

	if os.Getenv(contractSampleUpdateEnv) == "1" {
		// Rewriting tracked files must not be possible from a job that is supposed
		// to be checking them; the same reasoning as ENNOTE_REQUIRE_BWRAP in CI.
		require.Empty(s.t, os.Getenv("CI"), "refusing to rewrite contract samples in CI")
		require.NoError(s.t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(s.t, os.WriteFile(path, pretty, 0o644))
	} else {
		expected, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			s.t.Fatalf("%s is missing; regenerate with %s=1 go test ./internal/api/ -run TestContractSamples",
				path, contractSampleUpdateEnv)
		}
		require.NoError(s.t, err)
		if !bytes.Equal(expected, pretty) {
			s.t.Fatalf("%s changed.\n%s\nIf that is intended, regenerate with %s=1 and update the contract if the shape moved.",
				path, describeDifference(expected, pretty), contractSampleUpdateEnv)
		}
	}
	s.seen[name] = struct{}{}
}

// assertNoOrphans fails on a stored sample this run did not produce, so a removed
// operation cannot leave a stale assertion behind that still type-checks against
// the contract.
func (s *contractSamples) assertNoOrphans() {
	s.t.Helper()
	entries, err := os.ReadDir(contractSampleDirectory)
	if errors.Is(err, os.ErrNotExist) {
		// Every record above would already have failed on its missing file.
		require.NotEmpty(s.t, s.seen, "no samples were recorded")
		return
	}
	require.NoError(s.t, err)
	var orphans []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if _, ok := s.seen[entry.Name()]; !ok {
			orphans = append(orphans, entry.Name())
		}
	}
	sort.Strings(orphans)
	require.Empty(s.t, orphans,
		"these samples were not produced by this run; delete them or restore the operation they belong to")
}

// describeDifference names the first JSON path whose value differs, so the CI log
// says why a sample moved instead of printing two files side by side.
func describeDifference(expected, actual []byte) string {
	var want, got any
	if err := json.Unmarshal(expected, &want); err != nil {
		return "the stored sample is not JSON anymore"
	}
	if err := json.Unmarshal(actual, &got); err != nil {
		return "the new answer is not JSON"
	}
	if difference := firstDifference("", want, got); difference != "" {
		return difference
	}
	// The trees agree, so only formatting moved.
	return "the values are identical; only the file formatting differs"
}

func firstDifference(path string, want, got any) string {
	switch wanted := want.(type) {
	case map[string]any:
		supplied, ok := got.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s: stored a JSON object, answered %T", pathOrRoot(path), got)
		}
		for _, key := range sortedKeys(wanted) {
			if _, present := supplied[key]; !present {
				return fmt.Sprintf("%s: the stored sample has it, the answer does not", joinPath(path, key))
			}
		}
		for _, key := range sortedKeys(supplied) {
			if _, present := wanted[key]; !present {
				return fmt.Sprintf("%s: the answer has it, the stored sample does not", joinPath(path, key))
			}
		}
		for _, key := range sortedKeys(wanted) {
			if difference := firstDifference(joinPath(path, key), wanted[key], supplied[key]); difference != "" {
				return difference
			}
		}
		return ""
	case []any:
		supplied, ok := got.([]any)
		if !ok {
			return fmt.Sprintf("%s: stored a JSON array, answered %T", pathOrRoot(path), got)
		}
		if len(wanted) != len(supplied) {
			return fmt.Sprintf("%s: stored %d element(s), answered %d", pathOrRoot(path), len(wanted), len(supplied))
		}
		for index := range wanted {
			if difference := firstDifference(joinPath(path, strconv.Itoa(index)), wanted[index], supplied[index]); difference != "" {
				return difference
			}
		}
		return ""
	default:
		if want != got {
			return fmt.Sprintf("%s: stored %v, answered %v", pathOrRoot(path), want, got)
		}
		return ""
	}
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func joinPath(path, key string) string {
	if path == "" {
		return "." + key
	}
	return path + "." + key
}

func pathOrRoot(path string) string {
	if path == "" {
		return "."
	}
	return path
}

var (
	volatileUUID      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	volatileTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)
	volatileHex       = regexp.MustCompile(`^[0-9a-f]{16,64}$`)
)

// normalizer replaces volatile values — identifiers, timestamps, digests and the
// fixture's own paths — with stable stand-ins, otherwise every run would diff.
//
// Each distinct value gets its own numbered stand-in, and equal values keep
// sharing one: that is what preserves the structure the sample exists to show. A
// handler that fills rootRunId from sessionId is a diff here, where a single
// catch-all placeholder would have hidden it. The rules are narrow — a string that
// is not a UUID, an RFC 3339 timestamp, a long lowercase hex digest or a path under
// the temp root is left alone — so an enum value still shows up if it changes.
type normalizer struct {
	uuids      map[string]string
	timestamps map[string]string
	digests    map[string]string
	// roots are the fixture's own directories, registered by the test that created
	// them: a path under os.TempDir() carries a random directory name, so it can
	// only be replaced by prefix, not by pattern.
	roots map[string]string
}

func (n *normalizer) normalize(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		// Map order is random; sort so the numbering is reproducible.
		for _, key := range sortedKeys(typed) {
			typed[key] = n.normalize(typed[key])
		}
		return typed
	case []any:
		for index, nested := range typed {
			typed[index] = n.normalize(nested)
		}
		return typed
	case string:
		return n.normalizeString(typed)
	default:
		return value
	}
}

// contractSampleEpoch anchors normalised timestamps so relative order survives.
var contractSampleEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func (n *normalizer) normalizeString(value string) string {
	switch {
	case volatileUUID.MatchString(value):
		return remember(n.uuids, value, func(index int) string {
			return fmt.Sprintf("00000000-0000-0000-0000-%012d", index)
		})
	case volatileTimestamp.MatchString(value):
		return remember(n.timestamps, value, func(index int) string {
			return contractSampleEpoch.Add(time.Duration(index-1) * time.Second).Format(time.RFC3339)
		})
	case volatileHex.MatchString(value):
		return remember(n.digests, value, func(index int) string {
			return strings.Repeat("0", len(value)-4) + fmt.Sprintf("%04d", index)
		})
	}
	for root, standIn := range n.roots {
		if strings.HasPrefix(value, root+string(os.PathSeparator)) {
			return standIn + strings.TrimPrefix(value, root)
		}
	}
	if strings.HasPrefix(value, os.TempDir()+string(os.PathSeparator)) {
		return "<temp-path>"
	}
	return value
}

// remember hands one stable stand-in to each distinct value and back to every
// repeat of it, which is what keeps equality visible in the sample.
func remember(existing map[string]string, value string, standIn func(index int) string) string {
	if replaced, ok := existing[value]; ok {
		return replaced
	}
	replacement := standIn(len(existing) + 1)
	existing[value] = replacement
	return replacement
}

// TestContractSamples is the Go half of the response-shape guard. It drives a
// representative set of routes through the real mux, so a sample is the shape a
// client parses, not a second rendering of the domain structs. It is re-encoded
// before it is stored — keys sorted, HTML not escaped — so the file is readable and
// the comparison is about structure, not byte order.
func TestContractSamples(t *testing.T) {
	server, handler := setupServer(t, &fakeController{})
	// The identity the runtime endpoint reports; empty is not a legal instance id,
	// and a sample has to be a legal answer.
	server.InstanceID = "contract-sample"
	// The workspace path is the fixture's, and its random directory name would
	// diff on every run.
	hostPath := t.TempDir()

	// The Authoring surface (Roles, Graphs) reads the file-native source store, so
	// the fixture needs one before those routes can answer at all.
	server.GlobalSources = &globalsource.Store{HomeDir: t.TempDir()}

	samples := &contractSamples{
		t:          t,
		handler:    handler,
		operations: contractOperations(t),
		seen:       map[string]struct{}{},
		volatile: normalizer{
			uuids:      map[string]string{},
			timestamps: map[string]string{},
			digests:    map[string]string{},
			roots:      map[string]string{hostPath: "<host-path>"},
		},
	}

	samples.record(contractSampleRequest{method: http.MethodGet, route: "/v1/health/live", status: http.StatusOK})
	samples.record(contractSampleRequest{method: http.MethodGet, route: "/v1/health/ready", status: http.StatusOK})
	samples.record(contractSampleRequest{method: http.MethodGet, route: "/v1/runtime", auth: true, status: http.StatusOK})
	// The rejection is its own shape: the error envelope is what every client
	// branch that fails has to parse.
	samples.record(contractSampleRequest{method: http.MethodGet, route: "/v1/runtime", status: http.StatusUnauthorized})

	samples.record(contractSampleRequest{method: http.MethodGet, route: "/v1/projects", auth: true, status: http.StatusOK})

	createdProject := samples.record(contractSampleRequest{
		method: http.MethodPost, route: "/v1/projects", auth: true, status: http.StatusCreated,
		body: map[string]any{"name": "contract", "description": "samples", "hostPath": hostPath},
	})
	var project struct {
		Project domain.Project `json:"project"`
	}
	decodeData(t, createdProject, &project)
	require.NotEmpty(t, project.Project.ID)
	projectRoute := map[string]string{"projectID": project.Project.ID}

	samples.record(contractSampleRequest{
		method: http.MethodGet, route: "/v1/projects/{projectID}/sessions", auth: true,
		values: projectRoute, status: http.StatusOK,
	})

	// Authoring surfaces: a Portable Role document and the Graph draft it can be
	// referenced from. Both are the largest hand-shaped JSON the Worker answers
	// outside chat, and neither had a sample.
	roleDocument := map[string]any{
		"schemaVersion": 1, "handle": "reviewer", "name": "Reviewer",
		"description": "reviews", "positioning": "review", "icon": "user", "color": "blue",
		"model": map[string]any{
			"ref": "anthropic/claude-sonnet-4", "thinkingEffort": "default", "fallbacks": []string{},
		},
		"skills":            []any{},
		"authority":         "read_only",
		"permissionCeiling": "ask",
		"allowedTools":      []string{},
		"context": map[string]any{
			"defaultMode": "room", "allowedModes": []string{"room"}, "ownExecutionContinuity": "none",
		},
		"delegation": map[string]any{
			"admission": "deny", "allowedCallerKinds": []string{"host"},
			"allowedStrategies": []string{"single"}, "maxInvocationsPerParentRun": 1,
			"maxConcurrentInstances": 1,
			"budgetCeiling": map[string]any{
				"maxModelCalls": 4, "maxToolCalls": 16, "maxTotalTokens": 20000,
				"maxOutputTokens": 4000, "maxCostUsdMicros": 0, "maxWallTimeMs": 120000,
			},
		},
		"outputContract": "text-v1", "maxLoopIterations": 8,
		"prompt": "You review a diff and report what is wrong with it.",
	}
	samples.record(contractSampleRequest{
		method: http.MethodPost, route: "/v1/global-roles", auth: true,
		body: map[string]any{"document": roleDocument}, status: http.StatusCreated,
	})
	samples.record(contractSampleRequest{
		method: http.MethodGet, route: "/v1/global-roles", auth: true, status: http.StatusOK,
	})
	samples.record(contractSampleRequest{
		method: http.MethodGet, route: "/v1/global-roles/{roleID}", auth: true,
		values: map[string]string{"roleID": "reviewer"}, status: http.StatusOK,
	})
	samples.record(contractSampleRequest{
		method: http.MethodGet, route: "/v1/graphs", auth: true, status: http.StatusOK,
	})
	createdSession := samples.record(contractSampleRequest{
		method: http.MethodPost, route: "/v1/projects/{projectID}/sessions", auth: true,
		values: projectRoute, body: map[string]any{"title": "contract"}, status: http.StatusCreated,
	})
	var session domain.Session
	decodeData(t, createdSession, &session)
	require.NotEmpty(t, session.ID)
	sessionRoute := map[string]string{"sessionID": session.ID}

	// The turn comes before the message page: a session with no turn has no
	// messages, and an empty array validates every element type by not having any.
	turnResponse := samples.record(contractSampleRequest{
		method: http.MethodPost, route: "/v1/sessions/{sessionID}/turns", auth: true,
		values: sessionRoute, body: map[string]any{"text": "hello"},
		header: map[string]string{"Idempotency-Key": "contract-sample-turn"},
		status: http.StatusAccepted,
	})
	var submission domain.TurnSubmission
	decodeData(t, turnResponse, &submission)
	require.NotEmpty(t, submission.Run.ID)

	samples.record(contractSampleRequest{
		method: http.MethodGet, route: "/v1/sessions/{sessionID}/messages", auth: true,
		values: sessionRoute, status: http.StatusOK,
	})
	samples.record(contractSampleRequest{
		method: http.MethodGet, route: "/v1/sessions/{sessionID}/compactions", auth: true,
		values: sessionRoute, status: http.StatusOK,
	})

	runRoute := map[string]string{"runID": submission.Run.ID}
	samples.record(contractSampleRequest{
		method: http.MethodGet, route: "/v1/runs/{runID}", auth: true, values: runRoute, status: http.StatusOK,
	})
	samples.record(contractSampleRequest{
		method: http.MethodGet, route: "/v1/runs/{runID}", auth: true,
		values: map[string]string{"runID": "00000000-0000-0000-0000-000000000000"},
		status: http.StatusNotFound,
	})
	// Recorded after the turn: the submitted Run is queued, so both answers carry a
	// real element rather than an empty array that validates nothing.
	samples.record(contractSampleRequest{
		method: http.MethodGet, route: "/v1/sessions/{sessionID}/active-run", auth: true,
		values: sessionRoute, status: http.StatusOK,
	})
	samples.record(contractSampleRequest{
		method: http.MethodGet, route: "/v1/projects/{projectID}/active-runs", auth: true,
		values: projectRoute, status: http.StatusOK,
	})

	if os.Getenv(contractSampleUpdateEnv) != "1" {
		samples.assertNoOrphans()
	}
}

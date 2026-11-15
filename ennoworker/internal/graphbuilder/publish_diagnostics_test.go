package graphbuilder

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubResolver answers Hero lookups from a fixed table so a test can make a
// proposal unpublishable on purpose (hero_not_found) or make it publishable.
type stubResolver struct {
	heroes map[string]*agentflow.HeroInfo
	err    error
}

func (r stubResolver) ResolveHero(_ context.Context, ref string) (*agentflow.HeroInfo, error) {
	if r.err != nil {
		return nil, r.err
	}
	if info, ok := r.heroes[ref]; ok {
		return info, nil
	}
	return nil, errors.New("hero " + ref + " is not published")
}

func (stubResolver) KnownSkill(context.Context, string) bool { return true }

func (stubResolver) ToolReadOnly(context.Context, string) bool { return true }

// failingResolver always fails, which is what an unpublished Hero looks like.
func failingValidatorFactory() ValidatorFactory {
	return func(string) *agentflow.Validator {
		return &agentflow.Validator{Resolver: stubResolver{err: errors.New("hero catalog is empty")}}
	}
}

// panickingResolver proves a broken validator degrades one report instead of
// failing the whole proposal.
type panickingResolver struct{ stubResolver }

func (panickingResolver) ResolveHero(context.Context, string) (*agentflow.HeroInfo, error) {
	panic("resolver exploded")
}

func completerReturning(payload string) Completer {
	return CompleteFunc(func(context.Context, string, string, string) (string, error) {
		return payload, nil
	})
}

func newFileBuilder(t *testing.T, graphID string) (*Service, *globalsource.Store) {
	t.Helper()
	sources := &globalsource.Store{HomeDir: t.TempDir()}
	_, _, err := sources.CreateGraphDefinition(graphID, graphID)
	require.NoError(t, err)
	return &Service{Sources: sources}, sources
}

func publishCodes(diagnostics []agentflow.ValidationDiagnostic) []string {
	codes := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		codes = append(codes, diagnostic.Code)
	}
	return codes
}

// A draft may legitimately not be publishable yet: a Hero the user has not
// published is the ordinary case. The proposal must still apply, and must carry
// the publish verdict so the UI cannot promise that publishing will succeed.
func TestProposalReportsPublishFailureWithoutBlockingApply(t *testing.T) {
	service, sources := newFileBuilder(t, "rna-seq")
	service.ValidatorFactory = failingValidatorFactory()
	service.Completer = completerReturning(`{"message":"Add an alignment Task.","operations":[` +
		`{"kind":"upsert_task","taskId":"align","task":{"name":"Align","hero":"aligner@v000001","goal":"Align reads."}}]}`)

	thread, err := service.Send(context.Background(), "rna-seq", "model-1", "Add alignment")
	require.NoError(t, err)
	require.NotNil(t, thread.Proposal)
	assert.Empty(t, thread.Proposal.Diagnostics, "the publish verdict must not become an Apply blocker")
	require.NotEmpty(t, thread.Proposal.PublishDiagnostics)
	assert.Contains(t, publishCodes(thread.Proposal.PublishDiagnostics), "hero_not_found")
	assert.Equal(t, "tasks.align.hero", thread.Proposal.PublishDiagnostics[0].Field)
	assert.Empty(t, thread.Proposal.PublishDiagnosticsUnavailable)

	definition, _, err := service.Apply(context.Background(), "rna-seq", thread.Proposal.ID)
	require.NoError(t, err, "a proposal publish would reject must still apply")
	assert.Contains(t, definition.Tasks, "align")

	// The report is part of the persisted proposal, not just the response.
	raw, err := os.ReadFile(filepath.Join(sources.GraphsDir(), "rna-seq", "builder", "proposals", thread.Proposal.ID+".json"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"publishDiagnostics"`)
}

// A publishable post-state carries no publish diagnostics, and the Apply
// blockers keep working exactly as before.
func TestProposalReportsNoPublishDiagnosticsWhenPublishable(t *testing.T) {
	service, _ := newFileBuilder(t, "rna-seq")
	service.ValidatorFactory = failingValidatorFactory()
	service.Completer = completerReturning(`{"message":"Close the Graph with a terminal task.","operations":[` +
		`{"kind":"upsert_task","taskId":"done","task":{"name":"Done","terminal":{"status":"success"}}}]}`)

	thread, err := service.Send(context.Background(), "rna-seq", "model-1", "Finish")
	require.NoError(t, err)
	require.NotNil(t, thread.Proposal)
	assert.Empty(t, thread.Proposal.Diagnostics)
	assert.Empty(t, thread.Proposal.PublishDiagnostics)
	assert.Empty(t, thread.Proposal.PublishDiagnosticsUnavailable, "a checked, clean draft says nothing rather than \"unavailable\"")

	_, _, err = service.Apply(context.Background(), "rna-seq", thread.Proposal.ID)
	require.NoError(t, err)

	// A cycle is still an Apply blocker, and it is still reported as one.
	cyclic, _ := newFileBuilder(t, "cycle")
	cyclic.ValidatorFactory = failingValidatorFactory()
	cyclic.Completer = completerReturning(`{"message":"Draft two Tasks.","operations":[` +
		`{"kind":"upsert_task","taskId":"aa","task":{"name":"A","hero":"a@v1","goal":"A"}},` +
		`{"kind":"upsert_task","taskId":"bb","task":{"name":"B","hero":"b@v1","goal":"B"}},` +
		`{"kind":"set_dependencies","taskId":"aa","depends":["bb"]},` +
		`{"kind":"set_dependencies","taskId":"bb","depends":["aa"]}]}`)
	blocked, err := cyclic.Send(context.Background(), "cycle", "model-1", "Make a cycle")
	require.NoError(t, err)
	require.NotNil(t, blocked.Proposal)
	require.NotEmpty(t, blocked.Proposal.Diagnostics)
	_, _, err = cyclic.Apply(context.Background(), "cycle", blocked.Proposal.ID)
	require.Error(t, err, "an Apply blocker still refuses the rewrite")
}

// Nil factory: nothing was checked, so the response has to say that instead of
// returning an empty list that reads as "no problems".
func TestProposalReportsUnavailablePublishCheckWithoutFactory(t *testing.T) {
	service, _ := newFileBuilder(t, "rna-seq")
	service.Completer = completerReturning(`{"message":"Add a Task.","operations":[` +
		`{"kind":"upsert_task","taskId":"align","task":{"name":"Align","hero":"aligner@v000001","goal":"Align reads."}}]}`)

	thread, err := service.Send(context.Background(), "rna-seq", "model-1", "Add alignment")
	require.NoError(t, err)
	require.NotNil(t, thread.Proposal)
	assert.Empty(t, thread.Proposal.Diagnostics, "Diagnostics keeps its Apply-blocker meaning")
	assert.Empty(t, thread.Proposal.PublishDiagnostics)
	assert.Equal(t, publishReportUnavailableNoFactory, thread.Proposal.PublishDiagnosticsUnavailable)

	_, _, err = service.Apply(context.Background(), "rna-seq", thread.Proposal.ID)
	require.NoError(t, err, "an unchecked draft still applies")
}

// A validator factory that returns nothing (a down model catalog) is the same
// "not checked" state as a nil factory.
func TestProposalReportsUnavailableWhenFactoryReturnsNoValidator(t *testing.T) {
	service, _ := newFileBuilder(t, "rna-seq")
	service.ValidatorFactory = func(string) *agentflow.Validator { return nil }
	service.Completer = completerReturning(`{"message":"Add a Task.","operations":[` +
		`{"kind":"upsert_task","taskId":"align","task":{"name":"Align","hero":"aligner@v000001","goal":"Align reads."}}]}`)

	thread, err := service.Send(context.Background(), "rna-seq", "model-1", "Add alignment")
	require.NoError(t, err)
	require.NotNil(t, thread.Proposal)
	assert.Equal(t, publishReportUnavailableNoValidator, thread.Proposal.PublishDiagnosticsUnavailable)
}

// A panicking validator must not take the proposal with it.
func TestProposalSurvivesPanickingPublishValidator(t *testing.T) {
	service, _ := newFileBuilder(t, "rna-seq")
	service.ValidatorFactory = func(string) *agentflow.Validator {
		return &agentflow.Validator{Resolver: panickingResolver{}}
	}
	service.Completer = completerReturning(`{"message":"Add a Task.","operations":[` +
		`{"kind":"upsert_task","taskId":"align","task":{"name":"Align","hero":"aligner@v000001","goal":"Align reads."}}]}`)

	thread, err := service.Send(context.Background(), "rna-seq", "model-1", "Add alignment")
	require.NoError(t, err)
	require.NotNil(t, thread.Proposal)
	assert.Empty(t, thread.Proposal.Diagnostics)
	assert.Contains(t, thread.Proposal.PublishDiagnosticsUnavailable, "did not finish")
	assert.Contains(t, thread.Proposal.PublishDiagnosticsUnavailable, "panicked")

	_, _, err = service.Apply(context.Background(), "rna-seq", thread.Proposal.ID)
	require.NoError(t, err)
}

// An already-spent deadline is the timeout shape of the same degradation: the
// check is reported as not run, and the proposal still exists.
func TestProposalReportsUnavailableWhenPublishCheckCannotRun(t *testing.T) {
	service, _ := newFileBuilder(t, "rna-seq")
	service.ValidatorFactory = failingValidatorFactory()
	service.Completer = completerReturning(`{"message":"Add a Task.","operations":[` +
		`{"kind":"upsert_task","taskId":"align","task":{"name":"Align","hero":"aligner@v000001","goal":"Align reads."}}]}`)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	thread, err := service.Send(cancelled, "rna-seq", "model-1", "Add alignment")
	require.NoError(t, err)
	require.NotNil(t, thread.Proposal)
	assert.Empty(t, thread.Proposal.Diagnostics)
	assert.Empty(t, thread.Proposal.PublishDiagnostics)
	assert.Contains(t, thread.Proposal.PublishDiagnosticsUnavailable, "did not finish")
}

// A publish verdict also covers what the draft already carried, not only what
// the operations add: a hand-edited Graph with no budget is the ordinary case.
func TestProposalReportsPublishFailureAlreadyInTheDraft(t *testing.T) {
	sources := &globalsource.Store{HomeDir: t.TempDir()}
	_, err := sources.CreateGraphFromDefinition(&domain.FlowDefinition{
		SchemaVersion: domain.FlowSchemaVersion, ID: "no-budget", Name: "No budget",
		Tasks: map[string]domain.FlowTask{},
	})
	require.NoError(t, err)
	service := &Service{Sources: sources, ValidatorFactory: failingValidatorFactory(),
		Completer: completerReturning(`{"message":"Describe the Graph.","operations":[` +
			`{"kind":"update_graph","description":"QC pipeline"}]}`)}

	thread, err := service.Send(context.Background(), "no-budget", "model-1", "Describe it")
	require.NoError(t, err)
	require.NotNil(t, thread.Proposal)
	require.NotEmpty(t, thread.Proposal.PublishDiagnostics)
	assert.Contains(t, publishCodes(thread.Proposal.PublishDiagnostics), "flow_budget_required")
	assert.Empty(t, thread.Proposal.Diagnostics)

	definition, _, err := service.Apply(context.Background(), "no-budget", thread.Proposal.ID)
	require.NoError(t, err, "an unpublishable draft still applies")
	assert.Equal(t, "QC pipeline", definition.Description)
}

// The thread endpoint serialises the Proposal struct, so the persisted fields
// are what it returns. An older proposal file without them still reads.
func TestThreadReturnsPersistedPublishDiagnosticsForFileProposal(t *testing.T) {
	newFileBuilderForThread := func(graphID string) *Service {
		sources := &globalsource.Store{HomeDir: t.TempDir()}
		_, _, err := sources.CreateGraphDefinition(graphID, graphID)
		require.NoError(t, err)
		return &Service{Sources: sources}
	}
	service := newFileBuilderForThread("rna-seq")
	proposalsDir := filepath.Join(service.Sources.GraphsDir(), "rna-seq", "builder", "proposals")
	require.NoError(t, os.MkdirAll(proposalsDir, 0o700))

	proposal := Proposal{
		ID: uuid.NewString(), GraphID: "rna-seq", BaseDigest: "digest",
		Operations: []Operation{{Kind: "update_graph"}}, Summary: "Add a budget.", Status: "pending",
		Diagnostics: []string{},
		PublishDiagnostics: []agentflow.ValidationDiagnostic{
			{Code: "flow_budget_required", Message: "flow budget.maxTotalTokens is required and must be positive", Field: "budget.maxTotalTokens"},
		},
		CreatedAt: time.Now().UTC(),
	}
	encoded, err := json.MarshalIndent(proposal, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(proposalsDir, proposal.ID+".json"), encoded, 0o600))

	thread, err := service.GetThread(context.Background(), "rna-seq")
	require.NoError(t, err)
	require.NotNil(t, thread.Proposal)
	require.Len(t, thread.Proposal.PublishDiagnostics, 1)
	assert.Equal(t, "flow_budget_required", thread.Proposal.PublishDiagnostics[0].Code)
	assert.Empty(t, thread.Proposal.PublishDiagnosticsUnavailable)

	// A proposal written before the publish report existed.
	legacy := newFileBuilderForThread("legacy")
	legacyDir := filepath.Join(legacy.Sources.GraphsDir(), "legacy", "builder", "proposals")
	require.NoError(t, os.MkdirAll(legacyDir, 0o700))
	legacyID := uuid.NewString()
	oldShape := map[string]any{
		"id": legacyID, "graphId": "legacy", "baseDigest": "digest",
		"operations": []any{map[string]any{"kind": "update_graph"}}, "summary": "Old shape.",
		"status": "pending", "diagnostics": []string{}, "createdAt": time.Now().UTC().Format(time.RFC3339Nano),
	}
	encoded, err = json.MarshalIndent(oldShape, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(legacyDir, legacyID+".json"), encoded, 0o600))

	oldThread, err := legacy.GetThread(context.Background(), "legacy")
	require.NoError(t, err)
	require.NotNil(t, oldThread.Proposal)
	assert.Empty(t, oldThread.Proposal.PublishDiagnostics)
	assert.Empty(t, oldThread.Proposal.PublishDiagnosticsUnavailable)
}

// The SQL-backed proposal store keeps its column shape: the reader accepts both
// the new envelope and the legacy bare array.
func TestDecodeProposalDiagnosticsAcceptsBothShapes(t *testing.T) {
	apply, publish, unavailable := decodeProposalDiagnostics(
		`{"apply":["draft is not parseable"],"publish":[{"code":"flow_budget_required","message":"budget","field":"budget.maxTotalTokens"}]}`)
	assert.Equal(t, []string{"draft is not parseable"}, apply)
	require.Len(t, publish, 1)
	assert.Equal(t, "flow_budget_required", publish[0].Code)
	assert.Empty(t, unavailable)

	apply, publish, unavailable = decodeProposalDiagnostics(`["draft is not parseable"]`)
	assert.Equal(t, []string{"draft is not parseable"}, apply)
	assert.Empty(t, publish)
	assert.Empty(t, unavailable)

	apply, publish, unavailable = decodeProposalDiagnostics("")
	assert.Empty(t, apply)
	assert.Empty(t, publish)
	assert.Empty(t, unavailable)
}

package graphbuilder

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuilderPersistsValidatedProposalAndRequiresApply(t *testing.T) {
	sources := &globalsource.Store{HomeDir: t.TempDir()}
	_, originalDigest, err := sources.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)
	service := &Service{Sources: sources, Completer: CompleteFunc(func(context.Context, string, string, string) (string, error) {
		return `{"message":"Add an alignment Task.","operations":[{"kind":"upsert_task","taskId":"align","task":{"name":"Align","model":"anthropic/claude-sonnet-4","thinking":"high","skills":[],"goal":"Align reads."}}]}`, nil
	})}

	thread, err := service.Send(context.Background(), "rna-seq", "model-1", "Add alignment")
	require.NoError(t, err)
	require.Len(t, thread.Messages, 2)
	require.NotNil(t, thread.Proposal)
	assert.Empty(t, thread.Proposal.Diagnostics)
	_, currentDigest, err := sources.ReadGraph("rna-seq")
	require.NoError(t, err)
	assert.Equal(t, originalDigest, currentDigest, "proposal must not write before Apply")

	document, appliedDigest, err := service.Apply(context.Background(), "rna-seq", thread.Proposal.ID)
	require.NoError(t, err)
	assert.Contains(t, document.Tasks, "align")
	assert.NotEqual(t, originalDigest, appliedDigest)
	loaded, err := service.GetThread(context.Background(), "rna-seq")
	require.NoError(t, err)
	assert.Nil(t, loaded.Proposal)
}

func TestBuilderRetainsInvalidCycleProposalWithoutWriting(t *testing.T) {
	sources := &globalsource.Store{HomeDir: t.TempDir()}
	_, digest, err := sources.CreateGraph("cycle", "Cycle")
	require.NoError(t, err)
	service := &Service{Sources: sources, Completer: CompleteFunc(func(context.Context, string, string, string) (string, error) {
		return `{"message":"Draft two Tasks.","operations":[{"kind":"upsert_task","taskId":"aa","task":{"name":"A","model":"p/m","goal":"A"}},{"kind":"upsert_task","taskId":"bb","task":{"name":"B","model":"p/m","goal":"B"}},{"kind":"set_dependencies","taskId":"aa","depends":["bb"]},{"kind":"set_dependencies","taskId":"bb","depends":["aa"]}]}`, nil
	})}
	thread, err := service.Send(context.Background(), "cycle", "model-1", "Make a cycle")
	require.NoError(t, err)
	require.NotNil(t, thread.Proposal)
	assert.Contains(t, thread.Proposal.Diagnostics[0], "dependency cycle")
	_, _, err = service.Apply(context.Background(), "cycle", thread.Proposal.ID)
	require.Error(t, err)
	_, currentDigest, err := sources.ReadGraph("cycle")
	require.NoError(t, err)
	assert.Equal(t, digest, currentDigest)
}

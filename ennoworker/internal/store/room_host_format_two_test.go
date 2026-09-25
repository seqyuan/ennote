package store_test

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	store "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFormatTwoHostTurnResolvesWithoutRoleTarget pins the regression where a
// speaker-ledger (format 2) Host turn failed config freeze with
// invocation_target_invalid: the resolver unconditionally looked up a Role
// target, even though a plain user message on a room lineage has none.
func TestFormatTwoHostTurnResolvesWithoutRoleTarget(t *testing.T) {
	ctx := context.Background()
	stack := newFileConfigStack(t)
	db := store.SetupDB(t)
	project, _, err := newFileProjects(t).CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "room-host", HostPath: t.TempDir()})
	require.NoError(t, err)
	session := sqlCreateSession(t, db, project.ID)
	runs := &store.RunRepo{DB: db, Providers: stack.Providers, Models: stack.ModelRepo,
		Policies: stack.Policies, RoleSources: stack.Sources}
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "room-host", Text: "hello"})
	require.NoError(t, err)
	run, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	// A room lineage already upgraded to the speaker ledger commits Host turns
	// as format 2 too (runs.go), so the freeze must resolve a Host, not a Role.
	run.CommitFormatVersion = domain.CommitFormatSpeakerV2

	resolved, err := runs.ResolveAndFreezeConfig(ctx, run)
	require.NoError(t, err)
	assert.Nil(t, resolved.Effective.Role)
	assert.NotEmpty(t, resolved.Effective.InitialRuntime.ModelProfileID)
}

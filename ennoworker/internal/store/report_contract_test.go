package store_test

// Hero report contracts at the two freeze points and at the delegation
// completion:
//   - HeroDiscovery carries the contract from the Portable Hero document into the
//     frozen HeroDefinition;
//   - a Run's frozen Hero snapshot records it, so the child Run's config states
//     what its report had to look like;
//   - a delegated child that ends completed with a report that breaks it is
//     recorded as failed with report_contract_violation instead of being folded
//     in as a success.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reportContractDocument(defaultRef string) *heroesource.Document {
	return &heroesource.Document{
		SchemaVersion: 1, Handle: "report-writer", Name: "Report Writer",
		Description: "Writes a report with a fixed shape.", Positioning: "Report shapes.",
		Icon: "bot", Color: "neutral",
		Model:  heroesource.ModelBinding{Ref: defaultRef, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, Authority: heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read"},
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextTask,
			AllowedModes:           []domain.HeroContextMode{domain.HeroContextTask},
			OwnExecutionContinuity: domain.HeroContinuityNone},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 1, MaxConcurrentInstances: 1,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Produce the report.",
		ReportContract: &domain.HeroReportContract{Version: domain.HeroReportContractV1,
			SummaryMaxBytes: 512, RequireArtifacts: true, PayloadFields: []string{"changedFiles", "testsRun"}},
	}
}

// The resolved (frozen) definition must carry the contract, otherwise nothing
// downstream can enforce what the Hero promised.
func TestHeroDiscoveryFreezesReportContract(t *testing.T) {
	stack := newFileConfigStack(t)
	document := reportContractDocument(stack.DefaultRef)
	definition, diagnostics := (&store.HeroDiscovery{Models: stack.ModelRepo}).ResolveDocument(context.Background(), document)
	require.Empty(t, diagnostics)
	require.NotNil(t, definition)
	require.NotNil(t, definition.ReportContract)
	assert.Equal(t, 1, definition.ReportContract.Version)
	assert.Equal(t, 512, definition.ReportContract.SummaryMaxBytes)
	assert.True(t, definition.ReportContract.RequireArtifacts)
	assert.Equal(t, []string{"changedFiles", "testsRun"}, definition.ReportContract.PayloadFields)
	assert.NotSame(t, document.ReportContract, definition.ReportContract,
		"the frozen definition must not alias the source document")

	// A Hero without a contract keeps a nil definition field, so its digest and
	// the enforcement path are exactly what they were.
	plain := reportContractDocument(stack.DefaultRef)
	plain.ReportContract = nil
	plainDefinition, diagnostics := (&store.HeroDiscovery{Models: stack.ModelRepo}).ResolveDocument(context.Background(), plain)
	require.Empty(t, diagnostics)
	assert.Nil(t, plainDefinition.ReportContract)
}

// The child Run's frozen Hero snapshot is what the Run itself records, so the
// contract has to appear there as well as on the definition.
func TestFrozenRunHeroSnapshotCarriesReportContract(t *testing.T) {
	stack := newFileConfigStack(t)
	ctx := context.Background()
	db, _, session := newSessionDB(t)
	document := reportContractDocument(stack.DefaultRef)
	document.Context = heroesource.ContextPolicy{DefaultMode: domain.HeroContextRoom,
		AllowedModes:           []domain.HeroContextMode{domain.HeroContextRoom, domain.HeroContextReply, domain.HeroContextFresh, domain.HeroContextTask},
		OwnExecutionContinuity: domain.HeroContinuityNone}
	_, _, err := stack.Sources.CreateHero(document)
	require.NoError(t, err)
	revision, err := stack.Sources.PublishHeroRevision(document.Handle)
	require.NoError(t, err)
	parsed, _, err := stack.Sources.ReadHero(document.Handle)
	require.NoError(t, err)
	definition, diagnostics := (&store.HeroDiscovery{Models: stack.ModelRepo}).ResolveDocument(ctx, parsed)
	require.NotNil(t, definition, diagnostics)
	require.NotNil(t, definition.ReportContract)

	runs := &store.RunRepo{DB: db, Providers: stack.Providers, Models: stack.ModelRepo,
		Policies: stack.Policies, HeroSources: stack.Sources}
	revisionID := revision.ID()
	hero := domain.HeroIdentity{
		ID: document.Handle, Handle: document.Handle, Name: document.Name,
		Description: document.Description, Positioning: document.Positioning,
		Icon: document.Icon, Color: document.Color, Status: "active",
		SourceKind: "file", SourceLocator: document.Handle, SourceDigest: revision.Digest,
		CurrentVersionID: &revisionID, CurrentVersion: revision.Version, DelegationEnabled: true,
	}
	version := domain.HeroVersion{ID: revisionID, HeroID: document.Handle, Version: revision.Version,
		Definition: *definition, ConfigDigest: revision.Digest, Status: "published"}

	submission, err := runs.SubmitInvocation(ctx, heroInvocation(session.ID, "report-contract-freeze", hero, version))
	require.NoError(t, err)
	claimed, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	resolved, err := runs.ResolveAndFreezeConfig(ctx, claimed)
	require.NoError(t, err)
	require.NotNil(t, resolved.Effective.Hero)
	require.NotNil(t, resolved.Effective.Hero.ReportContract,
		"the frozen Hero snapshot must record the contract the child's report is checked against")
	assert.Equal(t, definition.ReportContract, resolved.Effective.Hero.ReportContract)
	assert.Equal(t, "text-v1", resolved.Effective.Hero.OutputContract,
		"the label equality rule stays untouched")
}

// childWithReportContract builds a delegation child whose frozen Hero meta
// declares the given report contract, on a parent Run that is not a Graph anchor
// (so the store owns the completion verdict).
func childWithReportContract(t *testing.T, contract *domain.HeroReportContract) (
	*store.DelegationRepo, *store.RunRepo, *domain.TurnSubmission, string) {
	t.Helper()
	delegations, runs, submission := setupRootBudgetParent(t, "report-contract-child")
	ctx := context.Background()
	item := explorerItem()
	item.HeroMeta.Definition.ReportContract = contract
	_, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "report-contract-call",
		Strategy: domain.DelegationStrategySingle,
		Items:    []store.CreateDelegationItemInput{item},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, children, 1)
	_, err = runs.Claim(ctx, children[0].ID)
	require.NoError(t, err)
	return delegations, runs, submission, children[0].ID
}

func TestFinalizeChildSuccessRecordsReportContractViolationAsFailure(t *testing.T) {
	contract := &domain.HeroReportContract{Version: domain.HeroReportContractV1,
		SummaryMaxBytes: 16, RequireArtifacts: true, PayloadFields: []string{"changedFiles"}}
	delegations, runs, submission, childID := childWithReportContract(t, contract)
	ctx := context.Background()

	// The report is a well-formed submit_result that simply does not keep the
	// promise: a long summary, no artifact, no changedFiles key.
	require.NoError(t, runs.FinalizeChildSuccess(ctx, childID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "summarized"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted,
			Summary: "a summary that is much longer than sixteen bytes",
			Payload: json.RawMessage(`{"other":true}`)},
	}), "recording the failure is a successful finalize, not a finalize error")

	var status, code, message string
	require.NoError(t, runs.DB.QueryRow(`SELECT status,error_code,error_message FROM agent_runs WHERE id=?`,
		childID).Scan(&status, &code, &message))
	assert.Equal(t, "failed", status)
	assert.Equal(t, "report_contract_violation", code)
	assert.Contains(t, message, "report contract v1 allows at most 16")

	// The item is failed with the same code, never folded in as a success.
	var itemStatus string
	var resultJSON string
	require.NoError(t, delegations.DB.QueryRow(`SELECT i.status,i.result_json FROM delegation_items i
		JOIN delegation_item_attempts a ON a.item_id=i.id WHERE a.child_run_id=?`, childID).
		Scan(&itemStatus, &resultJSON))
	assert.Equal(t, "failed", itemStatus)
	assert.Contains(t, resultJSON, `"errorCode":"report_contract_violation"`)

	var attemptStatus, attemptCode, attemptMessage string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status,error_code,error_message FROM delegation_item_attempts
		WHERE child_run_id=?`, childID).Scan(&attemptStatus, &attemptCode, &attemptMessage))
	assert.Equal(t, "failed", attemptStatus)
	assert.Equal(t, "report_contract_violation", attemptCode)
	assert.Contains(t, attemptMessage, "report contract v1 allows at most 16")

	// The group settled and the parent was woken with the folded failure, exactly
	// like every other child failure.
	var groupStatus, parentStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT g.status FROM delegation_groups g
		JOIN delegation_item_attempts a ON a.item_id IN (SELECT id FROM delegation_items WHERE group_id=g.id)
		WHERE a.child_run_id=?`, childID).Scan(&groupStatus))
	assert.Equal(t, "settled", groupStatus)
	require.NoError(t, runs.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, submission.Run.ID).Scan(&parentStatus))
	assert.Equal(t, "queued", parentStatus)
}

// A compliant report still finalizes as a success: the rule only fires on a
// violation.
func TestFinalizeChildSuccessAcceptsACompliantReport(t *testing.T) {
	contract := &domain.HeroReportContract{Version: domain.HeroReportContractV1,
		SummaryMaxBytes: 64, PayloadFields: []string{"changedFiles"}}
	_, runs, _, childID := childWithReportContract(t, contract)
	ctx := context.Background()
	require.NoError(t, runs.FinalizeChildSuccess(ctx, childID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "done",
			Payload: json.RawMessage(`{"changedFiles":["a.go"]}`)},
	}))
	var status string
	var code *string
	require.NoError(t, runs.DB.QueryRow(`SELECT status,error_code FROM agent_runs WHERE id=?`,
		childID).Scan(&status, &code))
	assert.Equal(t, "succeeded", status)
	assert.Nil(t, code)
}

// A Hero without a contract keeps finalizing exactly as before: a one-line
// summary with no payload is still a success.
func TestFinalizeChildSuccessWithoutContractKeepsLegacyBehaviour(t *testing.T) {
	_, runs, _, childID := childWithReportContract(t, nil)
	ctx := context.Background()
	require.NoError(t, runs.FinalizeChildSuccess(ctx, childID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "done"},
	}))
	var status string
	require.NoError(t, runs.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, childID).Scan(&status))
	assert.Equal(t, "succeeded", status)
}

// A Graph (flow) child is settled by the agentflow fold, which records the
// violation on the node with the child's output_ref kept. The store must not
// pre-empt that verdict by failing the child Run, or the fold would only ever be
// able to report a generic task failure.
func TestFinalizeChildSuccessLeavesFlowTaskVerdictToTheFold(t *testing.T) {
	contract := &domain.HeroReportContract{Version: domain.HeroReportContractV1, RequireArtifacts: true}
	delegations, runs, submission, childID := childWithReportContract(t, contract)
	ctx := context.Background()
	var projectID string
	require.NoError(t, runs.DB.QueryRow(`SELECT project_id FROM sessions WHERE id=?`,
		submission.Run.SessionID).Scan(&projectID))
	_, err := runs.DB.Exec(`INSERT INTO run_agent_flow
		(run_id,session_id,project_id,flow_version_id,manifest_digest,inputs_json,state,created_at,updated_at)
		VALUES(?,?,?,'version-1','manifest','{}','running','2026-08-03T00:00:00Z','2026-08-03T00:00:00Z')`,
		submission.Run.ID, submission.Run.SessionID, projectID)
	require.NoError(t, err)

	require.NoError(t, runs.FinalizeChildSuccess(ctx, childID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "done"},
	}))
	var status string
	require.NoError(t, runs.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, childID).Scan(&status))
	assert.Equal(t, "succeeded", status,
		"the flow fold owns the task verdict; the child Run keeps its successful report")
	var itemStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT i.status FROM delegation_items i
		JOIN delegation_item_attempts a ON a.item_id=i.id WHERE a.child_run_id=?`, childID).Scan(&itemStatus))
	assert.Equal(t, "succeeded", itemStatus)
}

package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeImageLoader struct{ image domain.ImageRef }

func (l fakeImageLoader) LoadImage(context.Context, string) (domain.ImageRef, error) {
	return l.image, nil
}

func TestVisionResolverRoutesOrDescribesUnsupportedImage(t *testing.T) {
	message := domain.ChatMessage{Role: domain.RoleUser, Content: []domain.ContentBlock{{
		Kind: domain.ContentImage, Image: &domain.ImageRef{ArtifactID: "img"},
	}}}
	resolver := &BuiltinVisionResolver{Loader: fakeImageLoader{image: domain.ImageRef{ArtifactID: "img", MIMEType: "image/png", Data: []byte{1}}}}
	routeConfig, _ := json.Marshal(domain.VisionPolicyConfig{Mode: "route"})
	resolution, err := resolver.ResolveImages(context.Background(), VisionContext{Messages: []domain.ChatMessage{message},
		Policy: domain.PolicySnapshot{Config: routeConfig}})
	require.NoError(t, err)
	assert.True(t, resolution.Constraint.RequiresVision)
	assert.Equal(t, []byte{1}, resolution.RewrittenMessages[0].Content[0].Image.Data)

	describeConfig, _ := json.Marshal(domain.VisionPolicyConfig{Mode: "describe", DescriptorModelProfileID: "vision", PromptVersion: "v1"})
	resolution, err = resolver.ResolveImages(context.Background(), VisionContext{Messages: []domain.ChatMessage{message},
		Policy: domain.PolicySnapshot{Config: describeConfig}})
	require.NoError(t, err)
	require.Len(t, resolution.DescriptorRequests, 1)
	assert.Equal(t, "vision", resolution.DescriptorRequests[0].ModelProfileID)
}

func TestLoopDescribesImageBeforeCallingTextModel(t *testing.T) {
	textProvider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
		Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "primary answer"}}, StopReason: domain.StopReasonStop,
	}})
	visionProvider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
		Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "a blue chart"}}, StopReason: domain.StopReasonStop,
	}})
	routing := domain.FrozenRoutingConfig{Candidates: []domain.ModelRuntimeSnapshot{
		{ModelProfileID: "text", APIModel: "text", ContextTokens: 1000, MaxOutputTokens: 100},
		{ModelProfileID: "vision", APIModel: "vision", ContextTokens: 1000, MaxOutputTokens: 100, SupportsVision: true},
	}}
	router := &SnapshotModelRouter{Factory: func(snapshot domain.ModelRuntimeSnapshot) (llm.Provider, error) {
		if snapshot.ModelProfileID == "vision" {
			return visionProvider, nil
		}
		return textProvider, nil
	}}
	config, _ := json.Marshal(domain.VisionPolicyConfig{Mode: "describe", DescriptorModelProfileID: "vision", PromptVersion: "v1"})
	loop := &Loop{Provider: textProvider, ModelRouter: router, Tools: &fakeTools{}, Events: &memoryWriter{},
		VisionResolver: &BuiltinVisionResolver{Loader: fakeImageLoader{image: domain.ImageRef{
			ArtifactID: "img", MIMEType: "image/png", SHA256: "sha", Data: []byte{1}}}}, MaxIterations: 2}
	result, err := loop.Run(context.Background(), RunInput{RunID: "vision-run", Model: "text",
		InitialRuntime: routing.Candidates[0], Routing: routing, VisionPolicy: domain.PolicySnapshot{Config: config},
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{{
			Kind: domain.ContentImage, Image: &domain.ImageRef{ArtifactID: "img"},
		}}}}})
	require.NoError(t, err)
	require.NotEmpty(t, result.Generated)
	assert.Equal(t, "primary answer", result.Generated[len(result.Generated)-1].Content[0].Text)
}

func TestVisionResolverRejectsUnsupportedImage(t *testing.T) {
	config, _ := json.Marshal(domain.VisionPolicyConfig{Mode: "reject"})
	resolver := &BuiltinVisionResolver{Loader: fakeImageLoader{image: domain.ImageRef{ArtifactID: "img", Data: []byte{1}}}}
	_, err := resolver.ResolveImages(context.Background(), VisionContext{Messages: []domain.ChatMessage{{Role: domain.RoleUser,
		Content: []domain.ContentBlock{{Kind: domain.ContentImage, Image: &domain.ImageRef{ArtifactID: "img"}}}}},
		Policy: domain.PolicySnapshot{Config: config}})
	assert.Equal(t, domain.ErrorVisionUnsupported, domain.ErrorCodeOf(err))
}

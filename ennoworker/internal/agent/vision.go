package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type ManagedImageLoader interface {
	LoadImage(context.Context, string) (domain.ImageRef, error)
}

type BuiltinVisionResolver struct {
	Loader ManagedImageLoader
}

func (r *BuiltinVisionResolver) ResolveImages(ctx context.Context, vision VisionContext) (VisionResolution, error) {
	messages := cloneMessages(vision.Messages)
	var config domain.VisionPolicyConfig
	if err := json.Unmarshal(vision.Policy.Config, &config); err != nil {
		return VisionResolution{}, domain.NewCodedError(domain.ErrorVisionFallbackFailed, err)
	}
	resolution := VisionResolution{RewrittenMessages: messages}
	for messageIndex := range messages {
		for blockIndex := range messages[messageIndex].Content {
			block := &messages[messageIndex].Content[blockIndex]
			if block.Kind != domain.ContentImage || block.Image == nil {
				continue
			}
			if r == nil || r.Loader == nil {
				return VisionResolution{}, domain.NewCodedError(domain.ErrorVisionFallbackFailed,
					fmt.Errorf("managed image loader is required"))
			}
			loaded, err := r.Loader.LoadImage(ctx, block.Image.ArtifactID)
			if err != nil {
				return VisionResolution{}, domain.NewCodedError(domain.ErrorImageInvalid, err)
			}
			block.Image = &loaded
			if vision.Current.SupportsVision {
				continue
			}
			switch config.Mode {
			case "route":
				resolution.Constraint.RequiresVision = true
			case "describe":
				resolution.DescriptorRequests = append(resolution.DescriptorRequests, ImageDescriptionRequest{
					Image: loaded, ModelProfileID: config.DescriptorModelProfileID, PromptVersion: config.PromptVersion})
			case "reject", "":
				return VisionResolution{}, domain.NewCodedError(domain.ErrorVisionUnsupported,
					fmt.Errorf("selected model does not support image input"))
			default:
				return VisionResolution{}, domain.NewCodedError(domain.ErrorVisionFallbackFailed,
					fmt.Errorf("unknown vision mode %q", config.Mode))
			}
		}
	}
	return resolution, nil
}

func cloneMessages(messages []domain.ChatMessage) []domain.ChatMessage {
	cloned := make([]domain.ChatMessage, len(messages))
	for index, message := range messages {
		cloned[index] = message
		cloned[index].Content = append([]domain.ContentBlock(nil), message.Content...)
		for blockIndex := range cloned[index].Content {
			block := &cloned[index].Content[blockIndex]
			if block.Image != nil {
				image := *block.Image
				image.Data = append([]byte(nil), block.Image.Data...)
				block.Image = &image
			}
		}
	}
	return cloned
}

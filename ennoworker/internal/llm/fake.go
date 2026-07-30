package llm

import (
	"context"
	"fmt"
	"sync"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type FakeStep struct {
	TextDeltas     []string
	ThinkingDeltas []string
	Completion     domain.Completion
	Err            error
}

type FakeProvider struct {
	mu       sync.Mutex
	steps    []FakeStep
	Requests []domain.CompletionRequest
}

func NewFakeProvider(steps ...FakeStep) *FakeProvider {
	return &FakeProvider{steps: append([]FakeStep(nil), steps...)}
}

func (p *FakeProvider) Capabilities() ModelCapabilities {
	return ModelCapabilities{Streaming: true, ToolUse: true, Thinking: true}
}

func (p *FakeProvider) Stream(ctx context.Context, request domain.CompletionRequest, sink StreamSink) (domain.Completion, error) {
	p.mu.Lock()
	if len(p.steps) == 0 {
		p.mu.Unlock()
		return domain.Completion{}, fmt.Errorf("fake provider has no remaining steps")
	}
	step := p.steps[0]
	p.steps = p.steps[1:]
	p.Requests = append(p.Requests, request)
	p.mu.Unlock()

	for _, delta := range step.TextDeltas {
		if err := ctx.Err(); err != nil {
			return domain.Completion{}, ErrCancelled
		}
		if err := sink.TextDelta(delta); err != nil {
			return domain.Completion{}, err
		}
	}
	for _, delta := range step.ThinkingDeltas {
		if err := sink.ThinkingDelta(delta); err != nil {
			return domain.Completion{}, err
		}
	}
	if step.Completion.Usage != (domain.Usage{}) {
		if err := sink.Usage(step.Completion.Usage); err != nil {
			return domain.Completion{}, err
		}
	}
	return step.Completion, step.Err
}

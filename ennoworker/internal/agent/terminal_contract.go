package agent

import (
	"errors"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// SubmitResultGate captures the structured terminal result of a child Run. The
// gate is non-nil only when the Loop executes a delegated_agent Run.
type SubmitResultGate struct {
	Result *domain.SubmitResult
}

var (
	ErrInvalidTerminalContract = errors.New("invalid terminal contract")
	ErrIncompleteTerminal      = domain.NewCodedError(domain.ErrorIncompleteTerminalContract,
		errors.New("child Run ended without calling submit_result"))
)

// interceptSubmitResult scans the model's tool calls for submit_result. When
// found it validates the contract and returns (result, true, nil). A malformed
// contract returns (nil, true, ErrInvalidTerminalContract) so the caller fails
// instead of executing a control tool through the policy gate.
func interceptSubmitResult(calls []domain.ToolCall) (*domain.SubmitResult, bool, error) {
	var submitIndex = -1
	for index := range calls {
		if calls[index].Name == "submit_result" {
			if submitIndex != -1 {
				return nil, true, fmt.Errorf("%w: submit_result may only be called once per turn", ErrInvalidTerminalContract)
			}
			submitIndex = index
		}
	}
	if submitIndex == -1 {
		return nil, false, nil
	}
	if len(calls) != 1 {
		return nil, true, fmt.Errorf("%w: submit_result must be the only tool call of the final turn", ErrInvalidTerminalContract)
	}
	result, err := domain.ValidateSubmitResult(calls[0].Arguments)
	if err != nil {
		return nil, true, fmt.Errorf("%w: %v", ErrInvalidTerminalContract, err)
	}
	return result, true, nil
}

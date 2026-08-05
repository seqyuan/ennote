package store

import (
	"fmt"
	"sort"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type retryRequestDigestPayload struct {
	ExpectedGeneration int                                 `json:"expectedGeneration"`
	ItemIDs            []string                            `json:"itemIds"`
	BudgetOverrides    map[string]domain.BudgetCeilingJSON `json:"budgetOverrides,omitempty"`
}

type continuationRequestDigestPayload struct {
	Kind               domain.DelegationGenerationKind `json:"kind"`
	ItemID             string                          `json:"itemId"`
	SourceAttemptID    string                          `json:"sourceAttemptId"`
	ExpectedGeneration int                             `json:"expectedGeneration"`
	Text               string                          `json:"text"`
	Budget             *domain.BudgetCeilingJSON       `json:"budget,omitempty"`
}

func retryRequestDigest(input domain.RetryDelegationInput) (string, error) {
	itemIDs := append([]string(nil), input.ItemIDs...)
	sort.Strings(itemIDs)
	for index, itemID := range itemIDs {
		if itemID == "" {
			return "", fmt.Errorf("retry selection contains an empty item id")
		}
		if index > 0 && itemIDs[index-1] == itemID {
			return "", fmt.Errorf("retry selection contains duplicate item %s", itemID)
		}
	}
	return digestJSON(retryRequestDigestPayload{
		ExpectedGeneration: input.ExpectedGeneration,
		ItemIDs:            itemIDs,
		BudgetOverrides:    input.BudgetOverrides,
	})
}

func continuationRequestDigest(itemID string, kind domain.DelegationGenerationKind,
	input domain.DelegationInputCommand) (string, error) {
	return digestJSON(continuationRequestDigestPayload{
		Kind: kind, ItemID: itemID, SourceAttemptID: input.SourceAttemptID,
		ExpectedGeneration: input.ExpectedGeneration, Text: input.Text, Budget: input.Budget,
	})
}

func requestDigestConflict(stored, requested string) error {
	if stored == "" || stored == requested {
		return nil
	}
	return domain.NewCodedError(domain.ErrorDelegationGenerationConflict,
		fmt.Errorf("client request id is already bound to a different generation request"))
}

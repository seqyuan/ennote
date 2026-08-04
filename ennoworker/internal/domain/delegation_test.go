package domain

import (
	"encoding/json"
	"testing"
)

func TestGenerationStatusTransitions(t *testing.T) {
	cases := []struct {
		from, to DelegationGenerationStatus
		ok       bool
	}{
		{DelegationGenerationAwaitingAuthorization, DelegationGenerationQueued, true},
		{DelegationGenerationAwaitingAuthorization, DelegationGenerationCancelled, true},
		{DelegationGenerationAwaitingAuthorization, DelegationGenerationFailed, true},
		{DelegationGenerationQueued, DelegationGenerationRunning, true},
		{DelegationGenerationQueued, DelegationGenerationCancelled, true},
		{DelegationGenerationRunning, DelegationGenerationSettled, true},
		{DelegationGenerationRunning, DelegationGenerationFailed, true},
		{DelegationGenerationRunning, DelegationGenerationCancelled, true},
		// Terminal is immutable.
		{DelegationGenerationSettled, DelegationGenerationRunning, false},
		{DelegationGenerationFailed, DelegationGenerationQueued, false},
		{DelegationGenerationCancelled, DelegationGenerationSettled, false},
		// No jumps across active states.
		{DelegationGenerationAwaitingAuthorization, DelegationGenerationRunning, false},
		{DelegationGenerationQueued, DelegationGenerationSettled, false},
	}
	for _, tc := range cases {
		if got := CanTransitionGeneration(tc.from, tc.to); got != tc.ok {
			t.Errorf("CanTransitionGeneration(%s,%s)=%v want %v", tc.from, tc.to, got, tc.ok)
		}
	}
}

func TestAttemptStatusTransitions(t *testing.T) {
	cases := []struct {
		from, to DelegationAttemptStatus
		ok       bool
	}{
		{DelegationAttemptQueued, DelegationAttemptRunning, true},
		{DelegationAttemptQueued, DelegationAttemptCancelled, true},
		{DelegationAttemptRunning, DelegationAttemptSucceeded, true},
		{DelegationAttemptRunning, DelegationAttemptBlocked, true},
		{DelegationAttemptRunning, DelegationAttemptNeedsInput, true},
		{DelegationAttemptRunning, DelegationAttemptNotAuthorized, true},
		{DelegationAttemptRunning, DelegationAttemptFailed, true},
		{DelegationAttemptRunning, DelegationAttemptCancelled, true},
		{DelegationAttemptRunning, DelegationAttemptInterrupted, true},
		// Terminal is immutable, including terminal -> terminal.
		{DelegationAttemptSucceeded, DelegationAttemptFailed, false},
		{DelegationAttemptFailed, DelegationAttemptQueued, false},
		{DelegationAttemptNeedsInput, DelegationAttemptRunning, false},
		// No jumps across active states.
		{DelegationAttemptQueued, DelegationAttemptSucceeded, false},
	}
	for _, tc := range cases {
		if got := CanTransitionAttempt(tc.from, tc.to); got != tc.ok {
			t.Errorf("CanTransitionAttempt(%s,%s)=%v want %v", tc.from, tc.to, got, tc.ok)
		}
	}
}

func TestAttemptRetryEligibility(t *testing.T) {
	eligible := []DelegationAttemptStatus{
		DelegationAttemptFailed, DelegationAttemptCancelled, DelegationAttemptInterrupted,
	}
	ineligible := []DelegationAttemptStatus{
		DelegationAttemptQueued, DelegationAttemptRunning,
		DelegationAttemptSucceeded, DelegationAttemptBlocked, DelegationAttemptNeedsInput,
		DelegationAttemptNotAuthorized,
	}
	for _, status := range eligible {
		if !AttemptRetryEligible(status) {
			t.Errorf("AttemptRetryEligible(%s)=false want true", status)
		}
	}
	for _, status := range ineligible {
		if AttemptRetryEligible(status) {
			t.Errorf("AttemptRetryEligible(%s)=true want false", status)
		}
	}
}

func TestGenerationAndAttemptJSONShape(t *testing.T) {
	// Strict wire shape: no unknown fields, stable keys.
	raw := `{
		"id":"g1","groupId":"grp","generation":1,"kind":"retry","status":"queued",
		"retrySelection":["item-a"],"reusedAttempts":[{"itemId":"item-b","attemptId":"att-2","generation":0,"childRunId":"c2","resultDigest":"sha256:aa"}],
		"authorizationSnapshot":{"roleVersionIds":["v1"]},"budgetSnapshot":{"maxModelCalls":4},
		"clientRequestId":"req-1","createdAt":"2026-08-04T00:00:00Z"
	}`
	var generation DelegationGeneration
	if err := json.Unmarshal([]byte(raw), &generation); err != nil {
		t.Fatalf("decode generation: %v", err)
	}
	if generation.Kind != DelegationGenerationRetry || generation.Status != DelegationGenerationQueued {
		t.Fatalf("unexpected decoded generation: %+v", generation)
	}
	if len(generation.RetrySelection) != 1 || generation.RetrySelection[0] != "item-a" {
		t.Fatalf("unexpected retry selection: %+v", generation.RetrySelection)
	}
	if len(generation.ReusedAttempts) != 1 || generation.ReusedAttempts[0].ChildRunID != "c2" {
		t.Fatalf("unexpected reused attempts: %+v", generation.ReusedAttempts)
	}
	encoded, err := json.Marshal(generation)
	if err != nil {
		t.Fatalf("encode generation: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("decode encoded generation: %v", err)
	}
	for _, key := range []string{"id", "groupId", "generation", "kind", "status", "retrySelection",
		"reusedAttempts", "authorizationSnapshot", "budgetSnapshot", "clientRequestId", "createdAt"} {
		if _, exists := object[key]; !exists {
			t.Errorf("encoded generation missing key %s", key)
		}
	}

	attemptRaw := `{
		"id":"att-1","itemId":"item-a","generation":0,"childRunId":"c1","status":"succeeded",
		"terminal":{"status":"completed","summary":"ok"},"resultDigest":"sha256:bb",
		"actualUsage":{"modelCalls":2,"toolCalls":3,"tokens":1000,"outputTokens":500,"costMicros":50},
		"createdAt":"2026-08-04T00:00:00Z"
	}`
	var attempt DelegationAttempt
	if err := json.Unmarshal([]byte(attemptRaw), &attempt); err != nil {
		t.Fatalf("decode attempt: %v", err)
	}
	if attempt.Terminal == nil || attempt.Terminal.Summary != "ok" {
		t.Fatalf("unexpected terminal: %+v", attempt.Terminal)
	}
	if attempt.ActualUsage.ModelCalls != 2 || attempt.ActualUsage.CostMicros != 50 {
		t.Fatalf("unexpected usage: %+v", attempt.ActualUsage)
	}
}

func TestRetryDelegationInputBounds(t *testing.T) {
	input := RetryDelegationInput{
		ExpectedGeneration: 0,
		ItemIDs:            []string{"a", "b"},
		BudgetOverrides:    map[string]BudgetCeilingJSON{"a": {MaxModelCalls: 8}},
		ClientRequestID:    "req-1",
	}
	if input.ExpectedGeneration != 0 || len(input.ItemIDs) != 2 || input.ClientRequestID == "" {
		t.Fatalf("unexpected input: %+v", input)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("encode retry input: %v", err)
	}
	var back RetryDelegationInput
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("decode retry input: %v", err)
	}
	if back.BudgetOverrides["a"].MaxModelCalls != 8 {
		t.Fatalf("budget override lost: %+v", back.BudgetOverrides)
	}
}

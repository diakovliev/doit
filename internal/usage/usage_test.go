package usage

import (
	"context"
	"testing"
)

func TestByteEstimatorCountsNonEmptyContent(t *testing.T) {
	estCounter := ByteEstimator{}
	count, err := estCounter.Count(context.Background(), []byte("12345"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected two estimated tokens, got %d", count)
	}
}

func TestEstimateAndReconcile(t *testing.T) {
	estEstimate := Estimate([]byte("input"), []byte("output"))
	providerInput := int64(11)
	providerOutput := int64(7)
	providerTotal := int64(18)
	testProvider := FromProvider(&providerInput, &providerOutput, &providerTotal)
	testReconciled := Reconcile(estEstimate, testProvider)

	if testReconciled.Source != SourceProvider || !testReconciled.Exact {
		t.Fatalf("expected exact provider usage, got source=%q exact=%t", testReconciled.Source, testReconciled.Exact)
	}
	if *testReconciled.InputTokens != providerInput || *testReconciled.OutputTokens != providerOutput || *testReconciled.TotalTokens != providerTotal {
		t.Fatalf("provider usage was not reconciled: %+v", testReconciled)
	}
}

func TestAddPreservesUnknownFields(t *testing.T) {
	firstInput := int64(4)
	secondInput := int64(6)
	first := FromProvider(&firstInput, nil, nil)
	second := FromProvider(&secondInput, nil, nil)
	combined := Add(first, second)

	if combined.InputTokens == nil || *combined.InputTokens != 10 {
		t.Fatalf("expected combined input count of 10, got %+v", combined.InputTokens)
	}
	if combined.OutputTokens != nil || combined.TotalTokens != nil {
		t.Fatalf("expected unknown output and total counts, got %+v", combined)
	}
}

func TestSummarizeStepsSeparatesGenerationAndRequestAverages(t *testing.T) {
	firstGeneration := 10.0
	secondGeneration := 30.0
	summary := SummarizeSteps([]StepMetrics{
		{ContextTokens: 100, RequestWallClockTokensPerSecond: 20, OutputTokensPerSecond: &firstGeneration},
		{ContextTokens: 200, RequestWallClockTokensPerSecond: 40, OutputTokensPerSecond: &secondGeneration},
		{ContextTokens: 300, RequestWallClockTokensPerSecond: 100},
	})
	if summary.Steps != 3 || summary.AverageContextTokens != 200 || summary.GenerationMeasuredSteps != 2 || summary.AverageGenerationTokensPerSec == nil || *summary.AverageGenerationTokensPerSec != 20 || summary.AverageRequestTokensPerSecond != 160.0/3.0 {
		t.Fatalf("unexpected metric averages: %+v", summary)
	}
}

func TestByteEstimatorHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (ByteEstimator{}).Count(ctx, []byte("input"))
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

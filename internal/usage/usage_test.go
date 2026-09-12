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

func TestByteEstimatorHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (ByteEstimator{}).Count(ctx, []byte("input"))
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

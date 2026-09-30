// Package usage provides token accounting primitives for model requests.
package usage

import "context"

// StepMetrics records context and generation measurements for one model round.
type StepMetrics struct {
	Round                           int      `json:"round"`
	ContextTokens                   int64    `json:"context_tokens"`
	ContextSource                   Source   `json:"context_source"`
	ProviderInputTokens             *int64   `json:"provider_input_tokens,omitempty"`
	OutputTokens                    int64    `json:"output_tokens"`
	OutputSource                    Source   `json:"output_source"`
	RequestDurationMs               int64    `json:"request_duration_ms"`
	RequestWallClockTokensPerSecond float64  `json:"request_wall_clock_tokens_per_second"`
	GenerationDurationMs            int64    `json:"generation_duration_ms,omitempty"`
	OutputTokensPerSecond           *float64 `json:"output_tokens_per_second,omitempty"`
	LegacyDurationKind              string   `json:"duration_kind,omitempty"`
	LegacyDurationMs                int64    `json:"duration_ms,omitempty"`
}

// StepSummary aggregates per-round context and throughput values for users.
type StepSummary struct {
	Steps                         int      `json:"steps"`
	AverageContextTokens          float64  `json:"average_context_tokens"`
	AverageGenerationTokensPerSec *float64 `json:"average_generation_tokens_per_second,omitempty"`
	GenerationMeasuredSteps       int      `json:"generation_measured_steps"`
	AverageRequestTokensPerSecond float64  `json:"average_request_wall_clock_tokens_per_second"`
}

// SummarizeSteps returns arithmetic per-round averages, excluding unmeasured generation rates.
func SummarizeSteps(steps []StepMetrics) StepSummary {
	if len(steps) == 0 {
		return StepSummary{}
	}
	var contextTotal int64
	var requestRateTotal float64
	var generationRateTotal float64
	requestRateCount := 0
	generationRateCount := 0
	for _, step := range steps {
		contextTotal += step.ContextTokens
		if rate, ok := stepRequestWallRate(step); ok {
			requestRateTotal += rate
			requestRateCount++
		}
		if step.OutputTokensPerSecond != nil && step.LegacyDurationKind != "request_wall_clock" {
			generationRateTotal += *step.OutputTokensPerSecond
			generationRateCount++
		}
	}
	summary := StepSummary{Steps: len(steps), AverageContextTokens: float64(contextTotal) / float64(len(steps)), GenerationMeasuredSteps: generationRateCount}
	if requestRateCount > 0 {
		summary.AverageRequestTokensPerSecond = requestRateTotal / float64(requestRateCount)
	}
	if generationRateCount > 0 {
		generationAverage := generationRateTotal / float64(generationRateCount)
		summary.AverageGenerationTokensPerSec = &generationAverage
	}
	return summary
}

func stepRequestWallRate(step StepMetrics) (float64, bool) {
	if step.RequestWallClockTokensPerSecond > 0 {
		return step.RequestWallClockTokensPerSecond, true
	}
	if step.LegacyDurationKind == "request_wall_clock" && step.OutputTokensPerSecond != nil {
		return *step.OutputTokensPerSecond, true
	}
	return 0, false
}

// Source describes where token counts came from.
type Source string

const (
	SourceProvider Source = "provider"
	SourceEstimate Source = "local-estimate"
	SourceMixed    Source = "mixed"
	SourceUnknown  Source = "unknown"
)

// Counts contains per-request or aggregate token usage.
// A nil value means the count is unknown, not zero.
type Counts struct {
	InputTokens       *int64 `json:"input_tokens"`
	OutputTokens      *int64 `json:"output_tokens"`
	TotalTokens       *int64 `json:"total_tokens"`
	CachedInputTokens *int64 `json:"cached_input_tokens"`
	ReasoningTokens   *int64 `json:"reasoning_tokens"`
	Source            Source `json:"source"`
	Exact             bool   `json:"exact"`
}

// TokenCounter estimates input tokens for content when a provider does not
// return authoritative usage data.
type TokenCounter interface {
	Count(context.Context, []byte) (int64, error)
}

// ByteEstimator implements the deterministic MVP fallback estimator.
type ByteEstimator struct{}

// Count estimates one token per four UTF-8 bytes, with a minimum of one token
// for non-empty content.
func (ByteEstimator) Count(ctx context.Context, content []byte) (int64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	if len(content) == 0 {
		return 0, nil
	}

	return int64((len(content) + 3) / 4), nil
}

// Estimate creates local usage counts for an input and output payload.
func Estimate(input, output []byte) Counts {
	inputTokens := estimateBytes(input)
	outputTokens := estimateBytes(output)
	totalTokens := inputTokens + outputTokens
	return Counts{
		InputTokens:  &inputTokens,
		OutputTokens: &outputTokens,
		TotalTokens:  &totalTokens,
		Source:       SourceEstimate,
		Exact:        false,
	}
}

// FromProvider creates authoritative usage counts. Nil fields remain unknown.
func FromProvider(inputTokens, outputTokens, totalTokens *int64) Counts {
	counts := Counts{
		InputTokens:  clone(inputTokens),
		OutputTokens: clone(outputTokens),
		TotalTokens:  clone(totalTokens),
		Source:       SourceProvider,
		Exact:        true,
	}
	if counts.TotalTokens == nil && counts.InputTokens != nil && counts.OutputTokens != nil {
		total := *counts.InputTokens + *counts.OutputTokens
		counts.TotalTokens = &total
	}
	return counts
}

// Reconcile replaces locally estimated fields with provider values when they
// are available while preserving locally known fields the provider omitted.
func Reconcile(estimated, provider Counts) Counts {
	result := estimated
	result.Source = SourceMixed
	result.Exact = false
	if provider.InputTokens != nil {
		result.InputTokens = clone(provider.InputTokens)
	}
	if provider.OutputTokens != nil {
		result.OutputTokens = clone(provider.OutputTokens)
	}
	if provider.TotalTokens != nil {
		result.TotalTokens = clone(provider.TotalTokens)
	}
	if provider.CachedInputTokens != nil {
		result.CachedInputTokens = clone(provider.CachedInputTokens)
	}
	if provider.ReasoningTokens != nil {
		result.ReasoningTokens = clone(provider.ReasoningTokens)
	}
	if provider.InputTokens != nil && provider.OutputTokens != nil && provider.TotalTokens != nil {
		result.Source = SourceProvider
		result.Exact = provider.Exact
	}
	return result
}

// Add aggregates two usage records. Unknown fields remain unknown.
func Add(first, second Counts) Counts {
	result := Counts{
		InputTokens:       addKnown(first.InputTokens, second.InputTokens),
		OutputTokens:      addKnown(first.OutputTokens, second.OutputTokens),
		TotalTokens:       addKnown(first.TotalTokens, second.TotalTokens),
		CachedInputTokens: addKnown(first.CachedInputTokens, second.CachedInputTokens),
		ReasoningTokens:   addKnown(first.ReasoningTokens, second.ReasoningTokens),
		Source:            sourceForAggregate(first.Source, second.Source),
		Exact:             first.Exact && second.Exact,
	}
	if result.TotalTokens == nil && result.InputTokens != nil && result.OutputTokens != nil {
		total := *result.InputTokens + *result.OutputTokens
		result.TotalTokens = &total
	}
	return result
}

func estimateBytes(content []byte) int64 {
	if len(content) == 0 {
		return 0
	}
	return int64((len(content) + 3) / 4)
}

func clone(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func addKnown(first, second *int64) *int64 {
	if first == nil || second == nil {
		return nil
	}
	total := *first + *second
	return &total
}

func sourceForAggregate(first, second Source) Source {
	if first == "" {
		return second
	}
	if second == "" || first == second {
		return first
	}
	return SourceMixed
}

// Package modelhttp implements the OpenAI Responses-compatible HTTP adapter.
package modelhttp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diakovliev/doit/internal/apperr"
	"github.com/diakovliev/doit/internal/config"
	"github.com/diakovliev/doit/internal/model"
	"github.com/diakovliev/doit/internal/usage"
)

const maxErrorBodyBytes = 16 * 1024

// Options configures transport, token estimation, and rate-limit behavior.
type Options struct {
	HTTPClient   *http.Client
	TokenCounter usage.TokenCounter
	APIKey       string
	RateLimit    *RateLimitPolicy
	OnRetry      func(attempt int, delay time.Duration)
}

// RateLimitPolicy bounds retries and pacing after provider throttling.
type RateLimitPolicy struct {
	MaxRetries        int
	InitialBackoff    time.Duration
	MaxBackoff        time.Duration
	MinInterval       time.Duration
	TokensPerMinute   int64
	RequestsPerMinute int
}

// Client sends normalized model requests to one configured backend profile.
type Client struct {
	profile           config.BackendProfile
	httpClient        *http.Client
	tokenCounter      usage.TokenCounter
	apiKey            string
	rateLimit         RateLimitPolicy
	rateMu            sync.Mutex
	nextRequest       time.Time
	requestTimes      []time.Time
	tokenReservations []tokenReservation
	onRetry           func(int, time.Duration)
}

type tokenReservation struct {
	at     time.Time
	tokens int64
}

// New creates an adapter for an OpenAI-compatible backend profile.
func New(profile config.BackendProfile, options Options) (*Client, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	apiKey := options.APIKey
	if apiKey == "" && profile.APIKeyEnv != "" {
		apiKey = os.Getenv(profile.APIKeyEnv)
		if apiKey == "" {
			return nil, apperr.New(apperr.KindConfig, "modelhttp.auth", "configured API key environment variable is empty")
		}
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	counter := options.TokenCounter
	if counter == nil {
		counter = usage.ByteEstimator{}
	}
	rateLimit := defaultRateLimit(profile.RateLimit)
	if options.RateLimit != nil {
		rateLimit = normalizeRateLimit(*options.RateLimit)
	}
	return &Client{profile: profile, httpClient: httpClient, tokenCounter: counter, apiKey: apiKey, rateLimit: rateLimit, onRetry: options.OnRetry}, nil
}

// Create implements model.ModelClient using POST {api_root}/responses.
func (client *Client) Create(ctx context.Context, request model.Request) (model.Response, error) {
	if err := request.Validate(); err != nil {
		return model.Response{}, err
	}
	wireNames := wireToolNames(request.Tools)
	request = wireRequest(request, wireNames)
	body, err := json.Marshal(request)
	if err != nil {
		return model.Response{}, apperr.Wrap(apperr.KindBackend, "modelhttp.encode", err)
	}
	reservedTokens := int64(request.MaxOutputTokens)
	if estimatedInput, countErr := client.tokenCounter.Count(ctx, body); countErr == nil {
		reservedTokens += estimatedInput
	}
	return client.execute(ctx, body, wireNames, reservedTokens)
}

// CreateStream implements the optional Responses SSE transport.
func (client *Client) CreateStream(ctx context.Context, request model.Request, onEvent func(model.StreamEvent) error) (model.Response, error) {
	if err := request.Validate(); err != nil {
		return model.Response{}, err
	}
	wireNames := wireToolNames(request.Tools)
	request = wireRequest(request, wireNames)
	request.Stream = true
	body, err := json.Marshal(request)
	if err != nil {
		return model.Response{}, apperr.Wrap(apperr.KindBackend, "modelhttp.encode", err)
	}
	reservedTokens := int64(request.MaxOutputTokens)
	if estimatedInput, countErr := client.tokenCounter.Count(ctx, body); countErr == nil {
		reservedTokens += estimatedInput
	}
	return client.executeStream(ctx, body, wireNames, reservedTokens, onEvent)
}

func (client *Client) executeStream(ctx context.Context, body []byte, wireNames map[string]string, reservedTokens int64, onEvent func(model.StreamEvent) error) (model.Response, error) {
	response, err := client.executeStreamOnce(ctx, body, reservedTokens, onEvent)
	if err != nil {
		return model.Response{}, err
	}
	return client.normalizeStreamResponse(ctx, body, response.body, response.streamedText, response.headers, response.clientRequestID, reverseNames(wireNames))
}

func (client *Client) executeStreamOnce(ctx context.Context, body []byte, reservedTokens int64, onEvent func(model.StreamEvent) error) (httpResult, error) {
	if err := client.waitForRateSlot(ctx, reservedTokens); err != nil {
		return httpResult{}, err
	}
	httpResponse, clientRequestID, err := client.doStreamRequest(ctx, body)
	if err != nil {
		return httpResult{}, err
	}
	defer func() { _ = httpResponse.Body.Close() }()
	return client.readStreamResponse(ctx, httpResponse, clientRequestID, onEvent)
}

func (client *Client) doStreamRequest(ctx context.Context, body []byte) (*http.Response, string, error) {
	httpRequest, clientRequestID, err := client.newStreamRequest(ctx, body)
	if err != nil {
		return nil, "", apperr.Wrap(apperr.KindBackend, "modelhttp.request", err)
	}
	httpResponse, err := client.httpClient.Do(httpRequest)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, "", ctx.Err()
		}
		return nil, "", apperr.Wrap(apperr.KindBackend, "modelhttp.request", err)
	}
	return httpResponse, clientRequestID, nil
}

func (client *Client) readStreamResponse(ctx context.Context, httpResponse *http.Response, clientRequestID string, onEvent func(model.StreamEvent) error) (httpResult, error) {
	if httpResponse.StatusCode < http.StatusOK || httpResponse.StatusCode >= http.StatusMultipleChoices {
		return httpResult{}, readStreamBackendError(httpResponse)
	}
	stream, err := client.readStream(httpResponse.Body, onEvent)
	if err != nil {
		if ctx.Err() != nil {
			return httpResult{}, ctx.Err()
		}
		return httpResult{}, err
	}
	if len(stream.body) == 0 {
		stream.body = []byte(`{"id":"","status":"completed","output":[]}`)
	}
	return httpResult{statusCode: httpResponse.StatusCode, headers: httpResponse.Header, body: stream.body, streamedText: stream.text, clientRequestID: clientRequestID}, nil
}

func (client *Client) newStreamRequest(ctx context.Context, body []byte) (*http.Request, string, error) {
	requestURL := strings.TrimRight(client.profile.APIRoot, "/") + "/responses"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	clientRequestID := requestID()
	httpRequest.Header.Set("X-Client-Request-Id", clientRequestID)
	if client.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+client.apiKey)
	}
	for name, value := range client.profile.Headers {
		httpRequest.Header.Set(name, value)
	}
	return httpRequest, clientRequestID, nil
}

func readStreamBackendError(response *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes*16))
	if err != nil {
		return apperr.Wrap(apperr.KindBackend, "modelhttp.read", err)
	}
	return backendError(response.StatusCode, body)
}

type streamReadResult struct {
	body []byte
	text string
}

func (client *Client) readStream(reader io.Reader, onEvent func(model.StreamEvent) error) (streamReadResult, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxErrorBodyBytes*64)
	dataLines := make([]string, 0, 1)
	result := streamReadResult{}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}
		if line == "" {
			if err := client.processStreamData(dataLines, onEvent, &result); err != nil {
				return streamReadResult{}, err
			}
			dataLines = dataLines[:0]
		}
	}
	if err := scanner.Err(); err != nil {
		return streamReadResult{}, apperr.Wrap(apperr.KindBackend, "modelhttp.stream", err)
	}
	if err := client.processStreamData(dataLines, onEvent, &result); err != nil {
		return streamReadResult{}, err
	}
	return result, nil
}

func (client *Client) processStreamData(dataLines []string, onEvent func(model.StreamEvent) error, result *streamReadResult) error {
	if len(dataLines) == 0 {
		return nil
	}
	data := strings.Join(dataLines, "\n")
	if data == "[DONE]" {
		return nil
	}
	var event streamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return apperr.Wrap(apperr.KindBackend, "modelhttp.stream", err)
	}
	if event.Delta != "" {
		result.text += event.Delta
		if onEvent != nil {
			if err := onEvent(model.StreamEvent{Type: event.Type, Text: event.Delta}); err != nil {
				return err
			}
		}
	}
	if isTerminalStreamEvent(event.Type) && len(event.Response) > 0 {
		result.body = append([]byte(nil), event.Response...)
	}
	return nil
}

type streamEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta"`
	Response json.RawMessage `json:"response"`
}

func isTerminalStreamEvent(eventType string) bool {
	switch eventType {
	case "response.completed", "response.failed", "response.incomplete", "response.cancelled":
		return true
	default:
		return false
	}
}

func (client *Client) normalizeStreamResponse(ctx context.Context, requestBody, responseBody []byte, streamedText string, headers http.Header, clientRequestID string, localNames map[string]string) (model.Response, error) {
	responseBody, err := appendStreamText(responseBody, streamedText)
	if err != nil {
		return model.Response{}, apperr.Wrap(apperr.KindBackend, "modelhttp.stream", err)
	}
	return client.normalizeResponse(ctx, requestBody, responseBody, headers, clientRequestID, localNames)
}

func appendStreamText(responseBody []byte, streamedText string) ([]byte, error) {
	if streamedText == "" {
		return responseBody, nil
	}
	var wire wireResponse
	if err := json.Unmarshal(responseBody, &wire); err != nil {
		return nil, err
	}
	for _, rawItem := range wire.Output {
		var item wireOutputItem
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return nil, err
		}
		if item.Type == "message" && outputText(item.Content) != "" {
			return responseBody, nil
		}
	}
	message, err := json.Marshal(wireOutputItem{Type: "message", Content: []wireContentPart{{Type: "output_text", Text: streamedText}}})
	if err != nil {
		return nil, err
	}
	wire.Output = append(wire.Output, message)
	return json.Marshal(wire)
}

func (client *Client) execute(ctx context.Context, body []byte, wireNames map[string]string, reservedTokens int64) (model.Response, error) {
	for attempt := 0; ; attempt++ {
		response, err := client.executeOnce(ctx, body, reservedTokens)
		if err != nil {
			return model.Response{}, err
		}
		if response.statusCode != http.StatusTooManyRequests {
			if response.statusCode < http.StatusOK || response.statusCode >= http.StatusMultipleChoices {
				return model.Response{}, backendError(response.statusCode, response.body)
			}
			return client.normalizeResponse(ctx, body, response.body, response.headers, response.clientRequestID, reverseNames(wireNames))
		}
		if attempt >= client.rateLimit.MaxRetries {
			return model.Response{}, backendError(response.statusCode, response.body)
		}
		delay := client.retryDelay(attempt, response.headers.Get("Retry-After"))
		if client.onRetry != nil {
			client.onRetry(attempt+1, delay)
		}
		if err := waitContext(ctx, delay); err != nil {
			return model.Response{}, err
		}
	}
}

type httpResult struct {
	statusCode      int
	headers         http.Header
	body            []byte
	streamedText    string
	clientRequestID string
}

func (client *Client) executeOnce(ctx context.Context, body []byte, reservedTokens int64) (httpResult, error) {
	if err := client.waitForRateSlot(ctx, reservedTokens); err != nil {
		return httpResult{}, err
	}
	requestURL := strings.TrimRight(client.profile.APIRoot, "/") + "/responses"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return httpResult{}, apperr.Wrap(apperr.KindBackend, "modelhttp.request", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	clientRequestID := requestID()
	httpRequest.Header.Set("X-Client-Request-Id", clientRequestID)
	if client.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+client.apiKey)
	}
	for name, value := range client.profile.Headers {
		httpRequest.Header.Set(name, value)
	}
	httpResponse, err := client.httpClient.Do(httpRequest)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return httpResult{}, ctx.Err()
		}
		return httpResult{}, apperr.Wrap(apperr.KindBackend, "modelhttp.request", err)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxErrorBodyBytes*16))
	if err != nil {
		return httpResult{}, apperr.Wrap(apperr.KindBackend, "modelhttp.read", err)
	}
	return httpResult{statusCode: httpResponse.StatusCode, headers: httpResponse.Header, body: responseBody, clientRequestID: clientRequestID}, nil
}

func defaultRateLimit(configuration config.RateLimitConfig) RateLimitPolicy {
	policy := RateLimitPolicy{MaxRetries: 3, InitialBackoff: 500 * time.Millisecond, MaxBackoff: 30 * time.Second}
	if configuration.MaxRetries > 0 {
		policy.MaxRetries = configuration.MaxRetries
	}
	if configuration.InitialBackoffMs > 0 {
		policy.InitialBackoff = time.Duration(configuration.InitialBackoffMs) * time.Millisecond
	}
	if configuration.MaxBackoffMs > 0 {
		policy.MaxBackoff = time.Duration(configuration.MaxBackoffMs) * time.Millisecond
	}
	if configuration.MinIntervalMs > 0 {
		policy.MinInterval = time.Duration(configuration.MinIntervalMs) * time.Millisecond
	}
	if configuration.TokensPerMinute > 0 {
		policy.TokensPerMinute = int64(configuration.TokensPerMinute)
	}
	if configuration.RequestsPerMinute > 0 {
		policy.RequestsPerMinute = configuration.RequestsPerMinute
	}
	return normalizeRateLimit(policy)
}

func normalizeRateLimit(policy RateLimitPolicy) RateLimitPolicy {
	if policy.MaxRetries < 0 {
		policy.MaxRetries = 0
	}
	if policy.InitialBackoff <= 0 {
		policy.InitialBackoff = 500 * time.Millisecond
	}
	if policy.MaxBackoff <= 0 {
		policy.MaxBackoff = 30 * time.Second
	}
	if policy.MaxBackoff < policy.InitialBackoff {
		policy.MaxBackoff = policy.InitialBackoff
	}
	if policy.MinInterval < 0 {
		policy.MinInterval = 0
	}
	return policy
}

func (client *Client) waitForRateSlot(ctx context.Context, tokens int64) error {
	for {
		delay, reserved := client.reserveRateSlot(tokens)
		if reserved {
			return nil
		}
		if err := waitContext(ctx, delay); err != nil {
			return err
		}
	}
}

func (client *Client) reserveRateSlot(tokens int64) (time.Duration, bool) {
	now := time.Now()
	client.rateMu.Lock()
	defer client.rateMu.Unlock()
	client.pruneRateWindow(now)
	start := client.nextRequest
	if !start.After(now) {
		start = now
	}
	if client.rateLimit.RequestsPerMinute > 0 && len(client.requestTimes) >= client.rateLimit.RequestsPerMinute {
		start = laterTime(start, client.requestTimes[0].Add(time.Minute))
	}
	if client.rateLimit.TokensPerMinute > 0 && tokens > 0 && client.reservedTokens()+tokens > client.rateLimit.TokensPerMinute && len(client.tokenReservations) > 0 {
		start = laterTime(start, client.tokenReservations[0].at.Add(time.Minute))
	}
	if delay := start.Sub(now); delay > 0 {
		return delay, false
	}
	client.requestTimes = append(client.requestTimes, now)
	if tokens > 0 {
		client.tokenReservations = append(client.tokenReservations, tokenReservation{at: now, tokens: tokens})
	}
	client.nextRequest = now.Add(client.rateLimit.MinInterval)
	return 0, true
}

func (client *Client) pruneRateWindow(now time.Time) {
	cutoff := now.Add(-time.Minute)
	firstRequest := 0
	for firstRequest < len(client.requestTimes) && client.requestTimes[firstRequest].Before(cutoff) {
		firstRequest++
	}
	client.requestTimes = client.requestTimes[firstRequest:]
	firstReservation := 0
	for firstReservation < len(client.tokenReservations) && client.tokenReservations[firstReservation].at.Before(cutoff) {
		firstReservation++
	}
	client.tokenReservations = client.tokenReservations[firstReservation:]
}

func (client *Client) reservedTokens() int64 {
	var total int64
	for _, reservation := range client.tokenReservations {
		total += reservation.tokens
	}
	return total
}

func laterTime(first, second time.Time) time.Time {
	if second.After(first) {
		return second
	}
	return first
}

func (client *Client) retryDelay(attempt int, retryAfter string) time.Duration {
	if parsed := parseRetryAfter(retryAfter); parsed > 0 {
		if parsed > client.rateLimit.MaxBackoff {
			return client.rateLimit.MaxBackoff
		}
		return parsed
	}
	multiplier := 1 << minInt(attempt, 10)
	delay := client.rateLimit.InitialBackoff * time.Duration(multiplier)
	if delay > client.rateLimit.MaxBackoff {
		delay = client.rateLimit.MaxBackoff
	}
	var jitter [1]byte
	if _, err := rand.Read(jitter[:]); err == nil {
		delay += delay * time.Duration(jitter[0]%26) / 100
	}
	if delay > client.rateLimit.MaxBackoff {
		return client.rateLimit.MaxBackoff
	}
	return delay
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if timestamp, err := http.ParseTime(value); err == nil {
		if delay := time.Until(timestamp); delay > 0 {
			return delay
		}
	}
	return 0
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func minInt(first, second int) int {
	if first < second {
		return first
	}
	return second
}

type wireResponse struct {
	ID     string            `json:"id"`
	Status string            `json:"status"`
	Output []json.RawMessage `json:"output"`
	Usage  *wireUsage        `json:"usage"`
}

type wireUsage struct {
	InputTokens   int64             `json:"input_tokens"`
	OutputTokens  int64             `json:"output_tokens"`
	TotalTokens   int64             `json:"total_tokens"`
	InputDetails  wireInputDetails  `json:"input_tokens_details"`
	OutputDetails wireOutputDetails `json:"output_tokens_details"`
}

type wireInputDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}
type wireOutputDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}
type wireOutputItem struct {
	Type      string            `json:"type"`
	CallID    string            `json:"call_id"`
	Name      string            `json:"name"`
	Arguments string            `json:"arguments"`
	Content   []wireContentPart `json:"content"`
}
type wireContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (client *Client) normalizeResponse(ctx context.Context, requestBody, responseBody []byte, headers http.Header, clientRequestID string, localNames map[string]string) (model.Response, error) {
	var wire wireResponse
	if err := json.Unmarshal(responseBody, &wire); err != nil {
		return model.Response{}, apperr.Wrap(apperr.KindBackend, "modelhttp.decode", err)
	}
	response := model.Response{ID: wire.ID, Status: wire.Status, RequestID: clientRequestID, ProviderRequestID: providerRequestID(headers)}
	for _, rawItem := range wire.Output {
		var item wireOutputItem
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return model.Response{}, apperr.Wrap(apperr.KindBackend, "modelhttp.output", err)
		}
		switch item.Type {
		case "message":
			response.Text += outputText(item.Content)
		case "function_call":
			name := item.Name
			if localName, exists := localNames[name]; exists {
				name = localName
			}
			response.ToolCalls = append(response.ToolCalls, model.ToolCall{CallID: item.CallID, Name: name, Arguments: item.Arguments})
		}
	}
	response.Usage = client.estimateUsage(ctx, requestBody, []byte(response.Text))
	if wire.Usage != nil {
		input := wire.Usage.InputTokens
		output := wire.Usage.OutputTokens
		total := wire.Usage.TotalTokens
		provider := usage.FromProvider(&input, &output, &total)
		cached := wire.Usage.InputDetails.CachedTokens
		reasoning := wire.Usage.OutputDetails.ReasoningTokens
		provider.CachedInputTokens = &cached
		provider.ReasoningTokens = &reasoning
		response.Usage = usage.Reconcile(response.Usage, provider)
	}
	return response, nil
}

func providerRequestID(headers http.Header) string {
	if value := headers.Get("X-Request-Id"); value != "" {
		return value
	}
	return headers.Get("Request-Id")
}

func (client *Client) estimateUsage(ctx context.Context, requestBody, output []byte) usage.Counts {
	inputTokens, inputErr := client.tokenCounter.Count(ctx, requestBody)
	outputTokens, outputErr := client.tokenCounter.Count(ctx, output)
	if inputErr != nil || outputErr != nil {
		return usage.Counts{Source: usage.SourceUnknown, Exact: false}
	}
	totalTokens := inputTokens + outputTokens
	return usage.Counts{InputTokens: &inputTokens, OutputTokens: &outputTokens, TotalTokens: &totalTokens, Source: usage.SourceEstimate, Exact: false}
}

func outputText(parts []wireContentPart) string {
	var builder strings.Builder
	for _, part := range parts {
		if part.Type == "output_text" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

func backendError(statusCode int, body []byte) error {
	message := strings.TrimSpace(string(body))
	if len(message) > maxErrorBodyBytes {
		message = message[:maxErrorBodyBytes]
	}
	kind := apperr.KindBackend
	if statusCode == http.StatusTooManyRequests {
		kind = apperr.KindRateLimit
	}
	return apperr.Wrap(kind, "modelhttp.response", fmt.Errorf("HTTP %d: %s", statusCode, message))
}

func requestID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("doit-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value)
}

var _ model.ModelClient = (*Client)(nil)
var _ model.StreamingModelClient = (*Client)(nil)

func wireRequest(request model.Request, names map[string]string) model.Request {
	request.Tools = append([]model.ToolDefinition(nil), request.Tools...)
	request.Input = append([]model.InputItem(nil), request.Input...)
	for index := range request.Tools {
		request.Tools[index].Name = names[request.Tools[index].Name]
	}
	for index := range request.Input {
		if request.Input[index].Name != "" {
			request.Input[index].Name = safeToolName(request.Input[index].Name)
		}
	}
	return request
}

func wireToolNames(definitions []model.ToolDefinition) map[string]string {
	names := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		names[definition.Name] = safeToolName(definition.Name)
	}
	return names
}

func reverseNames(names map[string]string) map[string]string {
	reversed := make(map[string]string, len(names))
	for localName, wireName := range names {
		reversed[wireName] = localName
	}
	return reversed
}

func safeToolName(name string) string {
	var builder strings.Builder
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' {
			builder.WriteRune(character)
			continue
		}
		builder.WriteByte('_')
	}
	return builder.String()
}

package modelhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/diakovliev/doit/internal/apperr"
	"github.com/diakovliev/doit/internal/config"
	"github.com/diakovliev/doit/internal/model"
	"github.com/diakovliev/doit/internal/usage"
)

func TestClientNormalizesTextFunctionCallsAndProviderUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(normalizedResponseHandler))
	defer server.Close()
	client, err := New(config.BackendProfile{APIRoot: server.URL + "/v1", Model: "test-model", APIKeyEnv: "TEST_DOIT_KEY"}, Options{APIKey: "test-key", TokenCounter: usage.ByteEstimator{}})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	response, err := client.Create(context.Background(), model.Request{Model: "test-model", Input: []model.InputItem{{Type: "message", Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatalf("create response: %v", err)
	}
	requireNormalizedResponse(t, response)
	requireProviderUsage(t, response.Usage)
}

func TestClientUsesConfiguredRequestTimeout(t *testing.T) {
	client, err := New(config.BackendProfile{APIRoot: "https://example.test/v1", Model: "test-model"}, Options{Timeout: 17 * time.Minute})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if client.httpClient.Timeout != 17*time.Minute {
		t.Fatalf("unexpected request timeout: %s", client.httpClient.Timeout)
	}
}

func TestClientSendsConfiguredRequestParameters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		var reasoning struct {
			Effort string `json:"effort"`
		}
		if err := json.Unmarshal(payload["reasoning"], &reasoning); err != nil || reasoning.Effort != "high" {
			http.Error(writer, "reasoning parameter missing", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"parameter-response","status":"completed","output":[]}`))
	}))
	defer server.Close()
	client, err := New(config.BackendProfile{
		APIRoot: server.URL,
		Model:   "test-model",
		RequestParameters: map[string]json.RawMessage{
			"reasoning": json.RawMessage(`{"effort":"high"}`),
		},
	}, Options{TokenCounter: usage.ByteEstimator{}})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.Create(context.Background(), model.Request{Model: "test-model"}); err != nil {
		t.Fatalf("create parameterized response: %v", err)
	}
}

func TestClientStreamsTextAndNormalizesTerminalResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Accept") != "text/event-stream" {
			http.Error(writer, "streaming was not requested", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hel\"}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"lo\"}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"stream-1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello\"}]}]}}\n\n"))
	}))
	defer server.Close()
	client, err := New(config.BackendProfile{APIRoot: server.URL + "/v1", Model: "test-model"}, Options{TokenCounter: usage.ByteEstimator{}})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	var streamed strings.Builder
	response, err := client.CreateStream(context.Background(), model.Request{Model: "test-model"}, func(event model.StreamEvent) error {
		streamed.WriteString(event.Text)
		return nil
	})
	if err != nil || response.ID != "stream-1" || response.Text != "Hello" || streamed.String() != "Hello" {
		t.Fatalf("unexpected streamed response: %+v streamed=%q error=%v", response, streamed.String(), err)
	}
}

func TestClientPreservesStreamedTextWhenTerminalOutputIsOmitted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"stream-2\",\"status\":\"completed\",\"output\":[]}}\n\n"))
	}))
	defer server.Close()
	client, err := New(config.BackendProfile{APIRoot: server.URL, Model: "test-model"}, Options{TokenCounter: usage.ByteEstimator{}})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	response, err := client.CreateStream(context.Background(), model.Request{Model: "test-model"}, nil)
	if err != nil || response.ID != "stream-2" || response.Text != "partial" {
		t.Fatalf("streamed text was lost: response=%+v error=%v", response, err)
	}
}

func TestClientReturnsContextCancellationDuringStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Error("streaming test server does not support flushing")
			return
		}
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"))
		flusher.Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	client, err := New(config.BackendProfile{APIRoot: server.URL, Model: "test-model"}, Options{TokenCounter: usage.ByteEstimator{}})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = client.CreateStream(ctx, model.Request{Model: "test-model"}, func(model.StreamEvent) error {
		cancel()
		return nil
	})
	if !strings.Contains(errString(err), "context canceled") {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func requireNormalizedResponse(t *testing.T, response model.Response) {
	t.Helper()
	if response.ID != "resp-1" || response.Text != "hello" || len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "fs.read" || response.RequestID == "" || response.ProviderRequestID != "provider-1" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func requireProviderUsage(t *testing.T, value usage.Counts) {
	t.Helper()
	if value.Source != usage.SourceProvider || !value.Exact || *value.InputTokens != 11 || *value.TotalTokens != 18 {
		t.Fatalf("unexpected usage: %+v", value)
	}
}

func normalizedResponseHandler(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v1/responses" || request.Header.Get("Authorization") != "Bearer test-key" || request.Header.Get("X-Client-Request-Id") == "" {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	var received model.Request
	if err := json.NewDecoder(request.Body).Decode(&received); err != nil || received.Model != "test-model" {
		http.Error(writer, "invalid JSON", http.StatusBadRequest)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("X-Request-Id", "provider-1")
	_, _ = writer.Write([]byte(`{"id":"resp-1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]},{"type":"function_call","call_id":"call-1","name":"fs.read","arguments":"{\"path\":\"README.md\"}"}],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18,"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":3}}}`))
}

func TestClientMapsBackendErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()
	client, err := New(config.BackendProfile{APIRoot: server.URL, Model: "test-model"}, Options{RateLimit: &RateLimitPolicy{MaxRetries: 0}})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.Create(context.Background(), model.Request{Model: "test-model"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("expected backend error, got %v", err)
	}
}

func TestClientRetriesRateLimitUsingRetryAfter(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			writer.Header().Set("Retry-After", "0")
			http.Error(writer, "rate limited", http.StatusTooManyRequests)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"retry-ok","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer server.Close()
	client, err := New(config.BackendProfile{APIRoot: server.URL, Model: "test-model"}, Options{RateLimit: &RateLimitPolicy{MaxRetries: 1, InitialBackoff: time.Millisecond, MaxBackoff: 10 * time.Millisecond}})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	response, err := client.Create(context.Background(), model.Request{Model: "test-model"})
	if err != nil || response.ID != "retry-ok" || attempts != 2 {
		t.Fatalf("expected one retry and success: response=%+v attempts=%d error=%v", response, attempts, err)
	}
}

func TestClientClassifiesExhaustedRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()
	client, err := New(config.BackendProfile{APIRoot: server.URL, Model: "test-model"}, Options{RateLimit: &RateLimitPolicy{MaxRetries: 0}})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.Create(context.Background(), model.Request{Model: "test-model"})
	if apperr.KindOf(err) != apperr.KindRateLimit {
		t.Fatalf("expected rate-limit error, got %v", err)
	}
}

func TestWireToolNamesAreProviderSafeAndRoundTrip(t *testing.T) {
	names := wireToolNames([]model.ToolDefinition{{Name: "fs.read"}, {Name: "code.apply_patch"}})
	if names["fs.read"] != "fs_read" || names["code.apply_patch"] != "code_apply_patch" {
		t.Fatalf("unexpected wire names: %+v", names)
	}
	reversed := reverseNames(names)
	if reversed["fs_read"] != "fs.read" || reversed["code_apply_patch"] != "code.apply_patch" {
		t.Fatalf("unexpected reverse mapping: %+v", reversed)
	}
}

func TestWireRequestSanitizesLocalFunctionCallOnFollowUp(t *testing.T) {
	request := model.Request{
		Model: "test-model",
		Tools: []model.ToolDefinition{{Type: "function", Name: "fs_read"}},
		Input: []model.InputItem{{Type: "function_call", Name: "fs.read", CallID: "call-1"}},
	}
	wireNames := wireToolNames(request.Tools)
	serialized, err := json.Marshal(wireRequest(request, wireNames))
	if err != nil {
		t.Fatalf("marshal follow-up request: %v", err)
	}
	if strings.Contains(string(serialized), `"name":"fs.read"`) {
		t.Fatalf("follow-up request contains dotted tool name: %s", serialized)
	}
	if !strings.Contains(string(serialized), `"name":"fs_read"`) {
		t.Fatalf("follow-up request lost safe tool name: %s", serialized)
	}
	if request.Tools[0].Name != "fs_read" {
		t.Fatalf("expected original request to remain safe in this fixture, got %q", request.Tools[0].Name)
	}
}

func TestWireRequestDoesNotMutateLocalToolDefinitions(t *testing.T) {
	request := model.Request{Tools: []model.ToolDefinition{{Type: "function", Name: "fs.read"}}}
	wireRequest(request, wireToolNames(request.Tools))
	if request.Tools[0].Name != "fs.read" {
		t.Fatalf("wire serialization mutated local tool name to %q", request.Tools[0].Name)
	}
}

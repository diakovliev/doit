// Package model defines provider-neutral model contracts.
package model

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/diakovliev/doit/internal/apperr"
	"github.com/diakovliev/doit/internal/usage"
)

// InputItem is a normalized Responses API input item.
type InputItem struct {
	Type      string `json:"type"`
	Role      string `json:"role,omitempty"`
	Content   string `json:"content,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
}

// ToolDefinition describes a function the model may request.
type ToolDefinition struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Request is the provider-neutral model request contract.
type Request struct {
	Model           string           `json:"model"`
	Instructions    string           `json:"instructions,omitempty"`
	Input           []InputItem      `json:"input,omitempty"`
	Tools           []ToolDefinition `json:"tools,omitempty"`
	ToolChoice      string           `json:"tool_choice,omitempty"`
	MaxOutputTokens int              `json:"max_output_tokens,omitempty"`
}

// ToolCall is a normalized function call returned by a model.
type ToolCall struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Response is the provider-neutral model response contract.
type Response struct {
	ID                string       `json:"id,omitempty"`
	Status            string       `json:"status"`
	Text              string       `json:"text,omitempty"`
	ToolCalls         []ToolCall   `json:"tool_calls,omitempty"`
	Usage             usage.Counts `json:"usage"`
	RequestID         string       `json:"request_id,omitempty"`
	ProviderRequestID string       `json:"provider_request_id,omitempty"`
}

// ModelClient creates normalized model responses.
type ModelClient interface {
	Create(context.Context, Request) (Response, error)
}

// Validate checks the fields required before a request reaches a backend.
func (request Request) Validate() error {
	if request.Model == "" {
		return apperr.New(apperr.KindConfig, "model.request", "model is required")
	}
	for index, tool := range request.Tools {
		if tool.Type == "" || tool.Name == "" {
			return apperr.New(apperr.KindUsage, "model.request", "tool type and name are required at index "+strconv.Itoa(index))
		}
	}
	return nil
}

// Package apperr defines categorized application errors shared by the CLI.
package apperr

import (
	"errors"
	"fmt"
)

// Kind identifies the boundary that produced an application error.
type Kind string

const (
	KindUsage     Kind = "usage"
	KindConfig    Kind = "config"
	KindBackend   Kind = "backend"
	KindRateLimit Kind = "rate-limit"
	KindTool      Kind = "tool"
	KindPolicy    Kind = "policy"
	KindCancelled Kind = "cancelled"
	KindInternal  Kind = "internal"
	KindUnknown   Kind = "unknown"
)

// Error carries a stable category and operation while preserving the cause.
type Error struct {
	Kind      Kind
	Operation string
	Message   string
	Cause     error
}

// Error implements the error interface.
func (applicationError *Error) Error() string {
	if applicationError == nil {
		return ""
	}

	message := applicationError.Message
	if message == "" && applicationError.Cause != nil {
		message = applicationError.Cause.Error()
	}
	if applicationError.Operation == "" {
		return message
	}
	if message == "" {
		return applicationError.Operation
	}
	return fmt.Sprintf("%s: %s", applicationError.Operation, message)
}

// Unwrap exposes the underlying cause to errors.Is and errors.As.
func (applicationError *Error) Unwrap() error {
	if applicationError == nil {
		return nil
	}
	return applicationError.Cause
}

// New creates a categorized application error with a human-readable message.
func New(kind Kind, operation, message string) *Error {
	return &Error{
		Kind:      kind,
		Operation: operation,
		Message:   message,
	}
}

// Wrap creates a categorized application error around an existing cause.
func Wrap(kind Kind, operation string, cause error) *Error {
	return &Error{
		Kind:      kind,
		Operation: operation,
		Cause:     cause,
	}
}

// KindOf returns the category carried by err, or KindUnknown when unavailable.
func KindOf(err error) Kind {
	if err == nil {
		return KindUnknown
	}

	var applicationError *Error
	if errors.As(err, &applicationError) && applicationError.Kind != "" {
		return applicationError.Kind
	}
	return KindUnknown
}

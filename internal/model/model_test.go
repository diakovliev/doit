package model

import "testing"

func TestRequestValidationRequiresModel(t *testing.T) {
	if err := (Request{}).Validate(); err == nil {
		t.Fatal("expected missing model to fail validation")
	}
}

func TestRequestValidationAcceptsTextRequest(t *testing.T) {
	request := Request{Model: "fake-model", Input: []InputItem{{Type: "message", Role: "user", Content: "hello"}}}
	if err := request.Validate(); err != nil {
		t.Fatalf("expected valid request, got %v", err)
	}
}

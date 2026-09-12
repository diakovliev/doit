package session

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEventRoundTripsAsJSON(t *testing.T) {
	event := Event{
		Sequence:  1,
		Timestamp: time.Unix(10, 0).UTC(),
		Type:      "lifecycle",
		Data:      json.RawMessage(`{"status":"active"}`),
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	var decoded Event
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if decoded.Sequence != event.Sequence || decoded.Type != event.Type || string(decoded.Data) != string(event.Data) {
		t.Fatalf("event changed during round trip: %+v", decoded)
	}
}

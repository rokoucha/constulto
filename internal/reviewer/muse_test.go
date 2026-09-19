package reviewer

import (
	"errors"
	"testing"
)

func TestParseMuseTerminalReview(t *testing.T) {
	raw := []byte(`{"payload_type":"run.output.delta","payload":{"text":"ignored"}}
{"payload_type":"run.terminal.completed","payload":{"terminal":"completed","text":"{\"summary\":\"ok\",\"findings\":[],\"decisions\":[],\"scope\":{\"reviewed\":[],\"notReviewed\":[],\"testsNotRun\":[]}}","reason":null}}
`)
	review, err := parseMuse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if review.Summary != "ok" {
		t.Fatalf("unexpected review: %#v", review)
	}
}

func TestParseMuseRejectsMalformedAndFailedTerminal(t *testing.T) {
	if _, err := parseMuse([]byte("not-json\n")); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("expected invalid output, got %v", err)
	}
	failed := []byte(`{"payload_type":"run.terminal.failed","payload":{"terminal":"failed","reason":null}}`)
	if _, err := parseMuse(failed); !errors.Is(err, ErrAgentFailed) {
		t.Fatalf("expected agent failure, got %v", err)
	}
}

func TestParseMuseRequiresTerminalResult(t *testing.T) {
	if _, err := parseMuse([]byte(`{"payload_type":"run.output.delta","payload":{"text":"x"}}`)); err == nil {
		t.Fatal("expected error")
	}
}

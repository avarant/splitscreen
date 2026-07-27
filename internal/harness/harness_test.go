package harness

import (
	"context"
	"strings"
	"testing"
)

type stubAdapter struct{ name string }

func (s *stubAdapter) Name() string                 { return s.name }
func (s *stubAdapter) DefaultCredentialEnv() string { return "STUB_KEY" }
func (s *stubAdapter) PermissionToolName() string   { return "mcp__stub__permission_prompt" }
func (s *stubAdapter) Start(context.Context, SessionConfig) (Session, error) {
	return nil, nil
}

func TestRegistry(t *testing.T) {
	Register(&stubAdapter{name: "stub-one"})

	got, err := Get("stub-one")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name() != "stub-one" {
		t.Fatalf("name = %q", got.Name())
	}

	var found bool
	for _, n := range Names() {
		if n == "stub-one" {
			found = true
		}
	}
	if !found {
		t.Errorf("Names() = %v, missing the registered adapter", Names())
	}
}

// An unknown harness must fail at startup with a message that says what is
// available, rather than failing later with an opaque nil.
func TestGetUnknownNamesWhatExists(t *testing.T) {
	Register(&stubAdapter{name: "stub-two"})

	_, err := Get("nonexistent")
	if err == nil {
		t.Fatal("an unknown adapter was returned")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error %q should name what was asked for", err)
	}
	if !strings.Contains(err.Error(), "stub-two") {
		t.Errorf("error %q should list what is available", err)
	}
}

// Usage must distinguish "no numbers" from "the numbers are zero"; the zero
// value of the struct is the unknown case, so an adapter that forgets to fill
// it in fails safe.
func TestUsageZeroValueIsUnknown(t *testing.T) {
	var u Usage
	if u.Known {
		t.Fatal("the zero value claims to have known usage")
	}
	if u.InputTokens != 0 || u.OutputTokens != 0 {
		t.Fatal("the zero value carries counters")
	}
}

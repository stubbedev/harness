package event

import "testing"

func TestSetNonInteractive(t *testing.T) {
	originalNonInteractive := baseProps[nonInteractiveAttrName]
	originalNonInteractiveNested := baseProps[nonInteractiveNestedAttrName]
	t.Cleanup(func() {
		baseProps = baseProps.
			Set(nonInteractiveAttrName, originalNonInteractive).
			Set(nonInteractiveNestedAttrName, originalNonInteractiveNested)
	})

	tests := []struct {
		name                     string
		nonInteractive           bool
		harness                  string
		wantNonInteractiveNested bool
	}{
		{
			name: "interactive direct invocation",
		},
		{
			name:           "non-interactive direct invocation",
			nonInteractive: true,
		},
		{
			name:    "interactive nested invocation",
			harness: "1",
		},
		{
			name:                     "non-interactive nested invocation",
			nonInteractive:           true,
			harness:                  "1",
			wantNonInteractiveNested: true,
		},
		{
			name:           "non-interactive invocation with unrecognized marker",
			nonInteractive: true,
			harness:        "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HARNESS", tt.harness)
			SetNonInteractive(tt.nonInteractive)

			if got := baseProps[nonInteractiveAttrName]; got != tt.nonInteractive {
				t.Errorf("%s = %v, want %v", nonInteractiveAttrName, got, tt.nonInteractive)
			}
			if got := baseProps[nonInteractiveNestedAttrName]; got != tt.wantNonInteractiveNested {
				t.Errorf("%s = %v, want %v", nonInteractiveNestedAttrName, got, tt.wantNonInteractiveNested)
			}
		})
	}
}

// TestReportingIsInert is the regression test for the thing this fork
// deliberately does not do: no usage data leaves the machine. The entry
// points stay callable so the call sites keep compiling, and every one of
// them must be a safe no-op, including for a nil error value.
func TestReportingIsInert(t *testing.T) {
	Init()

	send("app initialized")
	send("prompt sent", "tokens", 42)
	Error(nil)
	Error("some error")
	Error(newDefaultTestError("runtime error"), "key", "value")
	Alias("user-123")
	Flush()
}

// TestGetIDIsStable verifies the machine identifier — which is sent to
// providers as a rate-limit key, not to an analytics service — resolves
// without Init and does not change between calls.
func TestGetIDIsStable(t *testing.T) {
	first := GetID()
	if first == "" {
		t.Fatal("GetID() returned an empty identifier")
	}
	if second := GetID(); second != first {
		t.Fatalf("GetID() = %q on second call, want %q", second, first)
	}
}

// newDefaultTestError creates a test error that mimics runtime panic
// errors, so Error is exercised with the kind of value a panic recovery
// hands it.
func newDefaultTestError(s string) error {
	return testError(s)
}

type testError string

func (e testError) Error() string {
	return string(e)
}

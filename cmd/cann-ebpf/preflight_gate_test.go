package main

import "testing"

func TestPreflightGateModes(t *testing.T) {
	if code := runPreflightGate("off"); code != 0 {
		t.Fatalf("off exit = %d", code)
	}
	if code := runPreflightGate("OFF"); code != 0 {
		t.Fatalf("OFF exit = %d", code)
	}
	if code := runPreflightGate("sometimes"); code != 2 {
		t.Fatalf("invalid mode exit = %d, want 2", code)
	}
}

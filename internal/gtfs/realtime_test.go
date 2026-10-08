package gtfs

import "testing"

// A placeholder test so `go test ./...` passes from the start. Replace the real
// delay-matching logic's tests here in week 2.
func TestStopDelayZeroValue(t *testing.T) {
	var d StopDelay
	if d.DelaySeconds != 0 {
		t.Fatalf("expected zero-value delay to be 0, got %d", d.DelaySeconds)
	}
}

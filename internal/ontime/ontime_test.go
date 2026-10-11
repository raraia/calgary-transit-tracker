package ontime

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		delay int
		want  Status
	}{
		{0, OnTime},
		{-60, OnTime}, // exactly 1 min early: still on time
		{-61, Early},  // a second more: early
		{300, OnTime}, // exactly 5 min late: still on time
		{301, Late},   // a second more: late
		{-600, Early},
		{1200, Late},
	}
	for _, tt := range tests {
		if got := Default.Classify(tt.delay); got != tt.want {
			t.Errorf("Classify(%d) = %v, want %v", tt.delay, got, tt.want)
		}
	}
}

func TestSecs(t *testing.T) {
	if Default.EarlySecs() != 60 || Default.LateSecs() != 300 {
		t.Errorf("got %d/%d, want 60/300", Default.EarlySecs(), Default.LateSecs())
	}
}

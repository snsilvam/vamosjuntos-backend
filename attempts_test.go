package main

import (
	"testing"
	"time"
)

func TestRegisterAttempt(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, colombia)

	var s attemptState
	for i := 0; i < maxFailedAttempts-1; i++ {
		s = registerAttempt(s, false, now)
	}
	if d := blockedFor(s, now); d != 0 {
		t.Fatalf("blocked after %d failures for %v", maxFailedAttempts-1, d)
	}

	s = registerAttempt(s, false, now)
	if d := blockedFor(s, now); d != blockDuration {
		t.Fatalf("after the 5th failure blockedFor = %v, want %v", d, blockDuration)
	}

	later := now.Add(blockDuration)
	if d := blockedFor(s, later); d != 0 {
		t.Fatalf("still blocked when the block expired: %v", d)
	}

	// After the block expires the count starts again from zero.
	s = registerAttempt(s, false, later)
	if d := blockedFor(s, later); d != 0 {
		t.Fatalf("a single failure after the block blocked again for %v", d)
	}
}

func TestRegisterAttemptSuccessResets(t *testing.T) {
	now := time.Now()
	var s attemptState
	for i := 0; i < maxFailedAttempts-1; i++ {
		s = registerAttempt(s, false, now)
	}
	s = registerAttempt(s, true, now)
	if s != (attemptState{}) {
		t.Fatalf("success did not reset the state: %+v", s)
	}
	s = registerAttempt(s, false, now)
	if d := blockedFor(s, now); d != 0 {
		t.Fatalf("blocked after one failure following a success: %v", d)
	}
}

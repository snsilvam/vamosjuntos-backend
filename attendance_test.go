package main

import (
	"errors"
	"fmt"
	"testing"
)

func TestNextStatus(t *testing.T) {
	tests := []struct {
		name    string
		current Status
		action  action
		want    Status
		wantErr error
	}{
		{"pending confirms", statusPending, actionConfirm, statusConfirmed, nil},
		{"pending declines", statusPending, actionDecline, statusDeclined, nil},
		{"declined confirms via re-entry", statusDeclined, actionConfirm, statusConfirmed, nil},
		{"declined declines again", statusDeclined, actionDecline, statusDeclined, nil},
		{"confirmed confirms again", statusConfirmed, actionConfirm, statusConfirmed, nil},
		{"confirmed cannot decline", statusConfirmed, actionDecline, statusConfirmed, errAlreadyConfirmed},
		{"pending confirmed by other", statusPending, actionConfirmOther, statusConfirmed, nil},
		{"declined cannot be confirmed by other", statusDeclined, actionConfirmOther, statusDeclined, errAlreadyResponded},
		{"confirmed cannot be confirmed by other", statusConfirmed, actionConfirmOther, statusConfirmed, errAlreadyResponded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nextStatus(tt.current, tt.action)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

// Spec 009: only a pending guest without a linked account can be confirmed by someone else.
func TestConfirmOtherRule(t *testing.T) {
	tests := []struct {
		name    string
		current Status
		linked  bool
		want    Status
		wantErr error
	}{
		{"pending without account", statusPending, false, statusConfirmed, nil},
		{"pending with account", statusPending, true, statusPending, errGuestLinked},
		{"confirmed without account", statusConfirmed, false, statusConfirmed, errAlreadyResponded},
		{"confirmed with account", statusConfirmed, true, statusConfirmed, errAlreadyResponded},
		{"declined without account", statusDeclined, false, statusDeclined, errAlreadyResponded},
		{"declined with account", statusDeclined, true, statusDeclined, errAlreadyResponded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := confirmOther(tt.current, tt.linked)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

// testCatalog returns a catalog of n guests with ids g01, g02, ...
func testCatalog(t *testing.T, n int) Catalog {
	t.Helper()
	guests := make([]Guest, n)
	for i := range guests {
		id := fmt.Sprintf("g%02d", i+1)
		guests[i] = Guest{ID: id, Name: "Invitado " + id, code: "codigo-" + id}
	}
	c, err := newCatalog(guests)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// statusesFor marks the first confirmed guests as confirmed and the next declined as declined.
func statusesFor(confirmed, declined int) map[string]Status {
	out := map[string]Status{}
	for i := 1; i <= confirmed+declined; i++ {
		st := statusConfirmed
		if i > confirmed {
			st = statusDeclined
		}
		out[fmt.Sprintf("g%02d", i)] = st
	}
	return out
}

func TestSummarizeAttendance(t *testing.T) {
	removed := statusesFor(1, 0)
	removed["ya-no-esta"] = statusConfirmed // answer of a guest removed from the catalog

	tests := []struct {
		name     string
		invited  int
		statuses map[string]Status
		want     AttendanceSummary
	}{
		{"spec example", 22, statusesFor(5, 2), AttendanceSummary{Invited: 22, Confirmed: 5, Declined: 2, Pending: 15}},
		{"nobody answered", 22, map[string]Status{}, AttendanceSummary{Invited: 22, Pending: 22}},
		{"everybody answered", 3, statusesFor(2, 1), AttendanceSummary{Invited: 3, Confirmed: 2, Declined: 1}},
		{"removed guest does not count", 3, removed, AttendanceSummary{Invited: 3, Confirmed: 1, Pending: 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeAttendance(testCatalog(t, tt.invited), tt.statuses)
			if got != tt.want {
				t.Fatalf("summary = %+v, want %+v", got, tt.want)
			}
			if got.Confirmed+got.Declined+got.Pending != got.Invited {
				t.Fatalf("counts do not add up to invited: %+v", got)
			}
		})
	}
}

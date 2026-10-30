package main

import (
	"errors"
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

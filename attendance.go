package main

import (
	"context"
	"errors"
	"fmt"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Status is a guest's attendance answer. A guest without a stored answer is pending.
type Status string

const (
	statusPending   Status = ""
	statusConfirmed Status = "confirmed"
	statusDeclined  Status = "declined"
)

type action int

const (
	actionConfirm      action = iota // the guest confirms (FR-006, or after re-entry FR-011)
	actionDecline                    // the guest says they will not attend (FR-007)
	actionConfirmOther               // another guest confirms them with their code (FR-014)
)

var (
	errAlreadyConfirmed = errors.New("guest already confirmed")
	errAlreadyResponded = errors.New("guest already responded")
)

// nextStatus applies the transition table of data-model.md.
func nextStatus(current Status, a action) (Status, error) {
	switch a {
	case actionConfirm:
		return statusConfirmed, nil
	case actionDecline:
		if current == statusConfirmed {
			return current, errAlreadyConfirmed
		}
		return statusDeclined, nil
	case actionConfirmOther:
		if current != statusPending {
			return current, errAlreadyResponded
		}
		return statusConfirmed, nil
	default:
		return current, fmt.Errorf("unknown action %d", a)
	}
}

// AttendanceSummary counts the answers of the catalog guests (spec 003, data-model.md).
type AttendanceSummary struct {
	Invited   int `json:"invited"`
	Confirmed int `json:"confirmed"`
	Declined  int `json:"declined"`
	Pending   int `json:"pending"`
}

// summarizeAttendance counts only guests in the catalog: an answer left by a guest the
// organizer removed no longer counts, so confirmed + declined + pending is always invited.
func summarizeAttendance(catalog Catalog, statuses map[string]Status) AttendanceSummary {
	s := AttendanceSummary{Invited: catalog.Len()}
	for _, g := range catalog.Sorted() {
		switch statuses[g.ID] {
		case statusConfirmed:
			s.Confirmed++
		case statusDeclined:
			s.Declined++
		}
	}
	s.Pending = s.Invited - s.Confirmed - s.Declined
	return s
}

type attendanceRecord struct {
	Status Status `firestore:"status"`
}

// AttendanceStore keeps answers in events/{EVENT_ID}/attendance/{guestId}.
type AttendanceStore struct {
	client  *firestore.Client
	eventID string
}

// NewAttendanceStore returns an AttendanceStore for the given event.
func NewAttendanceStore(client *firestore.Client, eventID string) *AttendanceStore {
	return &AttendanceStore{client: client, eventID: eventID}
}

func (s *AttendanceStore) collection() *firestore.CollectionRef {
	return s.client.Collection("events").Doc(s.eventID).Collection("attendance")
}

// Statuses returns the stored answer of every guest that has responded, keyed by guest id.
func (s *AttendanceStore) Statuses(ctx context.Context) (map[string]Status, error) {
	docs, err := s.collection().Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("list attendance: %w", err)
	}
	out := make(map[string]Status, len(docs))
	for _, d := range docs {
		var r attendanceRecord
		if err := d.DataTo(&r); err != nil {
			return nil, fmt.Errorf("decode attendance %s: %w", d.Ref.ID, err)
		}
		out[d.Ref.ID] = r.Status
	}
	return out, nil
}

// Respond applies action a to guestID inside a transaction, so concurrent answers are
// recorded once. respondedBy is the guest who registered the answer.
func (s *AttendanceStore) Respond(ctx context.Context, guestID string, a action, respondedBy string) (Status, error) {
	ref := s.collection().Doc(guestID)
	var result Status
	err := s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		current := statusPending
		snap, err := tx.Get(ref)
		switch {
		case status.Code(err) == codes.NotFound:
			// No answer yet: pending.
		case err != nil:
			return fmt.Errorf("get attendance: %w", err)
		default:
			var r attendanceRecord
			if err := snap.DataTo(&r); err != nil {
				return fmt.Errorf("decode attendance: %w", err)
			}
			current = r.Status
		}

		next, err := nextStatus(current, a)
		if err != nil {
			return err
		}
		result = next
		if next == current {
			return nil
		}
		return tx.Set(ref, map[string]any{
			"status":      string(next),
			"respondedAt": firestore.ServerTimestamp,
			"respondedBy": respondedBy,
		})
	})
	if err != nil {
		return "", fmt.Errorf("respond for %s: %w", guestID, err)
	}
	return result, nil
}

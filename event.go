package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// colombia is UTC-5 all year: Colombia has no daylight saving time.
var colombia = time.FixedZone("COT", -5*60*60)

var errEventNotFound = errors.New("event not found")

// Event is the trip document that the organizer creates by hand in events/{EVENT_ID}.
type Event struct {
	Name            string    `firestore:"name"`
	StartsAt        time.Time `firestore:"startsAt"`
	EndsAt          time.Time `firestore:"endsAt"`
	Destination     string    `firestore:"destination"`
	DeparturePlaces []string  `firestore:"departurePlaces"`
	Capacity        int       `firestore:"capacity"`
}

func (e Event) valid() bool {
	return e.Name != "" &&
		!e.StartsAt.IsZero() &&
		e.EndsAt.After(e.StartsAt) &&
		e.Destination != "" &&
		len(e.DeparturePlaces) > 0 &&
		e.Capacity > 0 && e.Capacity <= maxGuests
}

// EventSummary is the public view of the event, with times in Colombia (RFC 3339, -05:00).
type EventSummary struct {
	Name            string   `json:"name"`
	StartsAt        string   `json:"startsAt"`
	EndsAt          string   `json:"endsAt"`
	Destination     string   `json:"destination"`
	DeparturePlaces []string `json:"departurePlaces"`
}

// Summary returns the public view of e.
func (e Event) Summary() EventSummary {
	return EventSummary{
		Name:            e.Name,
		StartsAt:        e.StartsAt.In(colombia).Format(time.RFC3339),
		EndsAt:          e.EndsAt.In(colombia).Format(time.RFC3339),
		Destination:     e.Destination,
		DeparturePlaces: e.DeparturePlaces,
	}
}

// EventStore reads the event document. The app never writes it (Principle I).
type EventStore struct {
	client  *firestore.Client
	eventID string
}

// NewEventStore returns an EventStore for events/{eventID}.
func NewEventStore(client *firestore.Client, eventID string) *EventStore {
	return &EventStore{client: client, eventID: eventID}
}

// Get returns the event. It returns an error wrapping errEventNotFound when the document is
// missing or incomplete (FR-004).
func (s *EventStore) Get(ctx context.Context) (Event, error) {
	snap, err := s.client.Collection("events").Doc(s.eventID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return Event{}, errEventNotFound
	}
	if err != nil {
		return Event{}, fmt.Errorf("get event %s: %w", s.eventID, err)
	}
	var e Event
	if err := snap.DataTo(&e); err != nil {
		return Event{}, fmt.Errorf("%w: decode %s: %v", errEventNotFound, s.eventID, err)
	}
	if !e.valid() {
		return Event{}, fmt.Errorf("%w: %s has missing or invalid fields", errEventNotFound, s.eventID)
	}
	return e, nil
}

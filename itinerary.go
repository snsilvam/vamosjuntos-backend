package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxActivityTitleLen       = 80
	maxActivityPlaceLen       = 80
	maxActivityDescriptionLen = 500
	minutesPerDay             = 24 * 60
)

// Domain errors of the itinerary module; handlers map them to HTTP codes.
var (
	errInvalidActivity    = errors.New("invalid activity")
	errActivityOutOfRange = errors.New("activity out of the trip days")
	errActivityTimeOrder  = errors.New("activity ends before it starts")
	errActivityNotFound   = errors.New("activity not found")
)

// Activity is a document of events/{EVENT_ID}/activities. Day and times are Colombia wall-clock
// text, so no time zone conversion can move an activity (FR-006).
type Activity struct {
	ID          string `firestore:"-" json:"id"`
	Title       string `firestore:"title" json:"title"`
	Date        string `firestore:"date" json:"date"`
	StartTime   string `firestore:"startTime" json:"startTime"`
	EndTime     string `firestore:"endTime" json:"endTime,omitempty"`
	EndsNextDay bool   `firestore:"endsNextDay" json:"endsNextDay,omitempty"`
	Place       string `firestore:"place" json:"place,omitempty"`
	Description string `firestore:"description" json:"description,omitempty"`
}

// ActivityInput is the editable part of an activity (FR-003).
type ActivityInput struct {
	Title       string `json:"title"`
	Date        string `json:"date"`
	StartTime   string `json:"startTime"`
	EndTime     string `json:"endTime"`
	EndsNextDay bool   `json:"endsNextDay"`
	Place       string `json:"place"`
	Description string `json:"description"`
}

// eventDays returns the Colombia calendar days of e, from its start to its end, both included
// (FR-004).
func eventDays(e Event) []string {
	last := e.EndsAt.In(colombia).Format(time.DateOnly)
	var days []string
	for d := colombiaDate(e.StartsAt); ; d = d.AddDate(0, 0, 1) {
		day := d.Format(time.DateOnly)
		days = append(days, day)
		if day >= last {
			return days
		}
	}
}

// validateActivity trims in and checks the rules of specs/006-modulo-itinerario/data-model.md.
func validateActivity(in ActivityInput, days []string) (ActivityInput, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.StartTime = strings.TrimSpace(in.StartTime)
	in.EndTime = strings.TrimSpace(in.EndTime)
	in.Place = strings.TrimSpace(in.Place)
	in.Description = strings.TrimSpace(in.Description)
	titleLen := utf8.RuneCountInString(in.Title)
	switch {
	case titleLen == 0 || titleLen > maxActivityTitleLen,
		utf8.RuneCountInString(in.Place) > maxActivityPlaceLen,
		utf8.RuneCountInString(in.Description) > maxActivityDescriptionLen,
		!clockTime.MatchString(in.StartTime),
		in.EndTime != "" && !clockTime.MatchString(in.EndTime),
		in.EndsNextDay && in.EndTime == "":
		return ActivityInput{}, errInvalidActivity
	}
	if !slices.Contains(days, in.Date) {
		return ActivityInput{}, errActivityOutOfRange
	}
	if in.EndTime != "" {
		duration := minutesOf(in.EndTime) - minutesOf(in.StartTime)
		if in.EndsNextDay {
			duration += minutesPerDay
		}
		// Without "ends next day" the end must come after the start (US2-2); with it, the activity
		// lasts at most 24 hours (FR-005).
		if duration <= 0 || duration > minutesPerDay {
			return ActivityInput{}, errActivityTimeOrder
		}
	}
	return in, nil
}

// minutesOf converts a valid HH:MM into minutes since midnight.
func minutesOf(hhmm string) int {
	return int(hhmm[0]-'0')*600 + int(hhmm[1]-'0')*60 + int(hhmm[3]-'0')*10 + int(hhmm[4]-'0')
}

// sortActivities orders by day, start time, title without accents and id (FR-002).
func sortActivities(activities []Activity) {
	slices.SortStableFunc(activities, func(a, b Activity) int {
		return cmp.Or(
			strings.Compare(a.Date, b.Date),
			strings.Compare(a.StartTime, b.StartTime),
			strings.Compare(foldAccents(a.Title), foldAccents(b.Title)),
			strings.Compare(a.ID, b.ID),
		)
	})
}

func withActivityInput(id string, in ActivityInput) Activity {
	return Activity{
		ID:          id,
		Title:       in.Title,
		Date:        in.Date,
		StartTime:   in.StartTime,
		EndTime:     in.EndTime,
		EndsNextDay: in.EndsNextDay,
		Place:       in.Place,
		Description: in.Description,
	}
}

// ItineraryStore keeps the activities in events/{EVENT_ID}/activities. Activities do not depend
// on each other, so each write touches only its own document (research R3).
type ItineraryStore struct {
	client  *firestore.Client
	eventID string
}

// NewItineraryStore returns an ItineraryStore for the given event.
func NewItineraryStore(client *firestore.Client, eventID string) *ItineraryStore {
	return &ItineraryStore{client: client, eventID: eventID}
}

func (s *ItineraryStore) collection() *firestore.CollectionRef {
	return s.client.Collection("events").Doc(s.eventID).Collection("activities")
}

func activityData(in ActivityInput, by string) map[string]any {
	return map[string]any{
		"title":       in.Title,
		"date":        in.Date,
		"startTime":   in.StartTime,
		"endTime":     in.EndTime,
		"endsNextDay": in.EndsNextDay,
		"place":       in.Place,
		"description": in.Description,
		"updatedAt":   firestore.ServerTimestamp,
		"updatedBy":   by,
	}
}

// List returns every activity of the event; handlers sort them.
func (s *ItineraryStore) List(ctx context.Context) ([]Activity, error) {
	docs, err := s.collection().Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("list activities: %w", err)
	}
	activities := make([]Activity, 0, len(docs))
	for _, d := range docs {
		var a Activity
		if err := d.DataTo(&a); err != nil {
			return nil, fmt.Errorf("decode activity %s: %w", d.Ref.ID, err)
		}
		a.ID = d.Ref.ID
		activities = append(activities, a)
	}
	return activities, nil
}

// Create stores a new, already validated activity. by is the guest who adds it.
func (s *ItineraryStore) Create(ctx context.Context, in ActivityInput, by string) error {
	if _, err := s.collection().NewDoc().Create(ctx, activityData(in, by)); err != nil {
		return fmt.Errorf("create activity: %w", err)
	}
	return nil
}

// Update replaces an existing activity, or returns errActivityNotFound.
func (s *ItineraryStore) Update(ctx context.Context, id string, in ActivityInput, by string) error {
	ref := s.collection().Doc(id)
	err := s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		if _, err := tx.Get(ref); err != nil {
			if status.Code(err) == codes.NotFound {
				return errActivityNotFound
			}
			return err
		}
		return tx.Set(ref, activityData(in, by))
	})
	if err != nil {
		return fmt.Errorf("update activity %s: %w", id, err)
	}
	return nil
}

// Delete removes an activity, or returns errActivityNotFound.
func (s *ItineraryStore) Delete(ctx context.Context, id string) error {
	_, err := s.collection().Doc(id).Delete(ctx, firestore.Exists)
	if status.Code(err) == codes.NotFound {
		return errActivityNotFound
	}
	if err != nil {
		return fmt.Errorf("delete activity %s: %w", id, err)
	}
	return nil
}

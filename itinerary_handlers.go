package main

import (
	"context"
	"errors"
	"net/http"
)

// HTTP side of the itinerary module (spec 006). Every route sits behind requireGuest: any
// confirmed guest can add, edit and delete any activity (FR-007).

type itineraryStore interface {
	List(ctx context.Context) ([]Activity, error)
	Create(ctx context.Context, in ActivityInput, by string) error
	Update(ctx context.Context, id string, in ActivityInput, by string) error
	Delete(ctx context.Context, id string) error
}

type itineraryResponse struct {
	Days       []string   `json:"days"`
	Activities []Activity `json:"activities"`
}

// writeItinerary answers with the valid days and every activity in order, the same view as
// GET /api/itinerary.
func (s *server) writeItinerary(w http.ResponseWriter, r *http.Request, event Event, status int) {
	activities, err := s.itinerary.List(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	sortActivities(activities)
	writeJSON(w, status, itineraryResponse{Days: eventDays(event), Activities: activities})
}

// handleItinerary lists the activities grouped by day on the client (US1).
func (s *server) handleItinerary(w http.ResponseWriter, r *http.Request, _ Account, _ Guest) {
	event, ok := s.loadEvent(w, r)
	if !ok {
		return
	}
	s.writeItinerary(w, r, event, http.StatusOK)
}

// saveActivity validates the request body against the event days and stores it with save.
func (s *server) saveActivity(w http.ResponseWriter, r *http.Request, status int, save func(ActivityInput) error) {
	var in ActivityInput
	if !decodeJSON(w, r, &in) {
		return
	}
	event, ok := s.loadEvent(w, r)
	if !ok {
		return
	}
	in, err := validateActivity(in, eventDays(event))
	if err == nil {
		err = save(in)
	}
	if err != nil {
		writeItineraryError(w, err)
		return
	}
	s.writeItinerary(w, r, event, status)
}

// handleCreateActivity adds an activity (US2).
func (s *server) handleCreateActivity(w http.ResponseWriter, r *http.Request, _ Account, me Guest) {
	s.saveActivity(w, r, http.StatusCreated, func(in ActivityInput) error {
		return s.itinerary.Create(r.Context(), in, me.ID)
	})
}

// handleUpdateActivity edits an activity (US3).
func (s *server) handleUpdateActivity(w http.ResponseWriter, r *http.Request, _ Account, me Guest) {
	s.saveActivity(w, r, http.StatusOK, func(in ActivityInput) error {
		return s.itinerary.Update(r.Context(), r.PathValue("activityID"), in, me.ID)
	})
}

// handleDeleteActivity deletes an activity; the UI asks for confirmation first (US3, FR-008).
func (s *server) handleDeleteActivity(w http.ResponseWriter, r *http.Request, _ Account, _ Guest) {
	if err := s.itinerary.Delete(r.Context(), r.PathValue("activityID")); err != nil {
		writeItineraryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeItineraryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInvalidActivity):
		writeError(w, http.StatusBadRequest, codeInvalidActivity)
	case errors.Is(err, errActivityOutOfRange):
		writeError(w, http.StatusBadRequest, codeActivityOutOfRange)
	case errors.Is(err, errActivityTimeOrder):
		writeError(w, http.StatusBadRequest, codeActivityTimeOrder)
	case errors.Is(err, errActivityNotFound):
		writeError(w, http.StatusNotFound, codeActivityNotFound)
	default:
		writeInternalError(w, err)
	}
}

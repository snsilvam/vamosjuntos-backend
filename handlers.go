package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"
)

const maxBodyBytes = 4 << 10

// Small interfaces, defined where they are consumed so handlers can be tested with fakes.

type guestCatalog interface {
	Catalog(ctx context.Context) (Catalog, error)
}

type eventGetter interface {
	Get(ctx context.Context) (Event, error)
}

type attendanceRecorder interface {
	Statuses(ctx context.Context) (map[string]Status, error)
	Respond(ctx context.Context, guestID string, a action, respondedBy string) (Status, error)
}

type attemptLimiter interface {
	Blocked(ctx context.Context, deviceID string) (time.Duration, error)
	Record(ctx context.Context, deviceID string, ok bool) error
}

type server struct {
	guests     guestCatalog
	events     eventGetter
	attendance attendanceRecorder
	attempts   attemptLimiter
	sessionKey []byte
	now        func() time.Time
}

// NewServer wires the HTTP handlers with their dependencies.
func NewServer(guests guestCatalog, events eventGetter, attendance attendanceRecorder, attempts attemptLimiter, sessionKey []byte) *server {
	return &server{
		guests:     guests,
		events:     events,
		attendance: attendance,
		attempts:   attempts,
		sessionKey: sessionKey,
		now:        time.Now,
	}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/onboarding/guests", s.handleGuests)
	mux.HandleFunc("POST /api/onboarding/verify", s.handleVerify)
	mux.HandleFunc("POST /api/onboarding/respond", s.handleRespond)
	mux.HandleFunc("POST /api/session/reentry", s.handleReentry)
	mux.HandleFunc("GET /api/session", s.requireSession(s.handleSession))
	mux.HandleFunc("DELETE /api/session", s.handleLogout)
	mux.HandleFunc("POST /api/guests/{guestID}/confirmation", s.requireSession(s.handleConfirmOther))
	return noStore(mux)
}

// noStore forbids caching API responses: they depend on the session cookie and on attendance
// that changes at any time, and they travel through the Firebase Hosting CDN.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type guestsResponse struct {
	Guests []Guest `json:"guests"`
}

type eventResponse struct {
	Event EventSummary `json:"event"`
}

type statusResponse struct {
	Status Status        `json:"status"`
	Guest  *Guest        `json:"guest,omitempty"`
	Event  *EventSummary `json:"event,omitempty"`
}

type sessionResponse struct {
	Guest Guest `json:"guest"`
}

// handleGuests lists the guests that have not answered yet (FR-002).
func (s *server) handleGuests(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.loadEvent(w, r); !ok {
		return
	}
	catalog, ok := s.loadCatalog(w, r)
	if !ok {
		return
	}
	statuses, err := s.attendance.Statuses(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	pending := []Guest{}
	for _, g := range catalog.Sorted() {
		if statuses[g.ID] == statusPending {
			pending = append(pending, g)
		}
	}
	writeJSON(w, http.StatusOK, guestsResponse{Guests: pending})
}

// handleVerify checks the code of the guest chosen from the list (FR-003, FR-005).
func (s *server) handleVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GuestID string `json:"guestId"`
		Code    string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	device, ok := deviceID(w, r)
	if !ok {
		return
	}
	event, ok := s.loadEvent(w, r)
	if !ok {
		return
	}
	catalog, ok := s.loadCatalog(w, r)
	if !ok {
		return
	}
	guest, ok := catalog.ByID(req.GuestID)
	if !ok {
		writeError(w, http.StatusNotFound, codeGuestNotFound)
		return
	}
	statuses, err := s.attendance.Statuses(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if statuses[guest.ID] != statusPending {
		writeError(w, http.StatusConflict, codeAlreadyResponded)
		return
	}
	match := catalog.Matches(guest.ID, req.Code)
	if !s.checkCode(r.Context(), w, device, match) {
		return
	}
	if !match {
		writeError(w, http.StatusUnauthorized, codeInvalidCode)
		return
	}
	writeJSON(w, http.StatusOK, eventResponse{Event: event.Summary()})
}

// handleRespond records the guest's answer (FR-006, FR-007). The code is checked again
// because the server keeps no state between onboarding steps.
func (s *server) handleRespond(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GuestID   string `json:"guestId"`
		Code      string `json:"code"`
		Attending *bool  `json:"attending"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Attending == nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest)
		return
	}
	device, ok := deviceID(w, r)
	if !ok {
		return
	}
	event, ok := s.loadEvent(w, r)
	if !ok {
		return
	}
	catalog, ok := s.loadCatalog(w, r)
	if !ok {
		return
	}
	guest, ok := catalog.ByID(req.GuestID)
	if !ok {
		writeError(w, http.StatusNotFound, codeGuestNotFound)
		return
	}
	match := catalog.Matches(guest.ID, req.Code)
	if !s.checkCode(r.Context(), w, device, match) {
		return
	}
	if !match {
		writeError(w, http.StatusUnauthorized, codeInvalidCode)
		return
	}

	if !*req.Attending {
		_, err := s.attendance.Respond(r.Context(), guest.ID, actionDecline, guest.ID)
		if errors.Is(err, errAlreadyConfirmed) {
			writeError(w, http.StatusConflict, codeAlreadyConfirmed)
			return
		}
		if err != nil {
			writeInternalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, statusResponse{Status: statusDeclined})
		return
	}

	if _, err := s.attendance.Respond(r.Context(), guest.ID, actionConfirm, guest.ID); err != nil {
		writeInternalError(w, err)
		return
	}
	s.startSession(w, guest, event)
	writeJSON(w, http.StatusOK, statusResponse{Status: statusConfirmed, Guest: &guest})
}

// handleReentry lets a guest who already answered come back with only their code (FR-011).
func (s *server) handleReentry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	device, ok := deviceID(w, r)
	if !ok {
		return
	}
	event, ok := s.loadEvent(w, r)
	if !ok {
		return
	}
	catalog, ok := s.loadCatalog(w, r)
	if !ok {
		return
	}
	guest, found := catalog.ByCode(req.Code)
	if !s.checkCode(r.Context(), w, device, found) {
		return
	}
	if !found {
		writeError(w, http.StatusUnauthorized, codeInvalidCode)
		return
	}
	statuses, err := s.attendance.Statuses(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	switch statuses[guest.ID] {
	case statusConfirmed:
		s.startSession(w, guest, event)
		writeJSON(w, http.StatusOK, statusResponse{Status: statusConfirmed, Guest: &guest})
	case statusDeclined:
		summary := event.Summary()
		writeJSON(w, http.StatusOK, statusResponse{Status: statusDeclined, Guest: &guest, Event: &summary})
	default:
		writeError(w, http.StatusConflict, codePendingUseList)
	}
}

// handleSession returns the guest of the current session.
func (s *server) handleSession(w http.ResponseWriter, _ *http.Request, guest Guest) {
	writeJSON(w, http.StatusOK, sessionResponse{Guest: guest})
}

// handleLogout ends the session on this device (FR-012). It works without a session.
func (s *server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleConfirmOther confirms another pending guest with that guest's code, without changing
// the caller's session (FR-014). A wrong code is a 400, not a 401, so the frontend does not
// treat it as a lost session.
func (s *server) handleConfirmOther(w http.ResponseWriter, r *http.Request, me Guest) {
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	device, ok := deviceID(w, r)
	if !ok {
		return
	}
	if _, ok := s.loadEvent(w, r); !ok {
		return
	}
	catalog, ok := s.loadCatalog(w, r)
	if !ok {
		return
	}
	target, ok := catalog.ByID(r.PathValue("guestID"))
	if !ok {
		writeError(w, http.StatusNotFound, codeGuestNotFound)
		return
	}
	match := catalog.Matches(target.ID, req.Code)
	if !s.checkCode(r.Context(), w, device, match) {
		return
	}
	if !match {
		writeError(w, http.StatusBadRequest, codeInvalidCode)
		return
	}
	_, err := s.attendance.Respond(r.Context(), target.ID, actionConfirmOther, me.ID)
	if errors.Is(err, errAlreadyResponded) {
		writeError(w, http.StatusConflict, codeAlreadyResponded)
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: statusConfirmed, Guest: &target})
}

func (s *server) startSession(w http.ResponseWriter, guest Guest, event Event) {
	exp := sessionExpiry(event)
	setSessionCookie(w, signToken(s.sessionKey, guest.ID, exp), exp)
}

// loadEvent reads the event and writes the error response when it is not available.
func (s *server) loadEvent(w http.ResponseWriter, r *http.Request) (Event, bool) {
	event, err := s.events.Get(r.Context())
	if errors.Is(err, errEventNotFound) {
		log.Printf("event not available: %v", err)
		writeError(w, http.StatusNotFound, codeEventNotFound)
		return Event{}, false
	}
	if err != nil {
		writeInternalError(w, err)
		return Event{}, false
	}
	return event, true
}

// loadCatalog reads the guest catalog and writes a 500 when it cannot be read or is invalid; the
// reason (without codes) goes to the log.
func (s *server) loadCatalog(w http.ResponseWriter, r *http.Request) (Catalog, bool) {
	catalog, err := s.guests.Catalog(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return Catalog{}, false
	}
	return catalog, true
}

// checkCode enforces the attempt limit (FR-010) and records the result of a code comparison.
// It returns false when the device is blocked, after writing the 429 response. A blocked
// device is rejected even with the right code.
func (s *server) checkCode(ctx context.Context, w http.ResponseWriter, device string, match bool) bool {
	blocked, err := s.attempts.Blocked(ctx, device)
	if err != nil {
		writeInternalError(w, err)
		return false
	}
	if blocked > 0 {
		writeTooManyAttempts(w, blocked)
		return false
	}
	if err := s.attempts.Record(ctx, device, match); err != nil {
		// The answer still goes through: losing one count is better than blocking a guest.
		log.Printf("record attempt: %v", err)
	}
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest)
		return false
	}
	return true
}

// deviceID reads X-Device-Id. It is used as a Firestore document id, so only UUID-like
// values are accepted.
func deviceID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.Header.Get("X-Device-Id")
	if !validDeviceID(id) {
		writeError(w, http.StatusBadRequest, codeMissingDeviceID)
		return "", false
	}
	return id, true
}

func validDeviceID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

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
	Blocked(ctx context.Context, uid string) (time.Duration, error)
	Record(ctx context.Context, uid string, ok bool) error
}

type tokenVerifier interface {
	Verify(ctx context.Context, raw string) (Account, error)
}

type linkStore interface {
	GuestOf(ctx context.Context, uid string) (string, bool, error)
	Link(ctx context.Context, uid, email, guestID string) error
}

type transportStore interface {
	List(ctx context.Context) ([]Vehicle, error)
	Apply(ctx context.Context, cmd transportCmd, confirmed map[string]bool, by string) error
}

type server struct {
	guests     guestCatalog
	events     eventGetter
	attendance attendanceRecorder
	attempts   attemptLimiter
	verifier   tokenVerifier
	links      linkStore
	transport  transportStore
	itinerary  itineraryStore
	purchases  purchaseStore
	// now is the clock of the countdown (spec 004); tests replace it with a fixed date.
	now func() time.Time
}

// NewServer wires the HTTP handlers with their dependencies.
func NewServer(guests guestCatalog, events eventGetter, attendance attendanceRecorder, attempts attemptLimiter, verifier tokenVerifier, links linkStore, transport transportStore, itinerary itineraryStore, purchases purchaseStore) *server {
	return &server{
		guests:     guests,
		events:     events,
		attendance: attendance,
		attempts:   attempts,
		verifier:   verifier,
		links:      links,
		transport:  transport,
		itinerary:  itinerary,
		purchases:  purchases,
		now:        time.Now,
	}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/me", s.requireAccount(s.handleMe))
	mux.HandleFunc("GET /api/onboarding/guests", s.requireAccount(s.handleGuests))
	mux.HandleFunc("POST /api/onboarding/link", s.requireAccount(s.handleLink))
	mux.HandleFunc("POST /api/onboarding/respond", s.requireAccount(s.handleRespond))
	mux.HandleFunc("POST /api/guests/{guestID}/confirmation", s.requireGuest(s.handleConfirmOther))
	mux.HandleFunc("GET /api/event", s.requireGuest(s.handleEvent))
	// Transport module (spec 005).
	mux.HandleFunc("GET /api/transport", s.requireGuest(s.handleTransport))
	mux.HandleFunc("POST /api/transport/vehicles", s.requireGuest(s.handleCreateVehicle))
	mux.HandleFunc("PUT /api/transport/vehicles/{vehicleID}", s.requireGuest(s.handleUpdateVehicle))
	mux.HandleFunc("DELETE /api/transport/vehicles/{vehicleID}", s.requireGuest(s.handleDeleteVehicle))
	mux.HandleFunc("POST /api/transport/vehicles/{vehicleID}/passengers", s.requireGuest(s.handleReserveSeat))
	mux.HandleFunc("DELETE /api/transport/vehicles/{vehicleID}/passengers/{guestID}", s.requireGuest(s.handleReleaseSeat))
	mux.HandleFunc("GET /api/countdown", s.requireGuest(s.handleCountdown))
	mux.HandleFunc("GET /api/itinerary", s.requireGuest(s.handleItinerary))
	mux.HandleFunc("POST /api/itinerary/activities", s.requireGuest(s.handleCreateActivity))
	mux.HandleFunc("PUT /api/itinerary/activities/{activityID}", s.requireGuest(s.handleUpdateActivity))
	mux.HandleFunc("DELETE /api/itinerary/activities/{activityID}", s.requireGuest(s.handleDeleteActivity))
	mux.HandleFunc("GET /api/purchases", s.requireGuest(s.handlePurchases))
	mux.HandleFunc("POST /api/purchases/items", s.requireGuest(s.handleCreateItem))
	mux.HandleFunc("PUT /api/purchases/items/{itemID}", s.requireGuest(s.handleUpdateItem))
	mux.HandleFunc("DELETE /api/purchases/items/{itemID}", s.requireGuest(s.handleDeleteItem))
	mux.HandleFunc("POST /api/purchases/items/{itemID}/take", s.requireGuest(s.handleTakeItem))
	mux.HandleFunc("POST /api/purchases/items/{itemID}/release", s.requireGuest(s.handleReleaseItem))
	mux.HandleFunc("POST /api/purchases/items/{itemID}/bought", s.requireGuest(s.handleMarkBought))
	return noStore(mux)
}

// noStore forbids caching API responses: they depend on the signed-in account and on attendance
// that changes at any time, and they travel through the Firebase Hosting CDN.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// Account states returned by GET /api/me (data-model.md, "Estado de la cuenta").
const (
	meUnlinked  = "unlinked"
	mePending   = "pending"
	meDeclined  = "declined"
	meConfirmed = "confirmed"
)

type meResponse struct {
	Status string        `json:"status"`
	Guest  *Guest        `json:"guest,omitempty"`
	Event  *EventSummary `json:"event,omitempty"`
}

type guestsResponse struct {
	Guests []Guest `json:"guests"`
}

type statusResponse struct {
	Status Status `json:"status"`
	Guest  *Guest `json:"guest,omitempty"`
}

// eventResponse feeds the "Próximo viaje" card: only counts, never names (spec 003, FR-007).
type eventResponse struct {
	Event      EventSummary      `json:"event"`
	Attendance AttendanceSummary `json:"attendance"`
}

// handleMe tells the app where the account belongs (FR-003, FR-007, FR-009).
func (s *server) handleMe(w http.ResponseWriter, r *http.Request, account Account) {
	if me, ok := s.meFor(w, r, account); ok {
		writeJSON(w, http.StatusOK, me)
	}
}

// meFor computes the state of the account, writing the error response when it cannot.
func (s *server) meFor(w http.ResponseWriter, r *http.Request, account Account) (meResponse, bool) {
	guestID, linked, err := s.links.GuestOf(r.Context(), account.UID)
	if err != nil {
		writeInternalError(w, err)
		return meResponse{}, false
	}
	if !linked {
		return meResponse{Status: meUnlinked}, true
	}
	catalog, ok := s.loadCatalog(w, r)
	if !ok {
		return meResponse{}, false
	}
	guest, ok := catalog.ByID(guestID)
	if !ok {
		writeError(w, http.StatusForbidden, codeNotInvited)
		return meResponse{}, false
	}
	statuses, err := s.attendance.Statuses(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return meResponse{}, false
	}
	if statuses[guest.ID] == statusConfirmed {
		return meResponse{Status: meConfirmed, Guest: &guest}, true
	}

	// Pending or declined: the app asks again, so it needs the event.
	event, ok := s.loadEvent(w, r)
	if !ok {
		return meResponse{}, false
	}
	summary := event.Summary()
	me := meResponse{Status: mePending, Guest: &guest, Event: &summary}
	if statuses[guest.ID] == statusDeclined {
		me.Status = meDeclined
	}
	return me, true
}

// handleGuests lists the guests that have not answered yet (FR-003).
func (s *server) handleGuests(w http.ResponseWriter, r *http.Request, _ Account) {
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

// handleLink links the account with a guest by their code (FR-004, FR-005). With guestId the
// code must belong to that guest (the list); without it the guest is found by the code alone
// ("entrar con mi código"). A wrong code is a 400: a 401 would mean the token is invalid.
func (s *server) handleLink(w http.ResponseWriter, r *http.Request, account Account) {
	var req struct {
		GuestID string `json:"guestId"`
		Code    string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, ok := s.loadEvent(w, r); !ok {
		return
	}
	catalog, ok := s.loadCatalog(w, r)
	if !ok {
		return
	}

	var guest Guest
	var match bool
	if req.GuestID != "" {
		guest, ok = catalog.ByID(req.GuestID)
		if !ok {
			writeError(w, http.StatusNotFound, codeGuestNotFound)
			return
		}
		match = catalog.Matches(guest.ID, req.Code)
	} else {
		guest, match = catalog.ByCode(req.Code)
	}
	if !s.checkCode(r.Context(), w, account.UID, match) {
		return
	}
	if !match {
		writeError(w, http.StatusBadRequest, codeInvalidCode)
		return
	}

	err := s.links.Link(r.Context(), account.UID, account.Email, guest.ID)
	switch {
	case errors.Is(err, errAccountLinked):
		writeError(w, http.StatusConflict, codeAccountAlreadyLinked)
		return
	case errors.Is(err, errGuestLinked):
		writeError(w, http.StatusConflict, codeGuestAlreadyLinked)
		return
	case err != nil:
		writeInternalError(w, err)
		return
	}
	s.handleMe(w, r, account)
}

// handleRespond records the answer of the guest linked to the account (FR-007, FR-008). The
// code is no longer needed: the link already proved who the account is.
func (s *server) handleRespond(w http.ResponseWriter, r *http.Request, account Account) {
	var req struct {
		Attending *bool `json:"attending"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Attending == nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest)
		return
	}
	guestID, linked, err := s.links.GuestOf(r.Context(), account.UID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !linked {
		writeError(w, http.StatusForbidden, codeNotLinked)
		return
	}
	if _, ok := s.loadEvent(w, r); !ok {
		return
	}
	catalog, ok := s.loadCatalog(w, r)
	if !ok {
		return
	}
	guest, ok := catalog.ByID(guestID)
	if !ok {
		writeError(w, http.StatusForbidden, codeNotInvited)
		return
	}

	a := actionConfirm
	if !*req.Attending {
		a = actionDecline
	}
	next, err := s.attendance.Respond(r.Context(), guest.ID, a, guest.ID)
	if errors.Is(err, errAlreadyConfirmed) {
		writeError(w, http.StatusConflict, codeAlreadyConfirmed)
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: next, Guest: &guest})
}

// handleConfirmOther confirms another pending guest with that guest's code (FR-013). It links
// no account and does not change the caller's session. A wrong code is a 400, not a 401, so the
// frontend does not treat it as a lost session.
func (s *server) handleConfirmOther(w http.ResponseWriter, r *http.Request, account Account, me Guest) {
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
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
	if !s.checkCode(r.Context(), w, account.UID, match) {
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

// handleEvent returns the trip and how attendance is going for the home card (spec 003,
// FR-002, FR-004). requireGuest already read the attendance; reading it again is cheap with at
// most 25 guests (research R6).
func (s *server) handleEvent(w http.ResponseWriter, r *http.Request, _ Account, _ Guest) {
	event, ok := s.loadEvent(w, r)
	if !ok {
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
	writeJSON(w, http.StatusOK, eventResponse{
		Event:      event.Summary(),
		Attendance: summarizeAttendance(catalog, statuses),
	})
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

// checkCode enforces the attempt limit per account (FR-014) and records the result of a code
// comparison. It returns false when the account is blocked, after writing the 429 response. A
// blocked account is rejected even with the right code.
func (s *server) checkCode(ctx context.Context, w http.ResponseWriter, uid string, match bool) bool {
	blocked, err := s.attempts.Blocked(ctx, uid)
	if err != nil {
		writeInternalError(w, err)
		return false
	}
	if blocked > 0 {
		writeTooManyAttempts(w, blocked)
		return false
	}
	if err := s.attempts.Record(ctx, uid, match); err != nil {
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

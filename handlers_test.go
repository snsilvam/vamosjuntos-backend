package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Fakes of the interfaces consumed by the handlers. They reuse the pure rules so the tests
// exercise the same transitions and limits as production.

type fakeGuests struct {
	catalog Catalog
	err     error
}

func (f *fakeGuests) Catalog(context.Context) (Catalog, error) { return f.catalog, f.err }

type fakeEvents struct {
	event Event
	err   error
}

func (f *fakeEvents) Get(context.Context) (Event, error) { return f.event, f.err }

type fakeAttendance struct {
	statuses    map[string]Status
	respondedBy map[string]string
}

func (f *fakeAttendance) Statuses(context.Context) (map[string]Status, error) {
	out := make(map[string]Status, len(f.statuses))
	for k, v := range f.statuses {
		out[k] = v
	}
	return out, nil
}

func (f *fakeAttendance) Respond(_ context.Context, guestID string, a action, respondedBy string) (Status, error) {
	next, err := nextStatus(f.statuses[guestID], a)
	if err != nil {
		return "", err
	}
	if next != f.statuses[guestID] {
		f.statuses[guestID] = next
		f.respondedBy[guestID] = respondedBy
	}
	return next, nil
}

type fakeAttempts struct {
	states map[string]attemptState
	now    time.Time
}

func (f *fakeAttempts) Blocked(_ context.Context, uid string) (time.Duration, error) {
	return blockedFor(f.states[uid], f.now), nil
}

func (f *fakeAttempts) Record(_ context.Context, uid string, ok bool) error {
	f.states[uid] = registerAttempt(f.states[uid], ok, f.now)
	return nil
}

// fakeVerifier accepts the tokens it knows; each token stands for one account.
type fakeVerifier map[string]Account

func (f fakeVerifier) Verify(_ context.Context, raw string) (Account, error) {
	a, ok := f[raw]
	if !ok {
		return Account{}, errInvalidToken
	}
	return a, nil
}

// fakeLinks keeps guestID → uid with the same rules as LinkStore.
type fakeLinks map[string]string

func (f fakeLinks) GuestOf(_ context.Context, uid string) (string, bool, error) {
	for guestID, u := range f {
		if u == uid {
			return guestID, true, nil
		}
	}
	return "", false, nil
}

func (f fakeLinks) Link(ctx context.Context, uid, _ string, guestID string) error {
	if linked, ok, _ := f.GuestOf(ctx, uid); ok {
		if linked == guestID {
			return nil
		}
		return errAccountLinked
	}
	if _, ok := f[guestID]; ok {
		return errGuestLinked
	}
	f[guestID] = uid
	return nil
}

type testEnv struct {
	handler    http.Handler
	guests     *fakeGuests
	events     *fakeEvents
	attendance *fakeAttendance
	attempts   *fakeAttempts
	links      fakeLinks
}

// Accounts used by the tests: their token is the account name.
var testAccounts = fakeVerifier{
	"ana":    {UID: "uid-ana", Email: "ana@example.com"},
	"beto":   {UID: "uid-beto", Email: "beto@example.com"},
	"carlos": {UID: "uid-carlos", Email: "carlos@example.com"},
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	catalog, err := newCatalog([]Guest{
		{ID: "carmencita", Name: "Carmencita", code: "prueba01"},
		{ID: "liam", Name: "Liam", code: "prueba02"},
		{ID: "papa-de-lore", Name: "papá de lore", code: "prueba03"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, colombia)
	env := &testEnv{
		guests: &fakeGuests{catalog: catalog},
		events: &fakeEvents{event: Event{
			Name:            "Paseo Familiar Diciembre 2026",
			StartsAt:        time.Date(2026, 12, 23, 14, 0, 0, 0, colombia),
			EndsAt:          time.Date(2026, 12, 26, 11, 0, 0, 0, colombia),
			Destination:     "Acacías, Meta, Colombia",
			DeparturePlaces: []string{"Bogotá", "Tocancipá"},
			Capacity:        25,
		}},
		attendance: &fakeAttendance{statuses: map[string]Status{}, respondedBy: map[string]string{}},
		attempts:   &fakeAttempts{states: map[string]attemptState{}, now: now},
		links:      fakeLinks{},
	}
	srv := NewServer(env.guests, env.events, env.attendance, env.attempts, testAccounts, env.links)
	env.handler = srv.routes()
	return env
}

type request struct {
	method string
	path   string
	body   any
	token  string
}

func (e *testEnv) do(t *testing.T, req request) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	if req.body != nil {
		if err := json.NewEncoder(&body).Encode(req.body); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(req.method, req.path, &body)
	if req.token != "" {
		r.Header.Set("Authorization", "Bearer "+req.token)
	}
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	return w
}

func (e *testEnv) me(t *testing.T, token string) meResponse {
	t.Helper()
	w := e.do(t, request{method: "GET", path: "/api/me", token: token})
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/me: status = %d, body %s", w.Code, w.Body.String())
	}
	var me meResponse
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	return me
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var b errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode error body %q: %v", w.Body.String(), err)
	}
	return b.Error.Code
}

func assertError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", w.Code, status, w.Body.String())
	}
	if got := errorCode(t, w); got != code {
		t.Fatalf("code = %q, want %q", got, code)
	}
}

func linkBody(guestID, code string) map[string]string {
	if guestID == "" {
		return map[string]string{"code": code}
	}
	return map[string]string{"guestId": guestID, "code": code}
}

// --- Fundamentos: token y estado de la cuenta ---

func TestRequestsWithoutValidToken(t *testing.T) {
	env := newTestEnv(t)
	for _, path := range []string{"/api/me", "/api/onboarding/guests"} {
		w := env.do(t, request{method: "GET", path: path})
		assertError(t, w, http.StatusUnauthorized, codeUnauthenticated)
		w = env.do(t, request{method: "GET", path: path, token: "token-falso"})
		assertError(t, w, http.StatusUnauthorized, codeUnauthenticated)
	}
}

func TestMe(t *testing.T) {
	env := newTestEnv(t)
	if got := env.me(t, "ana"); got.Status != meUnlinked || got.Guest != nil {
		t.Fatalf("me = %+v, want unlinked", got)
	}

	env.links["carmencita"] = "uid-ana"
	got := env.me(t, "ana")
	if got.Status != mePending || got.Guest.ID != "carmencita" || got.Event == nil {
		t.Fatalf("me = %+v, want pending with event", got)
	}

	env.attendance.statuses["carmencita"] = statusDeclined
	if got := env.me(t, "ana"); got.Status != meDeclined || got.Event == nil {
		t.Fatalf("me = %+v, want declined with event", got)
	}

	env.attendance.statuses["carmencita"] = statusConfirmed
	if got := env.me(t, "ana"); got.Status != meConfirmed || got.Guest.Name != "Carmencita" {
		t.Fatalf("me = %+v, want confirmed", got)
	}

	// The organizer removes the guest from the catalog.
	env.links["nadie"] = "uid-beto"
	w := env.do(t, request{method: "GET", path: "/api/me", token: "beto"})
	assertError(t, w, http.StatusForbidden, codeNotInvited)
}

// --- US1: primer ingreso ---

func TestGuestsListsOnlyPending(t *testing.T) {
	env := newTestEnv(t)
	env.attendance.statuses["carmencita"] = statusConfirmed
	env.attendance.statuses["liam"] = statusDeclined

	w := env.do(t, request{method: "GET", path: "/api/onboarding/guests", token: "ana"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got guestsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Guests) != 1 || got.Guests[0].ID != "papa-de-lore" {
		t.Fatalf("guests = %+v, want only papa-de-lore", got.Guests)
	}
	if strings.Contains(w.Body.String(), "prueba") {
		t.Fatalf("response leaks codes: %s", w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestGuestsWithoutEvent(t *testing.T) {
	env := newTestEnv(t)
	env.events.err = errEventNotFound
	w := env.do(t, request{method: "GET", path: "/api/onboarding/guests", token: "ana"})
	assertError(t, w, http.StatusNotFound, codeEventNotFound)
}

func TestLinkFromList(t *testing.T) {
	env := newTestEnv(t)

	w := env.do(t, request{method: "POST", path: "/api/onboarding/link", token: "ana", body: linkBody("carmencita", "prueba02")})
	assertError(t, w, http.StatusBadRequest, codeInvalidCode)
	if len(env.links) != 0 {
		t.Fatal("a wrong code created a link")
	}

	w = env.do(t, request{method: "POST", path: "/api/onboarding/link", token: "ana", body: linkBody("nadie", "prueba01")})
	assertError(t, w, http.StatusNotFound, codeGuestNotFound)

	w = env.do(t, request{method: "POST", path: "/api/onboarding/link", token: "ana", body: linkBody("carmencita", " PRUEBA01 ")})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var me meResponse
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me.Status != mePending || me.Guest.ID != "carmencita" || me.Event == nil {
		t.Fatalf("me = %+v, want pending Carmencita with event", me)
	}
	if env.links["carmencita"] != "uid-ana" {
		t.Fatalf("links = %v", env.links)
	}

	// Linking the same pair again is harmless.
	w = env.do(t, request{method: "POST", path: "/api/onboarding/link", token: "ana", body: linkBody("carmencita", "prueba01")})
	if w.Code != http.StatusOK {
		t.Fatalf("relink: status = %d", w.Code)
	}

	// One account, one guest; one guest, one account (FR-005).
	w = env.do(t, request{method: "POST", path: "/api/onboarding/link", token: "ana", body: linkBody("liam", "prueba02")})
	assertError(t, w, http.StatusConflict, codeAccountAlreadyLinked)
	w = env.do(t, request{method: "POST", path: "/api/onboarding/link", token: "beto", body: linkBody("carmencita", "prueba01")})
	assertError(t, w, http.StatusConflict, codeGuestAlreadyLinked)
}

func TestLinkBlocksAfterFiveFailures(t *testing.T) {
	env := newTestEnv(t)
	for i := range maxFailedAttempts {
		w := env.do(t, request{method: "POST", path: "/api/onboarding/link", token: "ana", body: linkBody("carmencita", "malo123")})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: status = %d", i+1, w.Code)
		}
	}
	w := env.do(t, request{method: "POST", path: "/api/onboarding/link", token: "ana", body: linkBody("carmencita", "prueba01")})
	assertError(t, w, http.StatusTooManyRequests, codeTooManyAttempts)
	if w.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After")
	}

	// The block belongs to the account, not to everybody.
	w = env.do(t, request{method: "POST", path: "/api/onboarding/link", token: "beto", body: linkBody("liam", "prueba02")})
	if w.Code != http.StatusOK {
		t.Fatalf("another account: status = %d", w.Code)
	}
}

func TestRespond(t *testing.T) {
	env := newTestEnv(t)

	w := env.do(t, request{method: "POST", path: "/api/onboarding/respond", token: "ana", body: map[string]bool{"attending": true}})
	assertError(t, w, http.StatusForbidden, codeNotLinked)

	env.links["carmencita"] = "uid-ana"
	w = env.do(t, request{method: "POST", path: "/api/onboarding/respond", token: "ana", body: map[string]string{}})
	assertError(t, w, http.StatusBadRequest, codeInvalidRequest)

	w = env.do(t, request{method: "POST", path: "/api/onboarding/respond", token: "ana", body: map[string]bool{"attending": true}})
	if w.Code != http.StatusOK || env.attendance.statuses["carmencita"] != statusConfirmed {
		t.Fatalf("confirm: status = %d, attendance = %v", w.Code, env.attendance.statuses)
	}
	if env.attendance.respondedBy["carmencita"] != "carmencita" {
		t.Errorf("respondedBy = %q", env.attendance.respondedBy["carmencita"])
	}

	w = env.do(t, request{method: "POST", path: "/api/onboarding/respond", token: "ana", body: map[string]bool{"attending": false}})
	assertError(t, w, http.StatusConflict, codeAlreadyConfirmed)
}

// --- US3: acceso protegido ---

func TestProtectedRoutes(t *testing.T) {
	env := newTestEnv(t)
	path := "/api/guests/liam/confirmation"
	body := map[string]string{"code": "prueba02"}

	w := env.do(t, request{method: "POST", path: path, body: body})
	assertError(t, w, http.StatusUnauthorized, codeUnauthenticated)

	w = env.do(t, request{method: "POST", path: path, token: "ana", body: body})
	assertError(t, w, http.StatusForbidden, codeNotLinked)

	env.links["carmencita"] = "uid-ana" // linked but still pending
	w = env.do(t, request{method: "POST", path: path, token: "ana", body: body})
	assertError(t, w, http.StatusForbidden, codeNotInvited)

	env.links["nadie"] = "uid-beto" // linked to a guest removed from the catalog
	w = env.do(t, request{method: "POST", path: path, token: "beto", body: body})
	assertError(t, w, http.StatusForbidden, codeNotInvited)
}

// --- US4: vincularse solo con el código ---

func TestLinkByCode(t *testing.T) {
	env := newTestEnv(t)
	env.attendance.statuses["liam"] = statusConfirmed // someone else confirmed him
	env.attendance.statuses["papa-de-lore"] = statusDeclined

	w := env.do(t, request{method: "POST", path: "/api/onboarding/link", token: "ana", body: linkBody("", "noexiste")})
	assertError(t, w, http.StatusBadRequest, codeInvalidCode)

	tests := []struct {
		token, code, want string
	}{
		{"ana", "prueba02", meConfirmed},
		{"beto", "prueba01", mePending},
		{"carlos", "prueba03", meDeclined},
	}
	for _, tt := range tests {
		w := env.do(t, request{method: "POST", path: "/api/onboarding/link", token: tt.token, body: linkBody("", tt.code)})
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body %s", tt.token, w.Code, w.Body.String())
		}
		var me meResponse
		if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
			t.Fatal(err)
		}
		if me.Status != tt.want {
			t.Errorf("%s: status = %q, want %q", tt.token, me.Status, tt.want)
		}
	}
}

// --- US5: cambiar de opinión ---

func TestDeclinedGuestCanConfirmLater(t *testing.T) {
	env := newTestEnv(t)
	env.links["carmencita"] = "uid-ana"

	w := env.do(t, request{method: "POST", path: "/api/onboarding/respond", token: "ana", body: map[string]bool{"attending": false}})
	if w.Code != http.StatusOK || env.attendance.statuses["carmencita"] != statusDeclined {
		t.Fatalf("decline: status = %d", w.Code)
	}
	if got := env.me(t, "ana"); got.Status != meDeclined {
		t.Fatalf("me = %+v, want declined", got)
	}
	w = env.do(t, request{method: "POST", path: "/api/onboarding/respond", token: "ana", body: map[string]bool{"attending": true}})
	if w.Code != http.StatusOK {
		t.Fatalf("confirm later: status = %d", w.Code)
	}
	if got := env.me(t, "ana"); got.Status != meConfirmed {
		t.Fatalf("me = %+v, want confirmed", got)
	}
}

// --- US6: confirmar a otro invitado ---

func TestConfirmOther(t *testing.T) {
	env := newTestEnv(t)
	env.links["carmencita"] = "uid-ana"
	env.attendance.statuses["carmencita"] = statusConfirmed
	path := "/api/guests/liam/confirmation"

	w := env.do(t, request{method: "POST", path: path, token: "ana", body: map[string]string{"code": "prueba01"}})
	assertError(t, w, http.StatusBadRequest, codeInvalidCode)

	w = env.do(t, request{method: "POST", path: "/api/guests/nadie/confirmation", token: "ana", body: map[string]string{"code": "prueba02"}})
	assertError(t, w, http.StatusNotFound, codeGuestNotFound)

	w = env.do(t, request{method: "POST", path: path, token: "ana", body: map[string]string{"code": "prueba02"}})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if env.attendance.statuses["liam"] != statusConfirmed || env.attendance.respondedBy["liam"] != "carmencita" {
		t.Fatalf("attendance = %v, respondedBy = %v", env.attendance.statuses, env.attendance.respondedBy)
	}
	if len(env.links) != 1 {
		t.Fatalf("confirming another guest created a link: %v", env.links)
	}

	w = env.do(t, request{method: "POST", path: path, token: "ana", body: map[string]string{"code": "prueba02"}})
	assertError(t, w, http.StatusConflict, codeAlreadyResponded)
}

// --- Catálogo en Firestore (enmienda v2.0.0) ---

// The catalog is read on every request, so the organizer's edits apply without a restart.
func TestCatalogChangesApplyRightAway(t *testing.T) {
	env := newTestEnv(t)
	catalog, err := newCatalog([]Guest{
		{ID: "carmencita", Name: "Carmencita", code: "prueba01"},
		{ID: "nuevo", Name: "Invitado Nuevo", code: "prueba09"},
	})
	if err != nil {
		t.Fatal(err)
	}
	env.guests.catalog = catalog

	w := env.do(t, request{method: "GET", path: "/api/onboarding/guests", token: "ana"})
	var got guestsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Guests) != 2 || got.Guests[1].ID != "nuevo" {
		t.Fatalf("guests = %+v, want Carmencita and Invitado Nuevo", got.Guests)
	}

	// Liam was removed from the catalog: his linked account loses access.
	env.links["liam"] = "uid-beto"
	env.attendance.statuses["liam"] = statusConfirmed
	w = env.do(t, request{method: "GET", path: "/api/me", token: "beto"})
	assertError(t, w, http.StatusForbidden, codeNotInvited)
}

func TestCatalogUnavailable(t *testing.T) {
	env := newTestEnv(t)
	env.links["carmencita"] = "uid-ana"
	env.guests.err = errors.New(`invitados: "Ana" y "Luis" tienen el mismo código`)

	w := env.do(t, request{method: "GET", path: "/api/onboarding/guests", token: "ana"})
	assertError(t, w, http.StatusInternalServerError, codeInternal)
	if strings.Contains(w.Body.String(), "Ana") {
		t.Fatalf("response leaks the internal error: %s", w.Body.String())
	}

	w = env.do(t, request{method: "POST", path: "/api/onboarding/link", token: "beto", body: linkBody("", "prueba01")})
	assertError(t, w, http.StatusInternalServerError, codeInternal)

	w = env.do(t, request{method: "GET", path: "/api/me", token: "ana"})
	assertError(t, w, http.StatusInternalServerError, codeInternal)
}

// --- Spec 003: card "Próximo viaje" ---

func TestEvent(t *testing.T) {
	env := newTestEnv(t)
	env.links["carmencita"] = "uid-ana"
	env.attendance.statuses["carmencita"] = statusConfirmed
	env.attendance.statuses["papa-de-lore"] = statusDeclined

	w := env.do(t, request{method: "GET", path: "/api/event", token: "ana"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got eventResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	wantEvent := EventSummary{
		Name:            "Paseo Familiar Diciembre 2026",
		StartsAt:        "2026-12-23T14:00:00-05:00",
		EndsAt:          "2026-12-26T11:00:00-05:00",
		Destination:     "Acacías, Meta, Colombia",
		DeparturePlaces: []string{"Bogotá", "Tocancipá"},
	}
	if got.Event.Name != wantEvent.Name || got.Event.StartsAt != wantEvent.StartsAt ||
		got.Event.EndsAt != wantEvent.EndsAt || got.Event.Destination != wantEvent.Destination ||
		strings.Join(got.Event.DeparturePlaces, ",") != strings.Join(wantEvent.DeparturePlaces, ",") {
		t.Fatalf("event = %+v, want %+v", got.Event, wantEvent)
	}
	wantAttendance := AttendanceSummary{Invited: 3, Confirmed: 1, Declined: 1, Pending: 1}
	if got.Attendance != wantAttendance {
		t.Fatalf("attendance = %+v, want %+v", got.Attendance, wantAttendance)
	}

	// Only counts: no guest names, ids or codes (FR-007).
	body := w.Body.String()
	for _, leak := range []string{"Carmencita", "carmencita", "Liam", "liam", "papá de lore", "papa-de-lore", "prueba0"} {
		if strings.Contains(body, leak) {
			t.Fatalf("response leaks %q: %s", leak, body)
		}
	}
}

func TestEventCountsChangeAfterConfirmingOther(t *testing.T) {
	env := newTestEnv(t)
	env.links["carmencita"] = "uid-ana"
	env.attendance.statuses["carmencita"] = statusConfirmed

	w := env.do(t, request{method: "POST", path: "/api/guests/liam/confirmation", token: "ana", body: map[string]string{"code": "prueba02"}})
	if w.Code != http.StatusOK {
		t.Fatalf("confirm other: status = %d, body %s", w.Code, w.Body.String())
	}
	w = env.do(t, request{method: "GET", path: "/api/event", token: "ana"})
	var got eventResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if want := (AttendanceSummary{Invited: 3, Confirmed: 2, Pending: 1}); got.Attendance != want {
		t.Fatalf("attendance = %+v, want %+v", got.Attendance, want)
	}
}

func TestEventErrors(t *testing.T) {
	env := newTestEnv(t)
	path := "/api/event"

	w := env.do(t, request{method: "GET", path: path})
	assertError(t, w, http.StatusUnauthorized, codeUnauthenticated)

	w = env.do(t, request{method: "GET", path: path, token: "ana"})
	assertError(t, w, http.StatusForbidden, codeNotLinked)

	env.links["carmencita"] = "uid-ana" // linked but still pending
	w = env.do(t, request{method: "GET", path: path, token: "ana"})
	assertError(t, w, http.StatusForbidden, codeNotInvited)

	env.attendance.statuses["carmencita"] = statusConfirmed
	env.events.err = errEventNotFound
	w = env.do(t, request{method: "GET", path: path, token: "ana"})
	assertError(t, w, http.StatusNotFound, codeEventNotFound)
}

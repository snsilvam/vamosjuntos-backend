package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Fakes of the interfaces consumed by the handlers. They reuse the pure rules so the tests
// exercise the same transitions and limits as production.

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

func (f *fakeAttempts) Blocked(_ context.Context, deviceID string) (time.Duration, error) {
	return blockedFor(f.states[deviceID], f.now), nil
}

func (f *fakeAttempts) Record(_ context.Context, deviceID string, ok bool) error {
	f.states[deviceID] = registerAttempt(f.states[deviceID], ok, f.now)
	return nil
}

const testDevice = "11111111-2222-3333-4444-555555555555"

type testEnv struct {
	srv        *server
	handler    http.Handler
	events     *fakeEvents
	attendance *fakeAttendance
	attempts   *fakeAttempts
	now        time.Time
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	catalog, err := parseCatalog(`[["Carmencita","prueba01"],["Liam","prueba02"],["papá de lore","prueba03"]]`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, colombia)
	env := &testEnv{
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
		now:        now,
	}
	env.srv = NewServer(catalog, env.events, env.attendance, env.attempts, bytes.Repeat([]byte{1}, sessionKeyBytes))
	env.srv.now = func() time.Time { return env.now }
	env.handler = env.srv.routes()
	return env
}

type request struct {
	method  string
	path    string
	body    any
	device  string
	session string
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
	if req.device != "" {
		r.Header.Set("X-Device-Id", req.device)
	}
	if req.session != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: req.session})
	}
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	return w
}

// sessionFor returns a valid session token for guestID.
func (e *testEnv) sessionFor(guestID string) string {
	return signToken(e.srv.sessionKey, guestID, sessionExpiry(e.events.event))
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

func sessionCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	return nil
}

func respondBody(guestID, code string, attending bool) map[string]any {
	return map[string]any{"guestId": guestID, "code": code, "attending": attending}
}

// --- US1: primer ingreso y confirmación ---

func TestGuestsListsOnlyPending(t *testing.T) {
	env := newTestEnv(t)
	env.attendance.statuses["carmencita"] = statusConfirmed
	env.attendance.statuses["liam"] = statusDeclined

	w := env.do(t, request{method: "GET", path: "/api/onboarding/guests"})
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
	w := env.do(t, request{method: "GET", path: "/api/onboarding/guests"})
	assertError(t, w, http.StatusNotFound, codeEventNotFound)
}

func TestVerify(t *testing.T) {
	env := newTestEnv(t)

	w := env.do(t, request{method: "POST", path: "/api/onboarding/verify", device: testDevice,
		body: map[string]string{"guestId": "carmencita", "code": " PRUEBA01 "}})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got eventResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Event.StartsAt != "2026-12-23T14:00:00-05:00" {
		t.Errorf("startsAt = %q, want Colombia time", got.Event.StartsAt)
	}

	w = env.do(t, request{method: "POST", path: "/api/onboarding/verify", device: testDevice,
		body: map[string]string{"guestId": "carmencita", "code": "prueba02"}})
	assertError(t, w, http.StatusUnauthorized, codeInvalidCode)
	if env.attempts.states[testDevice].Failures != 1 {
		t.Errorf("failed attempt not recorded: %+v", env.attempts.states[testDevice])
	}

	w = env.do(t, request{method: "POST", path: "/api/onboarding/verify",
		body: map[string]string{"guestId": "carmencita", "code": "prueba01"}})
	assertError(t, w, http.StatusBadRequest, codeMissingDeviceID)

	w = env.do(t, request{method: "POST", path: "/api/onboarding/verify", device: testDevice,
		body: map[string]string{"guestId": "nadie", "code": "prueba01"}})
	assertError(t, w, http.StatusNotFound, codeGuestNotFound)

	env.attendance.statuses["liam"] = statusConfirmed
	w = env.do(t, request{method: "POST", path: "/api/onboarding/verify", device: testDevice,
		body: map[string]string{"guestId": "liam", "code": "prueba02"}})
	assertError(t, w, http.StatusConflict, codeAlreadyResponded)
}

func TestVerifyBlocksAfterFiveFailures(t *testing.T) {
	env := newTestEnv(t)
	wrong := map[string]string{"guestId": "carmencita", "code": "incorrecto"}
	for i := 0; i < maxFailedAttempts; i++ {
		w := env.do(t, request{method: "POST", path: "/api/onboarding/verify", device: testDevice, body: wrong})
		assertError(t, w, http.StatusUnauthorized, codeInvalidCode)
	}

	right := map[string]string{"guestId": "carmencita", "code": "prueba01"}
	w := env.do(t, request{method: "POST", path: "/api/onboarding/verify", device: testDevice, body: right})
	assertError(t, w, http.StatusTooManyRequests, codeTooManyAttempts)
	if w.Header().Get("Retry-After") != "900" {
		t.Errorf("Retry-After = %q, want 900", w.Header().Get("Retry-After"))
	}

	env.attempts.now = env.attempts.now.Add(blockDuration)
	w = env.do(t, request{method: "POST", path: "/api/onboarding/verify", device: testDevice, body: right})
	if w.Code != http.StatusOK {
		t.Fatalf("after the block status = %d, body %s", w.Code, w.Body.String())
	}
}

func TestRespondConfirmStartsSession(t *testing.T) {
	env := newTestEnv(t)
	w := env.do(t, request{method: "POST", path: "/api/onboarding/respond", device: testDevice,
		body: respondBody("carmencita", "prueba01", true)})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	c := sessionCookie(w)
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Fatalf("session cookie = %+v", c)
	}
	if !c.Expires.Equal(sessionExpiry(env.events.event).Truncate(time.Second)) {
		t.Errorf("cookie expires %v, want %v", c.Expires, sessionExpiry(env.events.event))
	}
	if env.attendance.statuses["carmencita"] != statusConfirmed || env.attendance.respondedBy["carmencita"] != "carmencita" {
		t.Errorf("attendance = %v, respondedBy = %v", env.attendance.statuses, env.attendance.respondedBy)
	}

	// Confirming again (second device) is idempotent and also gets a session.
	w = env.do(t, request{method: "POST", path: "/api/onboarding/respond", device: testDevice,
		body: respondBody("carmencita", "prueba01", true)})
	if w.Code != http.StatusOK || sessionCookie(w) == nil {
		t.Fatalf("second confirmation: status = %d, cookie = %v", w.Code, sessionCookie(w))
	}
}

func TestRespondRejectsBadRequests(t *testing.T) {
	env := newTestEnv(t)

	w := env.do(t, request{method: "POST", path: "/api/onboarding/respond", device: testDevice,
		body: map[string]string{"guestId": "carmencita", "code": "prueba01"}})
	assertError(t, w, http.StatusBadRequest, codeInvalidRequest)

	w = env.do(t, request{method: "POST", path: "/api/onboarding/respond", device: testDevice,
		body: respondBody("carmencita", "prueba02", true)})
	assertError(t, w, http.StatusUnauthorized, codeInvalidCode)
	if sessionCookie(w) != nil {
		t.Fatal("a wrong code started a session")
	}
}

// --- US2: acceso protegido por sesión ---

func TestSessionRequired(t *testing.T) {
	env := newTestEnv(t)
	env.attendance.statuses["carmencita"] = statusConfirmed
	env.attendance.statuses["liam"] = statusDeclined

	w := env.do(t, request{method: "GET", path: "/api/session"})
	assertError(t, w, http.StatusUnauthorized, codeUnauthenticated)

	token := env.sessionFor("carmencita")
	payload, _, _ := strings.Cut(token, ".")
	w = env.do(t, request{method: "GET", path: "/api/session", session: payload + ".firmafalsa"})
	assertError(t, w, http.StatusUnauthorized, codeUnauthenticated)

	w = env.do(t, request{method: "GET", path: "/api/session", session: env.sessionFor("alguien-que-ya-no-esta")})
	assertError(t, w, http.StatusForbidden, codeNotInvited)

	w = env.do(t, request{method: "GET", path: "/api/session", session: env.sessionFor("liam")})
	assertError(t, w, http.StatusForbidden, codeNotInvited)

	w = env.do(t, request{method: "GET", path: "/api/session", session: token})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got sessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Guest.ID != "carmencita" || got.Guest.Name != "Carmencita" {
		t.Errorf("guest = %+v", got.Guest)
	}

	env.now = sessionExpiry(env.events.event)
	w = env.do(t, request{method: "GET", path: "/api/session", session: token})
	assertError(t, w, http.StatusUnauthorized, codeUnauthenticated)
}

// --- US3: volver a entrar con el código ---

func TestReentry(t *testing.T) {
	env := newTestEnv(t)
	env.attendance.statuses["carmencita"] = statusConfirmed
	env.attendance.statuses["papa-de-lore"] = statusDeclined
	reentry := func(code string) *httptest.ResponseRecorder {
		return env.do(t, request{method: "POST", path: "/api/session/reentry", device: testDevice,
			body: map[string]string{"code": code}})
	}

	w := reentry("PRUEBA01")
	if w.Code != http.StatusOK || sessionCookie(w) == nil {
		t.Fatalf("confirmed: status = %d, cookie = %v", w.Code, sessionCookie(w))
	}

	w = reentry("prueba03")
	if w.Code != http.StatusOK {
		t.Fatalf("declined: status = %d", w.Code)
	}
	if sessionCookie(w) != nil {
		t.Error("declined guest got a session")
	}
	var declined statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &declined); err != nil {
		t.Fatal(err)
	}
	if declined.Status != statusDeclined || declined.Guest == nil || declined.Guest.ID != "papa-de-lore" || declined.Event == nil {
		t.Errorf("declined response = %+v", declined)
	}

	// The declined guest changes their mind through /respond.
	w = env.do(t, request{method: "POST", path: "/api/onboarding/respond", device: testDevice,
		body: respondBody("papa-de-lore", "prueba03", true)})
	if w.Code != http.StatusOK || env.attendance.statuses["papa-de-lore"] != statusConfirmed {
		t.Fatalf("change of mind: status = %d, attendance = %v", w.Code, env.attendance.statuses)
	}

	w = reentry("prueba02")
	assertError(t, w, http.StatusConflict, codePendingUseList)

	before := env.attempts.states[testDevice].Failures
	w = reentry("noexiste")
	assertError(t, w, http.StatusUnauthorized, codeInvalidCode)
	if env.attempts.states[testDevice].Failures != before+1 {
		t.Error("an unknown code did not count for the block")
	}
}

// --- US4: confirmar a otro invitado ---

func TestConfirmOther(t *testing.T) {
	env := newTestEnv(t)
	env.attendance.statuses["carmencita"] = statusConfirmed
	session := env.sessionFor("carmencita")
	confirm := func(guestID, code string) *httptest.ResponseRecorder {
		return env.do(t, request{method: "POST", path: "/api/guests/" + guestID + "/confirmation",
			device: testDevice, session: session, body: map[string]string{"code": code}})
	}

	w := confirm("liam", "prueba01")
	assertError(t, w, http.StatusBadRequest, codeInvalidCode)

	w = confirm("liam", "prueba02")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if sessionCookie(w) != nil {
		t.Error("confirming another guest changed the session")
	}
	if env.attendance.statuses["liam"] != statusConfirmed || env.attendance.respondedBy["liam"] != "carmencita" {
		t.Errorf("attendance = %v, respondedBy = %v", env.attendance.statuses, env.attendance.respondedBy)
	}

	w = confirm("liam", "prueba02")
	assertError(t, w, http.StatusConflict, codeAlreadyResponded)

	w = confirm("nadie", "prueba02")
	assertError(t, w, http.StatusNotFound, codeGuestNotFound)

	w = env.do(t, request{method: "POST", path: "/api/guests/liam/confirmation", device: testDevice,
		body: map[string]string{"code": "prueba02"}})
	assertError(t, w, http.StatusUnauthorized, codeUnauthenticated)
}

// --- US5: decir que no asistirá ---

func TestRespondDecline(t *testing.T) {
	env := newTestEnv(t)
	w := env.do(t, request{method: "POST", path: "/api/onboarding/respond", device: testDevice,
		body: respondBody("liam", "prueba02", false)})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if sessionCookie(w) != nil {
		t.Error("declining started a session")
	}
	if env.attendance.statuses["liam"] != statusDeclined {
		t.Errorf("attendance = %v", env.attendance.statuses)
	}

	w = env.do(t, request{method: "GET", path: "/api/onboarding/guests"})
	if strings.Contains(w.Body.String(), `"liam"`) {
		t.Errorf("declined guest still listed: %s", w.Body.String())
	}

	env.attendance.statuses["carmencita"] = statusConfirmed
	w = env.do(t, request{method: "POST", path: "/api/onboarding/respond", device: testDevice,
		body: respondBody("carmencita", "prueba01", false)})
	assertError(t, w, http.StatusConflict, codeAlreadyConfirmed)
}

// --- US6: cerrar sesión ---

func TestLogout(t *testing.T) {
	env := newTestEnv(t)
	w := env.do(t, request{method: "DELETE", path: "/api/session"})
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d", w.Code)
	}
	c := sessionCookie(w)
	if c == nil || c.MaxAge >= 0 || c.Value != "" {
		t.Fatalf("logout cookie = %+v, want an expired __session", c)
	}
	if !strings.Contains(w.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Errorf("Set-Cookie = %q, want Max-Age=0", w.Header().Get("Set-Cookie"))
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
)

// fakeItinerary keeps activities in memory, with the same not-found rules as ItineraryStore.
type fakeItinerary struct {
	activities []Activity
	nextID     int
}

func (f *fakeItinerary) List(context.Context) ([]Activity, error) {
	return append([]Activity{}, f.activities...), nil
}

func (f *fakeItinerary) Create(_ context.Context, in ActivityInput, _ string) error {
	f.nextID++
	f.activities = append(f.activities, withActivityInput(fmt.Sprintf("a%d", f.nextID), in))
	return nil
}

func (f *fakeItinerary) index(id string) int {
	return slices.IndexFunc(f.activities, func(a Activity) bool { return a.ID == id })
}

func (f *fakeItinerary) Update(_ context.Context, id string, in ActivityInput, _ string) error {
	i := f.index(id)
	if i < 0 {
		return errActivityNotFound
	}
	f.activities[i] = withActivityInput(id, in)
	return nil
}

func (f *fakeItinerary) Delete(_ context.Context, id string) error {
	i := f.index(id)
	if i < 0 {
		return errActivityNotFound
	}
	f.activities = slices.Delete(f.activities, i, i+1)
	return nil
}

func decodeItinerary(t *testing.T, body []byte) itineraryResponse {
	t.Helper()
	var r itineraryResponse
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return r
}

func titles(r itineraryResponse) []string {
	var out []string
	for _, a := range r.Activities {
		out = append(out, a.Title)
	}
	return out
}

func TestItineraryRequiresConfirmedGuest(t *testing.T) {
	env := transportEnv(t)
	w := env.do(t, request{method: "GET", path: "/api/itinerary"})
	assertError(t, w, http.StatusUnauthorized, codeUnauthenticated)
	w = env.do(t, request{method: "GET", path: "/api/itinerary", token: "carlos"})
	assertError(t, w, http.StatusForbidden, codeNotLinked)
}

func TestItineraryFlow(t *testing.T) {
	env := transportEnv(t)
	activity := func(title, date, start, end string) map[string]any {
		return map[string]any{"title": title, "date": date, "startTime": start, "endTime": end}
	}

	// US1: empty itinerary with the days of the event.
	w := env.do(t, request{method: "GET", path: "/api/itinerary", token: "ana"})
	got := decodeItinerary(t, w.Body.Bytes())
	if w.Code != http.StatusOK || len(got.Activities) != 0 || !slices.Equal(got.Days, tripDays) {
		t.Fatalf("empty: %d %s", w.Code, w.Body.String())
	}

	// US2: add activities; invalid ones are rejected with their own codes.
	w = env.do(t, request{method: "POST", path: "/api/itinerary/activities", token: "ana", body: activity("Piscina", "2026-12-24", "10:00", "12:00")})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	w = env.do(t, request{method: "POST", path: "/api/itinerary/activities", token: "beto", body: activity("Desayuno", "2026-12-24", "08:00", "")})
	got = decodeItinerary(t, w.Body.Bytes())
	if w.Code != http.StatusCreated || !slices.Equal(titles(got), []string{"Desayuno", "Piscina"}) {
		t.Fatalf("second create: %d %s", w.Code, w.Body.String())
	}
	w = env.do(t, request{method: "POST", path: "/api/itinerary/activities", token: "ana", body: activity("Piscina", "2026-12-24", "10:00", "09:00")})
	assertError(t, w, http.StatusBadRequest, codeActivityTimeOrder)
	w = env.do(t, request{method: "POST", path: "/api/itinerary/activities", token: "ana", body: activity("Piscina", "2026-12-27", "10:00", "")})
	assertError(t, w, http.StatusBadRequest, codeActivityOutOfRange)
	w = env.do(t, request{method: "POST", path: "/api/itinerary/activities", token: "ana", body: activity("", "2026-12-24", "10:00", "")})
	assertError(t, w, http.StatusBadRequest, codeInvalidActivity)
	w = env.do(t, request{method: "POST", path: "/api/itinerary/activities", token: "ana", body: "no es un objeto"})
	assertError(t, w, http.StatusBadRequest, codeInvalidRequest)
	nochebuena := activity("Cena de Nochebuena", "2026-12-24", "20:00", "01:00")
	nochebuena["endsNextDay"] = true
	w = env.do(t, request{method: "POST", path: "/api/itinerary/activities", token: "ana", body: nochebuena})
	got = decodeItinerary(t, w.Body.Bytes())
	if w.Code != http.StatusCreated || !got.Activities[2].EndsNextDay {
		t.Fatalf("crossing midnight: %d %s", w.Code, w.Body.String())
	}

	// US3: editing the time reorders the day; deleting removes it.
	piscina := "/api/itinerary/activities/" + got.Activities[1].ID
	w = env.do(t, request{method: "PUT", path: piscina, token: "beto", body: activity("Piscina", "2026-12-24", "07:00", "")})
	got = decodeItinerary(t, w.Body.Bytes())
	if w.Code != http.StatusOK || !slices.Equal(titles(got), []string{"Piscina", "Desayuno", "Cena de Nochebuena"}) {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	w = env.do(t, request{method: "DELETE", path: piscina, token: "ana"})
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	w = env.do(t, request{method: "DELETE", path: piscina, token: "ana"})
	assertError(t, w, http.StatusNotFound, codeActivityNotFound)
	w = env.do(t, request{method: "PUT", path: piscina, token: "ana", body: activity("Piscina", "2026-12-24", "07:00", "")})
	assertError(t, w, http.StatusNotFound, codeActivityNotFound)
	w = env.do(t, request{method: "GET", path: "/api/itinerary", token: "beto"})
	if got = decodeItinerary(t, w.Body.Bytes()); !slices.Equal(titles(got), []string{"Desayuno", "Cena de Nochebuena"}) {
		t.Fatalf("after delete: %s", w.Body.String())
	}
}

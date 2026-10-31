package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// fakeTransport keeps vehicles in memory and reuses the pure rule, like the real store.
type fakeTransport struct {
	vehicles []Vehicle
	nextID   int
}

func (f *fakeTransport) List(context.Context) ([]Vehicle, error) {
	return append([]Vehicle{}, f.vehicles...), nil
}

func (f *fakeTransport) Apply(_ context.Context, cmd transportCmd, confirmed map[string]bool, _ string) error {
	if cmd.op == opCreateVehicle {
		f.nextID++
		cmd.vehicleID = fmt.Sprintf("v%d", f.nextID)
	}
	r, err := applyTransport(f.vehicles, cmd, confirmed)
	if err != nil {
		return err
	}
	for _, v := range r.put {
		if i := vehicleIndex(f.vehicles, v.ID); i >= 0 {
			f.vehicles[i] = v
		} else {
			f.vehicles = append(f.vehicles, v)
		}
	}
	if r.delete != "" {
		f.vehicles = append(f.vehicles[:vehicleIndex(f.vehicles, r.delete)], f.vehicles[vehicleIndex(f.vehicles, r.delete)+1:]...)
	}
	return nil
}

// transportEnv has Carmencita (ana) and Liam (beto) confirmed and linked; papá de lore pending.
func transportEnv(t *testing.T) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	env.links["carmencita"] = "uid-ana"
	env.links["liam"] = "uid-beto"
	env.attendance.statuses["carmencita"] = statusConfirmed
	env.attendance.statuses["liam"] = statusConfirmed
	return env
}

func decodeTransport(t *testing.T, body []byte) transportResponse {
	t.Helper()
	var r transportResponse
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return r
}

func TestTransportRequiresConfirmedGuest(t *testing.T) {
	env := transportEnv(t)
	w := env.do(t, request{method: "GET", path: "/api/transport"})
	assertError(t, w, http.StatusUnauthorized, codeUnauthenticated)
	w = env.do(t, request{method: "GET", path: "/api/transport", token: "carlos"})
	assertError(t, w, http.StatusForbidden, codeNotLinked)
}

func TestTransportFlow(t *testing.T) {
	env := transportEnv(t)

	// US1: empty module lists every confirmed guest without transport.
	w := env.do(t, request{method: "GET", path: "/api/transport", token: "ana"})
	got := decodeTransport(t, w.Body.Bytes())
	if w.Code != http.StatusOK || len(got.Vehicles) != 0 || len(got.WithoutTransport) != 2 || len(got.Confirmed) != 2 {
		t.Fatalf("empty: %d %+v", w.Code, got)
	}

	// US2: register a vehicle; invalid data and an unavailable driver are rejected.
	w = env.do(t, request{method: "POST", path: "/api/transport/vehicles", token: "ana", body: map[string]any{"name": "Carro", "driverId": "carmencita", "departure": "bogota", "seats": 0}})
	assertError(t, w, http.StatusBadRequest, codeInvalidVehicle)
	w = env.do(t, request{method: "POST", path: "/api/transport/vehicles", token: "ana", body: map[string]any{"name": "Carro", "driverId": "papa-de-lore", "departure": "bogota", "seats": 2}})
	assertError(t, w, http.StatusConflict, codeDriverUnavailable)
	w = env.do(t, request{method: "POST", path: "/api/transport/vehicles", token: "ana", body: map[string]any{"name": "Carro de Carmencita", "driverId": "carmencita", "departure": "bogota", "seats": 1, "departureTime": "06:30"}})
	got = decodeTransport(t, w.Body.Bytes())
	if w.Code != http.StatusCreated || len(got.Vehicles) != 1 || got.Vehicles[0].Free != 1 || got.Vehicles[0].Driver.Name != "Carmencita" {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if len(got.WithoutTransport) != 1 || got.WithoutTransport[0].ID != "liam" {
		t.Fatalf("withoutTransport = %+v, want only Liam", got.WithoutTransport)
	}
	vehicle := "/api/transport/vehicles/" + got.Vehicles[0].ID

	// US3: reserve for another guest, a driver cannot ride, a full vehicle rejects.
	w = env.do(t, request{method: "POST", path: vehicle + "/passengers", token: "ana", body: map[string]string{"guestId": "carmencita"}})
	assertError(t, w, http.StatusConflict, codeGuestIsDriver)
	w = env.do(t, request{method: "POST", path: vehicle + "/passengers", token: "ana", body: map[string]string{"guestId": "papa-de-lore"}})
	assertError(t, w, http.StatusConflict, codeGuestNotConfirmed)
	w = env.do(t, request{method: "POST", path: vehicle + "/passengers", token: "ana", body: map[string]string{"guestId": "liam"}})
	got = decodeTransport(t, w.Body.Bytes())
	if w.Code != http.StatusOK || got.Vehicles[0].Free != 0 || len(got.WithoutTransport) != 0 {
		t.Fatalf("reserve: %d %s", w.Code, w.Body.String())
	}
	env.attendance.statuses["papa-de-lore"] = statusConfirmed
	w = env.do(t, request{method: "POST", path: vehicle + "/passengers", token: "beto", body: map[string]string{"guestId": "papa-de-lore"}})
	assertError(t, w, http.StatusConflict, codeVehicleFull)

	// US4: seats cannot go below passengers.
	w = env.do(t, request{method: "PUT", path: vehicle, token: "beto", body: map[string]any{"name": "Carro", "driverId": "carmencita", "departure": "bogota", "seats": 0}})
	assertError(t, w, http.StatusBadRequest, codeInvalidVehicle)

	// Release, then delete: passengers end up without transport.
	w = env.do(t, request{method: "DELETE", path: vehicle + "/passengers/liam", token: "beto"})
	if w.Code != http.StatusNoContent {
		t.Fatalf("release: %d", w.Code)
	}
	w = env.do(t, request{method: "DELETE", path: vehicle + "/passengers/liam", token: "beto"})
	assertError(t, w, http.StatusNotFound, codePassengerNotFound)
	w = env.do(t, request{method: "POST", path: vehicle + "/passengers", token: "beto", body: map[string]string{"guestId": "liam"}})
	if w.Code != http.StatusOK {
		t.Fatalf("reserve again: %d", w.Code)
	}
	w = env.do(t, request{method: "DELETE", path: vehicle, token: "beto"})
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	w = env.do(t, request{method: "GET", path: "/api/transport", token: "ana"})
	got = decodeTransport(t, w.Body.Bytes())
	if len(got.Vehicles) != 0 || len(got.WithoutTransport) != 3 {
		t.Fatalf("after delete: %+v", got)
	}
	w = env.do(t, request{method: "DELETE", path: vehicle, token: "beto"})
	assertError(t, w, http.StatusNotFound, codeVehicleNotFound)
}

package main

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestValidateVehicleInput(t *testing.T) {
	valid := VehicleInput{Name: " Carro de Ana ", DriverID: "ana", Departure: departureBogota, Seats: 4, DepartureTime: "06:30", Note: "Salimos del parque"}
	got, err := validateVehicleInput(valid)
	if err != nil || got.Name != "Carro de Ana" {
		t.Fatalf("valid input: %+v, %v", got, err)
	}

	tests := []struct {
		name   string
		change func(*VehicleInput)
	}{
		{"empty name", func(in *VehicleInput) { in.Name = "   " }},
		{"long name", func(in *VehicleInput) { in.Name = strings.Repeat("a", maxVehicleNameLen+1) }},
		{"unknown departure", func(in *VehicleInput) { in.Departure = "medellin" }},
		{"zero seats", func(in *VehicleInput) { in.Seats = 0 }},
		{"25 seats", func(in *VehicleInput) { in.Seats = 25 }},
		{"bad time", func(in *VehicleInput) { in.DepartureTime = "25:00" }},
		{"long note", func(in *VehicleInput) { in.Note = strings.Repeat("n", maxVehicleNoteLen+1) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := valid
			tt.change(&in)
			if _, err := validateVehicleInput(in); !errors.Is(err, errInvalidVehicle) {
				t.Fatalf("err = %v, want errInvalidVehicle", err)
			}
		})
	}
}

// Fleet used by the rule tests: Ana drives v1 (2 seats) with Beto; Carla drives v2 (1 seat).
func testFleet() []Vehicle {
	return []Vehicle{
		{ID: "v1", Name: "Carro de Ana", DriverID: "ana", Departure: departureBogota, Seats: 2, Passengers: []string{"beto"}},
		{ID: "v2", Name: "Van de Carla", DriverID: "carla", Departure: departureTocancipa, Seats: 1, Passengers: []string{}},
	}
}

var testConfirmed = map[string]bool{"ana": true, "beto": true, "carla": true, "dani": true, "eva": true}

func input(driver string, seats int) VehicleInput {
	return VehicleInput{Name: "Moto", DriverID: driver, Departure: departureBogota, Seats: seats}
}

func TestApplyTransportErrors(t *testing.T) {
	tests := []struct {
		name string
		cmd  transportCmd
		want error
	}{
		{"create with unconfirmed driver", transportCmd{op: opCreateVehicle, vehicleID: "v3", input: input("zoe", 2)}, errDriverUnavailable},
		{"create with a driver who already drives", transportCmd{op: opCreateVehicle, vehicleID: "v3", input: input("ana", 2)}, errDriverUnavailable},
		{"create with a driver who rides", transportCmd{op: opCreateVehicle, vehicleID: "v3", input: input("beto", 2)}, errDriverUnavailable},
		{"create with invalid data", transportCmd{op: opCreateVehicle, vehicleID: "v3", input: input("dani", 0)}, errInvalidVehicle},
		{"update a missing vehicle", transportCmd{op: opUpdateVehicle, vehicleID: "nope", input: input("ana", 2)}, errVehicleNotFound},
		{"update below passengers", transportCmd{op: opUpdateVehicle, vehicleID: "v1", input: VehicleInput{Name: "Carro", DriverID: "ana", Departure: departureBogota, Seats: 0}}, errInvalidVehicle},
		{"update to a driver who rides here", transportCmd{op: opUpdateVehicle, vehicleID: "v1", input: input("beto", 2)}, errDriverUnavailable},
		{"delete a missing vehicle", transportCmd{op: opDeleteVehicle, vehicleID: "nope"}, errVehicleNotFound},
		{"reserve in a missing vehicle", transportCmd{op: opReserveSeat, vehicleID: "nope", guestID: "dani"}, errVehicleNotFound},
		{"reserve for an unconfirmed guest", transportCmd{op: opReserveSeat, vehicleID: "v1", guestID: "zoe"}, errGuestNotConfirmed},
		{"reserve for a driver", transportCmd{op: opReserveSeat, vehicleID: "v1", guestID: "carla"}, errGuestIsDriver},
		{"reserve in a full vehicle", transportCmd{op: opReserveSeat, vehicleID: "v2", guestID: "dani"}, nil},
		{"release someone who does not ride there", transportCmd{op: opReleaseSeat, vehicleID: "v2", guestID: "beto"}, errPassengerNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := applyTransport(testFleet(), tt.cmd, testConfirmed)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}

	// A vehicle that reached its seats rejects one more passenger.
	fleet := testFleet()
	fleet[1].Passengers = []string{"dani"}
	if _, err := applyTransport(fleet, transportCmd{op: opReserveSeat, vehicleID: "v2", guestID: "eva"}, testConfirmed); !errors.Is(err, errVehicleFull) {
		t.Fatalf("full vehicle: err = %v", err)
	}
	// Seats cannot go below the passengers already on board (FR-007).
	fleet[0].Passengers = []string{"beto", "eva"}
	if _, err := applyTransport(fleet, transportCmd{op: opUpdateVehicle, vehicleID: "v1", input: input("ana", 1)}, testConfirmed); !errors.Is(err, errSeatsBelowPassengers) {
		t.Fatalf("seats below passengers: err = %v", err)
	}
}

func TestApplyTransportChanges(t *testing.T) {
	fleet := testFleet()

	// Reserving in another vehicle moves the guest instead of giving them two seats (FR-005).
	r, err := applyTransport(fleet, transportCmd{op: opReserveSeat, vehicleID: "v2", guestID: "beto"}, testConfirmed)
	if err != nil || len(r.put) != 2 {
		t.Fatalf("move: %+v, %v", r, err)
	}
	if slices.Contains(r.put[0].Passengers, "beto") || !slices.Contains(r.put[1].Passengers, "beto") {
		t.Fatalf("move: %+v", r.put)
	}
	if len(fleet[0].Passengers) != 1 {
		t.Fatal("applyTransport changed its input")
	}

	// Reserving where the guest already rides changes nothing.
	r, err = applyTransport(fleet, transportCmd{op: opReserveSeat, vehicleID: "v1", guestID: "beto"}, testConfirmed)
	if err != nil || len(r.put) != 0 {
		t.Fatalf("no-op: %+v, %v", r, err)
	}

	// The driver can stay the same when editing.
	r, err = applyTransport(fleet, transportCmd{op: opUpdateVehicle, vehicleID: "v1", input: VehicleInput{Name: "Carro azul", DriverID: "ana", Departure: departureTocancipa, Seats: 3}}, testConfirmed)
	if err != nil || r.put[0].Name != "Carro azul" || r.put[0].Seats != 3 || !slices.Equal(r.put[0].Passengers, []string{"beto"}) {
		t.Fatalf("update: %+v, %v", r, err)
	}

	// Creating keeps the new id and starts with no passengers.
	r, err = applyTransport(fleet, transportCmd{op: opCreateVehicle, vehicleID: "v3", input: input("dani", 2)}, testConfirmed)
	if err != nil || r.put[0].ID != "v3" || r.put[0].Passengers == nil || len(r.put[0].Passengers) != 0 {
		t.Fatalf("create: %+v, %v", r, err)
	}

	r, err = applyTransport(fleet, transportCmd{op: opReleaseSeat, vehicleID: "v1", guestID: "beto"}, testConfirmed)
	if err != nil || len(r.put[0].Passengers) != 0 {
		t.Fatalf("release: %+v, %v", r, err)
	}

	r, err = applyTransport(fleet, transportCmd{op: opDeleteVehicle, vehicleID: "v1"}, testConfirmed)
	if err != nil || r.delete != "v1" {
		t.Fatalf("delete: %+v, %v", r, err)
	}
}

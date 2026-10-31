package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"cloud.google.com/go/firestore"
)

// Departure places fixed by the constitution.
const (
	departureBogota    = "bogota"
	departureTocancipa = "tocancipa"
)

const (
	maxVehicleSeats   = 24
	maxVehicleNameLen = 60
	maxVehicleNoteLen = 200
)

// Domain errors of the transport module; handlers map them to HTTP codes.
var (
	errInvalidVehicle       = errors.New("invalid vehicle")
	errDriverUnavailable    = errors.New("driver unavailable")
	errSeatsBelowPassengers = errors.New("seats below passengers")
	errVehicleFull          = errors.New("vehicle full")
	errGuestIsDriver        = errors.New("guest is a driver")
	errGuestNotConfirmed    = errors.New("guest not confirmed")
	errVehicleNotFound      = errors.New("vehicle not found")
	errPassengerNotFound    = errors.New("passenger not found")
)

// Vehicle is a document of events/{EVENT_ID}/vehicles. Passengers live inside it, so a vehicle
// and its seats are read and written at once.
type Vehicle struct {
	ID            string   `firestore:"-"`
	Name          string   `firestore:"name"`
	DriverID      string   `firestore:"driverId"`
	Departure     string   `firestore:"departure"`
	Seats         int      `firestore:"seats"`
	DepartureTime string   `firestore:"departureTime"`
	Note          string   `firestore:"note"`
	Passengers    []string `firestore:"passengers"`
}

// VehicleInput is the editable part of a vehicle (FR-003).
type VehicleInput struct {
	Name          string `json:"name"`
	DriverID      string `json:"driverId"`
	Departure     string `json:"departure"`
	Seats         int    `json:"seats"`
	DepartureTime string `json:"departureTime"`
	Note          string `json:"note"`
}

var clockTime = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// validateVehicleInput trims in and checks the rules of data-model.md.
func validateVehicleInput(in VehicleInput) (VehicleInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.DepartureTime = strings.TrimSpace(in.DepartureTime)
	in.Note = strings.TrimSpace(in.Note)
	nameLen := utf8.RuneCountInString(in.Name)
	switch {
	case nameLen == 0 || nameLen > maxVehicleNameLen,
		in.Departure != departureBogota && in.Departure != departureTocancipa,
		in.Seats < 1 || in.Seats > maxVehicleSeats,
		in.DepartureTime != "" && !clockTime.MatchString(in.DepartureTime),
		utf8.RuneCountInString(in.Note) > maxVehicleNoteLen:
		return VehicleInput{}, errInvalidVehicle
	}
	return in, nil
}

type transportOp int

const (
	opCreateVehicle transportOp = iota
	opUpdateVehicle
	opDeleteVehicle
	opReserveSeat
	opReleaseSeat
)

// transportCmd is one change to the transport module.
type transportCmd struct {
	op        transportOp
	vehicleID string       // the vehicle acted on; for opCreateVehicle, the id of the new one
	input     VehicleInput // opCreateVehicle and opUpdateVehicle
	guestID   string       // opReserveSeat and opReleaseSeat
}

// transportResult says what to store after a command.
type transportResult struct {
	put    []Vehicle // vehicles to write
	delete string    // id of the vehicle to delete
}

// applyTransport applies cmd to all the vehicles of the event and returns what changes. It is
// pure: the store runs it inside a transaction that read every vehicle, which keeps one place per
// person (FR-005) and never more passengers than seats (FR-006, FR-007) under concurrent writes.
func applyTransport(vehicles []Vehicle, cmd transportCmd, confirmed map[string]bool) (transportResult, error) {
	switch cmd.op {
	case opCreateVehicle:
		in, err := validateVehicleInput(cmd.input)
		if err != nil {
			return transportResult{}, err
		}
		if !confirmed[in.DriverID] || hasPlace(vehicles, in.DriverID) {
			return transportResult{}, errDriverUnavailable
		}
		v := Vehicle{ID: cmd.vehicleID, Passengers: []string{}}
		return transportResult{put: []Vehicle{withInput(v, in)}}, nil

	case opUpdateVehicle:
		i := vehicleIndex(vehicles, cmd.vehicleID)
		if i < 0 {
			return transportResult{}, errVehicleNotFound
		}
		in, err := validateVehicleInput(cmd.input)
		if err != nil {
			return transportResult{}, err
		}
		current := vehicles[i]
		if !confirmed[in.DriverID] || (in.DriverID != current.DriverID && hasPlace(vehicles, in.DriverID)) {
			return transportResult{}, errDriverUnavailable
		}
		if in.Seats < len(current.Passengers) {
			return transportResult{}, errSeatsBelowPassengers
		}
		return transportResult{put: []Vehicle{withInput(clone(current), in)}}, nil

	case opDeleteVehicle:
		if vehicleIndex(vehicles, cmd.vehicleID) < 0 {
			return transportResult{}, errVehicleNotFound
		}
		// Its passengers are left without transport (FR-008).
		return transportResult{delete: cmd.vehicleID}, nil

	case opReserveSeat:
		i := vehicleIndex(vehicles, cmd.vehicleID)
		if i < 0 {
			return transportResult{}, errVehicleNotFound
		}
		if !confirmed[cmd.guestID] {
			return transportResult{}, errGuestNotConfirmed
		}
		if drives(vehicles, cmd.guestID) {
			return transportResult{}, errGuestIsDriver
		}
		target := clone(vehicles[i])
		if slices.Contains(target.Passengers, cmd.guestID) {
			return transportResult{}, nil
		}
		if len(target.Passengers) >= target.Seats {
			return transportResult{}, errVehicleFull
		}
		var put []Vehicle
		// Reserving in another vehicle moves the guest: never two seats at once.
		if j := passengerIndex(vehicles, cmd.guestID); j >= 0 {
			from := clone(vehicles[j])
			from.Passengers = slices.DeleteFunc(from.Passengers, func(id string) bool { return id == cmd.guestID })
			put = append(put, from)
		}
		target.Passengers = append(target.Passengers, cmd.guestID)
		return transportResult{put: append(put, target)}, nil

	case opReleaseSeat:
		i := vehicleIndex(vehicles, cmd.vehicleID)
		if i < 0 {
			return transportResult{}, errVehicleNotFound
		}
		v := clone(vehicles[i])
		if !slices.Contains(v.Passengers, cmd.guestID) {
			return transportResult{}, errPassengerNotFound
		}
		v.Passengers = slices.DeleteFunc(v.Passengers, func(id string) bool { return id == cmd.guestID })
		return transportResult{put: []Vehicle{v}}, nil

	default:
		return transportResult{}, fmt.Errorf("unknown transport operation %d", cmd.op)
	}
}

func withInput(v Vehicle, in VehicleInput) Vehicle {
	v.Name = in.Name
	v.DriverID = in.DriverID
	v.Departure = in.Departure
	v.Seats = in.Seats
	v.DepartureTime = in.DepartureTime
	v.Note = in.Note
	return v
}

// clone copies v so the pure rule never changes its input.
func clone(v Vehicle) Vehicle {
	v.Passengers = append([]string{}, v.Passengers...)
	return v
}

func vehicleIndex(vehicles []Vehicle, id string) int {
	return slices.IndexFunc(vehicles, func(v Vehicle) bool { return v.ID == id })
}

func drives(vehicles []Vehicle, guestID string) bool {
	return slices.ContainsFunc(vehicles, func(v Vehicle) bool { return v.DriverID == guestID })
}

func passengerIndex(vehicles []Vehicle, guestID string) int {
	return slices.IndexFunc(vehicles, func(v Vehicle) bool { return slices.Contains(v.Passengers, guestID) })
}

// hasPlace reports whether the guest already drives or rides in some vehicle.
func hasPlace(vehicles []Vehicle, guestID string) bool {
	return drives(vehicles, guestID) || passengerIndex(vehicles, guestID) >= 0
}

// TransportStore keeps the vehicles in events/{EVENT_ID}/vehicles.
type TransportStore struct {
	client  *firestore.Client
	eventID string
}

// NewTransportStore returns a TransportStore for the given event.
func NewTransportStore(client *firestore.Client, eventID string) *TransportStore {
	return &TransportStore{client: client, eventID: eventID}
}

func (s *TransportStore) collection() *firestore.CollectionRef {
	return s.client.Collection("events").Doc(s.eventID).Collection("vehicles")
}

func decodeVehicles(docs []*firestore.DocumentSnapshot) ([]Vehicle, error) {
	out := make([]Vehicle, 0, len(docs))
	for _, d := range docs {
		var v Vehicle
		if err := d.DataTo(&v); err != nil {
			return nil, fmt.Errorf("decode vehicle %s: %w", d.Ref.ID, err)
		}
		v.ID = d.Ref.ID
		out = append(out, v)
	}
	return out, nil
}

// List returns every vehicle of the event.
func (s *TransportStore) List(ctx context.Context) ([]Vehicle, error) {
	docs, err := s.collection().Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("list vehicles: %w", err)
	}
	return decodeVehicles(docs)
}

// Apply runs cmd in a transaction that reads every vehicle, so a concurrent change to any of
// them makes Firestore retry with fresh data. by is the guest who makes the change.
func (s *TransportStore) Apply(ctx context.Context, cmd transportCmd, confirmed map[string]bool, by string) error {
	if cmd.op == opCreateVehicle {
		cmd.vehicleID = s.collection().NewDoc().ID
	}
	err := s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		docs, err := tx.Documents(s.collection()).GetAll()
		if err != nil {
			return fmt.Errorf("list vehicles: %w", err)
		}
		vehicles, err := decodeVehicles(docs)
		if err != nil {
			return err
		}
		result, err := applyTransport(vehicles, cmd, confirmed)
		if err != nil {
			return err
		}
		for _, v := range result.put {
			err := tx.Set(s.collection().Doc(v.ID), map[string]any{
				"name":          v.Name,
				"driverId":      v.DriverID,
				"departure":     v.Departure,
				"seats":         v.Seats,
				"departureTime": v.DepartureTime,
				"note":          v.Note,
				"passengers":    v.Passengers,
				"updatedAt":     firestore.ServerTimestamp,
				"updatedBy":     by,
			})
			if err != nil {
				return err
			}
		}
		if result.delete != "" {
			return tx.Delete(s.collection().Doc(result.delete))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("transport: %w", err)
	}
	return nil
}

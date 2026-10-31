package main

import (
	"errors"
	"net/http"
	"sort"
)

// HTTP side of the transport module (spec 005). Every route sits behind requireGuest: any
// confirmed guest can manage any vehicle and any seat (FR-009, FR-013).

type vehicleView struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Driver        Guest   `json:"driver"`
	Departure     string  `json:"departure"`
	Seats         int     `json:"seats"`
	Free          int     `json:"free"`
	Passengers    []Guest `json:"passengers"`
	DepartureTime string  `json:"departureTime,omitempty"`
	Note          string  `json:"note,omitempty"`
}

type transportResponse struct {
	Vehicles         []vehicleView `json:"vehicles"`
	WithoutTransport []Guest       `json:"withoutTransport"`
	Confirmed        []Guest       `json:"confirmed"`
}

// confirmedGuests returns the catalog and the ids of its confirmed guests, writing the error
// response when they cannot be read.
func (s *server) confirmedGuests(w http.ResponseWriter, r *http.Request) (Catalog, map[string]bool, bool) {
	catalog, ok := s.loadCatalog(w, r)
	if !ok {
		return Catalog{}, nil, false
	}
	statuses, err := s.attendance.Statuses(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return Catalog{}, nil, false
	}
	confirmed := make(map[string]bool)
	for _, g := range catalog.Sorted() {
		if statuses[g.ID] == statusConfirmed {
			confirmed[g.ID] = true
		}
	}
	return catalog, confirmed, true
}

// writeTransportState answers with the whole module, the same view as GET /api/transport.
func (s *server) writeTransportState(w http.ResponseWriter, r *http.Request, status int) {
	catalog, confirmed, ok := s.confirmedGuests(w, r)
	if !ok {
		return
	}
	vehicles, err := s.transport.List(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, status, transportView(vehicles, catalog, confirmed))
}

// transportView resolves names and computes free seats and the guests without transport
// (FR-002, FR-010). Guests who are no longer confirmed are left out of the lists.
func transportView(vehicles []Vehicle, catalog Catalog, confirmed map[string]bool) transportResponse {
	resp := transportResponse{Vehicles: []vehicleView{}, WithoutTransport: []Guest{}, Confirmed: []Guest{}}
	placed := make(map[string]bool)
	for _, v := range vehicles {
		driver, ok := catalog.ByID(v.DriverID)
		if !ok {
			driver = Guest{ID: v.DriverID}
		}
		placed[v.DriverID] = true
		view := vehicleView{
			ID:            v.ID,
			Name:          v.Name,
			Driver:        driver,
			Departure:     v.Departure,
			Seats:         v.Seats,
			Free:          v.Seats - len(v.Passengers),
			Passengers:    []Guest{},
			DepartureTime: v.DepartureTime,
			Note:          v.Note,
		}
		for _, id := range v.Passengers {
			placed[id] = true
			if g, ok := catalog.ByID(id); ok && confirmed[id] {
				view.Passengers = append(view.Passengers, g)
			}
		}
		resp.Vehicles = append(resp.Vehicles, view)
	}
	sort.SliceStable(resp.Vehicles, func(i, j int) bool {
		a, b := resp.Vehicles[i], resp.Vehicles[j]
		if a.Departure != b.Departure {
			return a.Departure < b.Departure
		}
		return foldAccents(a.Name) < foldAccents(b.Name)
	})
	for _, g := range catalog.Sorted() {
		if !confirmed[g.ID] {
			continue
		}
		resp.Confirmed = append(resp.Confirmed, g)
		if !placed[g.ID] {
			resp.WithoutTransport = append(resp.WithoutTransport, g)
		}
	}
	return resp
}

// handleTransport lists vehicles by departure place and who has no transport (US1).
func (s *server) handleTransport(w http.ResponseWriter, r *http.Request, _ Account, _ Guest) {
	s.writeTransportState(w, r, http.StatusOK)
}

// applyTransportCmd runs cmd and answers with the new state (successStatus) or the domain error.
func (s *server) applyTransportCmd(w http.ResponseWriter, r *http.Request, me Guest, cmd transportCmd, successStatus int) {
	_, confirmed, ok := s.confirmedGuests(w, r)
	if !ok {
		return
	}
	if err := s.transport.Apply(r.Context(), cmd, confirmed, me.ID); err != nil {
		writeTransportError(w, err)
		return
	}
	if successStatus == http.StatusNoContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.writeTransportState(w, r, successStatus)
}

// handleCreateVehicle registers a vehicle (US2).
func (s *server) handleCreateVehicle(w http.ResponseWriter, r *http.Request, _ Account, me Guest) {
	var in VehicleInput
	if !decodeJSON(w, r, &in) {
		return
	}
	s.applyTransportCmd(w, r, me, transportCmd{op: opCreateVehicle, input: in}, http.StatusCreated)
}

// handleUpdateVehicle edits a vehicle (US4).
func (s *server) handleUpdateVehicle(w http.ResponseWriter, r *http.Request, _ Account, me Guest) {
	var in VehicleInput
	if !decodeJSON(w, r, &in) {
		return
	}
	s.applyTransportCmd(w, r, me, transportCmd{op: opUpdateVehicle, vehicleID: r.PathValue("vehicleID"), input: in}, http.StatusOK)
}

// handleDeleteVehicle deletes a vehicle; its passengers are left without transport (US4).
func (s *server) handleDeleteVehicle(w http.ResponseWriter, r *http.Request, _ Account, me Guest) {
	s.applyTransportCmd(w, r, me, transportCmd{op: opDeleteVehicle, vehicleID: r.PathValue("vehicleID")}, http.StatusNoContent)
}

// handleReserveSeat reserves a seat for any confirmed guest, moving them if needed (US3).
func (s *server) handleReserveSeat(w http.ResponseWriter, r *http.Request, _ Account, me Guest) {
	var req struct {
		GuestID string `json:"guestId"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.GuestID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest)
		return
	}
	s.applyTransportCmd(w, r, me, transportCmd{op: opReserveSeat, vehicleID: r.PathValue("vehicleID"), guestID: req.GuestID}, http.StatusOK)
}

// handleReleaseSeat frees the seat of any passenger (US3).
func (s *server) handleReleaseSeat(w http.ResponseWriter, r *http.Request, _ Account, me Guest) {
	cmd := transportCmd{op: opReleaseSeat, vehicleID: r.PathValue("vehicleID"), guestID: r.PathValue("guestID")}
	s.applyTransportCmd(w, r, me, cmd, http.StatusNoContent)
}

func writeTransportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInvalidVehicle):
		writeError(w, http.StatusBadRequest, codeInvalidVehicle)
	case errors.Is(err, errVehicleNotFound):
		writeError(w, http.StatusNotFound, codeVehicleNotFound)
	case errors.Is(err, errPassengerNotFound):
		writeError(w, http.StatusNotFound, codePassengerNotFound)
	case errors.Is(err, errDriverUnavailable):
		writeError(w, http.StatusConflict, codeDriverUnavailable)
	case errors.Is(err, errSeatsBelowPassengers):
		writeError(w, http.StatusConflict, codeSeatsBelowPassengers)
	case errors.Is(err, errVehicleFull):
		writeError(w, http.StatusConflict, codeVehicleFull)
	case errors.Is(err, errGuestIsDriver):
		writeError(w, http.StatusConflict, codeGuestIsDriver)
	case errors.Is(err, errGuestNotConfirmed):
		writeError(w, http.StatusConflict, codeGuestNotConfirmed)
	default:
		writeInternalError(w, err)
	}
}

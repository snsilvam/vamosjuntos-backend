package main

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"
)

// Error codes returned in {"error":{"code":"..."}}. The frontend translates each one into a
// friendly message; the backend never sends technical text to the user.
const (
	codeInvalidRequest       = "invalid_request"
	codeInvalidCode          = "invalid_code"
	codeTooManyAttempts      = "too_many_attempts"
	codeEventNotFound        = "event_not_found"
	codeGuestNotFound        = "guest_not_found"
	codeAlreadyResponded     = "already_responded"
	codeAlreadyConfirmed     = "already_confirmed"
	codeAccountAlreadyLinked = "account_already_linked"
	codeGuestAlreadyLinked   = "guest_already_linked"
	codeUnauthenticated      = "unauthenticated"
	codeNotLinked            = "not_linked"
	codeNotInvited           = "not_invited"
	codeInternal             = "internal"

	// Transport module (spec 005).
	codeInvalidVehicle       = "invalid_vehicle"
	codeDriverUnavailable    = "driver_unavailable"
	codeSeatsBelowPassengers = "seats_below_passengers"
	codeVehicleFull          = "vehicle_full"
	codeGuestIsDriver        = "guest_is_driver"
	codeGuestNotConfirmed    = "guest_not_confirmed"
	codeVehicleNotFound      = "vehicle_not_found"
	codePassengerNotFound    = "passenger_not_found"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code              string `json:"code"`
	RetryAfterSeconds int    `json:"retryAfterSeconds,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code}})
}

func writeTooManyAttempts(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeJSON(w, http.StatusTooManyRequests, errorBody{Error: errorDetail{
		Code:              codeTooManyAttempts,
		RetryAfterSeconds: seconds,
	}})
}

// writeInternalError logs the cause and answers with a generic 500. The cause never reaches
// the client (Principle V).
func writeInternalError(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	writeError(w, http.StatusInternalServerError, codeInternal)
}

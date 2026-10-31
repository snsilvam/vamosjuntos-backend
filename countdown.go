package main

import (
	"math"
	"net/http"
	"time"
)

// Phases of the countdown banner (spec 004, FR-007), from the farthest to the last one.
const (
	phaseFar      = "far"
	phaseNear     = "near"
	phaseLastWeek = "lastWeek"
	phaseTomorrow = "tomorrow"
	phaseToday    = "today"
	phaseOngoing  = "ongoing"
	phaseFinished = "finished"
)

// Countdown is what the banner shows. SecondsToNextChange is relative so the frontend never needs
// the device clock (FR-003); it is left out once the event is over, because nothing changes again.
type Countdown struct {
	Phase               string `json:"phase"`
	DaysLeft            int    `json:"daysLeft"`
	SecondsToNextChange int    `json:"secondsToNextChange,omitempty"`
}

// countdownAt computes the countdown of e at now, with the rules of
// specs/004-banner-cuenta-regresiva/data-model.md. Days are calendar days in Colombia, not 24-hour
// periods: at 11:59 p.m. the day before, 1 day is left (FR-002).
func countdownAt(e Event, now time.Time) Countdown {
	y, m, d := now.In(colombia).Date()
	nextMidnight := time.Date(y, m, d+1, 0, 0, 0, 0, colombia)

	if !now.Before(e.EndsAt) {
		return Countdown{Phase: phaseFinished}
	}
	if !now.Before(e.StartsAt) {
		return Countdown{Phase: phaseOngoing, SecondsToNextChange: secondsUntil(now, earliest(nextMidnight, e.EndsAt))}
	}

	c := Countdown{
		DaysLeft:            int(colombiaDate(e.StartsAt).Sub(colombiaDate(now)).Hours() / 24),
		SecondsToNextChange: secondsUntil(now, earliest(nextMidnight, e.StartsAt)),
	}
	switch {
	case c.DaysLeft == 0:
		c.Phase = phaseToday
	case c.DaysLeft == 1:
		c.Phase = phaseTomorrow
	case c.DaysLeft <= 7:
		c.Phase = phaseLastWeek
	case c.DaysLeft <= 30:
		c.Phase = phaseNear
	default:
		c.Phase = phaseFar
	}
	return c
}

// colombiaDate returns the calendar date of t in Colombia as midnight UTC, so two dates are
// always a whole number of days apart.
func colombiaDate(t time.Time) time.Time {
	y, m, d := t.In(colombia).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// secondsUntil rounds up, so a client that waits that long never asks before the change.
func secondsUntil(now, t time.Time) int {
	return int(math.Ceil(t.Sub(now).Seconds()))
}

// handleCountdown returns the countdown for the home banner, computed with the backend clock in
// Colombia time (spec 004, FR-002, FR-003).
func (s *server) handleCountdown(w http.ResponseWriter, r *http.Request, _ Account, _ Guest) {
	event, ok := s.loadEvent(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, countdownAt(event, s.now()))
}

package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestCountdownAt(t *testing.T) {
	event := newTestEnv(t).events.event
	at := func(month time.Month, day, hour, min, sec int) time.Time {
		return time.Date(2026, month, day, hour, min, sec, 0, colombia)
	}

	tests := []struct {
		name string
		now  time.Time
		want Countdown
	}{
		{"US1: 54 days on Oct 30", at(10, 30, 10, 0, 0), Countdown{phaseFar, 54, 50400}},
		{"last second of far", at(11, 22, 23, 59, 59), Countdown{phaseFar, 31, 1}},
		{"midnight starts near", at(11, 23, 0, 0, 0), Countdown{phaseNear, 30, 86400}},
		{"last day of near", at(12, 15, 12, 0, 0), Countdown{phaseNear, 8, 43200}},
		{"midnight starts last week", at(12, 16, 0, 0, 0), Countdown{phaseLastWeek, 7, 86400}},
		{"tomorrow", at(12, 22, 8, 0, 0), Countdown{phaseTomorrow, 1, 57600}},
		{"tomorrow at 11:59 p.m.", at(12, 22, 23, 59, 0), Countdown{phaseTomorrow, 1, 60}},
		{"today waits for the start", at(12, 23, 0, 0, 0), Countdown{phaseToday, 0, 50400}},
		{"starts exactly at 2:00 p.m.", at(12, 23, 14, 0, 0), Countdown{phaseOngoing, 0, 36000}},
		{"last second of the trip", at(12, 26, 10, 59, 59), Countdown{phaseOngoing, 0, 1}},
		{"ends exactly at 11:00 a.m.", at(12, 26, 11, 0, 0), Countdown{phaseFinished, 0, 0}},
		{"long after", at(12, 31, 9, 0, 0), Countdown{phaseFinished, 0, 0}},
		// The same instant as the first case, seen from Madrid (SC-002).
		{"other time zone", at(10, 30, 10, 0, 0).In(time.FixedZone("CET", 60*60)), Countdown{phaseFar, 54, 50400}},
		// 8:00 p.m. on Oct 30 in Colombia is already Oct 31 in UTC: Colombia's date wins.
		{"UTC is already the next day", at(10, 30, 20, 0, 0).UTC(), Countdown{phaseFar, 54, 14400}},
		{"sub-second rounds up", at(12, 22, 23, 59, 59).Add(500 * time.Millisecond), Countdown{phaseTomorrow, 1, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countdownAt(event, tt.now); got != tt.want {
				t.Fatalf("countdownAt(%s) = %+v, want %+v", tt.now, got, tt.want)
			}
		})
	}
}

// newCountdownEnv returns a test env whose server clock is fixed at now.
func newCountdownEnv(t *testing.T, now time.Time) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	srv := NewServer(env.guests, env.events, env.attendance, env.attempts, testAccounts, env.links, env.transport)
	srv.now = func() time.Time { return now }
	env.handler = srv.routes()
	return env
}

func TestCountdown(t *testing.T) {
	env := newCountdownEnv(t, time.Date(2026, 10, 30, 10, 0, 0, 0, colombia))
	env.links["carmencita"] = "uid-ana"
	env.attendance.statuses["carmencita"] = statusConfirmed

	w := env.do(t, request{method: "GET", path: "/api/countdown", token: "ana"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"phase": "far", "daysLeft": float64(54), "secondsToNextChange": float64(50400)}
	if len(got) != len(want) {
		t.Fatalf("body = %s, want %v", w.Body.String(), want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("body = %s, want %v", w.Body.String(), want)
		}
	}
}

func TestCountdownFinishedHasNoNextChange(t *testing.T) {
	env := newCountdownEnv(t, time.Date(2026, 12, 27, 9, 0, 0, 0, colombia))
	env.links["carmencita"] = "uid-ana"
	env.attendance.statuses["carmencita"] = statusConfirmed

	w := env.do(t, request{method: "GET", path: "/api/countdown", token: "ana"})
	if body := w.Body.String(); w.Code != http.StatusOK || body != `{"phase":"finished","daysLeft":0}`+"\n" {
		t.Fatalf("status = %d, body %q", w.Code, body)
	}
}

func TestCountdownErrors(t *testing.T) {
	env := newCountdownEnv(t, time.Date(2026, 10, 30, 10, 0, 0, 0, colombia))
	path := "/api/countdown"

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

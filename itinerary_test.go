package main

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

var tripDays = []string{"2026-12-23", "2026-12-24", "2026-12-25", "2026-12-26"}

func TestEventDays(t *testing.T) {
	e := Event{
		StartsAt: time.Date(2026, 12, 23, 14, 0, 0, 0, colombia),
		EndsAt:   time.Date(2026, 12, 26, 11, 0, 0, 0, colombia),
	}
	if got := eventDays(e); !slices.Equal(got, tripDays) {
		t.Fatalf("eventDays = %v, want %v", got, tripDays)
	}
	// 9:00 p.m. on Dec 26 in Colombia is already Dec 27 in UTC: Colombia's date wins.
	e.EndsAt = time.Date(2026, 12, 26, 21, 0, 0, 0, colombia).UTC()
	if got := eventDays(e); !slices.Equal(got, tripDays) {
		t.Fatalf("eventDays with UTC end = %v, want %v", got, tripDays)
	}
}

func TestValidateActivity(t *testing.T) {
	valid := ActivityInput{Title: "Piscina", Date: "2026-12-24", StartTime: "10:00", EndTime: "12:00"}
	tests := []struct {
		name string
		edit func(*ActivityInput)
		want error
	}{
		{"valid", func(*ActivityInput) {}, nil},
		{"only required fields", func(in *ActivityInput) { in.EndTime = "" }, nil},
		{"empty title", func(in *ActivityInput) { in.Title = "   " }, errInvalidActivity},
		{"title of 80 runes", func(in *ActivityInput) { in.Title = strings.Repeat("ñ", 80) }, nil},
		{"title of 81 runes", func(in *ActivityInput) { in.Title = strings.Repeat("ñ", 81) }, errInvalidActivity},
		{"place of 81 runes", func(in *ActivityInput) { in.Place = strings.Repeat("a", 81) }, errInvalidActivity},
		{"description of 500 runes", func(in *ActivityInput) { in.Description = strings.Repeat("a", 500) }, nil},
		{"description of 501 runes", func(in *ActivityInput) { in.Description = strings.Repeat("a", 501) }, errInvalidActivity},
		{"bad start time", func(in *ActivityInput) { in.StartTime = "24:00" }, errInvalidActivity},
		{"bad end time", func(in *ActivityInput) { in.EndTime = "9:00" }, errInvalidActivity},
		{"ends next day without end time", func(in *ActivityInput) { in.EndTime, in.EndsNextDay = "", true }, errInvalidActivity},
		{"day after the trip", func(in *ActivityInput) { in.Date = "2026-12-27" }, errActivityOutOfRange},
		{"day before the trip", func(in *ActivityInput) { in.Date = "2026-12-22" }, errActivityOutOfRange},
		{"end before start (US2-2)", func(in *ActivityInput) { in.EndTime = "09:00" }, errActivityTimeOrder},
		{"end equal to start", func(in *ActivityInput) { in.EndTime = "10:00" }, errActivityTimeOrder},
		{"Nochebuena 20:00 to 01:00 next day", func(in *ActivityInput) { in.StartTime, in.EndTime, in.EndsNextDay = "20:00", "01:00", true }, nil},
		{"exactly 24 hours", func(in *ActivityInput) { in.StartTime, in.EndTime, in.EndsNextDay = "20:00", "20:00", true }, nil},
		{"25 hours", func(in *ActivityInput) { in.StartTime, in.EndTime, in.EndsNextDay = "22:00", "23:00", true }, errActivityTimeOrder},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := valid
			tt.edit(&in)
			if _, err := validateActivity(in, tripDays); !errors.Is(err, tt.want) {
				t.Fatalf("validateActivity(%+v) error = %v, want %v", in, err, tt.want)
			}
		})
	}
}

func TestValidateActivityTrims(t *testing.T) {
	got, err := validateActivity(ActivityInput{Title: "  Piscina ", Date: "2026-12-24", StartTime: " 10:00 ", Place: " Hotel ", Description: " Toalla "}, tripDays)
	if err != nil {
		t.Fatal(err)
	}
	want := ActivityInput{Title: "Piscina", Date: "2026-12-24", StartTime: "10:00", Place: "Hotel", Description: "Toalla"}
	if got != want {
		t.Fatalf("validateActivity = %+v, want %+v", got, want)
	}
}

func TestSortActivities(t *testing.T) {
	activities := []Activity{
		{ID: "4", Title: "Piscina", Date: "2026-12-24", StartTime: "10:00"},
		{ID: "3", Title: "Cena", Date: "2026-12-23", StartTime: "20:00"},
		{ID: "2", Title: "Ángeles", Date: "2026-12-24", StartTime: "10:00"},
		{ID: "1", Title: "Desayuno", Date: "2026-12-24", StartTime: "08:00"},
		{ID: "0", Title: "Piscina", Date: "2026-12-24", StartTime: "10:00"},
	}
	sortActivities(activities)
	var got []string
	for _, a := range activities {
		got = append(got, a.ID)
	}
	// Same hour: "Ángeles" sorts as "angeles", before "Piscina"; equal titles fall back to id.
	if want := []string{"3", "1", "2", "0", "4"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

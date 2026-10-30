package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNewCatalogRejects(t *testing.T) {
	tooMany := make([]Guest, maxGuests+1)
	for i := range tooMany {
		tooMany[i] = Guest{ID: fmt.Sprintf("id%d", i), Name: fmt.Sprintf("Invitado %d", i), code: fmt.Sprintf("codigo%03d", i)}
	}

	tests := []struct {
		name   string
		guests []Guest
	}{
		{"empty", nil},
		{"26 guests", tooMany},
		{"duplicate code ignoring case and spaces", []Guest{{ID: "a", Name: "Ana", code: "Abc123"}, {ID: "l", Name: "Luis", code: " abc123 "}}},
		{"short code", []Guest{{ID: "a", Name: "Ana", code: "abc12"}}},
		{"code that is short once trimmed", []Guest{{ID: "a", Name: "Ana", code: "  abc1  "}}},
		{"empty name", []Guest{{ID: "a", Name: "  ", code: "abc123"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := newCatalog(tt.guests); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestNewCatalogErrorsDoNotLeakCodes(t *testing.T) {
	_, err := newCatalog([]Guest{{ID: "a", Name: "Ana", code: "secreto1"}, {ID: "l", Name: "Luis", code: "SECRETO1"}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(strings.ToLower(err.Error()), "secreto1") {
		t.Fatalf("error leaks the code: %v", err)
	}
}

func TestCatalogLookups(t *testing.T) {
	c, err := newCatalog([]Guest{
		{ID: "doc4", Name: "Sebastián", code: "prueba04"},
		{ID: "doc1", Name: " Carmencita ", code: " prueba01 "},
		{ID: "doc3", Name: "papá de lore", code: "prueba03"},
		{ID: "doc2", Name: "Alisson", code: "prueba02"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !c.Matches("doc1", " PRUEBA01 ") {
		t.Error("Matches should ignore spaces and case")
	}
	if c.Matches("doc1", "prueba02") {
		t.Error("Matches accepted another guest's code")
	}
	if c.Matches("nadie", "prueba01") {
		t.Error("Matches accepted an unknown guest")
	}

	g, ok := c.ByCode("Prueba03")
	if !ok || g.ID != "doc3" {
		t.Errorf("ByCode = %+v, %v; want doc3", g, ok)
	}
	if _, ok := c.ByCode("noexiste"); ok {
		t.Error("ByCode found an unknown code")
	}

	var names []string
	for _, g := range c.Sorted() {
		names = append(names, g.Name)
	}
	want := "Alisson,Carmencita,papá de lore,Sebastián"
	if got := strings.Join(names, ","); got != want {
		t.Errorf("Sorted = %s, want %s", got, want)
	}

	b, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "prueba03") {
		t.Errorf("Guest JSON leaks the code: %s", b)
	}
}

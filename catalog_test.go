package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"papá de lore", "papa-de-lore"},
		{"Nicolás López", "nicolas-lopez"},
		{"El Fercho", "el-fercho"},
		{"Sebastián", "sebastian"},
		{"  Mafe  ", "mafe"},
		{"mamá de lore", "mama-de-lore"},
	}
	for _, tt := range tests {
		if got := slug(tt.name); got != tt.want {
			t.Errorf("slug(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func catalogJSON(t *testing.T, rows [][]string) string {
	t.Helper()
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseCatalogRejects(t *testing.T) {
	tooMany := make([][]string, maxGuests+1)
	for i := range tooMany {
		tooMany[i] = []string{fmt.Sprintf("Invitado %d", i), fmt.Sprintf("codigo%03d", i)}
	}

	tests := []struct {
		name string
		raw  string
	}{
		{"not json", "esto no es json"},
		{"empty", "[]"},
		{"26 guests", catalogJSON(t, tooMany)},
		{"duplicate code ignoring case and spaces", catalogJSON(t, [][]string{{"Ana", "Abc123"}, {"Luis", " abc123 "}})},
		{"short code", catalogJSON(t, [][]string{{"Ana", "abc12"}})},
		{"empty name", catalogJSON(t, [][]string{{"  ", "abc123"}})},
		{"row without code", catalogJSON(t, [][]string{{"Ana"}})},
		{"duplicate id", catalogJSON(t, [][]string{{"María", "abc123"}, {"maria", "xyz789"}})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseCatalog(tt.raw); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestParseCatalogErrorsDoNotLeakCodes(t *testing.T) {
	_, err := parseCatalog(catalogJSON(t, [][]string{{"Ana", "secreto1"}, {"Luis", "SECRETO1"}}))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(strings.ToLower(err.Error()), "secreto1") {
		t.Fatalf("error leaks the code: %v", err)
	}
}

func TestCatalogLookups(t *testing.T) {
	c, err := parseCatalog(catalogJSON(t, [][]string{
		{"Sebastián", "prueba04"},
		{"Carmencita", "prueba01"},
		{"papá de lore", "prueba03"},
		{"Alisson", "prueba02"},
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !c.Matches("carmencita", " PRUEBA01 ") {
		t.Error("Matches should ignore spaces and case")
	}
	if c.Matches("carmencita", "prueba02") {
		t.Error("Matches accepted another guest's code")
	}
	if c.Matches("nadie", "prueba01") {
		t.Error("Matches accepted an unknown guest")
	}

	g, ok := c.ByCode("Prueba03")
	if !ok || g.ID != "papa-de-lore" {
		t.Errorf("ByCode = %+v, %v; want papa-de-lore", g, ok)
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

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxGuests     = 25
	minCodeLength = 6
)

// Guest is a person from the invitation catalog. The code is unexported so it is never
// serialized to JSON.
type Guest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	code string
}

// Catalog is the closed list of guests configured in GUESTS_JSON. It is immutable after
// parseCatalog returns.
type Catalog struct {
	guests []Guest // sorted alphabetically, ignoring accents
	byID   map[string]Guest
}

// parseCatalog parses and validates GUESTS_JSON, whose format is [["Nombre","código"], ...].
// Error messages may name guests but never include their codes.
func parseCatalog(raw string) (Catalog, error) {
	var rows [][]string
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return Catalog{}, errors.New(`GUESTS_JSON: formato inválido, se espera [["Nombre","código"], ...]`)
	}
	if len(rows) == 0 {
		return Catalog{}, errors.New("GUESTS_JSON: el catálogo está vacío")
	}
	if len(rows) > maxGuests {
		return Catalog{}, fmt.Errorf("GUESTS_JSON: hay %d invitados y el máximo es %d", len(rows), maxGuests)
	}

	c := Catalog{byID: make(map[string]Guest, len(rows))}
	ownerOfCode := make(map[string]string, len(rows))
	for i, row := range rows {
		if len(row) != 2 {
			return Catalog{}, fmt.Errorf("GUESTS_JSON: la fila %d debe tener nombre y código", i+1)
		}
		name := strings.TrimSpace(row[0])
		code := strings.TrimSpace(row[1])
		if name == "" {
			return Catalog{}, fmt.Errorf("GUESTS_JSON: la fila %d tiene el nombre vacío", i+1)
		}
		if utf8.RuneCountInString(code) < minCodeLength {
			return Catalog{}, fmt.Errorf("GUESTS_JSON: el código de %q tiene menos de %d caracteres", name, minCodeLength)
		}
		codeKey := strings.ToLower(code)
		if other, ok := ownerOfCode[codeKey]; ok {
			return Catalog{}, fmt.Errorf("GUESTS_JSON: %q y %q tienen el mismo código", other, name)
		}
		ownerOfCode[codeKey] = name

		id := slug(name)
		if id == "" {
			return Catalog{}, fmt.Errorf("GUESTS_JSON: el nombre %q no genera un id válido", name)
		}
		if other, ok := c.byID[id]; ok {
			return Catalog{}, fmt.Errorf("GUESTS_JSON: %q y %q generan el mismo id %q", other.Name, name, id)
		}
		g := Guest{ID: id, Name: name, code: code}
		c.byID[id] = g
		c.guests = append(c.guests, g)
	}

	sort.SliceStable(c.guests, func(i, j int) bool {
		return foldAccents(c.guests[i].Name) < foldAccents(c.guests[j].Name)
	})
	return c, nil
}

// Len returns the number of guests in the catalog.
func (c Catalog) Len() int { return len(c.guests) }

// Sorted returns a copy of the guests in alphabetical order, ignoring accents.
func (c Catalog) Sorted() []Guest {
	out := make([]Guest, len(c.guests))
	copy(out, c.guests)
	return out
}

// ByID returns the guest with the given id.
func (c Catalog) ByID(id string) (Guest, bool) {
	g, ok := c.byID[id]
	return g, ok
}

// ByCode returns the guest that owns code (FR-011).
func (c Catalog) ByCode(code string) (Guest, bool) {
	for _, g := range c.guests {
		if codeMatches(g.code, code) {
			return g, true
		}
	}
	return Guest{}, false
}

// Matches reports whether code belongs to the guest with the given id (FR-003).
func (c Catalog) Matches(guestID, code string) bool {
	g, ok := c.byID[guestID]
	return ok && codeMatches(g.code, code)
}

// codeMatches compares codes ignoring surrounding spaces and letter case.
func codeMatches(want, input string) bool {
	return strings.EqualFold(strings.TrimSpace(input), want)
}

var accentReplacer = strings.NewReplacer(
	"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n",
)

// foldAccents lowercases s and removes Spanish accents and the tilde of ñ.
func foldAccents(s string) string {
	return accentReplacer.Replace(strings.ToLower(s))
}

// slug builds a stable id from a name: lowercase, without accents, with runs of any other
// character turned into a single "-". "papá de lore" becomes "papa-de-lore".
func slug(name string) string {
	var b strings.Builder
	pendingDash := false
	for _, r := range foldAccents(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteRune(r)
			continue
		}
		pendingDash = true
	}
	return b.String()
}

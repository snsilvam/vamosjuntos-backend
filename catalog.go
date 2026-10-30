package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"cloud.google.com/go/firestore"
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

// Catalog is the closed list of guests stored in events/{EVENT_ID}/users. It is immutable after
// newCatalog returns.
type Catalog struct {
	guests []Guest // sorted alphabetically, ignoring accents
	byID   map[string]Guest
}

// newCatalog trims and validates guests (FR-013, FR-016) and builds the catalog. Error messages
// may name guests but never include their codes.
func newCatalog(guests []Guest) (Catalog, error) {
	if len(guests) == 0 {
		return Catalog{}, errors.New("invitados: el catálogo está vacío")
	}
	if len(guests) > maxGuests {
		return Catalog{}, fmt.Errorf("invitados: hay %d y el máximo es %d", len(guests), maxGuests)
	}

	c := Catalog{byID: make(map[string]Guest, len(guests))}
	ownerOfCode := make(map[string]string, len(guests))
	for _, g := range guests {
		g.Name = strings.TrimSpace(g.Name)
		g.code = strings.TrimSpace(g.code)
		if g.Name == "" {
			return Catalog{}, fmt.Errorf("invitados: el documento %s no tiene Nombre", g.ID)
		}
		if utf8.RuneCountInString(g.code) < minCodeLength {
			return Catalog{}, fmt.Errorf("invitados: el código de %q tiene menos de %d caracteres", g.Name, minCodeLength)
		}
		codeKey := strings.ToLower(g.code)
		if other, ok := ownerOfCode[codeKey]; ok {
			return Catalog{}, fmt.Errorf("invitados: %q y %q tienen el mismo código", other, g.Name)
		}
		ownerOfCode[codeKey] = g.Name
		c.byID[g.ID] = g
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

// guestDoc is a document of events/{EVENT_ID}/users, created by hand in the Firestore console.
// Its field names are the ones the organizer chose. Other fields (such as "confirmado") are
// ignored: attendance lives in events/{EVENT_ID}/attendance.
type guestDoc struct {
	Name string `firestore:"Nombre"`
	Code string `firestore:"código"`
}

// GuestStore reads the guest catalog. The app never writes it (Principle I).
type GuestStore struct {
	client  *firestore.Client
	eventID string
}

// NewGuestStore returns a GuestStore for events/{eventID}/users.
func NewGuestStore(client *firestore.Client, eventID string) *GuestStore {
	return &GuestStore{client: client, eventID: eventID}
}

// Catalog reads and validates the whole catalog (at most 25 documents). It runs on every request
// that needs it, so the organizer's changes apply without a new deployment (FR-001). The guest id
// is the document id, which does not change when the name is edited.
func (s *GuestStore) Catalog(ctx context.Context) (Catalog, error) {
	snaps, err := s.client.Collection("events").Doc(s.eventID).Collection("users").Documents(ctx).GetAll()
	if err != nil {
		return Catalog{}, fmt.Errorf("list guests of %s: %w", s.eventID, err)
	}
	guests := make([]Guest, 0, len(snaps))
	for _, snap := range snaps {
		var d guestDoc
		if err := snap.DataTo(&d); err != nil {
			// The decoding error is not wrapped because it could quote a field value.
			return Catalog{}, fmt.Errorf("invitados: el documento %s debe tener Nombre y código de tipo texto", snap.Ref.ID)
		}
		guests = append(guests, Guest{ID: snap.Ref.ID, Name: d.Name, code: d.Code})
	}
	return newCatalog(guests)
}

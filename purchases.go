package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Categories fixed by the constitution ("comida y suministros").
const (
	categoryFood     = "comida"
	categorySupplies = "suministros"
)

const (
	maxItemNameLen     = 60
	maxItemQuantityLen = 30
	maxItemNoteLen     = 200
)

// Domain errors of the purchases module; handlers map them to HTTP codes.
var (
	errInvalidItem    = errors.New("invalid item")
	errItemNotFound   = errors.New("item not found")
	errItemTaken      = errors.New("item already taken")
	errNotResponsible = errors.New("not the responsible of the item")
)

// Item is a document of events/{EVENT_ID}/items. The responsible and the state live inside it, so
// one transaction on the document keeps a single responsible (FR-004, FR-008).
type Item struct {
	ID            string `firestore:"-"`
	Name          string `firestore:"name"`
	Category      string `firestore:"category"`
	Quantity      string `firestore:"quantity"`
	Note          string `firestore:"note"`
	ResponsibleID string `firestore:"responsibleId"`
	Bought        bool   `firestore:"bought"`
}

// ItemInput is the editable part of an item (FR-003, FR-007).
type ItemInput struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Quantity string `json:"quantity"`
	Note     string `json:"note"`
}

// validateItemInput trims in and checks the rules of specs/007-modulo-compras/data-model.md.
func validateItemInput(in ItemInput) (ItemInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Quantity = strings.TrimSpace(in.Quantity)
	in.Note = strings.TrimSpace(in.Note)
	nameLen := utf8.RuneCountInString(in.Name)
	switch {
	case nameLen == 0 || nameLen > maxItemNameLen,
		in.Category != categoryFood && in.Category != categorySupplies,
		utf8.RuneCountInString(in.Quantity) > maxItemQuantityLen,
		utf8.RuneCountInString(in.Note) > maxItemNoteLen:
		return ItemInput{}, errInvalidItem
	}
	return in, nil
}

type itemOp int

const (
	opEditItem itemOp = iota
	opDeleteItem
	opTakeItem
	opReleaseItem
	opMarkBought
)

// itemCmd is one change to an existing item.
type itemCmd struct {
	op     itemOp
	input  ItemInput // opEditItem
	bought bool      // opMarkBought
}

// applyItem applies cmd, made by the guest me, to item. It returns the new item and whether to
// delete it. It is pure: the store runs it inside a transaction on the item's document.
func applyItem(item Item, cmd itemCmd, me string) (Item, bool, error) {
	switch cmd.op {
	case opEditItem:
		// Any confirmed guest edits the data; responsible and state stay (FR-007).
		in, err := validateItemInput(cmd.input)
		if err != nil {
			return Item{}, false, err
		}
		item.Name, item.Category, item.Quantity, item.Note = in.Name, in.Category, in.Quantity, in.Note
		return item, false, nil

	case opDeleteItem:
		return item, true, nil

	case opTakeItem:
		if item.ResponsibleID != "" && item.ResponsibleID != me {
			return Item{}, false, errItemTaken
		}
		item.ResponsibleID = me
		return item, false, nil

	case opReleaseItem:
		if item.ResponsibleID != me {
			return Item{}, false, errNotResponsible
		}
		// Released items go back to "sin responsable" and "por comprar" (US3-4).
		item.ResponsibleID, item.Bought = "", false
		return item, false, nil

	case opMarkBought:
		// Only the responsible marks it, so a bought item always has one (FR-005).
		if item.ResponsibleID == "" || item.ResponsibleID != me {
			return Item{}, false, errNotResponsible
		}
		item.Bought = cmd.bought
		return item, false, nil

	default:
		return Item{}, false, fmt.Errorf("unknown item operation %d", cmd.op)
	}
}

// sortItems orders food before supplies, then by name without accents and by id.
func sortItems(items []Item) {
	slices.SortStableFunc(items, func(a, b Item) int {
		return cmp.Or(
			strings.Compare(a.Category, b.Category), // "comida" < "suministros"
			strings.Compare(foldAccents(a.Name), foldAccents(b.Name)),
			strings.Compare(a.ID, b.ID),
		)
	})
}

// PurchaseStore keeps the shopping list in events/{EVENT_ID}/items.
type PurchaseStore struct {
	client  *firestore.Client
	eventID string
}

// NewPurchaseStore returns a PurchaseStore for the given event.
func NewPurchaseStore(client *firestore.Client, eventID string) *PurchaseStore {
	return &PurchaseStore{client: client, eventID: eventID}
}

func (s *PurchaseStore) collection() *firestore.CollectionRef {
	return s.client.Collection("events").Doc(s.eventID).Collection("items")
}

func itemData(it Item, by string) map[string]any {
	return map[string]any{
		"name":          it.Name,
		"category":      it.Category,
		"quantity":      it.Quantity,
		"note":          it.Note,
		"responsibleId": it.ResponsibleID,
		"bought":        it.Bought,
		"updatedAt":     firestore.ServerTimestamp,
		"updatedBy":     by,
	}
}

// List returns every item of the event; handlers sort them.
func (s *PurchaseStore) List(ctx context.Context) ([]Item, error) {
	docs, err := s.collection().Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	items := make([]Item, 0, len(docs))
	for _, d := range docs {
		var it Item
		if err := d.DataTo(&it); err != nil {
			return nil, fmt.Errorf("decode item %s: %w", d.Ref.ID, err)
		}
		it.ID = d.Ref.ID
		items = append(items, it)
	}
	return items, nil
}

// Create stores a new, already validated item, without responsible and not bought (US2-1).
func (s *PurchaseStore) Create(ctx context.Context, in ItemInput, by string) error {
	it := Item{Name: in.Name, Category: in.Category, Quantity: in.Quantity, Note: in.Note}
	if _, err := s.collection().NewDoc().Create(ctx, itemData(it, by)); err != nil {
		return fmt.Errorf("create item: %w", err)
	}
	return nil
}

// Apply runs cmd on the item id inside a transaction, so concurrent "Yo lo llevo" on the same
// item end with a single responsible (FR-008). me is the guest who makes the change.
func (s *PurchaseStore) Apply(ctx context.Context, id string, cmd itemCmd, me string) error {
	ref := s.collection().Doc(id)
	err := s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return errItemNotFound
		}
		if err != nil {
			return err
		}
		var item Item
		if err := snap.DataTo(&item); err != nil {
			return fmt.Errorf("decode item %s: %w", id, err)
		}
		item.ID = id
		next, remove, err := applyItem(item, cmd, me)
		if err != nil {
			return err
		}
		if remove {
			return tx.Delete(ref)
		}
		return tx.Set(ref, itemData(next, me))
	})
	if err != nil {
		return fmt.Errorf("item %s: %w", id, err)
	}
	return nil
}

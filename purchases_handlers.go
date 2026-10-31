package main

import (
	"context"
	"errors"
	"net/http"
)

// HTTP side of the purchases module (spec 007). Every route sits behind requireGuest: any
// confirmed guest adds, edits and deletes items (FR-007), and the responsible is always the guest
// who makes the request (US3).

type purchaseStore interface {
	List(ctx context.Context) ([]Item, error)
	Create(ctx context.Context, in ItemInput, by string) error
	Apply(ctx context.Context, id string, cmd itemCmd, me string) error
}

type itemView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Quantity    string `json:"quantity,omitempty"`
	Note        string `json:"note,omitempty"`
	Responsible *Guest `json:"responsible,omitempty"`
	Bought      bool   `json:"bought"`
}

type purchasesSummary struct {
	Pending int `json:"pending"`
	Bought  int `json:"bought"`
}

type purchasesResponse struct {
	Items   []itemView       `json:"items"`
	Summary purchasesSummary `json:"summary"`
}

// purchasesView sorts the items, resolves each responsible with the catalog and counts the
// summary (FR-002, FR-006). A responsible no longer in the catalog keeps only its id.
func purchasesView(items []Item, catalog Catalog) purchasesResponse {
	sortItems(items)
	resp := purchasesResponse{Items: make([]itemView, 0, len(items))}
	for _, it := range items {
		view := itemView{ID: it.ID, Name: it.Name, Category: it.Category, Quantity: it.Quantity, Note: it.Note, Bought: it.Bought}
		if it.ResponsibleID != "" {
			g, ok := catalog.ByID(it.ResponsibleID)
			if !ok {
				g = Guest{ID: it.ResponsibleID}
			}
			view.Responsible = &g
		}
		if it.Bought {
			resp.Summary.Bought++
		} else {
			resp.Summary.Pending++
		}
		resp.Items = append(resp.Items, view)
	}
	return resp
}

// writePurchases answers with the whole list, the same view as GET /api/purchases.
func (s *server) writePurchases(w http.ResponseWriter, r *http.Request, status int) {
	catalog, ok := s.loadCatalog(w, r)
	if !ok {
		return
	}
	items, err := s.purchases.List(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, status, purchasesView(items, catalog))
}

// handlePurchases lists the items by category with the summary (US1).
func (s *server) handlePurchases(w http.ResponseWriter, r *http.Request, _ Account, _ Guest) {
	s.writePurchases(w, r, http.StatusOK)
}

// handleCreateItem adds an item without responsible, "por comprar" (US2).
func (s *server) handleCreateItem(w http.ResponseWriter, r *http.Request, _ Account, me Guest) {
	var in ItemInput
	if !decodeJSON(w, r, &in) {
		return
	}
	in, err := validateItemInput(in)
	if err == nil {
		err = s.purchases.Create(r.Context(), in, me.ID)
	}
	if err != nil {
		writePurchaseError(w, err)
		return
	}
	s.writePurchases(w, r, http.StatusCreated)
}

// applyItemCmd runs cmd on the item of the path and answers with the list or the domain error.
func (s *server) applyItemCmd(w http.ResponseWriter, r *http.Request, me Guest, cmd itemCmd, successStatus int) {
	if err := s.purchases.Apply(r.Context(), r.PathValue("itemID"), cmd, me.ID); err != nil {
		writePurchaseError(w, err)
		return
	}
	if successStatus == http.StatusNoContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.writePurchases(w, r, successStatus)
}

// handleUpdateItem edits name, category, quantity and note (US4).
func (s *server) handleUpdateItem(w http.ResponseWriter, r *http.Request, _ Account, me Guest) {
	var in ItemInput
	if !decodeJSON(w, r, &in) {
		return
	}
	s.applyItemCmd(w, r, me, itemCmd{op: opEditItem, input: in}, http.StatusOK)
}

// handleDeleteItem deletes an item; the UI asks for confirmation first (US4, FR-009).
func (s *server) handleDeleteItem(w http.ResponseWriter, r *http.Request, _ Account, me Guest) {
	s.applyItemCmd(w, r, me, itemCmd{op: opDeleteItem}, http.StatusNoContent)
}

// handleTakeItem makes the requester responsible for an item without one ("Yo lo llevo", US3).
func (s *server) handleTakeItem(w http.ResponseWriter, r *http.Request, _ Account, me Guest) {
	s.applyItemCmd(w, r, me, itemCmd{op: opTakeItem}, http.StatusOK)
}

// handleReleaseItem lets the responsible drop an item (US3-4).
func (s *server) handleReleaseItem(w http.ResponseWriter, r *http.Request, _ Account, me Guest) {
	s.applyItemCmd(w, r, me, itemCmd{op: opReleaseItem}, http.StatusOK)
}

// handleMarkBought lets the responsible mark or unmark an item as bought (US3-3).
func (s *server) handleMarkBought(w http.ResponseWriter, r *http.Request, _ Account, me Guest) {
	var req struct {
		Bought *bool `json:"bought"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Bought == nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest)
		return
	}
	s.applyItemCmd(w, r, me, itemCmd{op: opMarkBought, bought: *req.Bought}, http.StatusOK)
}

func writePurchaseError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInvalidItem):
		writeError(w, http.StatusBadRequest, codeInvalidItem)
	case errors.Is(err, errItemNotFound):
		writeError(w, http.StatusNotFound, codeItemNotFound)
	case errors.Is(err, errItemTaken):
		writeError(w, http.StatusConflict, codeItemTaken)
	case errors.Is(err, errNotResponsible):
		writeError(w, http.StatusConflict, codeNotResponsible)
	default:
		writeInternalError(w, err)
	}
}

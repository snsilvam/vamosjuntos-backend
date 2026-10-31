package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
)

// fakePurchases keeps items in memory and applies the same pure rule as PurchaseStore.
type fakePurchases struct {
	items  []Item
	nextID int
}

func (f *fakePurchases) List(context.Context) ([]Item, error) {
	return append([]Item{}, f.items...), nil
}

func (f *fakePurchases) Create(_ context.Context, in ItemInput, _ string) error {
	f.nextID++
	f.items = append(f.items, Item{ID: fmt.Sprintf("i%d", f.nextID), Name: in.Name, Category: in.Category, Quantity: in.Quantity, Note: in.Note})
	return nil
}

func (f *fakePurchases) Apply(_ context.Context, id string, cmd itemCmd, me string) error {
	i := slices.IndexFunc(f.items, func(it Item) bool { return it.ID == id })
	if i < 0 {
		return errItemNotFound
	}
	next, remove, err := applyItem(f.items[i], cmd, me)
	if err != nil {
		return err
	}
	if remove {
		f.items = slices.Delete(f.items, i, i+1)
	} else {
		f.items[i] = next
	}
	return nil
}

func decodePurchases(t *testing.T, body []byte) purchasesResponse {
	t.Helper()
	var r purchasesResponse
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return r
}

func TestPurchasesRequiresConfirmedGuest(t *testing.T) {
	env := transportEnv(t)
	w := env.do(t, request{method: "GET", path: "/api/purchases"})
	assertError(t, w, http.StatusUnauthorized, codeUnauthenticated)
	w = env.do(t, request{method: "GET", path: "/api/purchases", token: "carlos"})
	assertError(t, w, http.StatusForbidden, codeNotLinked)
}

func TestPurchasesFlow(t *testing.T) {
	// ana is Carmencita and beto is Liam, both confirmed.
	env := transportEnv(t)

	// US1: empty list with the summary at zero.
	w := env.do(t, request{method: "GET", path: "/api/purchases", token: "ana"})
	got := decodePurchases(t, w.Body.Bytes())
	if w.Code != http.StatusOK || len(got.Items) != 0 || got.Summary != (purchasesSummary{}) {
		t.Fatalf("empty: %d %s", w.Code, w.Body.String())
	}

	// US2: add items; an empty name is rejected.
	w = env.do(t, request{method: "POST", path: "/api/purchases/items", token: "ana", body: ItemInput{Name: " ", Category: categoryFood}})
	assertError(t, w, http.StatusBadRequest, codeInvalidItem)
	w = env.do(t, request{method: "POST", path: "/api/purchases/items", token: "ana", body: ItemInput{Name: "Carbón", Category: categorySupplies}})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	w = env.do(t, request{method: "POST", path: "/api/purchases/items", token: "beto", body: ItemInput{Name: "Arroz", Category: categoryFood, Quantity: "5 kg"}})
	got = decodePurchases(t, w.Body.Bytes())
	if w.Code != http.StatusCreated || got.Items[0].Name != "Arroz" || got.Items[0].Responsible != nil || got.Summary != (purchasesSummary{Pending: 2}) {
		t.Fatalf("second create: %d %s", w.Code, w.Body.String())
	}
	arroz := "/api/purchases/items/" + got.Items[0].ID

	// US3: Liam takes it; Carmencita cannot take, mark or release it.
	w = env.do(t, request{method: "POST", path: arroz + "/take", token: "beto"})
	got = decodePurchases(t, w.Body.Bytes())
	if w.Code != http.StatusOK || got.Items[0].Responsible == nil || got.Items[0].Responsible.Name != "Liam" {
		t.Fatalf("take: %d %s", w.Code, w.Body.String())
	}
	w = env.do(t, request{method: "POST", path: arroz + "/take", token: "ana"})
	assertError(t, w, http.StatusConflict, codeItemTaken)
	w = env.do(t, request{method: "POST", path: arroz + "/bought", token: "ana", body: map[string]bool{"bought": true}})
	assertError(t, w, http.StatusConflict, codeNotResponsible)
	w = env.do(t, request{method: "POST", path: arroz + "/release", token: "ana"})
	assertError(t, w, http.StatusConflict, codeNotResponsible)
	w = env.do(t, request{method: "POST", path: arroz + "/bought", token: "beto", body: map[string]string{}})
	assertError(t, w, http.StatusBadRequest, codeInvalidRequest)
	w = env.do(t, request{method: "POST", path: arroz + "/bought", token: "beto", body: map[string]bool{"bought": true}})
	got = decodePurchases(t, w.Body.Bytes())
	if w.Code != http.StatusOK || !got.Items[0].Bought || got.Summary != (purchasesSummary{Pending: 1, Bought: 1}) {
		t.Fatalf("mark bought: %d %s", w.Code, w.Body.String())
	}

	// US4: editing keeps responsible and state.
	w = env.do(t, request{method: "PUT", path: arroz, token: "ana", body: ItemInput{Name: "Arroz", Category: categoryFood, Quantity: "10 kg"}})
	got = decodePurchases(t, w.Body.Bytes())
	if w.Code != http.StatusOK || got.Items[0].Quantity != "10 kg" || !got.Items[0].Bought || got.Items[0].Responsible.Name != "Liam" {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}

	// Releasing goes back to "sin responsable" and "por comprar".
	w = env.do(t, request{method: "POST", path: arroz + "/release", token: "beto"})
	got = decodePurchases(t, w.Body.Bytes())
	if w.Code != http.StatusOK || got.Items[0].Responsible != nil || got.Items[0].Bought || got.Summary != (purchasesSummary{Pending: 2}) {
		t.Fatalf("release: %d %s", w.Code, w.Body.String())
	}

	// Delete, then 404 on every action.
	w = env.do(t, request{method: "DELETE", path: arroz, token: "ana"})
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	for _, req := range []request{
		{method: "DELETE", path: arroz, token: "ana"},
		{method: "POST", path: arroz + "/take", token: "ana"},
		{method: "PUT", path: arroz, token: "ana", body: ItemInput{Name: "Arroz", Category: categoryFood}},
	} {
		assertError(t, env.do(t, req), http.StatusNotFound, codeItemNotFound)
	}
}

package main

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestValidateItemInput(t *testing.T) {
	valid := ItemInput{Name: "Arroz", Category: categoryFood, Quantity: "5 kg"}
	tests := []struct {
		name string
		edit func(*ItemInput)
		want error
	}{
		{"valid", func(*ItemInput) {}, nil},
		{"supplies", func(in *ItemInput) { in.Category = categorySupplies }, nil},
		{"empty name", func(in *ItemInput) { in.Name = "  " }, errInvalidItem},
		{"name of 60 runes", func(in *ItemInput) { in.Name = strings.Repeat("ñ", 60) }, nil},
		{"name of 61 runes", func(in *ItemInput) { in.Name = strings.Repeat("ñ", 61) }, errInvalidItem},
		{"unknown category", func(in *ItemInput) { in.Category = "bebidas" }, errInvalidItem},
		{"quantity of 31 runes", func(in *ItemInput) { in.Quantity = strings.Repeat("1", 31) }, errInvalidItem},
		{"note of 200 runes", func(in *ItemInput) { in.Note = strings.Repeat("a", 200) }, nil},
		{"note of 201 runes", func(in *ItemInput) { in.Note = strings.Repeat("a", 201) }, errInvalidItem},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := valid
			tt.edit(&in)
			if _, err := validateItemInput(in); !errors.Is(err, tt.want) {
				t.Fatalf("validateItemInput(%+v) error = %v, want %v", in, err, tt.want)
			}
		})
	}
	got, _ := validateItemInput(ItemInput{Name: " Arroz ", Category: categoryFood, Quantity: " 5 kg ", Note: " integral "})
	if got != (ItemInput{Name: "Arroz", Category: categoryFood, Quantity: "5 kg", Note: "integral"}) {
		t.Fatalf("trim = %+v", got)
	}
}

func TestApplyItem(t *testing.T) {
	free := Item{ID: "i1", Name: "Arroz", Category: categoryFood}
	mine := Item{ID: "i1", Name: "Arroz", Category: categoryFood, ResponsibleID: "ana"}
	boughtByMe := Item{ID: "i1", Name: "Arroz", Category: categoryFood, ResponsibleID: "ana", Bought: true}
	tests := []struct {
		name       string
		item       Item
		cmd        itemCmd
		me         string
		want       Item
		wantDelete bool
		wantErr    error
	}{
		{"take a free item", free, itemCmd{op: opTakeItem}, "ana", mine, false, nil},
		{"take my own item again", mine, itemCmd{op: opTakeItem}, "ana", mine, false, nil},
		{"take someone else's item", mine, itemCmd{op: opTakeItem}, "beto", Item{}, false, errItemTaken},
		{"mark bought", mine, itemCmd{op: opMarkBought, bought: true}, "ana", boughtByMe, false, nil},
		{"unmark bought", boughtByMe, itemCmd{op: opMarkBought, bought: false}, "ana", mine, false, nil},
		{"mark someone else's item", mine, itemCmd{op: opMarkBought, bought: true}, "beto", Item{}, false, errNotResponsible},
		{"mark an item without responsible", free, itemCmd{op: opMarkBought, bought: true}, "ana", Item{}, false, errNotResponsible},
		{"release a bought item", boughtByMe, itemCmd{op: opReleaseItem}, "ana", free, false, nil},
		{"release someone else's item", mine, itemCmd{op: opReleaseItem}, "beto", Item{}, false, errNotResponsible},
		{"release an item without responsible", free, itemCmd{op: opReleaseItem}, "ana", Item{}, false, errNotResponsible},
		{"edit keeps responsible and state", boughtByMe, itemCmd{op: opEditItem, input: ItemInput{Name: "Arroz blanco", Category: categoryFood, Quantity: "10 kg"}}, "beto",
			Item{ID: "i1", Name: "Arroz blanco", Category: categoryFood, Quantity: "10 kg", ResponsibleID: "ana", Bought: true}, false, nil},
		{"invalid edit", mine, itemCmd{op: opEditItem, input: ItemInput{Name: "", Category: categoryFood}}, "ana", Item{}, false, errInvalidItem},
		{"anyone deletes", mine, itemCmd{op: opDeleteItem}, "beto", mine, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, del, err := applyItem(tt.item, tt.cmd, tt.me)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if err == nil && (got != tt.want || del != tt.wantDelete) {
				t.Fatalf("applyItem = %+v, delete %v; want %+v, delete %v", got, del, tt.want, tt.wantDelete)
			}
		})
	}
}

func TestSortItems(t *testing.T) {
	items := []Item{
		{ID: "4", Name: "Carbón", Category: categorySupplies},
		{ID: "3", Name: "Huevos", Category: categoryFood},
		{ID: "2", Name: "Árepas", Category: categoryFood},
		{ID: "1", Name: "Huevos", Category: categoryFood},
	}
	sortItems(items)
	var got []string
	for _, it := range items {
		got = append(got, it.ID)
	}
	// Food first; "Árepas" sorts as "arepas"; equal names fall back to id.
	if want := []string{"2", "1", "3", "4"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

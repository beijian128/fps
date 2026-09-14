package logic

import (
	"reflect"
	"testing"
)

func TestDefaultCatalog(t *testing.T) {
	catalog := MustDefaultCatalog()
	ids := make([]string, 0, len(catalog.Definitions()))
	for _, def := range catalog.Definitions() {
		ids = append(ids, def.ID)
	}
	want := []string{"rifle", "pistol", "shotgun", "medkit"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids=%v want=%v", ids, want)
	}
	rifle, ok := catalog.Lookup("rifle")
	if !ok || rifle.Price != 300 || rifle.EquipSlot != SlotPrimaryWeapon {
		t.Fatalf("rifle=%+v ok=%v", rifle, ok)
	}
}

func TestNewCatalogRejectsInvalidDefinitions(t *testing.T) {
	cases := []struct {
		name string
		defs []ItemDefinition
	}{
		{"empty-id", []ItemDefinition{{DisplayName: "x", Price: 1}}},
		{"negative-price", []ItemDefinition{{ID: "x", DisplayName: "x", Price: -1}}},
		{"bad-slot", []ItemDefinition{{ID: "x", DisplayName: "x", Price: 1, EquipSlot: "back"}}},
		{"duplicate", []ItemDefinition{
			{ID: "x", DisplayName: "x", Price: 1},
			{ID: "x", DisplayName: "y", Price: 2},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewCatalog(tc.defs); err == nil {
				t.Fatal("应拒绝非法目录")
			}
		})
	}
}

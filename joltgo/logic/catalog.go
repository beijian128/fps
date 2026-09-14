package logic

import (
	"fmt"
	"strings"
)

const SlotPrimaryWeapon = "primary_weapon"

type ItemDefinition struct {
	ID          string
	DisplayName string
	Price       int64
	EquipSlot   string
}

type Catalog struct {
	defs []ItemDefinition
	byID map[string]ItemDefinition
}

func NewCatalog(defs []ItemDefinition) (Catalog, error) {
	byID := make(map[string]ItemDefinition, len(defs))
	copied := make([]ItemDefinition, 0, len(defs))
	for _, def := range defs {
		if strings.TrimSpace(def.ID) == "" {
			return Catalog{}, fmt.Errorf("logic: empty item id")
		}
		if strings.TrimSpace(def.DisplayName) == "" {
			return Catalog{}, fmt.Errorf("logic: empty display name for %s", def.ID)
		}
		if def.Price < 0 {
			return Catalog{}, fmt.Errorf("logic: negative price for %s", def.ID)
		}
		if def.EquipSlot != "" && def.EquipSlot != SlotPrimaryWeapon {
			return Catalog{}, fmt.Errorf("logic: unknown slot %q for %s", def.EquipSlot, def.ID)
		}
		if _, exists := byID[def.ID]; exists {
			return Catalog{}, fmt.Errorf("logic: duplicate item id %s", def.ID)
		}
		byID[def.ID] = def
		copied = append(copied, def)
	}
	return Catalog{defs: copied, byID: byID}, nil
}

func MustDefaultCatalog() Catalog {
	catalog, err := NewCatalog([]ItemDefinition{
		{ID: "rifle", DisplayName: "步枪", Price: 300, EquipSlot: SlotPrimaryWeapon},
		{ID: "pistol", DisplayName: "手枪", Price: 150, EquipSlot: SlotPrimaryWeapon},
		{ID: "shotgun", DisplayName: "霰弹枪", Price: 500, EquipSlot: SlotPrimaryWeapon},
		{ID: "medkit", DisplayName: "医疗包", Price: 50},
	})
	if err != nil {
		panic(err)
	}
	return catalog
}

func (c Catalog) Lookup(itemID string) (ItemDefinition, bool) {
	def, ok := c.byID[itemID]
	return def, ok
}

func (c Catalog) Definitions() []ItemDefinition {
	return append([]ItemDefinition(nil), c.defs...)
}

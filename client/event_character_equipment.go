package client

import (
	"sync"
	"time"

	"github.com/ao-data/albiondata-client/log"
)

// EquippedItem is one slot of a player's gear at a point in time.
type EquippedItem struct {
	Slot      int    `json:"slot"`      // slot index (0..n) — game-defined order
	ItemID    string `json:"itemId"`    // resolved string ID (e.g. T6_HEAD_PLATE_KEEPER)
	NumericID int    `json:"numericId"` // raw item type id from packet
}

// equipmentSnapshot wraps a player's last-known equipment with a timestamp.
type equipmentSnapshot struct {
	items     []EquippedItem
	updatedAt time.Time
}

// equipmentByName maps player name -> latest equipment snapshot. We key by name
// (not ObjectID) so that the death handler — which only has the victim name —
// can look up gear at death time.
var equipmentByName sync.Map // map[string]*equipmentSnapshot

const equipmentTTL = 30 * time.Minute

func init() {
	go equipmentCacheCleanup()
}

func equipmentCacheCleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		equipmentByName.Range(func(key, value interface{}) bool {
			snap, ok := value.(*equipmentSnapshot)
			if ok && now.Sub(snap.updatedAt) > equipmentTTL {
				equipmentByName.Delete(key)
			}
			return true
		})
	}
}

// getEquipmentForPlayer returns the cached equipment for a player name, or nil
// if we have not seen any equipment for them recently.
func getEquipmentForPlayer(name string) []EquippedItem {
	if name == "" {
		return nil
	}
	if v, ok := equipmentByName.Load(name); ok {
		if snap, ok2 := v.(*equipmentSnapshot); ok2 {
			out := make([]EquippedItem, len(snap.items))
			copy(out, snap.items)
			return out
		}
	}
	return nil
}

// eventCharacterEquipmentChanged fires when a player swaps gear.
//
// The exact param layout for opcode 90 has shifted between game versions.
// We capture ObjectID + the most likely "items" slice and resolve names
// best-effort. If the layout is wrong on a given build, the slice will be
// empty and we silently skip — better than crashing on bad data.
type eventCharacterEquipmentChanged struct {
	ObjectID int64   `mapstructure:"0"`
	Items    []int32 `mapstructure:"1"`
}

func (ev eventCharacterEquipmentChanged) Process(state *albionState) {
	if ev.ObjectID == 0 || len(ev.Items) == 0 {
		return
	}
	name := playerNameByObjectID(ev.ObjectID)
	if name == "" {
		return // we have not seen this character yet — skip rather than store under empty key
	}

	items := make([]EquippedItem, 0, len(ev.Items))
	for slot, numID := range ev.Items {
		if numID <= 0 {
			continue // empty slot
		}
		itemName := resolveItemName(int(numID))
		if IsSpecialItem(int(numID)) {
			continue
		}
		items = append(items, EquippedItem{
			Slot:      slot,
			ItemID:    itemName,
			NumericID: int(numID),
		})
	}
	if len(items) == 0 {
		return
	}
	equipmentByName.Store(name, &equipmentSnapshot{
		items:     items,
		updatedAt: time.Now(),
	})
	log.Debugf("[Equipment] %s now wearing %d items", name, len(items))
}

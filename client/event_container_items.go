package client

import (
	"sync"
	"time"

	"github.com/ao-data/albiondata-client/log"
)

// === ITEM EVENT STRUCTS (typed from discovered Photon params) ===

// eventNewSimpleItem fires for each stackable item in a container
type eventNewSimpleItem struct {
	SlotPosition int32 `mapstructure:"0"` // Global slot index assigned by the game (int32 — values can exceed 500k+)
	ItemTypeID   int32 `mapstructure:"1"` // Numeric item type ID (map via itemmap.json)
	Quantity     int16 `mapstructure:"2"` // Stack count
	ObjectID     int64 `mapstructure:"4"` // Unique object instance ID
}

func (event eventNewSimpleItem) Process(state *albionState) {
	itemName := resolveItemName(int(event.ItemTypeID))
	qty := int(event.Quantity)
	if qty <= 0 {
		qty = 1
	}
	log.Debugf("[SimpleItem] slot=%d item=%s (id=%d) qty=%d", event.SlotPosition, itemName, event.ItemTypeID, qty)

	// Store in global cache — evAttachItemContainer will look these up by slot
	globalItemCache.Store(int(event.SlotPosition), CapturedItem{
		ItemID:      itemName,
		NumericID:   int(event.ItemTypeID),
		Quality:     1, // Simple items are always Normal quality
		Quantity:    qty,
		Enchantment: 0,
		IsEquipment: false,
		Slot:        int(event.SlotPosition),
		Weight:      resolveItemWeight(int(event.ItemTypeID)),
	})
}

// eventNewEquipmentItem fires for each gear item in a container
type eventNewEquipmentItem struct {
	SlotPosition   int32    `mapstructure:"0"`  // Global slot index (int32 — values can exceed 500k+)
	ItemTypeID     int32    `mapstructure:"1"`  // Numeric item type ID
	Quality        int8     `mapstructure:"2"`  // 1=Normal, 2=Good, 3=Outstanding, 4=Excellent, 5=Masterpiece
	ObjectID       int64    `mapstructure:"4"`  // Unique object instance ID
	CrafterName    string   `mapstructure:"5"`  // Who crafted it
	Enchantment    int8     `mapstructure:"6"`  // Enchantment level (0-4)
	Durability     int64    `mapstructure:"7"`  // Current durability
	SpellIDs       []int16  `mapstructure:"8"`  // Equipped spells
	SocketData     []int16  `mapstructure:"9"`  // Socket info
	UnknownParam10 int8     `mapstructure:"10"` // Always 0
}

func (event eventNewEquipmentItem) Process(state *albionState) {
	itemName := resolveItemName(int(event.ItemTypeID))
	qual := int(event.Quality)
	if qual <= 0 {
		qual = 1
	}
	log.Debugf("[EquipItem] slot=%d item=%s (id=%d) quality=%d crafter=%s", event.SlotPosition, itemName, event.ItemTypeID, qual, event.CrafterName)

	// Store in global cache — evAttachItemContainer will look these up by slot
	globalItemCache.Store(int(event.SlotPosition), CapturedItem{
		ItemID:      itemName,
		NumericID:   int(event.ItemTypeID),
		Quality:     qual,
		Quantity:    1, // Equipment is always qty 1
		Enchantment: int(event.Enchantment),
		IsEquipment: true,
		Slot:        int(event.SlotPosition),
		CrafterName: event.CrafterName,
		Weight:      resolveItemWeight(int(event.ItemTypeID)),
	})
}

// eventInventoryPutItem fires when an item is placed into a slot
type eventInventoryPutItem struct {
	// Not needed for chest capture — items come via NewSimpleItem/NewEquipmentItem
}

func (event eventInventoryPutItem) Process(state *albionState) {
	// No-op — keeping handler registered to prevent debug spam
}

// eventNewJournalItem fires for journal items in a container
type eventNewJournalItem struct {
	SlotPosition int32 `mapstructure:"0"`
	ItemTypeID   int32 `mapstructure:"1"`
	Quantity     int16 `mapstructure:"2"`
}

func (event eventNewJournalItem) Process(state *albionState) {
	itemName := resolveItemName(int(event.ItemTypeID))
	qty := int(event.Quantity)
	if qty <= 0 {
		qty = 1
	}
	log.Debugf("[JournalItem] slot=%d item=%s qty=%d", event.SlotPosition, itemName, qty)

	// Store in global cache
	globalItemCache.Store(int(event.SlotPosition), CapturedItem{
		ItemID:      itemName,
		NumericID:   int(event.ItemTypeID),
		Quality:     1,
		Quantity:    qty,
		IsEquipment: false,
		Slot:        int(event.SlotPosition),
		Weight:      resolveItemWeight(int(event.ItemTypeID)),
	})
}

// eventNewFurnitureItem fires for furniture items (islands, chests, decorations, mounts stored as furniture)
type eventNewFurnitureItem struct {
	SlotPosition int32 `mapstructure:"0"`
	ItemTypeID   int32 `mapstructure:"1"`
	Quality      int8  `mapstructure:"2"`
	ObjectID     int64 `mapstructure:"4"`
}

func (event eventNewFurnitureItem) Process(state *albionState) {
	itemName := resolveItemName(int(event.ItemTypeID))
	qual := int(event.Quality)
	if qual <= 0 {
		qual = 1
	}
	log.Debugf("[FurnitureItem] slot=%d item=%s (id=%d) quality=%d", event.SlotPosition, itemName, event.ItemTypeID, qual)

	globalItemCache.Store(int(event.SlotPosition), CapturedItem{
		ItemID:      itemName,
		NumericID:   int(event.ItemTypeID),
		Quality:     qual,
		Quantity:    1,
		IsEquipment: false,
		Slot:        int(event.SlotPosition),
		Weight:      resolveItemWeight(int(event.ItemTypeID)),
	})
}

// eventNewKillTrophyItem fires for kill trophy items in containers
type eventNewKillTrophyItem struct {
	SlotPosition int32 `mapstructure:"0"`
	ItemTypeID   int32 `mapstructure:"1"`
	Quality      int8  `mapstructure:"2"`
	ObjectID     int64 `mapstructure:"4"`
}

func (event eventNewKillTrophyItem) Process(state *albionState) {
	itemName := resolveItemName(int(event.ItemTypeID))
	qual := int(event.Quality)
	if qual <= 0 {
		qual = 1
	}
	log.Debugf("[KillTrophyItem] slot=%d item=%s (id=%d) quality=%d", event.SlotPosition, itemName, event.ItemTypeID, qual)

	globalItemCache.Store(int(event.SlotPosition), CapturedItem{
		ItemID:      itemName,
		NumericID:   int(event.ItemTypeID),
		Quality:     qual,
		Quantity:    1,
		IsEquipment: false,
		Slot:        int(event.SlotPosition),
		Weight:      resolveItemWeight(int(event.ItemTypeID)),
	})
}

// eventNewLaborerItem fires for laborer contract items
type eventNewLaborerItem struct {
	SlotPosition int32 `mapstructure:"0"`
	ItemTypeID   int32 `mapstructure:"1"`
	Quantity     int16 `mapstructure:"2"`
	ObjectID     int64 `mapstructure:"4"`
}

func (event eventNewLaborerItem) Process(state *albionState) {
	itemName := resolveItemName(int(event.ItemTypeID))
	qty := int(event.Quantity)
	if qty <= 0 {
		qty = 1
	}
	log.Debugf("[LaborerItem] slot=%d item=%s (id=%d) qty=%d", event.SlotPosition, itemName, event.ItemTypeID, qty)

	globalItemCache.Store(int(event.SlotPosition), CapturedItem{
		ItemID:      itemName,
		NumericID:   int(event.ItemTypeID),
		Quality:     1,
		Quantity:    qty,
		IsEquipment: false,
		Slot:        int(event.SlotPosition),
		Weight:      resolveItemWeight(int(event.ItemTypeID)),
	})
}

// === GLOBAL ITEM CACHE ===
// The game sends item events (EquipItem, SimpleItem, etc.) for ALL nearby containers.
// Each item has a unique global slot number. evAttachItemContainer param 3 contains
// the slot numbers that belong to a specific container tab. We look them up here.

var globalItemCache sync.Map // map[int]CapturedItem — key is global slot number

// === CONTAINER CAPTURE STRUCTS ===

type CapturedItem struct {
	ItemID      string  `json:"itemId"`
	NumericID   int     `json:"numericId"`
	Quality     int     `json:"quality"`
	Quantity    int     `json:"quantity"`
	Enchantment int     `json:"enchantment,omitempty"`
	IsEquipment bool    `json:"isEquipment"`
	Slot        int     `json:"slot"`
	CrafterName string  `json:"crafterName,omitempty"`
	Weight      float64 `json:"weight"` // per-unit weight in kg from game data
}

type ContainerCapture struct {
	Items       []CapturedItem `json:"items"`
	ContainerID string         `json:"containerId"`
	TabName     string         `json:"tabName,omitempty"`
	TabIndex    int            `json:"tabIndex"` // 0-based index into VaultTabs; -1 if unknown
	VaultTabs   []VaultTab     `json:"vaultTabs,omitempty"`
	IsGuild     bool           `json:"isGuild"`
	PlayerName  string         `json:"playerName"`
	Location    string         `json:"location"`
	CapturedAt  int64          `json:"capturedAt"`
	ItemCount   int            `json:"itemCount"`
	TotalWeight float64        `json:"totalWeight"` // sum of all item weights in kg
}

// BuildCaptureFromSlots looks up items from the global cache using the slot IDs
// from evAttachItemContainer param 3, then builds and sends the capture.
func BuildCaptureFromSlots(slotIDs []int, containerGUID string, tabName string, tabIndex int) {
	var items []CapturedItem
	var missing int

	for _, slotID := range slotIDs {
		if slotID == 0 {
			continue // empty slot
		}
		if val, ok := globalItemCache.Load(slotID); ok {
			item := val.(CapturedItem)
			if IsSpecialItem(item.NumericID) {
				log.Debugf("[ContainerCapture] Skipping special item at slot %d: %s (id=%d)", slotID, item.ItemID, item.NumericID)
				continue
			}
			items = append(items, item)
		} else {
			missing++
			log.Debugf("[ContainerCapture] Slot %d referenced but not in cache (item type not captured — mount/furniture/trophy?)", slotID)
		}
	}

	if len(items) == 0 && missing == 0 {
		log.Info("[ContainerCapture] Empty tab — no items to capture")
		return
	}

	// Resolve vault tab info
	resolvedTabName := tabName
	var vaultTabs []VaultTab
	var isGuild bool
	if vi := GetCurrentVaultTabs(); vi != nil {
		vaultTabs = vi.Tabs
		isGuild = vi.IsGuild
		if resolvedTabName == "" && tabIndex >= 0 && tabIndex < len(vi.Tabs) {
			name := vi.Tabs[tabIndex].Name
			if name != "" {
				resolvedTabName = name
				log.Infof("[ContainerCapture] Resolved tab name from index %d: %s", tabIndex, resolvedTabName)
			}
		}
	}

	// Calculate total weight
	var totalWeight float64
	for _, item := range items {
		totalWeight += item.Weight * float64(item.Quantity)
	}

	capture := &ContainerCapture{
		Items:       items,
		ContainerID: containerGUID,
		TabName:     resolvedTabName,
		TabIndex:    tabIndex,
		VaultTabs:   vaultTabs,
		IsGuild:     isGuild,
		CapturedAt:  time.Now().UnixMilli(),
		ItemCount:   len(items),
		TotalWeight: totalWeight,
	}

	log.Infof("[ContainerCapture] Captured %d items (%d equipment, %d stackable, %d missing from cache) — total weight: %.1f kg",
		len(items),
		countEquipment(items),
		len(items)-countEquipment(items),
		missing,
		totalWeight)

	// Log first 10 items as summary
	for i, item := range items {
		if i >= 10 {
			log.Infof("[ContainerCapture]   ... and %d more items", len(items)-10)
			break
		}
		log.Infof("[ContainerCapture]   %s q%d x%d (%.1fkg)", item.ItemID, item.Quality, item.Quantity, item.Weight*float64(item.Quantity))
	}

	// Send to VPS via WebSocket relay
	SendChestCapture(capture)
}

func countEquipment(items []CapturedItem) int {
	count := 0
	for _, item := range items {
		if item.IsEquipment {
			count++
		}
	}
	return count
}

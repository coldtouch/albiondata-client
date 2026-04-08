package client

import (
	"sync"
	"time"

	"github.com/ao-data/albiondata-client/log"
)

// === ITEM EVENT STRUCTS (typed from discovered Photon params) ===

// eventNewSimpleItem fires for each stackable item in a container
type eventNewSimpleItem struct {
	SlotPosition int16 `mapstructure:"0"` // Slot index in the container
	ItemTypeID   int16 `mapstructure:"1"` // Numeric item type ID (map via itemmap.json)
	Quantity     int8  `mapstructure:"2"` // Stack count
	ObjectID     int64 `mapstructure:"4"` // Unique object instance ID
}

func (event eventNewSimpleItem) Process(state *albionState) {
	itemName := resolveItemName(int(event.ItemTypeID))
	qty := int(event.Quantity)
	if qty <= 0 {
		qty = 1
	}
	log.Debugf("[SimpleItem] slot=%d item=%s (id=%d) qty=%d", event.SlotPosition, itemName, event.ItemTypeID, qty)

	containerCollector.addItem(CapturedItem{
		ItemID:      itemName,
		NumericID:   int(event.ItemTypeID),
		Quality:     1, // Simple items are always Normal quality
		Quantity:    qty,
		Enchantment: 0,
		IsEquipment: false,
		Slot:        int(event.SlotPosition),
	})
}

// eventNewEquipmentItem fires for each gear item in a container
type eventNewEquipmentItem struct {
	SlotPosition   int16    `mapstructure:"0"`  // Slot index
	ItemTypeID     int16    `mapstructure:"1"`  // Numeric item type ID
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

	containerCollector.addItem(CapturedItem{
		ItemID:      itemName,
		NumericID:   int(event.ItemTypeID),
		Quality:     qual,
		Quantity:    1, // Equipment is always qty 1
		Enchantment: int(event.Enchantment),
		IsEquipment: true,
		Slot:        int(event.SlotPosition),
		CrafterName: event.CrafterName,
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
	SlotPosition int16 `mapstructure:"0"`
	ItemTypeID   int16 `mapstructure:"1"`
	Quantity     int8  `mapstructure:"2"`
}

func (event eventNewJournalItem) Process(state *albionState) {
	itemName := resolveItemName(int(event.ItemTypeID))
	qty := int(event.Quantity)
	if qty <= 0 {
		qty = 1
	}
	log.Debugf("[JournalItem] slot=%d item=%s qty=%d", event.SlotPosition, itemName, qty)

	containerCollector.addItem(CapturedItem{
		ItemID:      itemName,
		NumericID:   int(event.ItemTypeID),
		Quality:     1,
		Quantity:    qty,
		IsEquipment: false,
		Slot:        int(event.SlotPosition),
	})
}

// === CONTAINER ITEM COLLECTOR ===
// Collects items between ContainerOpen and a timeout/close, then bundles them

type CapturedItem struct {
	ItemID      string `json:"itemId"`
	NumericID   int    `json:"numericId"`
	Quality     int    `json:"quality"`
	Quantity    int    `json:"quantity"`
	Enchantment int    `json:"enchantment,omitempty"`
	IsEquipment bool   `json:"isEquipment"`
	Slot        int    `json:"slot"`
	CrafterName string `json:"crafterName,omitempty"`
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
}

type itemCollector struct {
	mu          sync.Mutex
	items       []CapturedItem
	containerID string
	tabName     string
	tabIndex    int // current tab index (0-based); set by ContainerOpen/ContainerManageSubContainer
	collecting  bool
	timer       *time.Timer
	lastCapture *ContainerCapture
}

var containerCollector = &itemCollector{}

func (c *itemCollector) setContainerID(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.containerID = id
}

func (c *itemCollector) setTabName(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tabName = name
}

func (c *itemCollector) resetTabIndex() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tabIndex = 0
}

func (c *itemCollector) incrementTabIndex() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tabIndex++
}

func (c *itemCollector) setTabIndex(idx int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tabIndex = idx
}

func (c *itemCollector) startCollecting() {
	c.mu.Lock()

	// If we already have items from a previous tab, finalize them first
	// This handles rapid tab switching where the user clicks tabs faster than the timeout
	if c.collecting && len(c.items) > 0 {
		if c.timer != nil {
			c.timer.Stop()
		}
		c.finalizeUnlocked() // sends previous tab's capture while we hold the lock
	}

	// Reset for new container
	c.items = nil
	c.tabName = ""
	c.collecting = true

	// Auto-finalize after 3 seconds of no new items
	// (server sends all items in rapid burst, then stops)
	if c.timer != nil {
		c.timer.Stop()
	}
	c.timer = time.AfterFunc(3*time.Second, func() {
		c.finalize()
	})

	c.mu.Unlock()
}

func (c *itemCollector) addItem(item CapturedItem) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.collecting {
		// Ignore item events that happen outside of an explicit ContainerOpen
		// (zone loading, player equipment, bank proximity, etc.)
		return
	}

	// Skip internal/currency items (silver, gold, fame credits, etc.)
	// These have negative numeric IDs and aren't tradable on the market
	if IsSpecialItem(item.NumericID) {
		log.Debugf("[ItemCollector] Skipping special item: %s (id=%d)", item.ItemID, item.NumericID)
		return
	}

	c.items = append(c.items, item)

	// Reset the timer — more items might be coming
	if c.timer != nil {
		c.timer.Stop()
	}
	c.timer = time.AfterFunc(2*time.Second, func() {
		c.finalize()
	})
}

func (c *itemCollector) finalize() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finalizeUnlocked()
}

// finalizeUnlocked does the actual finalization work. Caller must hold c.mu.
func (c *itemCollector) finalizeUnlocked() {
	if !c.collecting || len(c.items) == 0 {
		c.collecting = false
		return
	}

	// Resolve tab name: use direct GUID-matched name if set, otherwise look up by tabIndex
	resolvedTabName := c.tabName
	var vaultTabs []VaultTab
	var isGuild bool
	if vi := GetCurrentVaultTabs(); vi != nil {
		vaultTabs = vi.Tabs
		isGuild = vi.IsGuild
		if resolvedTabName == "" && c.tabIndex >= 0 && c.tabIndex < len(vi.Tabs) {
			name := vi.Tabs[c.tabIndex].Name
			if name != "" {
				resolvedTabName = name
				log.Infof("[ContainerCapture] Resolved tab name from index %d: %s", c.tabIndex, resolvedTabName)
			}
		}
	}

	capture := &ContainerCapture{
		Items:       c.items,
		ContainerID: c.containerID,
		TabName:     resolvedTabName,
		TabIndex:    c.tabIndex,
		VaultTabs:   vaultTabs,
		IsGuild:     isGuild,
		CapturedAt:  time.Now().UnixMilli(),
		ItemCount:   len(c.items),
	}

	c.lastCapture = capture
	c.collecting = false

	log.Infof("[ContainerCapture] Captured %d items (%d equipment, %d stackable)",
		len(c.items),
		countEquipment(c.items),
		len(c.items)-countEquipment(c.items))

	// Log first 10 items as summary (avoid flooding logs for large tabs)
	for i, item := range c.items {
		if i >= 10 {
			log.Infof("[ContainerCapture]   ... and %d more items", len(c.items)-10)
			break
		}
		log.Infof("[ContainerCapture]   %s q%d x%d", item.ItemID, item.Quality, item.Quantity)
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

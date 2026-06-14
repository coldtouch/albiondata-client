package client

import (
	"strings"
	"sync"
	"time"

	"github.com/ao-data/albiondata-client/log"
)

const lootContainerTTL = 30 * time.Minute

type lootContainer struct {
	id        int
	owner     string
	kind      string
	guids     map[string]struct{}
	items     map[int]CapturedItem
	updatedAt time.Time
}

var (
	lootContainerMu     sync.Mutex
	lootContainersByID  = make(map[int]*lootContainer)
	lootContainersByUID = make(map[string]*lootContainer)
)

func init() {
	go lootContainerCleanupLoop()
}

// eventNewLoot announces a corpse/bag loot container and the original owner.
// OtherGrabbedLoot does not fire for the local player, so self-loot is inferred
// later when the local client moves an item out of one of these containers.
type eventNewLoot struct {
	ContainerID int    `mapstructure:"0"`
	Owner       string `mapstructure:"3"`
}

func (ev eventNewLoot) Process(state *albionState) {
	if ev.ContainerID <= 0 || ev.Owner == "" {
		return
	}

	kind := "player"
	if strings.HasPrefix(ev.Owner, "@MOB") {
		kind = "monster"
	}

	upsertLootContainer(ev.ContainerID, ev.Owner, kind)
	log.Debugf("[LootContainer] New loot container id=%d owner=%s type=%s", ev.ContainerID, ev.Owner, kind)
}

type eventDetachItemContainer struct {
	GUID []int8 `mapstructure:"0"`
}

func (ev eventDetachItemContainer) Process(state *albionState) {
	deleteLootContainerByGUID(guidHex(ev.GUID))
}

// operationInventoryMoveItem fires when the local player moves an item between
// containers. If the source GUID belongs to a known loot container, this is the
// missing self-loot signal.
type operationInventoryMoveItem struct {
	FromSlot int    `mapstructure:"0"`
	FromGUID []int8 `mapstructure:"1"`
	ToSlot   int    `mapstructure:"3"`
	ToGUID   []int8 `mapstructure:"4"`
}

func (op operationInventoryMoveItem) Process(state *albionState) {
	fromGUID := guidHex(op.FromGUID)
	toGUID := guidHex(op.ToGUID)
	if fromGUID == "" || fromGUID == toGUID {
		return
	}

	selfName := state.GetCharacterName()
	if selfName == "" {
		log.Debug("[SelfLoot] Local character name unknown; skipping self-loot attribution")
		return
	}

	item, owner, ok := popLootContainerItem(fromGUID, op.FromSlot)
	if !ok {
		return
	}
	if owner == "" || item.ItemID == "" || item.NumericID <= 0 {
		log.Debugf("[SelfLoot] Incomplete loot-container item from guid=%s slot=%d; skipping", fromGUID, op.FromSlot)
		return
	}

	qty := item.Quantity
	if qty <= 0 {
		qty = 1
	}

	lootedBy := getPlayer(selfName)
	lootedFrom := getPlayer(owner)
	lootEvent := &LootEvent{
		Timestamp:  time.Now().UnixMilli(),
		LootedBy:   *lootedBy,
		LootedFrom: *lootedFrom,
		ItemID:     item.ItemID,
		NumericID:  item.NumericID,
		Quantity:   qty,
		IsSilver:   false,
		Weight:     item.Weight,
		Location:   state.GetCurrentZone(),
	}

	log.Debugf("[SelfLoot] %s picked up %s x%d from %s", selfName, item.ItemID, qty, owner)
	lootEventCount.Add(1)
	lootWriter.append(lootEvent)
	SendLootEvent(lootEvent)
}

func upsertLootContainer(id int, owner string, kind string) {
	lootContainerMu.Lock()
	defer lootContainerMu.Unlock()

	c := lootContainersByID[id]
	if c == nil {
		c = &lootContainer{
			id:    id,
			guids: make(map[string]struct{}),
			items: make(map[int]CapturedItem),
		}
		lootContainersByID[id] = c
	}
	c.owner = owner
	c.kind = kind
	c.updatedAt = time.Now()
}

func attachLootContainerItems(containerID int, guids []string, slotIDs []int) {
	if containerID <= 0 || len(slotIDs) == 0 {
		return
	}

	lootContainerMu.Lock()
	defer lootContainerMu.Unlock()

	c := lootContainersByID[containerID]
	if c == nil || !c.isLootContainer() {
		return
	}

	for _, guid := range guids {
		if guid == "" {
			continue
		}
		c.guids[guid] = struct{}{}
		lootContainersByUID[guid] = c
	}

	attached := 0
	missing := 0
	for position, slotID := range slotIDs {
		if slotID <= 0 {
			continue
		}
		val, ok := globalItemCache.Load(slotID)
		if !ok {
			missing++
			continue
		}
		entry, ok := val.(cachedItemEntry)
		if !ok {
			missing++
			continue
		}
		item := entry.item
		item.Slot = position
		c.items[position] = item
		attached++
	}
	c.updatedAt = time.Now()

	if attached > 0 || missing > 0 {
		log.Debugf("[LootContainer] Attached %d item(s) to container id=%d (%d missing)", attached, containerID, missing)
	}
}

func popLootContainerItem(guid string, slot int) (CapturedItem, string, bool) {
	lootContainerMu.Lock()
	defer lootContainerMu.Unlock()

	c := lootContainersByUID[guid]
	if c == nil || !c.isLootContainer() {
		return CapturedItem{}, "", false
	}

	item, ok := c.items[slot]
	if !ok {
		log.Debugf("[SelfLoot] Loot container guid=%s has no cached item at slot=%d", guid, slot)
		return CapturedItem{}, "", false
	}

	delete(c.items, slot)
	c.updatedAt = time.Now()
	return item, c.owner, true
}

func deleteLootContainerByGUID(guid string) {
	if guid == "" {
		return
	}

	lootContainerMu.Lock()
	defer lootContainerMu.Unlock()

	c := lootContainersByUID[guid]
	if c == nil {
		return
	}

	for linkedGUID := range c.guids {
		if lootContainersByUID[linkedGUID] == c {
			delete(lootContainersByUID, linkedGUID)
		}
	}
	delete(lootContainersByID, c.id)
	log.Debugf("[LootContainer] Detached container guid=%s id=%d", guid, c.id)
}

func (c *lootContainer) isLootContainer() bool {
	return c != nil && (c.kind == "monster" || c.kind == "player")
}

func lootContainerCleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		cleanupLootContainers(time.Now().Add(-lootContainerTTL))
	}
}

func cleanupLootContainers(cutoff time.Time) {
	lootContainerMu.Lock()
	defer lootContainerMu.Unlock()

	for id, c := range lootContainersByID {
		if c.updatedAt.After(cutoff) {
			continue
		}
		delete(lootContainersByID, id)
		for guid := range c.guids {
			if lootContainersByUID[guid] == c {
				delete(lootContainersByUID, guid)
			}
		}
	}
}

func ClearLootContainerCache() {
	lootContainerMu.Lock()
	defer lootContainerMu.Unlock()

	lootContainersByID = make(map[int]*lootContainer)
	lootContainersByUID = make(map[string]*lootContainer)
	log.Debug("[LootContainer] Cleared")
}

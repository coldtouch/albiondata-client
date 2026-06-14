package client

import (
	"testing"
	"time"
)

func TestSelfLootContainerTracksItemsByPositionAndGUID(t *testing.T) {
	ClearItemCache()
	ClearLootContainerCache()
	t.Cleanup(func() {
		ClearItemCache()
		ClearLootContainerCache()
	})

	eventNewLoot{ContainerID: 123, Owner: "@MOB_KEEPER"}.Process(&albionState{})
	globalItemCache.Store(4242, cachedItemEntry{
		item: CapturedItem{
			ItemID:    "T4_BAG",
			NumericID: 1001,
			Quality:   2,
			Quantity:  1,
			Slot:      4242,
			Weight:    1.5,
		},
		cachedAt: time.Now(),
	})

	attachLootContainerItems(123, []string{"source-guid", "tab-guid"}, []int{0, 4242})

	item, owner, ok := popLootContainerItem("source-guid", 1)
	if !ok {
		t.Fatal("expected cached self-loot item")
	}
	if owner != "@MOB_KEEPER" {
		t.Fatalf("owner = %q, want @MOB_KEEPER", owner)
	}
	if item.ItemID != "T4_BAG" || item.NumericID != 1001 || item.Slot != 1 {
		t.Fatalf("unexpected item: %+v", item)
	}

	if _, _, ok := popLootContainerItem("tab-guid", 1); ok {
		t.Fatal("expected item to be consumed after first pop")
	}
}

func TestSelfLootContainerIgnoresUnknownContainers(t *testing.T) {
	ClearItemCache()
	ClearLootContainerCache()
	t.Cleanup(func() {
		ClearItemCache()
		ClearLootContainerCache()
	})

	globalItemCache.Store(99, cachedItemEntry{
		item:     CapturedItem{ItemID: "T4_HIDE", NumericID: 2002, Quantity: 7},
		cachedAt: time.Now(),
	})

	attachLootContainerItems(777, []string{"unknown-guid"}, []int{99})

	if _, _, ok := popLootContainerItem("unknown-guid", 0); ok {
		t.Fatal("expected unknown containers to be ignored")
	}
}

func TestSelfLootContainerDetachRemovesAllGUIDAliases(t *testing.T) {
	ClearItemCache()
	ClearLootContainerCache()
	t.Cleanup(func() {
		ClearItemCache()
		ClearLootContainerCache()
	})

	eventNewLoot{ContainerID: 321, Owner: "Enemy"}.Process(&albionState{})
	globalItemCache.Store(77, cachedItemEntry{
		item:     CapturedItem{ItemID: "T5_CAPE", NumericID: 3003, Quantity: 1},
		cachedAt: time.Now(),
	})
	attachLootContainerItems(321, []string{"source-guid", "tab-guid"}, []int{77})

	deleteLootContainerByGUID("source-guid")

	if _, _, ok := popLootContainerItem("tab-guid", 0); ok {
		t.Fatal("expected detach to remove linked GUID aliases")
	}
}

func TestDecodeRequestAcceptsCurrentInventoryMoveOpcodeAlias(t *testing.T) {
	fromGUID := []int8{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	toGUID := []int8{15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0}

	op, err := decodeRequest(map[uint8]interface{}{
		253: int16(30),
		0:   int32(2),
		1:   fromGUID,
		3:   int32(5),
		4:   toGUID,
	})
	if err != nil {
		t.Fatalf("decodeRequest returned error: %v", err)
	}
	if _, ok := op.(*operationInventoryMoveItem); !ok {
		t.Fatalf("operation type = %T, want *operationInventoryMoveItem", op)
	}
}

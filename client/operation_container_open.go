package client

import (
	"fmt"

	"github.com/ao-data/albiondata-client/log"
)

// operationContainerOpen is triggered when a player clicks a container tab.
// We log it for debugging but the actual capture happens in evAttachItemContainer
// which fires right after with the slot map (param 3).
type operationContainerOpen struct {
	ContainerSlot int8   `mapstructure:"0"`
	ContainerGUID []int8 `mapstructure:"2"`
}

func (op operationContainerOpen) Process(state *albionState) {
	guid := ""
	for _, b := range op.ContainerGUID {
		guid += fmt.Sprintf("%02x", byte(b))
	}
	log.Infof("[ContainerOpen] slot=%d guid=%s len=%d", op.ContainerSlot, guid, len(op.ContainerGUID))

	// Try to match for logging purposes
	matchedName, matchedIdx := matchContainerToVaultTab(guid)
	if matchedName != "" {
		log.Infof("[ContainerOpen] Matched to vault tab %d: %s", matchedIdx, matchedName)
	}
	// Actual capture happens in eventAttachItemContainer.Process()
}

// operationContainerOpenResponse — server acknowledges the open
type operationContainerOpenResponse struct{}

func (op operationContainerOpenResponse) Process(state *albionState) {}

// eventAttachItemContainer fires when a container tab's content is attached.
// This is the AUTHORITATIVE event for capturing chest contents.
//
// How it works:
//   - The game sends item events (EquipItem, SimpleItem, etc.) with global slot numbers
//   - These are stored in globalItemCache (see event_container_items.go)
//   - Param 3 of this event is an array where each non-zero value is a global slot number
//     referencing an item in the cache
//   - We look up each slot from the cache to build the actual tab contents
//
// Params:
//
//	0: container object slot (int)
//	1: container GUID ([]byte)
//	2: tab/sub-container GUID ([]byte) — matches vault tab GUIDs
//	3: slot map ([]int) — globalSlotIds for items in this tab. 0 = empty slot.
//	4: capacity (int)
type eventAttachItemContainer struct {
	ContainerSlot int        `mapstructure:"0"`
	ContainerGUID []int8     `mapstructure:"1"`
	TabGUID       []int8     `mapstructure:"2"`
	SlotMap       []int      `mapstructure:"3"`
	Capacity      int        `mapstructure:"4"`
	RawParams     map[string]interface{} `mapstructure:",remain"`
}

func (ev eventAttachItemContainer) Process(state *albionState) {
	guid := ""
	for _, b := range ev.TabGUID {
		guid += fmt.Sprintf("%02x", byte(b))
	}

	// Count non-zero slots
	nonZero := 0
	for _, s := range ev.SlotMap {
		if s != 0 {
			nonZero++
		}
	}

	log.Infof("[AttachItemContainer] slot=%d tabGuid=%s slots=%d/%d capacity=%d",
		ev.ContainerSlot, guid, nonZero, len(ev.SlotMap), ev.Capacity)

	if !ConfigGlobal.CaptureEnabled {
		log.Debug("[AttachItemContainer] Capture disabled — ignoring")
		return
	}

	// Match the tab GUID to a known vault tab
	tabName := ""
	tabIndex := -1
	matchedName, matchedIdx := matchContainerToVaultTab(guid)
	if matchedName != "" {
		log.Infof("[AttachItemContainer] Matched to vault tab %d: %s", matchedIdx, matchedName)
		tabName = matchedName
		tabIndex = matchedIdx
	} else {
		log.Info("[AttachItemContainer] No vault tab GUID match")
	}

	// Build capture from slot map → global item cache lookup
	if nonZero > 0 {
		BuildCaptureFromSlots(ev.SlotMap, guid, tabName, tabIndex)
	} else {
		log.Info("[AttachItemContainer] Empty tab — no slots to look up")
	}
}

// operationContainerManageSubContainer — player switching tabs in a chest.
// Logged for debugging; actual capture happens in evAttachItemContainer.
type operationContainerManageSubContainer struct {
	ContainerSlot int8   `mapstructure:"0"`
	ContainerGUID []int8 `mapstructure:"2"`
	RawParams     map[string]interface{} `mapstructure:",remain"`
}

func (op operationContainerManageSubContainer) Process(state *albionState) {
	guid := ""
	for _, b := range op.ContainerGUID {
		guid += fmt.Sprintf("%02x", byte(b))
	}
	log.Infof("[ContainerManageSubContainer] slot=%d guid=%s len=%d extra=%v",
		op.ContainerSlot, guid, len(op.ContainerGUID), op.RawParams)

	matchedName, matchedIdx := matchContainerToVaultTab(guid)
	if matchedName != "" {
		log.Infof("[ContainerManageSubContainer] Matched vault tab %d: %s", matchedIdx, matchedName)
	}
	// Actual capture happens in eventAttachItemContainer.Process()
}

type operationContainerManageSubContainerResponse struct{}

func (op operationContainerManageSubContainerResponse) Process(state *albionState) {}

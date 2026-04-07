package client

import (
	"fmt"

	"github.com/ao-data/albiondata-client/log"
)

// operationContainerOpen is triggered when a player opens a container
type operationContainerOpen struct {
	ContainerSlot int8   `mapstructure:"0"`
	ContainerGUID []int8 `mapstructure:"2"`
}

func (op operationContainerOpen) Process(state *albionState) {
	// Build a hex string from the container GUID for grouping
	guid := ""
	for _, b := range op.ContainerGUID {
		guid += fmt.Sprintf("%02x", byte(b))
	}
	log.Infof("[ContainerOpen] slot=%d guid=%s len=%d", op.ContainerSlot, guid, len(op.ContainerGUID))

	if !ConfigGlobal.CaptureEnabled {
		log.Debug("[ContainerOpen] Capture disabled — ignoring")
		return
	}

	// Reset tab index — this is the first (or only) tab being opened
	containerCollector.resetTabIndex()
	containerCollector.startCollecting()
	containerCollector.setContainerID(guid)

	// Try to match this container GUID against known vault tab GUIDs
	matchedName, matchedIdx := matchContainerToVaultTab(guid)
	if matchedName != "" {
		log.Infof("[ContainerOpen] Matched to vault tab %d: %s", matchedIdx, matchedName)
		containerCollector.setTabName(matchedName)
		containerCollector.setTabIndex(matchedIdx)
	} else {
		log.Infof("[ContainerOpen] No vault tab GUID match — using tab index 0 (first tab)")
		// Log all known vault GUIDs for comparison
		if currentGuildVaultInfo != nil {
			for i, tab := range currentGuildVaultInfo.Tabs {
				log.Infof("[ContainerOpen]   Guild tab %d: guid=%s name=%s", i, tab.GUID, tab.Name)
			}
		}
		if currentBankVaultInfo != nil {
			for i, tab := range currentBankVaultInfo.Tabs {
				log.Infof("[ContainerOpen]   Bank tab %d: guid=%s name=%s", i, tab.GUID, tab.Name)
			}
		}
	}
}

// operationContainerOpenResponse — server acknowledges the open
type operationContainerOpenResponse struct{}

func (op operationContainerOpenResponse) Process(state *albionState) {}

// operationContainerManageSubContainer — player switching tabs in a chest.
// Param layout mirrors ContainerOpen: param 0 = slot, param 2 = target tab GUID.
type operationContainerManageSubContainer struct {
	ContainerSlot int8   `mapstructure:"0"`
	ContainerGUID []int8 `mapstructure:"2"`
	// Capture all params so we can log unknown ones during debugging
	RawParams map[string]interface{} `mapstructure:",remain"`
}

func (op operationContainerManageSubContainer) Process(state *albionState) {
	guid := ""
	for _, b := range op.ContainerGUID {
		guid += fmt.Sprintf("%02x", byte(b))
	}
	log.Infof("[ContainerManageSubContainer] slot=%d guid=%s len=%d extra=%v",
		op.ContainerSlot, guid, len(op.ContainerGUID), op.RawParams)

	if !ConfigGlobal.CaptureEnabled {
		return
	}

	// Try GUID matching first — gives the exact tab regardless of click order
	matchedName, matchedIdx := matchContainerToVaultTab(guid)
	if matchedName != "" {
		log.Infof("[ContainerManageSubContainer] Matched vault tab %d: %s", matchedIdx, matchedName)
		containerCollector.setTabIndex(matchedIdx)
		containerCollector.startCollecting()
		containerCollector.setContainerID(guid)
		containerCollector.setTabName(matchedName)
		return
	}

	// No GUID match — fall back to incrementing the sequential counter
	log.Infof("[ContainerManageSubContainer] No GUID match — incrementing tab index")
	containerCollector.incrementTabIndex()
	containerCollector.startCollecting()
	if guid != "" {
		containerCollector.setContainerID(guid)
	}
}

type operationContainerManageSubContainerResponse struct{}

func (op operationContainerManageSubContainerResponse) Process(state *albionState) {}

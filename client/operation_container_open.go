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

// operationContainerManageSubContainer — player switching tabs in a chest
type operationContainerManageSubContainerRaw struct {
	RawParams map[string]interface{} `mapstructure:",remain"`
}

type operationContainerManageSubContainer struct{}

func (op operationContainerManageSubContainer) Process(state *albionState) {
	log.Debug("ContainerManageSubContainer — tab switch")
	// Increment tab index before starting new collection so finalize() sees correct index
	containerCollector.incrementTabIndex()
	containerCollector.startCollecting()
}

type operationContainerManageSubContainerResponse struct{}

func (op operationContainerManageSubContainerResponse) Process(state *albionState) {}

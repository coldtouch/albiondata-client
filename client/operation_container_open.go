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
	log.Infof("[ContainerOpen] slot=%d guid=%s", op.ContainerSlot, guid)

	// Try to match this container GUID against known vault tab GUIDs
	matchedTab := matchContainerToVaultTab(guid)
	if matchedTab != "" {
		log.Infof("[ContainerOpen] Matched to vault tab: %s", matchedTab)
	} else {
		log.Infof("[ContainerOpen] No vault tab match found for guid=%s", guid)
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

	containerCollector.startCollecting()
	containerCollector.setContainerID(guid)
	if matchedTab != "" {
		containerCollector.setTabName(matchedTab)
	}
}

// operationContainerOpenResponse — server acknowledges the open
type operationContainerOpenResponse struct{}

func (op operationContainerOpenResponse) Process(state *albionState) {}

// operationContainerManageSubContainer — player switching tabs in a chest
type operationContainerManageSubContainer struct{}

func (op operationContainerManageSubContainer) Process(state *albionState) {
	log.Debug("ContainerManageSubContainer — tab switch, starting new collection")
	containerCollector.startCollecting()
}

type operationContainerManageSubContainerResponse struct{}

func (op operationContainerManageSubContainerResponse) Process(state *albionState) {}

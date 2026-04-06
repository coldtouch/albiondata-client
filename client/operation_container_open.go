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
	log.Debugf("ContainerOpen: slot=%d guid=%s", op.ContainerSlot, guid)
	containerCollector.startCollecting()
	containerCollector.setContainerID(guid)
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

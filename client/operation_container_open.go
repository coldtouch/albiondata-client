package client

import (
	"github.com/ao-data/albiondata-client/log"
)

// operationContainerOpen is triggered when a player opens a container
type operationContainerOpen struct{}

func (op operationContainerOpen) Process(state *albionState) {
	log.Debug("Got ContainerOpen request — starting item collection")
	containerCollector.startCollecting()
}

// operationContainerOpenResponse — server acknowledges the open
type operationContainerOpenResponse struct{}

func (op operationContainerOpenResponse) Process(state *albionState) {
	log.Debug("Got ContainerOpen response")
	// Items come via NewSimpleItem/NewEquipmentItem events, not in this response
}

// operationContainerManageSubContainer — player switching tabs in a chest
type operationContainerManageSubContainer struct{}

func (op operationContainerManageSubContainer) Process(state *albionState) {
	log.Debug("Got ContainerManageSubContainer request — switching tab, starting new collection")
	containerCollector.startCollecting()
}

type operationContainerManageSubContainerResponse struct{}

func (op operationContainerManageSubContainerResponse) Process(state *albionState) {
	log.Debug("Got ContainerManageSubContainer response")
}

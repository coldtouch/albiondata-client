package client

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/ao-data/albiondata-client/log"
)

// operationContainerOpen is triggered when a player opens a container (chest, bank, guild vault, etc.)
// Phase 1: Debug logging to discover the parameter structure
// Phase 2: Parse items and relay to private server
type operationContainerOpen struct {
	// Placeholder — we don't know the param mapping yet.
	// The Process method will log raw params for discovery.
}

func (op operationContainerOpen) Process(state *albionState) {
	log.Info("Got ContainerOpen request — player is opening a container")
}

// operationContainerOpenResponse receives the container contents from the server
type operationContainerOpenResponse struct {
	// We'll capture raw params in decode.go and log them here
	RawParams map[string]interface{} `mapstructure:",remain"`
}

func (op operationContainerOpenResponse) Process(state *albionState) {
	log.Info("Got ContainerOpen response — container contents received")

	// Debug: log all parameter keys and their types/values to discover the structure
	for key, val := range op.RawParams {
		valType := reflect.TypeOf(val)
		var preview string
		if valType != nil {
			switch valType.Kind() {
			case reflect.Slice:
				sliceVal := reflect.ValueOf(val)
				preview = fmt.Sprintf("[%s len=%d", valType.String(), sliceVal.Len())
				if sliceVal.Len() > 0 && sliceVal.Len() <= 3 {
					// Show first few elements for small slices
					jsonBytes, _ := json.Marshal(val)
					preview = fmt.Sprintf("[%s] %s", valType.String(), string(jsonBytes))
				} else if sliceVal.Len() > 3 {
					// Show first element for large slices
					first := sliceVal.Index(0).Interface()
					preview = fmt.Sprintf("[%s len=%d first=%v]", valType.String(), sliceVal.Len(), first)
				}
			case reflect.String:
				s := val.(string)
				if len(s) > 100 {
					preview = fmt.Sprintf("string(%d chars): %s...", len(s), s[:100])
				} else {
					preview = fmt.Sprintf("string: %s", s)
				}
			default:
				preview = fmt.Sprintf("%v (%s)", val, valType.String())
			}
		} else {
			preview = "nil"
		}
		log.Infof("[ContainerOpen] param %s = %s", key, preview)
	}

	// Also try to JSON-dump the entire response for analysis
	jsonBytes, err := json.MarshalIndent(op.RawParams, "", "  ")
	if err == nil && len(jsonBytes) < 5000 {
		log.Infof("[ContainerOpen] Full JSON dump:\n%s", string(jsonBytes))
	} else if err == nil {
		log.Infof("[ContainerOpen] Response too large for full dump (%d bytes), logged individual params above", len(jsonBytes))
	}
}

// operationContainerManageSubContainer is triggered when switching tabs in a chest
type operationContainerManageSubContainer struct{}

func (op operationContainerManageSubContainer) Process(state *albionState) {
	log.Info("Got ContainerManageSubContainer request — player switching tab")
}

type operationContainerManageSubContainerResponse struct {
	RawParams map[string]interface{} `mapstructure:",remain"`
}

func (op operationContainerManageSubContainerResponse) Process(state *albionState) {
	log.Info("Got ContainerManageSubContainer response — tab contents received")

	for key, val := range op.RawParams {
		valType := reflect.TypeOf(val)
		var preview string
		if valType != nil {
			switch valType.Kind() {
			case reflect.Slice:
				sliceVal := reflect.ValueOf(val)
				preview = fmt.Sprintf("[%s len=%d", valType.String(), sliceVal.Len())
				if sliceVal.Len() > 0 && sliceVal.Len() <= 3 {
					jsonBytes, _ := json.Marshal(val)
					preview = fmt.Sprintf("[%s] %s", valType.String(), string(jsonBytes))
				} else if sliceVal.Len() > 3 {
					first := sliceVal.Index(0).Interface()
					preview = fmt.Sprintf("[%s len=%d first=%v]", valType.String(), sliceVal.Len(), first)
				}
			case reflect.String:
				s := val.(string)
				if len(s) > 100 {
					preview = fmt.Sprintf("string(%d chars): %s...", len(s), s[:100])
				} else {
					preview = fmt.Sprintf("string: %s", s)
				}
			default:
				preview = fmt.Sprintf("%v (%s)", val, valType.String())
			}
		} else {
			preview = "nil"
		}
		log.Infof("[SubContainer] param %s = %s", key, preview)
	}

	jsonBytes, err := json.MarshalIndent(op.RawParams, "", "  ")
	if err == nil && len(jsonBytes) < 5000 {
		log.Infof("[SubContainer] Full JSON dump:\n%s", string(jsonBytes))
	}
}

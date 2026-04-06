package client

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/ao-data/albiondata-client/log"
)

// Debug helper to log all params for an event
func logEventParams(eventName string, params map[string]interface{}) {
	for key, val := range params {
		valType := reflect.TypeOf(val)
		var preview string
		if valType != nil {
			switch valType.Kind() {
			case reflect.Slice:
				sliceVal := reflect.ValueOf(val)
				if sliceVal.Len() <= 5 {
					jsonBytes, _ := json.Marshal(val)
					preview = fmt.Sprintf("[%s] %s", valType.String(), string(jsonBytes))
				} else {
					first := sliceVal.Index(0).Interface()
					preview = fmt.Sprintf("[%s len=%d first=%v]", valType.String(), sliceVal.Len(), first)
				}
			case reflect.String:
				s := val.(string)
				if len(s) > 100 {
					preview = fmt.Sprintf("string(%d): %s...", len(s), s[:100])
				} else {
					preview = fmt.Sprintf("string: %q", s)
				}
			default:
				preview = fmt.Sprintf("%v (%s)", val, valType.String())
			}
		} else {
			preview = "nil"
		}
		log.Infof("[%s] param %s = %s", eventName, key, preview)
	}
}

// eventNewSimpleItem fires when a simple (stackable) item appears in a container
type eventNewSimpleItem struct {
	RawParams map[string]interface{} `mapstructure:",remain"`
}

func (event eventNewSimpleItem) Process(state *albionState) {
	log.Info("[Event] NewSimpleItem — stackable item in container")
	logEventParams("NewSimpleItem", event.RawParams)
}

// eventNewEquipmentItem fires when a gear item appears in a container
type eventNewEquipmentItem struct {
	RawParams map[string]interface{} `mapstructure:",remain"`
}

func (event eventNewEquipmentItem) Process(state *albionState) {
	log.Info("[Event] NewEquipmentItem — gear item in container")
	logEventParams("NewEquipmentItem", event.RawParams)
}

// eventInventoryPutItem fires when an item is placed into a container slot
type eventInventoryPutItem struct {
	RawParams map[string]interface{} `mapstructure:",remain"`
}

func (event eventInventoryPutItem) Process(state *albionState) {
	log.Info("[Event] InventoryPutItem — item placed in slot")
	logEventParams("InventoryPutItem", event.RawParams)
}

// eventNewJournalItem fires when a journal appears in a container
type eventNewJournalItem struct {
	RawParams map[string]interface{} `mapstructure:",remain"`
}

func (event eventNewJournalItem) Process(state *albionState) {
	log.Info("[Event] NewJournalItem — journal in container")
	logEventParams("NewJournalItem", event.RawParams)
}

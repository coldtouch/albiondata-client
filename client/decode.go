package client

import (
	"encoding/hex"
	"fmt"
	"reflect"
	"strconv"

	"github.com/ao-data/albiondata-client/lib"
	"github.com/ao-data/albiondata-client/log"
	"github.com/mitchellh/mapstructure"
)

// toInt16 safely converts a Photon param to int16. The game can send codes as
// int8, int16, int32, or even string depending on the protocol version.
func toInt16(v interface{}) (int16, bool) {
	switch val := v.(type) {
	case int16:
		return val, true
	case int8:
		return int16(val), true
	case int32:
		return int16(val), true
	case int64:
		return int16(val), true
	case string:
		// Game update may send numeric codes as strings
		n, err := strconv.ParseInt(val, 10, 16)
		if err != nil {
			return 0, false
		}
		return int16(n), true
	default:
		return 0, false
	}
}

var decodeEventLogCount int

func paramKeys(params map[uint8]interface{}) []uint8 {
	keys := make([]uint8, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	return keys
}

// April 13 2026 game update shifted operation codes by +6.
// We subtract 6 from the incoming code to match our enum constants.
// Events below 25 did NOT shift; events 25+ need separate mapping.
const opCodeShift int16 = 6

func adjustOpCode(code int16) int16 {
	return code - opCodeShift
}

func formatRawParams(params map[uint8]interface{}) string {
	result := ""
	for k, v := range params {
		if k == 252 || k == 253 {
			continue
		}
		if result != "" {
			result += ", "
		}
		result += fmt.Sprintf("%d:%T=%v", k, v, v)
	}
	return result
}

// dumpParams logs all params for an operation — used to reverse-engineer new opcodes.
func dumpParams(label string, code int16, params map[uint8]interface{}) {
	log.Infof("[TRADE-DIAG] %s opcode=%d — %d params:", label, code, len(params))
	for k, v := range params {
		if k == 253 || k == 255 {
			continue
		}
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
			n := rv.Len()
			preview := ""
			for i := 0; i < n && i < 5; i++ {
				if i > 0 {
					preview += ", "
				}
				preview += fmt.Sprintf("%v", rv.Index(i).Interface())
			}
			if n > 5 {
				preview += fmt.Sprintf("... (%d more)", n-5)
			}
			log.Infof("[TRADE-DIAG]   %d: %T len=%d [%s]", k, v, n, preview)
		} else {
			log.Infof("[TRADE-DIAG]   %d: %T = %v", k, v, v)
		}
	}
}

func decodeRequest(params map[uint8]interface{}) (operation operation, err error) {
	if _, ok := params[253]; !ok {
		return nil, nil
	}

	rawCode, ok := toInt16(params[253])
	if !ok {
		log.Infof("[Decode] Request param 253 unexpected type: %T = %v", params[253], params[253])
		return nil, nil
	}
	// Try raw code first, then shifted (-6) for April 2026 update compatibility
	code := rawCode
	shifted := rawCode - opCodeShift

	switch OperationType(code) {
	case opGetGameServerByCluster:
		operation = &operationGetGameServerByCluster{}
	case opAuctionGetOffers:
		operation = &operationAuctionGetOffers{}
	case opAuctionGetItemAverageStats:
		operation = &operationAuctionGetItemAverageStats{}
	case opGetClusterMapInfo:
		operation = &operationGetClusterMapInfo{}
	case opGoldMarketGetAverageInfo:
		operation = &operationGoldMarketGetAverageInfo{}
	case opRealEstateGetAuctionData:
		operation = &operationRealEstateGetAuctionData{}
	case opRealEstateBidOnAuction:
		operation = &operationRealEstateBidOnAuction{}
	case opContainerOpen:
		operation = &operationContainerOpen{}
	case opContainerManageSubContainer:
		operation = &operationContainerManageSubContainer{}

	// === TRADE TRACKING (private VPS only — does NOT affect AODP uploads) ===
	case opAuctionBuyOffer:
		operation = &operationAuctionBuyOfferRequest{}
	case opAuctionCreateOffer:
		operation = &operationAuctionCreateOfferRequest{}
	case opAuctionCreateRequest:
		operation = &operationAuctionCreateRequestReq{}
	// Opcodes we monitor but haven't seen fire yet
	case opAuctionSellRequest:
		dumpParams("REQUEST AuctionSellRequest", code, params)
		return nil, nil
	case opQuickSellAuctionQueryAction:
		dumpParams("REQUEST QuickSellQuery", code, params)
		return nil, nil
	case opQuickSellAuctionSellAction:
		dumpParams("REQUEST QuickSellAction", code, params)
		return nil, nil

	default:
		// Try shifted code (-6) for operations that moved in the April 2026 update
		switch OperationType(shifted) {
		case opAuctionGetOffers:
			operation = &operationAuctionGetOffers{}
		case opAuctionGetItemAverageStats:
			operation = &operationAuctionGetItemAverageStats{}
		case opContainerOpen:
			operation = &operationContainerOpen{}
		case opContainerManageSubContainer:
			operation = &operationContainerManageSubContainer{}
		case opAuctionBuyOffer:
			operation = &operationAuctionBuyOfferRequest{}
		case opAuctionCreateOffer:
			operation = &operationAuctionCreateOfferRequest{}
		case opAuctionCreateRequest:
			operation = &operationAuctionCreateRequestReq{}
		default:
			// Neither the raw nor shifted opcode matched — record for reverse-engineering.
			recordUnknownEvent("REQUEST", rawCode, params)
			return nil, nil
		}
	}

	err = decodeParams(params, operation)

	return operation, err
}

func decodeResponse(params map[uint8]interface{}) (operation operation, err error) {
	if _, ok := params[253]; !ok {
		return nil, nil
	}

	rawCode, ok := toInt16(params[253])
	if !ok {
		log.Infof("[Decode] Response param 253 unexpected type: %T = %v", params[253], params[253])
		return nil, nil
	}
	code := rawCode
	shifted := rawCode - opCodeShift

	switch OperationType(code) {
	case opJoin:
		operation = &operationJoinResponse{}
	case opAuctionGetOffers:
		operation = &operationAuctionGetOffersResponse{}
	case opAuctionGetRequests:
		operation = &operationAuctionGetRequestsResponse{}
	case opAuctionBuyOffer:
		operation = &operationAuctionGetRequestsResponse{} // AODP market data upload
	case opAuctionGetItemAverageStats:
		operation = &operationAuctionGetItemAverageStatsResponse{}
	case opGetMailInfos:
		// Decode raw — protocol changed, mapstructure can't handle the new param layout
		processMailInfosRaw(params)
		return nil, nil
	case opReadMail:
		operation = &operationReadMail{}
	case opGetClusterMapInfo:
		operation = &operationGetClusterMapInfoResponse{}
	case opGoldMarketGetAverageInfo:
		operation = &operationGoldMarketGetAverageInfoResponse{}
	case opRealEstateGetAuctionData:
		operation = &operationRealEstateGetAuctionDataResponse{}
	case opRealEstateBidOnAuction:
		operation = &operationRealEstateBidOnAuctionResponse{}
	case opContainerOpen:
		operation = &operationContainerOpenResponse{}
	case opContainerManageSubContainer:
		operation = &operationContainerManageSubContainerResponse{}

	// Trade response diagnostics — keep monitoring for undiscovered opcodes
	case opAuctionSellRequest, opQuickSellAuctionQueryAction, opQuickSellAuctionSellAction:
		dumpParams("RESPONSE trade opcode", code, params)
		return nil, nil

	default:
		// Try shifted code (-6) for operations that moved in the April 2026 update
		switch OperationType(shifted) {
		case opJoin:
			operation = &operationJoinResponse{}
		case opAuctionGetOffers:
			operation = &operationAuctionGetOffersResponse{}
		case opAuctionGetRequests:
			operation = &operationAuctionGetRequestsResponse{}
		case opAuctionBuyOffer:
			operation = &operationAuctionGetRequestsResponse{}
		case opAuctionGetItemAverageStats:
			operation = &operationAuctionGetItemAverageStatsResponse{}
		case opGetMailInfos:
			processMailInfosRaw(params)
			return nil, nil
		case opReadMail:
			operation = &operationReadMail{}
		case opContainerOpen:
			operation = &operationContainerOpenResponse{}
		case opContainerManageSubContainer:
			operation = &operationContainerManageSubContainerResponse{}
		case opGetClusterMapInfo:
			operation = &operationGetClusterMapInfoResponse{}
		case opGoldMarketGetAverageInfo:
			operation = &operationGoldMarketGetAverageInfoResponse{}
		default:
			// Neither the raw nor shifted opcode matched.
			recordUnknownEvent("RESPONSE", rawCode, params)
			return nil, nil
		}
	}

	err = decodeParams(params, operation)

	return operation, err
}

func decodeEvent(params map[uint8]interface{}) (event operation, err error) {
	if _, ok := params[252]; !ok {
		return nil, nil
	}

	eventType, ok := toInt16(params[252])
	if !ok {
		return nil, nil
	}
	// Events did NOT shift with the April 13 update — only operations shifted +6

	switch EventType(eventType) {
	case evNewCharacter:
		event = &eventNewCharacter{}
	case evCharacterStats:
		event = &eventCharacterStats{}
	case evCharacterStats + 2: // April 2026 update shifted +2
		event = &eventCharacterStats{}
	case evOtherGrabbedLoot + 2: // April 2026 update shifted loot event 275→277
		event = &eventOtherGrabbedLoot{}
	case evRedZoneWorldMapEvent, evRedZoneWorldMapEvent + 2:
		event = &eventRedZoneWorldMapEvent{}
	case evNewSimpleItem:
		event = &eventNewSimpleItem{}
	case evNewEquipmentItem:
		event = &eventNewEquipmentItem{}
	case evInventoryPutItem:
		event = &eventInventoryPutItem{}
	case evNewJournalItem:
		event = &eventNewJournalItem{}
	case evNewFurnitureItem:
		event = &eventNewFurnitureItem{}
	case evNewKillTrophyItem:
		event = &eventNewKillTrophyItem{}
	case evNewLaborerItem:
		event = &eventNewLaborerItem{}
	case evGuildVaultInfo:
		event = &eventGuildVaultInfo{}
	case evGuildVaultInfo + 2: // April 2026 update may have shifted +2
		event = &eventGuildVaultInfo{}
	case evBankVaultInfo:
		event = &eventBankVaultInfo{}
	case evBankVaultInfo + 2: // April 2026 update may have shifted +2
		event = &eventBankVaultInfo{}
	case evAttachItemContainer:
		event = &eventAttachItemContainer{}
	case evAttachItemContainer + 2: // April 2026 update shifted +2
		event = &eventAttachItemContainer{}
	case evDied:
		event = &eventDied{}
	case evDied + 2: // April 2026 update shifted +2
		event = &eventDied{}
	case evKilledPlayer:
		event = &eventKilledPlayer{}
	case evKilledPlayer + 2: // April 2026 update shifted +2
		event = &eventKilledPlayer{}
	case evCharacterEquipmentChanged:
		event = &eventCharacterEquipmentChanged{}
	case evCharacterEquipmentChanged + 2: // April 2026 update shifted +2 (precaution)
		event = &eventCharacterEquipmentChanged{}
	default:
		log.Debugf("[Decode] Unhandled event code: %d (params: %d)", eventType, len(params))
		recordUnknownEvent("EVENT", eventType, params)
		return nil, nil
	}

	err = decodeParams(params, event)

	return event, err
}

func decodeParams(params map[uint8]interface{}, operation operation) error {
	convertGameObjects := func(from reflect.Type, to reflect.Type, v interface{}) (interface{}, error) {
		if from == reflect.TypeOf([]int8{}) && to == reflect.TypeOf(lib.CharacterID("")) {
			log.Debug("Parsing character ID from mixed-endian UUID")
			return decodeCharacterID(v.([]int8)), nil
		}

		// V18 sends CompressedInt (int32) where V16 sent int16 — auto-convert
		if from.Kind() == reflect.Int32 && to.Kind() == reflect.Int16 {
			return int16(v.(int32)), nil
		}
		if from.Kind() == reflect.Int32 && to.Kind() == reflect.Int8 {
			return int8(v.(int32)), nil
		}
		if from.Kind() == reflect.Int64 && to.Kind() == reflect.Int32 {
			return int32(v.(int64)), nil
		}

		return v, nil
	}

	config := mapstructure.DecoderConfig{
		WeaklyTypedInput: true, // V18 sends different Go types than struct fields expect
		DecodeHook:       convertGameObjects,
		Result:           operation,
	}

	decoder, err := mapstructure.NewDecoder(&config)
	if err != nil {
		return err
	}

	stringMap := make(map[string]interface{})
	for k, v := range params {
		stringMap[strconv.Itoa(int(k))] = v
	}

	err = decoder.Decode(stringMap)

	return err
}

func decodeCharacterID(array []int8) lib.CharacterID {
	b := make([]byte, len(array))

	for k, v := range array {
		b[k] = byte(v)
	}

	// swap first component (little-endian to big-endian)
	b[0], b[1], b[2], b[3] = b[3], b[2], b[1], b[0]
	b[4], b[5] = b[5], b[4]
	b[6], b[7] = b[7], b[6]

	var buf [36]byte
	hex.Encode(buf[:], b[:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:], b[10:])

	return lib.CharacterID(buf[:])
}

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

	code := params[253].(int16)

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
		return nil, nil
	}

	err = decodeParams(params, operation)

	return operation, err
}

func decodeResponse(params map[uint8]interface{}) (operation operation, err error) {
	if _, ok := params[253]; !ok {
		return nil, nil
	}

	code := params[253].(int16)

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
		return nil, nil
	}

	err = decodeParams(params, operation)

	return operation, err
}

func decodeEvent(params map[uint8]interface{}) (event operation, err error) {
	if _, ok := params[252]; !ok {
		return nil, nil
	}

	eventType := params[252].(int16)

	switch EventType(eventType) {
	case evNewCharacter:
		event = &eventNewCharacter{}
	case evCharacterStats:
		event = &eventCharacterStats{}
	case evOtherGrabbedLoot:
		event = &eventOtherGrabbedLoot{}
	case evRedZoneWorldMapEvent:
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
	case evBankVaultInfo:
		event = &eventBankVaultInfo{}
	case evAttachItemContainer:
		event = &eventAttachItemContainer{}
	case evDied:
		event = &eventDied{}
	case evKilledPlayer:
		event = &eventKilledPlayer{}
	default:
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

		return v, nil
	}

	config := mapstructure.DecoderConfig{
		DecodeHook: convertGameObjects,
		Result:     operation,
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

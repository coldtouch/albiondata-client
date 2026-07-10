package client

import (
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/ao-data/albiondata-client/lib"
	"github.com/ao-data/albiondata-client/log"
	"github.com/mitchellh/mapstructure"
)

// Hoisted package-level reflect types — these were previously created on every
// decodeParams call inside the convertGameObjects closure (per-event allocs).
var (
	int8SliceType   = reflect.TypeOf([]int8{})
	characterIDType = reflect.TypeOf(lib.CharacterID(""))
)

// convertGameObjects is the mapstructure decode hook — hoisted from a per-call
// closure inside decodeParams so the function value isn't reallocated each
// event. Captures nothing.
func convertGameObjects(from reflect.Type, to reflect.Type, v interface{}) (interface{}, error) {
	if from == int8SliceType && to == characterIDType {
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

// stringMapPool reuses the uint8→string param map across decodeParams calls so
// the listener goroutine doesn't allocate a fresh map[string]interface{} per
// decoded event (was a top GC source during ZvZ at 100+ events/sec).
var stringMapPool = sync.Pool{
	New: func() interface{} {
		return make(map[string]interface{}, 32)
	},
}

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
// Only emits output when --debug is active to avoid polluting production logs (GO-M3).
func dumpParams(label string, code int16, params map[uint8]interface{}) {
	if !ConfigGlobal.Debug {
		return
	}
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

// === OPEN-WORLD RESOURCE DISCOVERY (temporary diagnostic) ===
// Goal: pin down the exact live opcode the server uses to stream harvestable
// resource nodes (wood/ore/fiber/rock) and hide-giving animals in the open
// world, plus the param layout (resource type / tier / enchant / charges).
//
// We instrument the whole canonical neighborhood AND the +2-shifted variants
// (April 2026 protocol shifted events +2) so we catch the live code regardless
// of the exact shift. Anything we miss still lands in unknown-events-*.log via
// the existing --log-unknown-events safety net.
//
//	38 evNewSimpleHarvestableObject      | 39 evNewSimpleHarvestableObjectList (zone-entry batch)
//	40 evNewHarvestableObject            | 46 evHarvestableChangeState
//	47 evMobChangeState                  | 123 evNewMob (hide animals)
//	(+2 shifted: 40,41,42,48,49,125)
var resourceDiagCodes = map[int16]string{
	38:  "evNewSimpleHarvestableObject?",
	39:  "evNewSimpleHarvestableObjectList?",
	40:  "evNewHarvestableObject? / evNewSimpleHarvestableObject+2?",
	41:  "evNewSimpleHarvestableObjectList+2?",
	42:  "evNewHarvestableObject+2?",
	46:  "evHarvestableChangeState?",
	47:  "evMobChangeState?",
	48:  "evHarvestableChangeState+2?",
	49:  "evMobChangeState+2?",
	123: "evNewMob? (hide animal)",
	124: "evNewMob+1?",
	125: "evNewMob+2? (hide animal, shifted)",
}

var (
	_resourceDiagMu     sync.Mutex
	_resourceDiagSeen   = map[int16]int{}
	_resourceDiagFirstN = 8 // full-dump first N of each code, then go quiet
)

func isResourceDiagCode(code int16) bool {
	_, ok := resourceDiagCodes[code]
	return ok
}

// dumpResourceEvent prints a full, readable param dump for a suspected
// open-world resource event so we can identify the exact opcode + which param
// index carries resource type / tier / enchant / charges. Arrays are printed up
// to 30 elements (parallel arrays line up so type[i]/tier[i]/enchant[i] match).
func dumpResourceEvent(code int16, params map[uint8]interface{}) {
	if !ConfigGlobal.Debug && !ConfigGlobal.LogUnknownEvents {
		return
	}
	_resourceDiagMu.Lock()
	n := _resourceDiagSeen[code] + 1
	_resourceDiagSeen[code] = n
	_resourceDiagMu.Unlock()
	if n > _resourceDiagFirstN {
		if n%200 == 0 {
			log.Infof("[RESOURCE-DIAG] raw_code=%d still firing (occurrence %d) — %s", code, n, resourceDiagCodes[code])
		}
		return
	}

	log.Infof("[RESOURCE-DIAG] raw_code=%d  guess=%s  params=%d  (occurrence %d/%d)",
		code, resourceDiagCodes[code], len(params), n, _resourceDiagFirstN)
	keys := paramKeys(params)
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		if k == 252 || k == 253 {
			continue
		}
		v := params[k]
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
			ln := rv.Len()
			cap := ln
			if cap > 30 {
				cap = 30
			}
			parts := make([]string, 0, cap)
			for i := 0; i < cap; i++ {
				parts = append(parts, fmt.Sprintf("%v", rv.Index(i).Interface()))
			}
			more := ""
			if ln > 30 {
				more = fmt.Sprintf(" …+%d", ln-30)
			}
			log.Infof("[RESOURCE-DIAG]   p%d %T len=%d = [%s]%s", k, v, ln, strings.Join(parts, ", "), more)
		} else {
			log.Infof("[RESOURCE-DIAG]   p%d %T = %v", k, v, v)
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
	case opInventoryMoveItem, opInventoryMoveItem + 1:
		operation = &operationInventoryMoveItem{}

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

	// Chest-log REQUEST — pair with the response so we can tag each row as
	// deposit or withdraw. Response body doesn't carry direction; the game
	// sends two separate requests per viewing (param 6 differs). We stash
	// (opID → filterValue) keyed by the Photon invocation counter (255).
	case opGetChestLogs:
		captureChestLogRequestParams(params)
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
		case opInventoryMoveItem:
			operation = &operationInventoryMoveItem{}
		case opAuctionBuyOffer:
			operation = &operationAuctionBuyOfferRequest{}
		case opAuctionCreateOffer:
			operation = &operationAuctionCreateOfferRequest{}
		case opAuctionCreateRequest:
			operation = &operationAuctionCreateRequestReq{}
		case opGetChestLogs:
			// Shifted opcode 157 — the actual one seen in the April 2026 build.
			captureChestLogRequestParams(params)
			return nil, nil
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
		dumpJoinParams("RESPONSE opJoin (raw)", code, params)
		operation = &operationJoinResponse{}
	case opChangeCluster:
		// Zone-transition response — fires every time the player crosses a
		// cluster boundary (walking out of hideout, traversing portals, etc.).
		// Diag dump captures the params so we can verify which index carries
		// the destination zone identifier (best guess: 67, same as opJoin).
		dumpJoinParams("RESPONSE opChangeCluster (raw)", code, params)
		operation = &operationChangeClusterResponse{}
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

	// Chest log response — parallel-array layout discovered 2026-04-20.
	// Each log row is (player, itemID, quality, qty, ticks, extra). Decoded in
	// operation_get_chest_logs.go; entries append to logs/chest-logs-*.tsv.
	// Other guild-log opcodes keep the raw dump since their layout isn't confirmed.
	case opGetChestLogs:
		operation = &operationGetChestLogsResponse{}
	case opGetAccessRightLogs, opGetGuildAccountLogs, opGetGuildAccountLogsLargeAmount:
		if ConfigGlobal.LogUnknownEvents {
			dumpParams("RESPONSE guild log opcode", code, params)
		}
		return nil, nil

	default:
		// Try shifted code (-6) for operations that moved in the April 2026 update
		switch OperationType(shifted) {
		case opJoin:
			dumpJoinParams("RESPONSE opJoin (shifted)", code, params)
			operation = &operationJoinResponse{}
		case opChangeCluster:
			dumpJoinParams("RESPONSE opChangeCluster (shifted)", code, params)
			operation = &operationChangeClusterResponse{}
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
		case opGetChestLogs:
			// Shifted opcode 157 (151+6) — the actual one seen in the April 2026 game build.
			operation = &operationGetChestLogsResponse{}
		case opGetAccessRightLogs, opGetGuildAccountLogs, opGetGuildAccountLogsLargeAmount:
			if ConfigGlobal.LogUnknownEvents {
				dumpParams("RESPONSE guild log opcode (shifted)", code, params)
			}
			return nil, nil
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
	// Event-code drift history (verified against live enum 2026-07-06):
	//   - Events ≤171 (our numbering) never shifted — base codes are live.
	//   - April 13 2026 update inserted 2 events (~172) → +2 for everything above
	//     (loot 275→277, trades 174-179→176-181, vaults 409/410→411/412).
	//   - June 29 2026 update inserted 2 more events (between ~181 and 246) →
	//     +4 total for events ≥246 (loot →279, vaults →413/414, redzone →479).
	//     Live 277 is now PlayerCounts — decoding it as loot produced the
	//     UNKNOWN_0 garbage sessions seen July 6.

	switch EventType(eventType) {
	case evNewCharacter:
		// Trade-debug correlation: keep the latest FULL param map per player so
		// trade events can be matched against ANY id field a nearby player has
		// (partner-name on initiator-side trades is the open question). Cheap
		// in-memory copy; only ever written to disk when a trade event fires.
		rememberCharParams(params)
		event = &eventNewCharacter{}
	case evCharacterStats:
		rememberCharParams(params)
		event = &eventCharacterStats{}
	case evOtherGrabbedLoot + 4: // live 279 since June 29 2026 update (was 277 Apr-Jun, 275 before)
		event = &eventOtherGrabbedLoot{}
	case evRedZoneWorldMapEvent + 4: // live 479 ("RedZoneWorldEvent") since June 29 2026 update
		event = &eventRedZoneWorldMapEvent{}
	case evNewSimpleItem:
		event = &eventNewSimpleItem{}
	case evNewEquipmentItem:
		event = &eventNewEquipmentItem{}
	case evNewLoot:
		event = &eventNewLoot{}
	case evInventoryPutItem:
		// Short-circuit: handler is intentionally a no-op, so skip the entire
		// decodeParams pipeline (mapstructure.NewDecoder + map[string]interface{}
		// allocation per packet). evInventoryPutItem fires every time an item
		// moves in any visible container — high frequency in chests/storage UI.
		return nil, nil
	case evNewJournalItem:
		event = &eventNewJournalItem{}
	case evNewFurnitureItem:
		event = &eventNewFurnitureItem{}
	case evNewKillTrophyItem:
		event = &eventNewKillTrophyItem{}
	case evNewLaborerItem:
		event = &eventNewLaborerItem{}
	case evGuildVaultInfo + 4: // live 413 since June 29 2026 update; live 411 is CustomizationChanged — do NOT listen there
		event = &eventGuildVaultInfo{}
	case evBankVaultInfo + 4: // live 414 since June 29 2026 update
		event = &eventBankVaultInfo{}
	case evAttachItemContainer:
		event = &eventAttachItemContainer{}
	case evDetachItemContainer:
		event = &eventDetachItemContainer{}
	case evDied:
		event = &eventDied{}
	case evKilledPlayer:
		event = &eventKilledPlayer{}
	case evCharacterEquipmentChanged:
		event = &eventCharacterEquipmentChanged{}
	case 6: // live HealthUpdate — combat meter (never shifted; Phase 0 confirmed 2026-07-09)
		combatHandleHealthUpdate(params)
		return nil, nil
	case 7: // live HealthUpdates (batch variant on one target)
		combatHandleHealthUpdates(params)
		return nil, nil
	case 82: // live UpdateFame — p2 = fame gained ×10⁴ (premium-inclusive)
		combatHandleUpdateFame(params)
		return nil, nil
	case 231: // live PartyJoined — full roster sync (Phase 0 confirmed)
		combatHandlePartyJoined(params)
		return nil, nil
	case 233: // live PartyPlayerJoined (SAT layout; unobserved live — defensive parse)
		combatHandlePartyPlayerJoined(params)
		return nil, nil
	case 235: // live PartyPlayerLeft (SAT layout; unobserved live — defensive parse)
		combatHandlePartyPlayerLeft(params)
		return nil, nil
	case 232: // live PartyDisbanded (Phase 0 confirmed)
		combatHandlePartyDisbanded(params)
		return nil, nil
	case 278: // live InCombatStateUpdate — self combat flags close encounters early
		combatHandleInCombatState(params)
		return nil, nil
	case evInvitationPlayerTrade, evPlayerTradeStart, evPlayerTradeCancel,
		evPlayerTradeUpdate, evPlayerTradeFinished, evPlayerTradeAcceptChange,
		evPlayerTradeFinished + 2, evPlayerTradeAcceptChange + 2:
		// Player-trade event bus. Wire format reverse-engineered 2026-04-25;
		// April 2026 protocol shifted these +2 so the actual codes seen on-wire
		// are 176-181 (covered by base 174-179 cases plus the two +2 extensions
		// below; the middle +2 codes collide with later base events).
		//
		// Two consumers:
		//   1. recordTradeEvent — raw dumper to logs/trade-debug-<ts>.log
		//      (kept as a safety net for protocol shifts / unknown variants).
		//   2. trade_tracker — typed state machine that emits structured
		//      "[Trade] completed tradeID=… partner=… local_gave=… …" lines
		//      to the main log. Future consumer of WS-relay upload.
		recordTradeEvent(eventType, params)
		switch eventType {
		case 176:
			handleTradeInvitation(params)
		case 179:
			handleTradeUpdate(params)
		case 181:
			handleTradeAcceptChange(params)
		case 178, 180:
			handleTradeEnd(params, eventType)
		}
		return nil, nil
	default:
		if isCombatDiagCode(eventType) {
			// Damage-meter Phase 0: full dump to confirm HealthUpdate/party/
			// combat-state codes + param layout (see client/combat_diag.go).
			dumpCombatEvent(eventType, params)
		}
		if isResourceDiagCode(eventType) {
			// Open-world resource discovery: full dump to identify the live opcode
			// + param layout. Still also recorded in unknown-events for the summary.
			dumpResourceEvent(eventType, params)
		}
		log.Debugf("[Decode] Unhandled event code: %d (params: %d)", eventType, len(params))
		recordUnknownEvent("EVENT", eventType, params)
		return nil, nil
	}

	err = decodeParams(params, event)

	return event, err
}

func decodeParams(params map[uint8]interface{}, operation operation) error {
	config := mapstructure.DecoderConfig{
		WeaklyTypedInput: true, // V18 sends different Go types than struct fields expect
		DecodeHook:       convertGameObjects,
		Result:           operation,
	}

	decoder, err := mapstructure.NewDecoder(&config)
	if err != nil {
		return err
	}

	stringMap := stringMapPool.Get().(map[string]interface{})
	clear(stringMap)
	for k, v := range params {
		stringMap[strconv.Itoa(int(k))] = v
	}

	err = decoder.Decode(stringMap)

	stringMapPool.Put(stringMap)

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

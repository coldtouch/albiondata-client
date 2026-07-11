package client

// trade_tracker.go — typed state machine for player trade events.
//
// Wire format (April 2026 +2 shift confirmed via test sessions 2026-04-25):
//
//   opcode 176 — invitation (fires on bystander or recipient client; missed on
//   pure initiator). Carries the OTHER party's name + guild + character ID.
//     param[1]   = string  partner name
//     param[2]   = string  partner guild
//     param[3]   = int32   partner character session ID
//     param[6]   = int32   trade session ID
//
//   opcode 179 — update (fires repeatedly as items are added). Two parallel
//   slot blocks; symmetry: slot1@8/15  slot2@18/25.
//     param[0]   = int32   trade session ID
//     param[1]   = int32   monotonic state counter
//     SLOT 1 (local player or first-side from bystander view):
//       param[8]   = []int16  item IDs
//       param[7]   = []int8   enchant levels
//       param[15]  = []int8   quantities
//     SLOT 2 (other party):
//       param[18]  = []int16  item IDs
//       param[17]  = []int8   enchant levels
//       param[25]  = []int8   quantities
//     param[6]: present in slot 1 only, value is some T6 item ID unrelated to
//     the actual traded items — likely an equipped-gear leak in the protocol.
//     IGNORED.
//
//   opcode 181 — accept change (sometimes fires bare with just the trade ID,
//   sometimes with `param[1]=bool true` or `param[2]=bool true` indicating
//   final accept).
//     param[0]   = int32   trade session ID
//     param[1]/[2] = bool   accept toggle (when present)
//
//   opcode 180 — finished (completed trade). Carries trade ID only; we look up
//   the accumulated state to emit the final summary.
//
//   opcode 178 — end variant. Empirically sometimes fires on completed
//   participant-side trades (1299 was successful per the user but ended with
//   178). For now we treat 178 + 180 as "trade ended" and rely on whether the
//   accept-bool ever fired to decide success vs cancel.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ao-data/albiondata-client/log"
)

// TradeOffer is one party's items in a trade — parallel arrays of item ID,
// enchant level, and quantity (each index is one item entry).
type TradeOffer struct {
	ItemIDs    []int16
	Enchants   []int8
	Quantities []int8
}

// IsEmpty reports whether the offer carries no items.
func (o TradeOffer) IsEmpty() bool { return len(o.ItemIDs) == 0 }

// String renders the offer as "5x T4_RUNE + 2x T6_BAG.1" for log output.
func (o TradeOffer) String() string {
	if o.IsEmpty() {
		return "(empty)"
	}
	parts := make([]string, 0, len(o.ItemIDs))
	for i, id := range o.ItemIDs {
		var qty int8 = 1
		if i < len(o.Quantities) {
			qty = o.Quantities[i]
		}
		var ench int8 = 0
		if i < len(o.Enchants) {
			ench = o.Enchants[i]
		}
		name := resolveItemName(int(id))
		if ench > 0 {
			name = fmt.Sprintf("%s.%d", name, ench)
		}
		parts = append(parts, fmt.Sprintf("%dx %s", qty, name))
	}
	return strings.Join(parts, " + ")
}

// ActiveTrade holds the accumulated state of one in-progress trade.
type ActiveTrade struct {
	TradeID         int32
	PartnerName     string // empty if invitation event was missed
	PartnerGuild    string
	PartnerCharID   int32
	LocalOffer      TradeOffer
	PartnerOffer    TradeOffer
	StartedAt       time.Time
	LastState       int32
	AcceptSeen      bool
}

var (
	_tradeMu      sync.Mutex
	_activeTrades = map[int32]*ActiveTrade{}
)

func tradeIDFromInvitation(params map[uint8]interface{}) int32 {
	switch v := params[6].(type) {
	case int32:
		return v
	case int64:
		return int32(v)
	case int16:
		return int32(v)
	}
	return 0
}

func tradeIDFromBody(params map[uint8]interface{}) int32 {
	switch v := params[0].(type) {
	case int32:
		return v
	case int64:
		return int32(v)
	case int16:
		return int32(v)
	}
	return 0
}

// handleTradeInvitation processes opcode 176. Captures partner identity for
// later cross-reference when items + finish events arrive.
func handleTradeInvitation(params map[uint8]interface{}) {
	tradeID := tradeIDFromInvitation(params)
	if tradeID == 0 {
		return
	}
	name, _ := params[1].(string)
	guild, _ := params[2].(string)
	var charID int32
	if v, ok := params[3].(int32); ok {
		charID = v
	}

	_tradeMu.Lock()
	defer _tradeMu.Unlock()
	t, ok := _activeTrades[tradeID]
	if !ok {
		t = &ActiveTrade{TradeID: tradeID, StartedAt: time.Now().UTC()}
		_activeTrades[tradeID] = t
	}
	t.PartnerName = name
	t.PartnerGuild = guild
	t.PartnerCharID = charID

	log.Infof("[Trade] Invitation tradeID=%d partner=%s [%s] (charID=%d)",
		tradeID, name, guild, charID)
}

// handleTradeUpdate processes opcode 179. State counter is monotonic per trade,
// so we always overwrite with the latest snapshot — earlier states are subsets
// of the final state.
func handleTradeUpdate(params map[uint8]interface{}) {
	tradeID := tradeIDFromBody(params)
	if tradeID == 0 {
		return
	}
	var state int32
	if v, ok := params[1].(int32); ok {
		state = v
	}

	slot1 := readOffer(params, 8, 7, 15)
	slot2 := readOffer(params, 18, 17, 25)

	_tradeMu.Lock()
	defer _tradeMu.Unlock()
	t, ok := _activeTrades[tradeID]
	if !ok {
		// No invitation seen — likely a user-initiated trade where only the
		// recipient gets the invite event. Create a placeholder so we still
		// capture items.
		t = &ActiveTrade{TradeID: tradeID, StartedAt: time.Now().UTC()}
		_activeTrades[tradeID] = t
	}
	if state >= t.LastState {
		t.LastState = state
		t.LocalOffer = slot1
		t.PartnerOffer = slot2
	}
}

// readOffer extracts a TradeOffer from the param map at the given key offsets.
// idsKey points to the []int16 item ID array; the other keys are siblings.
func readOffer(params map[uint8]interface{}, idsKey, enchantsKey, qtyKey uint8) TradeOffer {
	var o TradeOffer
	if ids, ok := params[idsKey].([]int16); ok && len(ids) > 0 {
		o.ItemIDs = ids
		if ench, ok := params[enchantsKey].([]int8); ok {
			o.Enchants = ench
		}
		if qty, ok := params[qtyKey].([]int8); ok {
			o.Quantities = qty
		}
	}
	return o
}

// handleTradeAcceptChange processes opcode 181. We only care about the variants
// that carry a `bool true` (final accept) so we can mark the trade as
// accepted before the end event fires.
func handleTradeAcceptChange(params map[uint8]interface{}) {
	tradeID := tradeIDFromBody(params)
	if tradeID == 0 {
		return
	}
	accepted := false
	if v, ok := params[1].(bool); ok && v {
		accepted = true
	}
	if v, ok := params[2].(bool); ok && v {
		accepted = true
	}
	if !accepted {
		return
	}
	_tradeMu.Lock()
	defer _tradeMu.Unlock()
	if t, ok := _activeTrades[tradeID]; ok {
		t.AcceptSeen = true
	}
}

// handleTradeEnd processes opcode 178 or 180 — both are end-of-trade signals.
// Emits the final structured summary and clears the active state.
func handleTradeEnd(params map[uint8]interface{}, opcode int16) {
	tradeID := tradeIDFromBody(params)
	if tradeID == 0 {
		return
	}
	_tradeMu.Lock()
	t, ok := _activeTrades[tradeID]
	if ok {
		delete(_activeTrades, tradeID)
	}
	_tradeMu.Unlock()
	if !ok || t == nil {
		log.Infof("[Trade] End tradeID=%d (no captured state)", tradeID)
		return
	}

	status := "ended"
	switch opcode {
	case 180:
		status = "completed"
	case 178:
		if t.AcceptSeen {
			status = "completed"
		} else {
			status = "cancelled"
		}
	}

	partner := t.PartnerName
	if partner == "" {
		partner = "(unknown)"
	}
	log.Infof("[Trade] %s tradeID=%d partner=%s [%s] | local_gave=[%s] | local_received=[%s] (state=%d)",
		status, tradeID, partner, t.PartnerGuild, t.LocalOffer.String(), t.PartnerOffer.String(), t.LastState)

	// Relay completed trades to the VPS for loot accountability ("looter handed
	// their pickups to the banker" credit). Cancelled trades carry no signal.
	// Unknown-partner (initiator-side) trades are still sent — the item data is
	// real and the backend/frontend label them as unattributed.
	if status == "completed" && (!t.LocalOffer.IsEmpty() || !t.PartnerOffer.IsEmpty()) {
		SendPlayerTradeEvent(t)
	}
}

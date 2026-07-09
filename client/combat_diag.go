package client

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

// ============================================================================
// COMBAT-DIAG — Phase 0 of the damage meter (albion_market_analyzer repo,
// DEEP_RESEARCH_2026-07-09_CRAFTING_DPS.md Part C). One dungeon run with
// debugging enabled (run-discover.bat / --debug / --log-unknown-events)
// confirms the live event codes + param layout before the real combat
// handlers are built.
//
// Expected layout, verified against TWO independently maintained live enums
// (Triky313 StatisticsAnalysisTool + JPCodeCraft AlbionDataAvalonia,
// 2026-07-09) — this run is the in-game confirmation:
//
//	6   HealthUpdate         p0 target objId, p1 gameTs, p2 change (NEG=damage,
//	                         POS=heal), p3 newHP, p6 causer objId, p7 spellIdx
//	7   HealthUpdates        batch variant: p0 target, p2..p7 parallel arrays
//	231 PartyJoined          p8 = GUID[] roster, p9 = names[]
//	233 PartyPlayerJoined    p1 = GUID, p2 = name
//	235 PartyPlayerLeft
//	232 PartyDisbanded
//	243 PartyOnClusterPartyJoined (roster re-sync on zone change)
//	278 InCombatStateUpdate  p0 objId, p1 activeCombat, p2 passiveCombat
//
// PHASE 0 RESULT (in-game capture 2026-07-09, Mists + Morgana sub-dungeon,
// 2-man party "Coldtouch"+"goblin1" formed 16:15:10, disbanded 16:21:39):
//   6/7/278 CONFIRMED at the codes and layouts above. 278: p1/p2 bools are
//   simply OMITTED when false.
//   231 CONFIRMED = full-roster party sync: p0 int32 partyId (18030 — same id
//   stamps NewSilverObject p9 and PartySetRoleFlag p0), p4 leader GUID,
//   p8 flat []int8 of 16-byte member GUIDs, p9 []string member names,
//   p16 []bool online flags.
//   232 CONFIRMED = disband/leave: p1 int32 partyId.
//   243 fired on every cluster join: p0 = OWN char GUID, p1=[9], p2 = 6 empty
//   strings — roster re-sync trigger, not the roster source.
//   233/235 never fired (nobody joined/left mid-party) — wire defensively per
//   SAT layout (p1 GUID, p2 name) and confirm opportunistically.
//   The capture also exposed that V18 float params were decoded big-endian —
//   fixed to little-endian in photon-spectator decode_reliable_message.go
//   (damage/HP values were denormal garbage before that fix).
//
// If a dump disagrees with the table above, the live enum shifted again —
// re-run the Triky313 EventCodes.cs diff before wiring handlers.
// ============================================================================

var combatDiagCodes = map[int16]string{
	6:   "HealthUpdate?",
	7:   "HealthUpdates(batch)?",
	231: "PartyJoined?",
	232: "PartyDisbanded?",
	233: "PartyPlayerJoined?",
	235: "PartyPlayerLeft?",
	243: "PartyOnClusterPartyJoined?",
	278: "InCombatStateUpdate?",
}

var (
	_combatDiagMu     sync.Mutex
	_combatDiagSeen   = map[int16]int{}
	_combatDiagFirstN = 12 // full-dump first N of each code, then go quiet
)

func isCombatDiagCode(code int16) bool {
	_, ok := combatDiagCodes[code]
	return ok
}

// dumpCombatEvent prints a full, readable param dump for a suspected combat/
// party event. HealthUpdate fires constantly in combat (5–100/s), so after the
// first N dumps per code it only emits a heartbeat every 2000th occurrence —
// the heartbeat itself is useful (proves the code keeps firing at combat rate).
func dumpCombatEvent(code int16, params map[uint8]interface{}) {
	if !ConfigGlobal.Debug && !ConfigGlobal.LogUnknownEvents {
		return
	}
	_combatDiagMu.Lock()
	n := _combatDiagSeen[code] + 1
	_combatDiagSeen[code] = n
	_combatDiagMu.Unlock()
	if n > _combatDiagFirstN {
		if n%2000 == 0 {
			log.Infof("[COMBAT-DIAG] raw_code=%d still firing (occurrence %d) — %s", code, n, combatDiagCodes[code])
		}
		return
	}

	log.Infof("[COMBAT-DIAG] raw_code=%d  guess=%s  params=%d  (occurrence %d/%d)",
		code, combatDiagCodes[code], len(params), n, _combatDiagFirstN)
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
			capN := ln
			if capN > 30 {
				capN = 30
			}
			parts := make([]string, 0, capN)
			for i := 0; i < capN; i++ {
				parts = append(parts, fmt.Sprintf("%v", rv.Index(i).Interface()))
			}
			more := ""
			if ln > 30 {
				more = fmt.Sprintf(" …+%d", ln-30)
			}
			log.Infof("[COMBAT-DIAG]   p%d %T len=%d = [%s]%s", k, v, ln, strings.Join(parts, ", "), more)
		} else {
			log.Infof("[COMBAT-DIAG]   p%d %T = %v", k, v, v)
		}
	}
}

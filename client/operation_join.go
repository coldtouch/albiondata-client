package client

import (
	"github.com/ao-data/albiondata-client/lib"
	"github.com/ao-data/albiondata-client/log"
)

type operationJoinResponse struct {
	// Param 0 = own in-zone object id — every HealthUpdate/combat event
	// references entities by these per-zone ids (combat_tracker.go).
	ObjectID      int64           `mapstructure:"0"`
	CharacterID   lib.CharacterID `mapstructure:"1"`
	CharacterName string          `mapstructure:"2"`
	// Param 8 IS the current zone. Earlier in this session I incorrectly
	// switched to param 67 because I'd confused the player's HOME reference
	// (param 67, always "@HIDEOUT@3312@<UUID>" for this user) with the player's
	// CURRENT location. A 3-zone walk-out test (hideout → 3312 → 3348 → 3312
	// → hideout) on 2026-04-27 confirmed param 8 matches the destination of the
	// preceding opChangeCluster every time:
	//   16:27:53 OpJoin param 8 = "3312"                 (in zone 3312)
	//   16:28:57 OpJoin param 8 = "3348"                 (in zone 3348)
	//   16:30:15 OpJoin param 8 = "@HIDEOUT@3312@<UUID>" (in hideout)
	// while param 67 stayed pinned to the hideout string the whole time.
	// The April 2026 opcode shift did NOT move this field — it always was 8.
	Location  string          `mapstructure:"8"`
	GuildID   lib.CharacterID `mapstructure:"53"`
	GuildName string          `mapstructure:"57"`
}

//CharacterPartsJSON string          `mapstructure:"6"`
//Edition            string          `mapstructure:"38"`

func (op operationJoinResponse) Process(state *albionState) {
	log.Debugf("Got JoinResponse operation...")

	// Reset the AODataServerID here. This leads to a fresh execution
	// of SetServerID() incase the player switched servers
	state.SetAODataServerID(0)

	// Clear item cache on zone change to prevent unbounded memory growth
	ClearItemCache()
	ClearLootContainerCache()

	log.Infof("Updating player location to %v.", op.Location)
	state.SetLocationId(op.Location)
	state.SetCurrentZone(op.Location)

	if state.GetCharacterId() != op.CharacterID {
		log.Infof("Updating player ID to %v.", op.CharacterID)
	}
	state.SetCharacterId(op.CharacterID)

	if state.GetCharacterName() != op.CharacterName {
		log.Infof("Updating player to %v.", op.CharacterName)
	}
	state.SetCharacterName(op.CharacterName)

	// Combat meter: own objId + zone (finalizes any encounter left open
	// across the zone transition — object ids are per-zone).
	CombatSetSelf(op.ObjectID, op.CharacterName, op.Location)
}

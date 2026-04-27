package client

import (

	"github.com/ao-data/albiondata-client/lib"
	"github.com/ao-data/albiondata-client/log"
)

type operationJoinResponse struct {
	CharacterID   lib.CharacterID `mapstructure:"1"`
	CharacterName string          `mapstructure:"2"`
	// April 2026 opcode shift moved the zone identifier from param 8 → param 67.
	// Confirmed via [ZONE-DIAG] dump on 2026-04-27: param 67 carries values like
	// "@HIDEOUT@3312@ea1f0b23-…" (Albion's hideout zone identifier convention)
	// while param 8 now holds an unrelated numeric string that varies per join.
	Location  string          `mapstructure:"67"`
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
}

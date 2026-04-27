package client

import (
	"github.com/ao-data/albiondata-client/log"
)

// operationChangeClusterResponse fires when the server confirms a zone change
// initiated by the player (walking through a zone boundary, teleporting, etc.).
//
// This is distinct from operationJoinResponse, which only fires on the initial
// client→server connection. For in-session zone tracking (the ZvZ use case
// where players move between zones during a session), opChangeCluster is the
// authoritative source.
//
// Confirmed via [ZONE-DIAG] dump on 2026-04-27 (walk hideout → open world → hideout):
//   - Open-world destination: param 0 = "3312" (numeric zone ID only)
//   - Hideout destination:    param 0 = "@HIDEOUT@3312@<UUID>"
//                             param 1 = "HIDEOUT-0001b" (hideout instance code)
//                             param 2 = "Saggin" (owner guild)
// Param 0 is always present and always the destination cluster identifier.
// The format mirrors what we already emit elsewhere (OpJoin's @HIDEOUT@ strings
// for hideouts, raw zone IDs for open world). Downstream lookup of zone IDs
// → human-readable names ("Bridgewatch") needs a separate ZoneMap; for now
// we pass the raw identifier through and the website can resolve it.
type operationChangeClusterResponse struct {
	Location string `mapstructure:"0"`
}

func (op operationChangeClusterResponse) Process(state *albionState) {
	if op.Location == "" {
		return
	}
	log.Infof("[ChangeCluster] Updating player location to %v.", op.Location)
	state.SetLocationId(op.Location)
	state.SetCurrentZone(op.Location)
}

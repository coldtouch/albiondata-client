package client

import (
	"time"

	"github.com/ao-data/albiondata-client/log"
)

// DeathEvent is the payload sent to the VPS relay for kill/death events.
type DeathEvent struct {
	Timestamp        int64          `json:"timestamp"` // Unix millis
	VictimName       string         `json:"victimName"`
	VictimGuild      string         `json:"victimGuild"`
	KillerName       string         `json:"killerName"`
	KillerGuild      string         `json:"killerGuild"`
	EquipmentAtDeath []EquippedItem `json:"equipmentAtDeath,omitempty"` // B6
}

// eventDied fires on the victim's client when they die (opcode 165).
type eventDied struct {
	VictimName  string `mapstructure:"2"`
	VictimGuild string `mapstructure:"3"`
	KillerName  string `mapstructure:"10"`
	KillerGuild string `mapstructure:"11"`
}

func (ev eventDied) Process(state *albionState) {
	log.Infof("[Death] %s [%s] was killed by %s [%s]",
		ev.VictimName, ev.VictimGuild,
		ev.KillerName, ev.KillerGuild)

	deathEvent := &DeathEvent{
		Timestamp:        time.Now().UnixMilli(),
		VictimName:       ev.VictimName,
		VictimGuild:      ev.VictimGuild,
		KillerName:       ev.KillerName,
		KillerGuild:      ev.KillerGuild,
		EquipmentAtDeath: getEquipmentForPlayer(ev.VictimName),
	}
	// Persist to the local .txt log file (so offline uploads carry deaths, not just loot)
	lootWriter.appendDeath(deathEvent)
	SendDeathEvent(deathEvent)
}

// eventKilledPlayer fires on the killer's client when they kill someone (opcode 164).
type eventKilledPlayer struct {
	VictimName  string `mapstructure:"2"`
	VictimGuild string `mapstructure:"3"`
	KillerName  string `mapstructure:"10"`
	KillerGuild string `mapstructure:"11"`
}

func (ev eventKilledPlayer) Process(state *albionState) {
	log.Infof("[Kill] %s [%s] killed %s [%s]",
		ev.KillerName, ev.KillerGuild,
		ev.VictimName, ev.VictimGuild)

	deathEvent := &DeathEvent{
		Timestamp:        time.Now().UnixMilli(),
		VictimName:       ev.VictimName,
		VictimGuild:      ev.VictimGuild,
		KillerName:       ev.KillerName,
		KillerGuild:      ev.KillerGuild,
		EquipmentAtDeath: getEquipmentForPlayer(ev.VictimName),
	}
	// Persist to the local .txt log file (so offline uploads carry deaths, not just loot)
	lootWriter.appendDeath(deathEvent)
	SendDeathEvent(deathEvent)
}

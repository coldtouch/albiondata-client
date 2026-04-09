package client

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ao-data/albiondata-client/log"
)

// === PLAYER TRACKING ===
// EvNewCharacter and EvCharacterStats register players with their guild/alliance.
// This data is needed to attribute loot events to players.

type PlayerInfo struct {
	Name     string `json:"name"`
	Guild    string `json:"guild,omitempty"`
	Alliance string `json:"alliance,omitempty"`
}

var playerCache sync.Map // map[string]*PlayerInfo — key is player name

func getPlayer(name string) *PlayerInfo {
	if val, ok := playerCache.Load(name); ok {
		return val.(*PlayerInfo)
	}
	return &PlayerInfo{Name: name}
}

// eventNewCharacter fires when a player enters your view range.
// Contains player name, guild, and alliance info.
type eventNewCharacter struct {
	ObjectID     int64  `mapstructure:"0"`
	PlayerName   string `mapstructure:"1"`
	GuildName    string `mapstructure:"8"`
	AllianceName string `mapstructure:"51"`
}

func (ev eventNewCharacter) Process(state *albionState) {
	if ev.PlayerName == "" {
		return
	}
	p := &PlayerInfo{
		Name:     ev.PlayerName,
		Guild:    ev.GuildName,
		Alliance: ev.AllianceName,
	}
	playerCache.Store(ev.PlayerName, p)
	log.Debugf("[NewCharacter] %s [%s] <%s>", ev.PlayerName, ev.GuildName, ev.AllianceName)
}

// eventCharacterStats fires with player info (name, guild, alliance).
// Secondary source — sometimes fires when EvNewCharacter doesn't.
type eventCharacterStats struct {
	ObjectID     int64  `mapstructure:"0"`
	PlayerName   string `mapstructure:"1"`
	GuildName    string `mapstructure:"2"`
	AllianceName string `mapstructure:"4"`
}

func (ev eventCharacterStats) Process(state *albionState) {
	if ev.PlayerName == "" {
		return
	}
	p := &PlayerInfo{
		Name:     ev.PlayerName,
		Guild:    ev.GuildName,
		Alliance: ev.AllianceName,
	}
	playerCache.Store(ev.PlayerName, p)
	log.Debugf("[CharacterStats] %s [%s] <%s>", ev.PlayerName, ev.GuildName, ev.AllianceName)
}

// === LOOT CAPTURE ===
// EvOtherGrabbedLoot fires when any player in range picks up loot.

type LootEvent struct {
	Timestamp       int64      `json:"timestamp"`       // Unix millis
	LootedBy        PlayerInfo `json:"lootedBy"`        // Who picked it up
	LootedFrom      PlayerInfo `json:"lootedFrom"`      // Who/what dropped it
	ItemID          string     `json:"itemId"`           // String item ID (from itemmap)
	NumericID       int        `json:"numericId"`        // Raw numeric ID
	ItemName        string     `json:"itemName"`         // Friendly name if available
	Quantity        int        `json:"quantity"`
	IsSilver        bool       `json:"isSilver"`
	Weight          float64    `json:"weight"`           // Per-unit weight
}

// eventOtherGrabbedLoot fires when a player picks up loot from a corpse/bag.
type eventOtherGrabbedLoot struct {
	ObjectID   int64  `mapstructure:"0"`
	LootedFrom string `mapstructure:"1"` // Player/mob name who dropped the loot
	LootedBy   string `mapstructure:"2"` // Player name who picked up the loot
	IsSilver   bool   `mapstructure:"3"` // True if silver pickup (no item data)
	ItemNumID  int16  `mapstructure:"4"` // Numeric item type ID
	Quantity   int16  `mapstructure:"5"` // Quantity looted
}

func (ev eventOtherGrabbedLoot) Process(state *albionState) {
	if ev.LootedBy == "" {
		return
	}

	// Skip silver pickups — no item data
	if ev.IsSilver {
		log.Debugf("[Loot] %s picked up silver from %s", ev.LootedBy, ev.LootedFrom)
		return
	}

	itemName := resolveItemName(int(ev.ItemNumID))
	qty := int(ev.Quantity)
	if qty <= 0 {
		qty = 1
	}

	lootedBy := getPlayer(ev.LootedBy)
	lootedFrom := getPlayer(ev.LootedFrom)

	lootEvent := &LootEvent{
		Timestamp:  time.Now().UnixMilli(),
		LootedBy:   *lootedBy,
		LootedFrom: *lootedFrom,
		ItemID:     itemName,
		NumericID:  int(ev.ItemNumID),
		Quantity:   qty,
		IsSilver:   ev.IsSilver,
		Weight:     resolveItemWeight(int(ev.ItemNumID)),
	}

	log.Infof("[Loot] %s [%s] picked up %s x%d from %s [%s]",
		lootedBy.Name, lootedBy.Guild,
		itemName, qty,
		lootedFrom.Name, lootedFrom.Guild)

	// Store in local buffer + send to VPS
	lootBuffer.add(lootEvent)
	SendLootEvent(lootEvent)
}

// === LOOT BUFFER ===
// Keeps recent loot events in memory for local access

type lootEventBuffer struct {
	mu     sync.Mutex
	events []*LootEvent
	max    int
}

var lootBuffer = &lootEventBuffer{max: 1000}

func (b *lootEventBuffer) add(ev *LootEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev)
	if len(b.events) > b.max {
		b.events = b.events[len(b.events)-b.max:]
	}
}

func (b *lootEventBuffer) getAll() []*LootEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]*LootEvent, len(b.events))
	copy(result, b.events)
	return result
}

// SaveLootLog writes all buffered loot events to a .txt file in ao-loot-logger format.
// Called on client shutdown so the user can upload the file to the website.
// Format: timestamp_utc;looted_by__alliance;looted_by__guild;looted_by__name;item_id;item_name;quantity;looted_from__alliance;looted_from__guild;looted_from__name
func SaveLootLog() {
	events := lootBuffer.getAll()
	if len(events) == 0 {
		log.Info("[LootLog] No loot events to save")
		return
	}

	// Build filename with timestamp
	t := time.Now()
	filename := fmt.Sprintf("loot-events-%s.txt", t.Format("2006-01-02-15-04-05"))

	// Save next to the executable
	exePath, err := os.Executable()
	if err != nil {
		exePath = "."
	}
	filePath := filepath.Join(filepath.Dir(exePath), filename)

	// Also try current working directory as fallback
	if _, err := os.Stat(filepath.Dir(filePath)); err != nil {
		filePath = filename
	}

	f, err := os.Create(filePath)
	if err != nil {
		log.Errorf("[LootLog] Failed to create file %s: %v", filePath, err)
		return
	}
	defer f.Close()

	// Write header
	fmt.Fprintln(f, "timestamp_utc;looted_by__alliance;looted_by__guild;looted_by__name;item_id;item_name;quantity;looted_from__alliance;looted_from__guild;looted_from__name")

	// Write events
	for _, ev := range events {
		ts := time.UnixMilli(ev.Timestamp).UTC().Format(time.RFC3339)
		// item_name: use friendly name from ITEM_NAMES if we had it, otherwise use itemId
		itemName := ev.ItemID // Already the string ID like T4_MAIN_SPEAR
		fmt.Fprintf(f, "%s;%s;%s;%s;%s;%s;%d;%s;%s;%s\n",
			ts,
			ev.LootedBy.Alliance,
			ev.LootedBy.Guild,
			ev.LootedBy.Name,
			ev.ItemID,
			itemName,
			ev.Quantity,
			ev.LootedFrom.Alliance,
			ev.LootedFrom.Guild,
			ev.LootedFrom.Name,
		)
	}

	log.Infof("[LootLog] Saved %d loot events to %s", len(events), filePath)
}

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

// cachedPlayer wraps PlayerInfo with a timestamp for TTL-based eviction.
type cachedPlayer struct {
	info     *PlayerInfo
	cachedAt time.Time
}

var playerCache sync.Map // map[string]*cachedPlayer — key is player name

const playerCacheTTL = 30 * time.Minute

func init() {
	go playerCacheCleanup()
}

// playerCacheCleanup sweeps playerCache every 5 minutes, deleting entries older than 30 minutes.
func playerCacheCleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		playerCache.Range(func(key, value interface{}) bool {
			entry := value.(*cachedPlayer)
			if now.Sub(entry.cachedAt) > playerCacheTTL {
				playerCache.Delete(key)
			}
			return true
		})
	}
}

func getPlayer(name string) *PlayerInfo {
	if val, ok := playerCache.Load(name); ok {
		return val.(*cachedPlayer).info
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
	playerCache.Store(ev.PlayerName, &cachedPlayer{info: p, cachedAt: time.Now()})
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
	playerCache.Store(ev.PlayerName, &cachedPlayer{info: p, cachedAt: time.Now()})
	log.Debugf("[CharacterStats] %s [%s] <%s>", ev.PlayerName, ev.GuildName, ev.AllianceName)
}

// === LOOT CAPTURE ===
// EvOtherGrabbedLoot fires when any player in range picks up loot.

type LootEvent struct {
	Timestamp  int64      `json:"timestamp"`  // Unix millis
	LootedBy   PlayerInfo `json:"lootedBy"`   // Who picked it up
	LootedFrom PlayerInfo `json:"lootedFrom"` // Who/what dropped it
	ItemID     string     `json:"itemId"`     // String item ID (from itemmap)
	NumericID  int        `json:"numericId"`  // Raw numeric ID
	Quantity   int        `json:"quantity"`
	IsSilver   bool       `json:"isSilver"`
	Weight     float64    `json:"weight"` // Per-unit weight
}

// eventOtherGrabbedLoot fires when a player picks up loot from a corpse/bag.
type eventOtherGrabbedLoot struct {
	ObjectID   int64  `mapstructure:"0"`
	LootedFrom string `mapstructure:"1"` // Player/mob name who dropped the loot
	LootedBy   string `mapstructure:"2"` // Player name who picked up the loot
	IsSilver   bool   `mapstructure:"3"` // True if silver pickup (no item data)
	ItemNumID  int32  `mapstructure:"4"` // Numeric item type ID (int32 to match protocol)
	Quantity   int32  `mapstructure:"5"` // Quantity looted
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

	// Write to local log file and send to VPS
	lootWriter.append(lootEvent)
	SendLootEvent(lootEvent)
}

// === LOOT FILE WRITER ===
// Writes each loot event to a semicolon-delimited .txt file as it arrives,
// compatible with ao-loot-logger format so files can be uploaded to the website.
//
// Format: timestamp_utc;looted_by__alliance;looted_by__guild;looted_by__name;item_id;item_name;quantity;looted_from__alliance;looted_from__guild;looted_from__name

type lootFileWriter struct {
	mu       sync.Mutex
	file     *os.File
	filePath string
	ready    bool
}

var lootWriter = &lootFileWriter{}

// ensureInit opens (or creates) the loot log file. Must be called with mu held.
func (w *lootFileWriter) ensureInit() error {
	if w.ready {
		return nil
	}

	// Resolve logs directory next to the executable
	exePath, err := os.Executable()
	if err != nil {
		exePath = "."
	}
	logsDir := filepath.Join(filepath.Dir(exePath), "logs")
	if mkErr := os.MkdirAll(logsDir, 0755); mkErr != nil {
		// Fall back to a local logs/ directory
		logsDir = "logs"
		_ = os.MkdirAll(logsDir, 0755)
	}

	t := time.Now().UTC()
	filename := fmt.Sprintf("loot-events-%s.txt", t.Format("2006-01-02_15-04-05"))
	w.filePath = filepath.Join(logsDir, filename)

	f, err := os.Create(w.filePath)
	if err != nil {
		return fmt.Errorf("failed to create loot log %s: %w", w.filePath, err)
	}

	// Write the header line
	_, err = fmt.Fprintln(f, "timestamp_utc;looted_by__alliance;looted_by__guild;looted_by__name;item_id;item_name;quantity;looted_from__alliance;looted_from__guild;looted_from__name")
	if err != nil {
		f.Close()
		return fmt.Errorf("failed to write loot log header: %w", err)
	}
	_ = f.Sync()

	w.file = f
	w.ready = true
	log.Infof("[LootLog] Writing loot events to %s", w.filePath)
	return nil
}

// append writes a single loot event to the file and syncs immediately.
func (w *lootFileWriter) append(ev *LootEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureInit(); err != nil {
		log.Errorf("[LootLog] %v", err)
		return
	}

	ts := time.UnixMilli(ev.Timestamp).UTC().Format(time.RFC3339)
	// item_name: we only have the string ID (e.g. T4_BAG), not a localised display name
	fmt.Fprintf(w.file, "%s;%s;%s;%s;%s;%s;%d;%s;%s;%s\n",
		ts,
		ev.LootedBy.Alliance,
		ev.LootedBy.Guild,
		ev.LootedBy.Name,
		ev.ItemID,
		ev.ItemID,
		ev.Quantity,
		ev.LootedFrom.Alliance,
		ev.LootedFrom.Guild,
		ev.LootedFrom.Name,
	)
	_ = w.file.Sync()
}

// CloseLootFile flushes and closes the loot log file. Call on shutdown.
func CloseLootFile() {
	lootWriter.mu.Lock()
	defer lootWriter.mu.Unlock()

	if lootWriter.file == nil {
		return
	}
	_ = lootWriter.file.Sync()
	lootWriter.file.Close()
	lootWriter.file = nil
	lootWriter.ready = false

	if lootWriter.filePath != "" {
		log.Infof("[LootLog] Loot log closed: %s", lootWriter.filePath)
	}
}

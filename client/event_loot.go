package client

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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
var objectIDToName sync.Map // map[int64]string — populated by eventNewCharacter / eventCharacterStats

const playerCacheTTL = 30 * time.Minute

// Loot event activity counters — incremented lock-free by eventOtherGrabbedLoot
// and summarised once per 30s by lootSummaryLoop. Replaces the per-event
// log.Infof that spammed journald 100+ lines/sec during ZvZ.
var lootEventCount atomic.Uint64
var deathEventCount atomic.Uint64

func init() {
	go playerCacheCleanup()
	go lootSummaryLoop()
}

// lootSummaryLoop emits one aggregated Info log every 30 seconds summarising
// how many loot/death events were captured. Individual events are still logged
// at Debug level for detailed troubleshooting.
func lootSummaryLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		l := lootEventCount.Swap(0)
		d := deathEventCount.Swap(0)
		if l == 0 && d == 0 {
			continue
		}
		log.Infof("[Loot] Captured %d loot event(s) and %d death(s) in the last 30s", l, d)
	}
}

// playerNameByObjectID returns the cached character name for an objectID, or ""
// if we have not yet seen that character in range.
func playerNameByObjectID(objectID int64) string {
	if v, ok := objectIDToName.Load(objectID); ok {
		if s, ok2 := v.(string); ok2 {
			return s
		}
	}
	return ""
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
	if ev.ObjectID != 0 {
		objectIDToName.Store(ev.ObjectID, ev.PlayerName)
	}
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
	if ev.ObjectID != 0 {
		objectIDToName.Store(ev.ObjectID, ev.PlayerName)
	}
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

	// Per-event logging at Debug level only — under ZvZ this fires 100+ times/s
	// and log.Infof to journald is synchronous, which stalls the event goroutine.
	// The 30s summary in lootSummaryLoop covers operational visibility.
	log.Debugf("[Loot] %s [%s] picked up %s x%d from %s [%s]",
		lootedBy.Name, lootedBy.Guild,
		itemName, qty,
		lootedFrom.Name, lootedFrom.Guild)
	lootEventCount.Add(1)

	// Write to local log file and send to VPS
	lootWriter.append(lootEvent)
	SendLootEvent(lootEvent)
}

// === LOOT FILE WRITER ===
// Writes each loot event to a semicolon-delimited .txt file as it arrives,
// compatible with ao-loot-logger format so files can be uploaded to the website.
//
// Format: timestamp_utc;looted_by__alliance;looted_by__guild;looted_by__name;item_id;item_name;quantity;looted_from__alliance;looted_from__guild;looted_from__name

// lootFileWriter uses a bufio.Writer over the underlying *os.File so each loot
// event hits an in-process buffer (no syscall), is flushed to the kernel every
// 5s by flushLoop, and is fsynced on shutdown via CloseLootFile.
//
// Durability vs. the old "Sync() after every event" behaviour:
//   - Every event is immediately relayed to the VPS (vps_relay.go) — that's the
//     source of truth for the website's Loot Logger.
//   - The local .txt is the secondary copy. On a process crash, up to ~5s of
//     buffered bytes may not be on disk yet, but normal shutdown (systray exit,
//     Ctrl+C) drains them via CloseLootFile.
//
// This removes the per-event fsync hot-path that bottlenecked ZvZ sessions
// (50+ loots/sec → 50+ fsync syscalls/sec).
type lootFileWriter struct {
	mu       sync.Mutex
	file     *os.File
	buf      *bufio.Writer
	filePath string
	ready    bool
	stopCh   chan struct{}
}

var lootWriter = &lootFileWriter{}

// flushLoopStarted guards the background flushLoop so we only launch it once
// across ensureInit calls (the first event per session triggers it).
var flushLoopStarted atomic.Bool

// Sized to hold ~60–120 typical loot rows without a kernel write. bufio.Writer
// auto-flushes on full, so this is a soft cap — no events are dropped.
const lootWriteBufSize = 32 * 1024

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

	buf := bufio.NewWriterSize(f, lootWriteBufSize)

	// Write the header line
	_, err = fmt.Fprintln(buf, "timestamp_utc;looted_by__alliance;looted_by__guild;looted_by__name;item_id;item_name;quantity;looted_from__alliance;looted_from__guild;looted_from__name")
	if err != nil {
		f.Close()
		return fmt.Errorf("failed to write loot log header: %w", err)
	}
	// Flush header to kernel + fsync so the file isn't empty if the process dies
	// before the first loot event.
	if err := buf.Flush(); err != nil {
		f.Close()
		return fmt.Errorf("failed to flush loot log header: %w", err)
	}
	_ = f.Sync()

	w.file = f
	w.buf = buf
	w.stopCh = make(chan struct{})
	w.ready = true

	// Launch the background flusher on first init for this process. Survives
	// file rotation — ensureInit swaps the underlying file/buf but the loop
	// keeps pulling the current lootWriter state.
	if flushLoopStarted.CompareAndSwap(false, true) {
		go lootFlushLoop()
	}

	log.Infof("[LootLog] Writing loot events to %s", w.filePath)
	return nil
}

// lootFlushLoop pushes the bufio buffer to the kernel every 5s so data is
// durable within a small window without an fsync-per-event hot path.
func lootFlushLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		lootWriter.mu.Lock()
		if lootWriter.ready && lootWriter.buf != nil {
			if err := lootWriter.buf.Flush(); err != nil {
				log.Debugf("[LootLog] Periodic flush failed: %v", err)
			}
		}
		lootWriter.mu.Unlock()
	}
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
	if _, err := fmt.Fprintf(w.buf, "%s;%s;%s;%s;%s;%s;%d;%s;%s;%s\n",
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
	); err != nil {
		log.Errorf("[LootLog] Write failed: %v", err)
		return
	}
	// No per-event fsync: buffered writer absorbs bursts, flushLoop persists
	// every 5s, CloseLootFile drains on shutdown, VPS relay has realtime copy.
}

// appendDeath writes a single death event to the loot log file using the
// __DEATH__ sentinel item_id. The backend upload parser + frontend Loot Logger
// both recognize this marker and render it as a death (not a regular loot row).
//
// Row layout reuses the standard schema:
//   looted_by_* = killer
//   looted_from_* = victim
//   item_id / item_name = "__DEATH__"
//   quantity = 1
func (w *lootFileWriter) appendDeath(ev *DeathEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureInit(); err != nil {
		log.Errorf("[LootLog] %v", err)
		return
	}

	ts := time.UnixMilli(ev.Timestamp).UTC().Format(time.RFC3339)
	// Alliance is not available on DeathEvent today — leave blank for now.
	fmt.Fprintf(w.buf, "%s;%s;%s;%s;%s;%s;%d;%s;%s;%s\n",
		ts,
		"",             // killer alliance
		ev.KillerGuild, // killer guild
		ev.KillerName,  // killer name
		"__DEATH__",    // item_id sentinel
		"__DEATH__",    // item_name sentinel
		1,
		"",             // victim alliance
		ev.VictimGuild, // victim guild
		ev.VictimName,  // victim name
	)
	// Deaths are rare (compared to loot) but we still let flushLoop handle
	// persistence — the VPS relay path already has the event in-flight.
}

// CloseLootFile flushes and closes the loot log file. Call on shutdown.
// Drains the bufio buffer → kernel → disk so no buffered events are lost on exit.
func CloseLootFile() {
	lootWriter.mu.Lock()
	defer lootWriter.mu.Unlock()

	if lootWriter.file == nil {
		return
	}
	if lootWriter.buf != nil {
		if err := lootWriter.buf.Flush(); err != nil {
			log.Errorf("[LootLog] Final flush failed: %v", err)
		}
	}
	_ = lootWriter.file.Sync()
	lootWriter.file.Close()
	lootWriter.file = nil
	lootWriter.buf = nil
	lootWriter.ready = false

	if lootWriter.filePath != "" {
		log.Infof("[LootLog] Loot log closed: %s", lootWriter.filePath)
	}
}

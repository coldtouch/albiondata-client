package client

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ao-data/albiondata-client/log"
)

// operationGetChestLogsResponse decodes the per-chest deposit/withdraw log the
// game streams when a player opens the Log tab on a personal or guild chest.
//
// Discovered by running `--log-unknown-events --debug` and opening a chest log
// on 2026-04-20. Server responds on opcode 157 (opGetChestLogs with +6 shift)
// with parallel arrays — 253/255 are Photon framing, 0..5 are the data:
//
//	0: []string  — player name (who acted)
//	1: []int16   — item type ID  (map via itemmap.json)
//	2: []int8    — quality  (1=Normal .. 5=Masterpiece)
//	3: []int16   — quantity
//	4: []int64   — .NET ticks (100-ns intervals since 0001-01-01 UTC)
//	5: []string  — always empty across a 303-row mixed-direction sample; NOT
//	               the action type. Kept as `extra` for completeness — may
//	               later turn out to be a rarely-populated tag we haven't
//	               seen yet (e.g. enchantment name for charged items).
//
// Action type (deposit vs withdraw) is NOT in the response body. The game
// pulls deposits and withdrawals via separate REQUEST 157 calls distinguished
// by param 6 (observed values: 1 and 28). We pair request→response via the
// Photon invocation counter (param 255) — captured here as OpID — to tag each
// response batch with its originating filter. See chest_log_request.go.
//
// The game paginates: a single viewing produces multiple responses with
// overlapping data, so the file writer dedupes by (player, itemID, quality,
// qty, ticks) — action tag does NOT participate in the dedupe key (a single
// entry shouldn't appear in both filters).
type operationGetChestLogsResponse struct {
	PlayerNames []string `mapstructure:"0"`
	ItemTypeIDs []int16  `mapstructure:"1"`
	Qualities   []int8   `mapstructure:"2"`
	Quantities  []int16  `mapstructure:"3"`
	TimestampsT []int64  `mapstructure:"4"` // .NET ticks
	ExtraStr    []string `mapstructure:"5"`
	OpID        int16    `mapstructure:"255"` // Photon invocation counter — pairs with request
}

// ChestLogEntry is one decoded row from the chest log response.
type ChestLogEntry struct {
	Timestamp   int64  `json:"timestamp"`    // Unix millis
	PlayerName  string `json:"playerName"`
	ItemID      string `json:"itemId"`       // Resolved via itemmap; empty if unknown
	NumericID   int    `json:"numericId"`
	Quality     int    `json:"quality"`
	Quantity    int    `json:"quantity"`
	Extra       string `json:"extra,omitempty"`       // param 5, raw
	Action      string `json:"action,omitempty"`      // "deposit"/"withdraw"/"filter_N" (from request param 6)
	FilterValue int    `json:"filterValue,omitempty"` // raw param 6 from request, -1 if not paired
}

// .NET epoch → Unix epoch conversion. .NET ticks are 100-nanosecond intervals
// since 0001-01-01T00:00:00 UTC. Unix is seconds since 1970-01-01T00:00:00 UTC.
// Diff: 62135596800 seconds = 621355968000000000 ticks.
const dotNetTicksUnixEpoch = int64(621355968000000000)

func ticksToUnixMillis(ticks int64) int64 {
	if ticks <= dotNetTicksUnixEpoch {
		return 0
	}
	// ticks are in 100-ns intervals. (ticks - epoch) / 10000 = millis
	return (ticks - dotNetTicksUnixEpoch) / 10000
}

func (op operationGetChestLogsResponse) Process(state *albionState) {
	n := len(op.PlayerNames)
	if n == 0 {
		return
	}
	minLen := func(sizes ...int) int {
		m := sizes[0]
		for _, s := range sizes[1:] {
			if s < m {
				m = s
			}
		}
		return m
	}
	n = minLen(
		len(op.PlayerNames),
		len(op.ItemTypeIDs),
		len(op.Qualities),
		len(op.Quantities),
		len(op.TimestampsT),
	)
	if n == 0 {
		return
	}

	// Pair with the originating request to learn the action filter. OpID is
	// the Photon invocation counter and is shared by request + response.
	filterValue, actionTag := resolveChestLogAction(op.OpID)

	entries := make([]ChestLogEntry, 0, n)
	for i := 0; i < n; i++ {
		numericID := int(op.ItemTypeIDs[i])
		itemName := ""
		if numericID > 0 {
			itemName = resolveItemName(numericID)
		}
		var extra string
		if i < len(op.ExtraStr) {
			extra = op.ExtraStr[i]
		}
		// Game logs withdrawals of non-stackable gear as qty=-1 (no stack-size
		// concept on unique instances). Normalize to 1 so downstream code
		// doesn't need to special-case the sentinel.
		qty := int(op.Quantities[i])
		if qty < 1 {
			qty = 1
		}
		entries = append(entries, ChestLogEntry{
			Timestamp:   ticksToUnixMillis(op.TimestampsT[i]),
			PlayerName:  op.PlayerNames[i],
			ItemID:      itemName,
			NumericID:   numericID,
			Quality:     int(op.Qualities[i]),
			Quantity:    qty,
			Extra:       extra,
			Action:      actionTag,
			FilterValue: filterValue,
		})
	}

	log.Infof("[ChestLog] Decoded %d entries (opID=%d action=%s filter=%d)",
		len(entries), op.OpID, actionTag, filterValue)
	if len(entries) > 0 {
		first := entries[0]
		log.Infof("[ChestLog]   first: %s · %s q%d ×%d · %s · action=%s",
			first.PlayerName,
			first.ItemID,
			first.Quality,
			first.Quantity,
			time.UnixMilli(first.Timestamp).UTC().Format(time.RFC3339),
			first.Action,
		)
	}

	// Local TSV for debugging / offline analysis
	chestLogWriter.append(entries)

	// Stream to VPS so the website can render per-player deposit ground truth
	// and cross-check against pickup events for accountability verification.
	SendChestLogBatch(&ChestLogBatch{
		CapturedAt:  time.Now().UnixMilli(),
		Action:      actionTag,
		FilterValue: filterValue,
		Entries:     entries,
	})
}

// === CHEST LOG FILE WRITER ===
// Writes decoded entries to logs/chest-logs-<ts>.tsv so we can eyeball them
// after a session and design the website attribution UI against real data.
// Dedupes by (player, numericID, quality, qty, ticks) — the game paginates
// chest logs and will stream the same entries twice in a single viewing.

type chestLogFileWriter struct {
	mu       sync.Mutex
	file     *os.File
	filePath string
	ready    bool
	seen     map[string]struct{}
}

var chestLogWriter = &chestLogFileWriter{seen: map[string]struct{}{}}

func (w *chestLogFileWriter) ensureInit() error {
	if w.ready {
		return nil
	}
	exePath, err := os.Executable()
	if err != nil {
		exePath = "."
	}
	logsDir := filepath.Join(filepath.Dir(exePath), "logs")
	if mkErr := os.MkdirAll(logsDir, 0755); mkErr != nil {
		logsDir = "logs"
		_ = os.MkdirAll(logsDir, 0755)
	}

	t := time.Now().UTC()
	filename := fmt.Sprintf("chest-logs-%s.tsv", t.Format("2006-01-02_15-04-05"))
	w.filePath = filepath.Join(logsDir, filename)

	f, err := os.Create(w.filePath)
	if err != nil {
		return fmt.Errorf("create chest log file %s: %w", w.filePath, err)
	}
	if _, err := fmt.Fprintln(f, "timestamp_utc\tplayer_name\titem_id\tnumeric_id\tquality\tquantity\taction\tfilter_value\textra"); err != nil {
		f.Close()
		return fmt.Errorf("write chest log header: %w", err)
	}
	_ = f.Sync()
	w.file = f
	w.ready = true
	log.Infof("[ChestLog] Writing decoded chest log entries to %s", w.filePath)
	return nil
}

func (w *chestLogFileWriter) append(entries []ChestLogEntry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureInit(); err != nil {
		log.Errorf("[ChestLog] %v", err)
		return
	}
	written := 0
	for _, e := range entries {
		key := fmt.Sprintf("%s|%d|%d|%d|%d", e.PlayerName, e.NumericID, e.Quality, e.Quantity, e.Timestamp)
		if _, ok := w.seen[key]; ok {
			continue
		}
		w.seen[key] = struct{}{}
		ts := time.UnixMilli(e.Timestamp).UTC().Format(time.RFC3339)
		if _, err := fmt.Fprintf(w.file, "%s\t%s\t%s\t%d\t%d\t%d\t%s\t%d\t%s\n",
			ts, e.PlayerName, e.ItemID, e.NumericID, e.Quality, e.Quantity,
			e.Action, e.FilterValue, e.Extra); err != nil {
			log.Errorf("[ChestLog] write row: %v", err)
			continue
		}
		written++
	}
	if written > 0 {
		_ = w.file.Sync()
		log.Infof("[ChestLog] Appended %d new entries (dedup skipped %d) — total unique: %d",
			written, len(entries)-written, len(w.seen))
	}
}

// CloseChestLogFile flushes and closes the chest log file. Call on shutdown.
func CloseChestLogFile() {
	chestLogWriter.mu.Lock()
	defer chestLogWriter.mu.Unlock()
	if chestLogWriter.file == nil {
		return
	}
	_ = chestLogWriter.file.Sync()
	chestLogWriter.file.Close()
	chestLogWriter.file = nil
	chestLogWriter.ready = false
	if chestLogWriter.filePath != "" {
		log.Infof("[ChestLog] Closed %s", chestLogWriter.filePath)
	}
}

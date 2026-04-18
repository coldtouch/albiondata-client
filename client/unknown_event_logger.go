package client

// unknown_event_logger.go — writes every unrecognised opcode we see into a dedicated
// log file (logs/unknown-events-<start-ts>.log) so we can reverse-engineer new
// game events during a PvP session.
//
// Rate-limited: each (direction, opcode) combination logs at most 5 times per session,
// then only emits a summary counter every 100 subsequent occurrences. This keeps the
// file under a few MB even during a heavy ZvZ.
//
// Enabled via:
//   - command line: --log-unknown-events
//   - config.yaml:  LogUnknownEvents: true
// Default is OFF.

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/ao-data/albiondata-client/log"
)

type unknownOpKey struct {
	direction string // "REQUEST" | "RESPONSE" | "EVENT"
	opcode    int16
}

type unknownLoggerState struct {
	mu       sync.Mutex
	file     *os.File
	filePath string
	seen     map[unknownOpKey]int // count per (direction,opcode) this session
	started  time.Time
}

var _unknownLogger unknownLoggerState

const (
	_unknownLogFirstN     = 5   // fully log first N occurrences of each (dir,opcode)
	_unknownLogSummaryMod = 100 // then log a summary line every N occurrences
)

// initUnknownLogger lazy-opens the log file on first unrecognised event.
func initUnknownLogger() error {
	if _unknownLogger.file != nil {
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
	_unknownLogger.started = time.Now().UTC()
	filename := fmt.Sprintf("unknown-events-%s.log", _unknownLogger.started.Format("2006-01-02_15-04-05"))
	path := filepath.Join(logsDir, filename)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create unknown-events log %s: %w", path, err)
	}
	_unknownLogger.file = f
	_unknownLogger.filePath = path
	_unknownLogger.seen = map[unknownOpKey]int{}

	// Header with a legend so analysts know what to expect.
	fmt.Fprintln(f, "# Coldtouch Data Client — Unknown-Events Log")
	fmt.Fprintln(f, "# Rows are TSV: timestamp_utc <tab> direction <tab> opcode <tab> param_count <tab> params_preview")
	fmt.Fprintln(f, "# Each (direction,opcode) is fully logged the first 5 times, then a summary every 100.")
	fmt.Fprintln(f, "# Directions: REQUEST / RESPONSE / EVENT")
	fmt.Fprintf(f, "# Session started: %s\n", _unknownLogger.started.Format(time.RFC3339))
	fmt.Fprintln(f, "#")

	log.Infof("[UnknownEvents] Logging unrecognised opcodes to %s", path)
	return nil
}

// CloseUnknownLogger writes a summary table and closes the file. Safe to call
// multiple times; no-op if logging was never enabled.
func CloseUnknownLogger() {
	_unknownLogger.mu.Lock()
	defer _unknownLogger.mu.Unlock()
	if _unknownLogger.file == nil {
		return
	}
	// Summary table at the end — sorted by total count descending.
	fmt.Fprintln(_unknownLogger.file, "")
	fmt.Fprintln(_unknownLogger.file, "# === SUMMARY — count per unique (direction,opcode) ===")
	type kv struct {
		key   unknownOpKey
		count int
	}
	sorted := make([]kv, 0, len(_unknownLogger.seen))
	for k, v := range _unknownLogger.seen {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })
	for _, it := range sorted {
		fmt.Fprintf(_unknownLogger.file, "# %s opcode=%d  count=%d\n", it.key.direction, it.key.opcode, it.count)
	}
	fmt.Fprintf(_unknownLogger.file, "# Session ended: %s (duration %s)\n",
		time.Now().UTC().Format(time.RFC3339),
		time.Since(_unknownLogger.started).Round(time.Second).String())
	_unknownLogger.file.Close()
	_unknownLogger.file = nil
}

// recordUnknownEvent writes one row to the log, respecting the rate-limit.
// `direction` = "REQUEST" / "RESPONSE" / "EVENT".
func recordUnknownEvent(direction string, opcode int16, params map[uint8]interface{}) {
	if !ConfigGlobal.LogUnknownEvents {
		return
	}
	_unknownLogger.mu.Lock()
	defer _unknownLogger.mu.Unlock()
	if _unknownLogger.file == nil {
		if err := initUnknownLogger(); err != nil {
			log.Errorf("[UnknownEvents] init failed: %v", err)
			return
		}
	}
	key := unknownOpKey{direction: direction, opcode: opcode}
	count := _unknownLogger.seen[key] + 1
	_unknownLogger.seen[key] = count

	// Rate-limit: full log for first N, then every Nth summary.
	if count > _unknownLogFirstN && count%_unknownLogSummaryMod != 0 {
		return
	}

	ts := time.Now().UTC().Format(time.RFC3339)
	preview := formatParamsPreview(params)
	if count > _unknownLogFirstN {
		fmt.Fprintf(_unknownLogger.file, "%s\t%s\t%d\t%d\t[occurrence %d — still firing]\n",
			ts, direction, opcode, len(params), count)
	} else {
		fmt.Fprintf(_unknownLogger.file, "%s\t%s\t%d\t%d\t%s\n",
			ts, direction, opcode, len(params), preview)
	}
}

// formatParamsPreview produces a one-line human-readable dump of params suitable for logging.
// Byte arrays > 32 bytes are hex-truncated. Slices/arrays show first 5 elements + count.
func formatParamsPreview(params map[uint8]interface{}) string {
	if len(params) == 0 {
		return "(no params)"
	}
	// Sort keys for deterministic output
	keys := make([]uint8, 0, len(params))
	for k := range params {
		if k == 252 || k == 253 {
			continue // these are the event-type/op-code param, always present
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := params[k]
		rv := reflect.ValueOf(v)
		kind := rv.Kind()
		var valStr string
		switch kind {
		case reflect.Slice, reflect.Array:
			n := rv.Len()
			// Byte slice → hex
			if rv.Type().Elem().Kind() == reflect.Uint8 {
				b := rv.Bytes()
				if n > 32 {
					valStr = fmt.Sprintf("[]byte len=%d hex=%s…", n, hex.EncodeToString(b[:32]))
				} else {
					valStr = fmt.Sprintf("[]byte len=%d hex=%s", n, hex.EncodeToString(b))
				}
			} else {
				preview := ""
				for i := 0; i < n && i < 5; i++ {
					if i > 0 {
						preview += ","
					}
					preview += fmt.Sprintf("%v", rv.Index(i).Interface())
				}
				if n > 5 {
					preview += fmt.Sprintf(",…+%d", n-5)
				}
				valStr = fmt.Sprintf("[%s]len=%d", preview, n)
			}
		default:
			s := fmt.Sprintf("%v", v)
			if len(s) > 120 {
				s = s[:120] + "…"
			}
			valStr = s
		}
		parts = append(parts, fmt.Sprintf("%d=%s", k, valStr))
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " | "
		}
		out += p
	}
	return out
}

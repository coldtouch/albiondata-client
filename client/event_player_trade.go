package client

// event_player_trade.go — testing-only logger for player trade opcodes.
//
// Captures full parameter payloads for the player-trade event range (opcodes
// 174-179 plus +2 variants in case the April 2026 protocol shift moved them)
// to a dedicated logs/trade-debug-<session-ts>.log file. Used to reverse-engineer
// the trade-event format so we can build the production accountability handler
// that ties picked-up loot → in-party trade → chest deposit through a guild
// looter.
//
// Always-on (no config flag). Trades are infrequent enough that there's no
// log-volume concern, and we want full fidelity (no rate limit, no truncation).
// Production builds will route specific opcodes to typed handlers and stop
// using this dump file.

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

const (
	tradeOpcodeMin int16 = 174 // evInvitationPlayerTrade
	tradeOpcodeMax int16 = 181 // evPlayerTradeAcceptChange + 2 (April 2026 shift safety)
)

type tradeLoggerState struct {
	mu       sync.Mutex
	file     *os.File
	filePath string
	started  time.Time
	count    int
}

var _tradeLogger tradeLoggerState

// isTradeEventCode returns true for any opcode in the player-trade range.
func isTradeEventCode(code int16) bool {
	return code >= tradeOpcodeMin && code <= tradeOpcodeMax
}

// tradeEventLabel maps a code to a human-readable name. The 174-179 range is
// evInvitationPlayerTrade .. evPlayerTradeAcceptChange. Other events shifted
// +2 in the April 2026 protocol update, so 176-181 carry both possibilities
// until in-game testing confirms which one fires.
func tradeEventLabel(code int16) string {
	switch code {
	case 174:
		return "evInvitationPlayerTrade"
	case 175:
		return "evPlayerTradeStart"
	case 176:
		return "evPlayerTradeCancel | evInvitationPlayerTrade+2?"
	case 177:
		return "evPlayerTradeUpdate | evPlayerTradeStart+2?"
	case 178:
		return "evPlayerTradeFinished | evPlayerTradeCancel+2?"
	case 179:
		return "evPlayerTradeAcceptChange | evPlayerTradeUpdate+2?"
	case 180:
		return "evPlayerTradeFinished+2?"
	case 181:
		return "evPlayerTradeAcceptChange+2?"
	}
	return fmt.Sprintf("trade?(%d)", code)
}

func initTradeLogger() error {
	if _tradeLogger.file != nil {
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
	_tradeLogger.started = time.Now().UTC()
	filename := fmt.Sprintf("trade-debug-%s.log", _tradeLogger.started.Format("2006-01-02_15-04-05"))
	path := filepath.Join(logsDir, filename)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("trade logger create %s: %w", path, err)
	}
	_tradeLogger.file = f
	_tradeLogger.filePath = path

	fmt.Fprintln(f, "# Coldtouch Data Client — Player Trade Debug Log")
	fmt.Fprintln(f, "# Captures full param payloads for trade opcodes 174-181 (player trade events + April 2026 +2 shift variants).")
	fmt.Fprintln(f, "# No rate limit. No truncation. Every byte slice is logged in full hex.")
	fmt.Fprintf(f, "# Session started: %s\n", _tradeLogger.started.Format(time.RFC3339))
	fmt.Fprintln(f, "#")
	log.Infof("[TradeDebug] Logging player-trade opcodes to %s", path)
	return nil
}

// CloseTradeLogger writes a footer and closes the file. Safe to call multiple times.
func CloseTradeLogger() {
	_tradeLogger.mu.Lock()
	defer _tradeLogger.mu.Unlock()
	if _tradeLogger.file == nil {
		return
	}
	fmt.Fprintf(_tradeLogger.file, "\n# Session ended: %s — captured %d trade events\n",
		time.Now().UTC().Format(time.RFC3339), _tradeLogger.count)
	_tradeLogger.file.Close()
	_tradeLogger.file = nil
}

// recordTradeEvent dumps the full param map for one trade event.
func recordTradeEvent(code int16, params map[uint8]interface{}) {
	_tradeLogger.mu.Lock()
	defer _tradeLogger.mu.Unlock()
	if _tradeLogger.file == nil {
		if err := initTradeLogger(); err != nil {
			log.Errorf("[TradeDebug] init failed: %v", err)
			return
		}
	}
	_tradeLogger.count++

	ts := time.Now().UTC().Format(time.RFC3339)
	fmt.Fprintf(_tradeLogger.file, "\n=== %s | EVENT opcode=%d (%s) | %d params ===\n",
		ts, code, tradeEventLabel(code), len(params))

	keys := make([]uint8, 0, len(params))
	for k := range params {
		if k == 252 || k == 253 {
			continue // event-type / op-code marker, always present
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		dumpTradeParam(_tradeLogger.file, k, params[k])
	}

	log.Infof("[TradeDebug] Captured opcode=%d (%s) with %d params → %s",
		code, tradeEventLabel(code), len(params), filepath.Base(_tradeLogger.filePath))
}

// dumpTradeParam writes one param with full fidelity. Byte slices logged as
// full hex; non-byte slices/arrays expanded element-by-element; everything
// else printed via %v.
func dumpTradeParam(f *os.File, key uint8, v interface{}) {
	if v == nil {
		fmt.Fprintf(f, "  param[%d] = nil\n", key)
		return
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		n := rv.Len()
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			b := rv.Bytes()
			fmt.Fprintf(f, "  param[%d] = []byte len=%d hex=%s\n", key, n, hex.EncodeToString(b))
			return
		}
		fmt.Fprintf(f, "  param[%d] = %s len=%d:\n", key, rv.Type().String(), n)
		for i := 0; i < n; i++ {
			fmt.Fprintf(f, "    [%d] = %v\n", i, rv.Index(i).Interface())
		}
	default:
		fmt.Fprintf(f, "  param[%d] = %T %v\n", key, v, v)
	}
}

package client

// trade_ops_debug.go — captures OPERATIONS (requests/responses) around a trade,
// to find the initiator-side partner reference. Trade EVENTS (176-181) carry no
// partner id when WE open the trade; the partner id is almost certainly in the
// trade-initiation OPERATION our client sends when we click "trade" on a player.
//
// We can't know that opcode a priori, so we keep a small ring buffer of every
// recent operation (req + resp, full params) and flush the last few seconds
// into the trade-debug log whenever any trade event fires. The trade-init op
// will be in that window, identifiable by a param that matches a nearby
// player's objId / character id.

import (
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"sort"
	"sync"
	"time"
)

type opRecord struct {
	ts     time.Time
	kind   string // "REQ" | "RESP"
	code   int16
	params map[uint8]interface{}
}

var (
	_opMu   sync.Mutex
	_opRing []opRecord
)

const opRingCap = 80              // ~a few seconds of operations
const opFlushWindow = 8 * time.Second

// recordOpForTradeDebug stores a defensive copy of one operation's params in the
// ring buffer. Called from the top of decodeRequest / decodeResponse.
func recordOpForTradeDebug(kind string, code int16, params map[uint8]interface{}) {
	cp := make(map[uint8]interface{}, len(params))
	for k, v := range params {
		cp[k] = v
	}
	_opMu.Lock()
	_opRing = append(_opRing, opRecord{ts: time.Now(), kind: kind, code: code, params: cp})
	if len(_opRing) > opRingCap {
		_opRing = _opRing[len(_opRing)-opRingCap:]
	}
	_opMu.Unlock()
}

// flushRecentOpsToTradeLog writes the operations from the last opFlushWindow to
// the given file — the trade-init op (with the partner reference) lives here.
func flushRecentOpsToTradeLog(f *os.File) {
	if f == nil {
		return
	}
	cutoff := time.Now().Add(-opFlushWindow)
	_opMu.Lock()
	recent := make([]opRecord, 0, len(_opRing))
	for _, r := range _opRing {
		if r.ts.After(cutoff) {
			recent = append(recent, r)
		}
	}
	_opMu.Unlock()

	fmt.Fprintf(f, "  --- operations in the last %s (%d) — look for the trade-init op ---\n", opFlushWindow, len(recent))
	for _, r := range recent {
		fmt.Fprintf(f, "    [%s] %s op=%d (%s):\n", r.ts.UTC().Format("15:04:05.000"), r.kind, r.code, OperationType(r.code))
		keys := make([]uint8, 0, len(r.params))
		for k := range r.params {
			if k == 252 || k == 253 {
				continue
			}
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		for _, k := range keys {
			dumpOpParam(f, k, r.params[k])
		}
	}
}

func dumpOpParam(f *os.File, key uint8, v interface{}) {
	if v == nil {
		fmt.Fprintf(f, "      param[%d] = nil\n", key)
		return
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			fmt.Fprintf(f, "      param[%d] = []byte hex=%s\n", key, hex.EncodeToString(rv.Bytes()))
			return
		}
		fmt.Fprintf(f, "      param[%d] = %s len=%d: ", key, rv.Type().String(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			fmt.Fprintf(f, "%v ", rv.Index(i).Interface())
		}
		fmt.Fprintln(f)
	default:
		fmt.Fprintf(f, "      param[%d] = %T %v\n", key, v, v)
	}
}

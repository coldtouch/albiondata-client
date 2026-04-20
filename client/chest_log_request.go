package client

import (
	"reflect"
	"sync"
	"time"

	"github.com/ao-data/albiondata-client/log"
)

// captureChestLogRequestParams extracts the Photon invocation counter (255)
// and the filter value (param 6) from a REQUEST 157, stashing them for later
// pairing with the response. Called from decode.go.
func captureChestLogRequestParams(params map[uint8]interface{}) {
	op255, ok := readInt16Loose(params[255])
	if !ok {
		log.Debugf("[ChestLog] REQUEST missing param 255 (opID): %T", params[255])
		return
	}
	filter, ok := readIntLoose(params[6])
	if !ok {
		log.Debugf("[ChestLog] REQUEST missing param 6 (filter): %T", params[6])
		return
	}
	recordChestLogRequestFilter(op255, filter)
}

// readInt16Loose accepts any integer-typed value and returns it as int16.
// Photon sometimes decodes small integers as int32 or uint32 depending on the
// byte width on the wire, so we accept all numeric kinds.
func readInt16Loose(v interface{}) (int16, bool) {
	if v == nil {
		return 0, false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int16(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int16(rv.Uint()), true
	}
	return 0, false
}

func readIntLoose(v interface{}) (int, bool) {
	if v == nil {
		return 0, false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int(rv.Uint()), true
	}
	return 0, false
}

// chest_log_request.go — tracks outgoing opGetChestLogs requests so we can pair
// each response back to the filter value the client sent.
//
// Why: the response body for opcode 157 does NOT mark entries as deposits or
// withdrawals. The game instead issues TWO separate requests per chest viewing,
// one per direction, distinguished by REQUEST param 6 (observed values 1 and
// 28). To tag each response row with its direction we stash the filter value
// keyed by the Photon invocation counter (param 255) at request time, then
// look it up when the matching response arrives.
//
// The map is bounded by TTL eviction (30s should be ample; requests normally
// resolve in well under a second) and sized caps so it can't leak on a hostile
// or misbehaving server.

type chestLogRequestEntry struct {
	filterValue int
	recordedAt  time.Time
}

var (
	chestLogReqMu    sync.Mutex
	chestLogReqIndex = make(map[int16]chestLogRequestEntry)
)

const (
	chestLogReqTTL     = 30 * time.Second
	chestLogReqMaxSize = 256
)

// recordChestLogRequestFilter stashes (opID → filterValue). Called from
// decodeRequest when we see a REQUEST for opGetChestLogs.
func recordChestLogRequestFilter(opID int16, filterValue int) {
	chestLogReqMu.Lock()
	defer chestLogReqMu.Unlock()

	// Evict expired entries (cheap O(n) since the cap is small).
	cutoff := time.Now().Add(-chestLogReqTTL)
	for k, v := range chestLogReqIndex {
		if v.recordedAt.Before(cutoff) {
			delete(chestLogReqIndex, k)
		}
	}
	// Enforce hard size cap as a belt-and-suspenders guard.
	if len(chestLogReqIndex) >= chestLogReqMaxSize {
		// Remove the oldest half. Not a priority queue — brute force is fine
		// at this size and only fires in pathological cases.
		var oldest time.Time
		for _, v := range chestLogReqIndex {
			if oldest.IsZero() || v.recordedAt.Before(oldest) {
				oldest = v.recordedAt
			}
		}
		midpoint := oldest.Add(chestLogReqTTL / 2)
		for k, v := range chestLogReqIndex {
			if v.recordedAt.Before(midpoint) {
				delete(chestLogReqIndex, k)
			}
		}
	}

	chestLogReqIndex[opID] = chestLogRequestEntry{
		filterValue: filterValue,
		recordedAt:  time.Now(),
	}
	log.Debugf("[ChestLog] Stashed request opID=%d filterValue=%d", opID, filterValue)
}

// resolveChestLogAction returns (filterValue, actionTag) for a response opID.
// filterValue is -1 when no matching request was recorded. actionTag is a
// human-readable label derived from the filter value — our current best guess
// based on the first mixed capture (2026-04-20):
//
//	1   → "withdraw"  (conjectured; fires first when Log tab opens)
//	28  → "deposit"   (conjectured; fires after withdraw filter)
//	other/unknown → "filter_<n>"
//
// Both guesses will get verified once we have a capture where the human
// operator wrote down which filter they toggled. Until then, the raw
// filter_value column in the TSV lets us flip the mapping without another code
// round-trip.
func resolveChestLogAction(opID int16) (int, string) {
	chestLogReqMu.Lock()
	defer chestLogReqMu.Unlock()
	entry, ok := chestLogReqIndex[opID]
	if !ok {
		return -1, "unpaired"
	}
	delete(chestLogReqIndex, opID) // consume — responses are one-to-one with requests
	switch entry.filterValue {
	case 1:
		return entry.filterValue, "withdraw"
	case 28:
		return entry.filterValue, "deposit"
	default:
		return entry.filterValue, "filter_unknown"
	}
}

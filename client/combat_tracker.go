package client

import (
	"encoding/hex"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// ============================================================================
// COMBAT TRACKER — Phase 1 of the damage meter (albion_market_analyzer repo,
// DEEP_RESEARCH_2026-07-09_CRAFTING_DPS.md Part C).
//
// Aggregates HealthUpdate traffic into per-encounter summaries (per-player
// damage / healing / damage-taken totals + 1-second buckets) and relays ONE
// message per finished encounter to the VPS ("combat-encounter").
//
// Live event codes + param layouts confirmed by the 2026-07-09 Phase 0
// capture (see combat_diag.go header):
//
//	6   HealthUpdate        p0 target, p2 change (NEG=damage POS=heal),
//	                        p3 newHP, p6 causer, p7 spellIdx (-1 = none/regen)
//	7   HealthUpdates       batch on ONE target: p2/p6/p7 parallel arrays
//	82  UpdateFame          p2 fame gained ×10⁴ (premium-inclusive)
//	231 PartyJoined         p0 partyId, p8 flat 16-byte member GUIDs,
//	                        p9 member names, p16 online flags
//	232 PartyDisbanded      p1 partyId
//	233 PartyPlayerJoined   p1 GUID, p2 name (defensive parse)
//	235 PartyPlayerLeft     p1 GUID
//	278 InCombatStateUpdate p0 objId, p1/p2 combat bools (omitted when false)
//
// Attribution: causer/target objId → player name via the objectIDToName
// registry (evNewCharacter) + own objId/name from opJoin (CombatSetSelf).
// Only "tracked" players are aggregated: self + current party members.
// ============================================================================

const (
	combatIdleTimeoutMs  = 8000    // finalize after this much silence
	combatOOCGraceMs     = 3000    // finalize sooner once self leaves combat
	combatMaxDurationSec = 1800    // hard split for marathon encounters
	combatMaxPlayers     = 12      // payload cap (party is ≤20, usually ≤10)
	combatMaxBuckets     = 900     // downsample payload beyond this many buckets
)

type combatBucket struct{ d, h, t float64 }

type combatPlayerAgg struct {
	damage  float64
	healing float64
	taken   float64
	buckets []combatBucket
}

// --- relay payload ---

type CombatEncounterPlayer struct {
	Name    string `json:"name"`
	Self    bool   `json:"self,omitempty"`
	Damage  int64  `json:"damage"`
	Healing int64  `json:"healing"`
	Taken   int64  `json:"taken"`
	// Parallel per-bucket series (ints, silver-free — raw HP values)
	D []int64 `json:"d"`
	H []int64 `json:"h"`
	T []int64 `json:"t"`
}

type CombatEncounter struct {
	StartedAt   int64                   `json:"startedAt"` // unix ms
	EndedAt     int64                   `json:"endedAt"`
	DurationSec int64                   `json:"durationSec"`
	BucketSec   int64                   `json:"bucketSec"` // seconds per bucket (≥1)
	Zone        string                  `json:"zone"`      // raw location id — frontend formats
	PartySize   int                     `json:"partySize"` // 0 = solo
	Fame        int64                   `json:"fame"`      // premium-inclusive, /10⁴ applied
	Players     []CombatEncounterPlayer `json:"players"`
}

// --- tracker state (package singleton) ---

type combatTracker struct {
	mu sync.Mutex

	// self identity (set from opJoin — objIds are per-zone)
	selfObjID int64
	selfName  string
	zone      string

	// party roster: name set + guid(hex)→name for 235 removals
	partyID    int64
	partyNames map[string]bool
	guidToName map[string]string

	// active encounter
	active         bool
	startMs        int64
	lastActivityMs int64
	oocSinceMs     int64 // 0 while self is in combat
	fameRaw        float64
	players        map[string]*combatPlayerAgg

	tickerOnce sync.Once
}

var cTracker = &combatTracker{
	partyNames: map[string]bool{},
	guidToName: map[string]string{},
	players:    map[string]*combatPlayerAgg{},
}

// --- tolerant param converters (V18 compact encodings vary the wire type
// with value magnitude: the same param arrives as int8/int16/int32/int64) ---

func cvInt64(v interface{}) (int64, bool) {
	switch x := v.(type) {
	case int8:
		return int64(x), true
	case int16:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case float32:
		return int64(x), true
	case float64:
		return int64(x), true
	}
	return 0, false
}

func cvFloat(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case float32:
		return float64(x), true
	case float64:
		return x, true
	case int8:
		return float64(x), true
	case int16:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

func cvInt64Slice(v interface{}) []int64 {
	switch x := v.(type) {
	case []int8:
		out := make([]int64, len(x))
		for i, e := range x {
			out[i] = int64(e)
		}
		return out
	case []int16:
		out := make([]int64, len(x))
		for i, e := range x {
			out[i] = int64(e)
		}
		return out
	case []int32:
		out := make([]int64, len(x))
		for i, e := range x {
			out[i] = int64(e)
		}
		return out
	case []int64:
		return x
	}
	return nil
}

func cvFloatSlice(v interface{}) []float64 {
	switch x := v.(type) {
	case []float32:
		out := make([]float64, len(x))
		for i, e := range x {
			out[i] = float64(e)
		}
		return out
	case []float64:
		return x
	}
	// some servers compact small change arrays into int slices
	if ints := cvInt64Slice(v); ints != nil {
		out := make([]float64, len(ints))
		for i, e := range ints {
			out[i] = float64(e)
		}
		return out
	}
	return nil
}

// CombatSetSelf records own objId/name/zone from opJoin. A zone change (or
// objId change) invalidates all object ids, so any active encounter is
// finalized first.
func CombatSetSelf(objID int64, name, zone string) {
	ct := cTracker
	ct.mu.Lock()
	var payload *CombatEncounter
	if ct.active && (zone != ct.zone || objID != ct.selfObjID) {
		payload = ct.finalizeLocked("zone-change")
	}
	ct.selfObjID = objID
	if name != "" {
		ct.selfName = name
	}
	ct.zone = zone
	ct.mu.Unlock()
	if payload != nil {
		SendCombatEncounter(payload)
	}
}

// --- name resolution / tracking ---

// resolveTrackedName maps an objId to a player name IF that player is
// tracked (self, or current party member). Empty string = not tracked.
func (ct *combatTracker) resolveTrackedNameLocked(objID int64) string {
	if objID <= 0 {
		return ""
	}
	if objID == ct.selfObjID {
		if ct.selfName == "" {
			return ""
		}
		return ct.selfName
	}
	v, ok := objectIDToName.Load(objID)
	if !ok {
		return ""
	}
	name, _ := v.(string)
	if name == "" {
		return ""
	}
	if len(ct.partyNames) > 0 && ct.partyNames[name] {
		return name
	}
	return ""
}

// --- core recording ---

func (ct *combatTracker) record(target, causer int64, change float64, spell int64, nowMs int64) {
	if change == 0 {
		return
	}
	ct.mu.Lock()

	causerName := ct.resolveTrackedNameLocked(causer)
	targetName := ct.resolveTrackedNameLocked(target)
	if causerName == "" && targetName == "" {
		ct.mu.Unlock()
		return // mob-vs-mob / untracked players
	}
	// Positive change with no spell = natural regen / food — not "healing done".
	if change > 0 && (spell < 0 || causerName == "") {
		ct.mu.Unlock()
		return
	}

	var payload *CombatEncounter
	if ct.active && (nowMs-ct.startMs)/1000 >= combatMaxDurationSec {
		payload = ct.finalizeLocked("max-duration")
	}
	if !ct.active {
		ct.active = true
		ct.startMs = nowMs
		ct.fameRaw = 0
		ct.oocSinceMs = 0
		ct.players = map[string]*combatPlayerAgg{}
	}
	ct.lastActivityMs = nowMs

	sec := int((nowMs - ct.startMs) / 1000)
	if sec < 0 {
		sec = 0
	}
	bump := func(name string, kind byte, amount float64) {
		p := ct.players[name]
		if p == nil {
			p = &combatPlayerAgg{}
			ct.players[name] = p
		}
		for len(p.buckets) <= sec {
			p.buckets = append(p.buckets, combatBucket{})
		}
		switch kind {
		case 'd':
			p.damage += amount
			p.buckets[sec].d += amount
		case 'h':
			p.healing += amount
			p.buckets[sec].h += amount
		case 't':
			p.taken += amount
			p.buckets[sec].t += amount
		}
	}

	if change < 0 {
		dmg := -change
		if causerName != "" {
			bump(causerName, 'd', dmg)
		}
		if targetName != "" {
			bump(targetName, 't', dmg)
		}
	} else {
		bump(causerName, 'h', change)
	}
	ct.mu.Unlock()

	if payload != nil {
		SendCombatEncounter(payload)
	}
	ct.ensureTicker()
}

// --- event handlers (called from decode.go on raw live codes) ---

func combatHandleHealthUpdate(params map[uint8]interface{}) {
	target, ok := cvInt64(params[0])
	if !ok {
		return
	}
	change, ok := cvFloat(params[2])
	if !ok {
		return
	}
	causer, _ := cvInt64(params[6])
	spell := int64(-1)
	if s, ok := cvInt64(params[7]); ok {
		spell = s
	}
	cTracker.record(target, causer, change, spell, time.Now().UnixMilli())
}

func combatHandleHealthUpdates(params map[uint8]interface{}) {
	target, ok := cvInt64(params[0])
	if !ok {
		return
	}
	changes := cvFloatSlice(params[2])
	if len(changes) == 0 {
		return
	}
	causers := cvInt64Slice(params[6])
	spells := cvInt64Slice(params[7])
	nowMs := time.Now().UnixMilli()
	for i, change := range changes {
		causer := int64(0)
		if i < len(causers) {
			causer = causers[i]
		}
		spell := int64(-1)
		if i < len(spells) {
			spell = spells[i]
		}
		cTracker.record(target, causer, change, spell, nowMs)
	}
}

func combatHandleUpdateFame(params map[uint8]interface{}) {
	gained, ok := cvFloat(params[2])
	if !ok || gained <= 0 {
		return
	}
	ct := cTracker
	ct.mu.Lock()
	if ct.active {
		ct.fameRaw += gained
	}
	ct.mu.Unlock()
}

func combatHandleInCombatState(params map[uint8]interface{}) {
	objID, ok := cvInt64(params[0])
	if !ok {
		return
	}
	ct := cTracker
	ct.mu.Lock()
	if objID != ct.selfObjID {
		ct.mu.Unlock()
		return
	}
	active, _ := params[1].(bool)
	passive, _ := params[2].(bool)
	if active || passive {
		ct.oocSinceMs = 0
	} else if ct.oocSinceMs == 0 {
		ct.oocSinceMs = time.Now().UnixMilli()
	}
	ct.mu.Unlock()
}

// guid chunks in 231 p8 are flat 16-byte segments parallel to the p9 names.
func combatHandlePartyJoined(params map[uint8]interface{}) {
	names, _ := params[9].([]string)
	if len(names) == 0 {
		return
	}
	partyID, _ := cvInt64(params[0])
	guidFlat := cvInt64Slice(params[8]) // []int8 → widened
	ct := cTracker
	ct.mu.Lock()
	ct.partyID = partyID
	ct.partyNames = map[string]bool{}
	ct.guidToName = map[string]string{}
	for i, n := range names {
		if n == "" {
			continue
		}
		ct.partyNames[n] = true
		if len(guidFlat) >= (i+1)*16 {
			b := make([]byte, 16)
			for j := 0; j < 16; j++ {
				b[j] = byte(guidFlat[i*16+j])
			}
			ct.guidToName[hex.EncodeToString(b)] = n
		}
	}
	size := len(ct.partyNames)
	ct.mu.Unlock()
	log.Infof("[Combat] Party roster: %d members %v", size, names)
}

func combatHandlePartyDisbanded(params map[uint8]interface{}) {
	ct := cTracker
	ct.mu.Lock()
	ct.partyID = 0
	ct.partyNames = map[string]bool{}
	ct.guidToName = map[string]string{}
	ct.mu.Unlock()
	log.Info("[Combat] Party disbanded")
}

func combatGuidHexFromParam(v interface{}) string {
	raw := cvInt64Slice(v)
	if len(raw) != 16 {
		return ""
	}
	b := make([]byte, 16)
	for i := 0; i < 16; i++ {
		b[i] = byte(raw[i])
	}
	return hex.EncodeToString(b)
}

func combatHandlePartyPlayerJoined(params map[uint8]interface{}) {
	// Expected p2 = name (SAT layout); scan a few slots defensively since
	// 233 was never observed live.
	name := ""
	for _, k := range []uint8{2, 1, 3, 4} {
		if s, ok := params[k].(string); ok && s != "" {
			name = s
			break
		}
	}
	if name == "" {
		return
	}
	guidHex := combatGuidHexFromParam(params[1])
	ct := cTracker
	ct.mu.Lock()
	ct.partyNames[name] = true
	if guidHex != "" {
		ct.guidToName[guidHex] = name
	}
	ct.mu.Unlock()
	log.Infof("[Combat] Party member joined: %s", name)
}

func combatHandlePartyPlayerLeft(params map[uint8]interface{}) {
	guidHex := combatGuidHexFromParam(params[1])
	if guidHex == "" {
		return
	}
	ct := cTracker
	ct.mu.Lock()
	if name, ok := ct.guidToName[guidHex]; ok {
		delete(ct.partyNames, name)
		delete(ct.guidToName, guidHex)
		log.Infof("[Combat] Party member left: %s", name)
	}
	ct.mu.Unlock()
}

// --- encounter finalization ---

// ensureTicker lazily starts the 1s lifecycle goroutine on first combat.
func (ct *combatTracker) ensureTicker() {
	ct.tickerOnce.Do(func() {
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for range t.C {
				nowMs := time.Now().UnixMilli()
				ct.mu.Lock()
				var payload *CombatEncounter
				if ct.active {
					idle := nowMs - ct.lastActivityMs
					if idle > combatIdleTimeoutMs ||
						(ct.oocSinceMs > 0 && nowMs-ct.oocSinceMs > combatOOCGraceMs && idle > combatOOCGraceMs) {
						payload = ct.finalizeLocked("idle")
					}
				}
				ct.mu.Unlock()
				if payload != nil {
					SendCombatEncounter(payload)
				}
			}
		}()
	})
}

// finalizeLocked closes the active encounter and returns the relay payload
// (nil when the encounter is noise). Caller must hold ct.mu and send the
// payload AFTER releasing the lock.
func (ct *combatTracker) finalizeLocked(reason string) *CombatEncounter {
	if !ct.active {
		return nil
	}
	ct.active = false
	players := ct.players
	ct.players = map[string]*combatPlayerAgg{}
	fame := int64(ct.fameRaw / 10000)
	ct.fameRaw = 0

	var dealt, taken, healed float64
	for _, p := range players {
		dealt += p.damage
		taken += p.taken
		healed += p.healing
	}
	// Noise gate: skip chip-damage blips (ran past a mob, single stray tick).
	if dealt < 500 && taken < 2000 && healed < 500 {
		log.Debugf("[Combat] Dropping noise encounter (%s): dealt=%.0f taken=%.0f healed=%.0f", reason, dealt, taken, healed)
		return nil
	}

	durSec := (ct.lastActivityMs-ct.startMs)/1000 + 1
	if durSec < 1 {
		durSec = 1
	}
	bucketSec := int64(1)
	nBuckets := durSec
	for nBuckets > combatMaxBuckets {
		bucketSec *= 2
		nBuckets = (durSec + bucketSec - 1) / bucketSec
	}

	enc := &CombatEncounter{
		StartedAt:   ct.startMs,
		EndedAt:     ct.lastActivityMs,
		DurationSec: durSec,
		BucketSec:   bucketSec,
		Zone:        ct.zone,
		PartySize:   len(ct.partyNames),
		Fame:        fame,
	}

	// deterministic order: damage desc
	names := make([]string, 0, len(players))
	for n := range players {
		names = append(names, n)
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if players[names[j]].damage > players[names[i]].damage {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	if len(names) > combatMaxPlayers {
		names = names[:combatMaxPlayers]
	}
	for _, n := range names {
		p := players[n]
		cp := CombatEncounterPlayer{
			Name:    n,
			Self:    n == ct.selfName,
			Damage:  int64(p.damage),
			Healing: int64(p.healing),
			Taken:   int64(p.taken),
			D:       make([]int64, nBuckets),
			H:       make([]int64, nBuckets),
			T:       make([]int64, nBuckets),
		}
		for s, b := range p.buckets {
			bi := int64(s) / bucketSec
			if bi >= nBuckets {
				bi = nBuckets - 1
			}
			cp.D[bi] += int64(b.d)
			cp.H[bi] += int64(b.h)
			cp.T[bi] += int64(b.t)
		}
		enc.Players = append(enc.Players, cp)
	}

	log.Infof("[Combat] Encounter finished (%s): %ds, %d players, dealt=%.0f taken=%.0f healed=%.0f fame=%d",
		reason, durSec, len(enc.Players), dealt, taken, healed, fame)
	return enc
}

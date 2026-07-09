package client

import (
	"testing"
)

func newTestTracker() *combatTracker {
	return &combatTracker{
		selfObjID:  9215,
		selfName:   "Coldtouch",
		zone:       "5100",
		partyNames: map[string]bool{"Coldtouch": true, "goblin1": true},
		guidToName: map[string]string{},
		players:    map[string]*combatPlayerAgg{},
	}
}

func TestCombatTrackerAggregation(t *testing.T) {
	ct := newTestTracker()
	objectIDToName.Store(int64(3001), "goblin1")
	objectIDToName.Store(int64(4001), "RandomStranger") // not in party — untracked
	defer objectIDToName.Delete(int64(3001))
	defer objectIDToName.Delete(int64(4001))

	base := int64(1_000_000)
	// self hits mob 7992 for 172 + 591
	ct.record(7992, 9215, -172, 3190, base)
	ct.record(7992, 9215, -591, 3223, base+1200)
	// party member hits mob for 300 at t=+2s
	ct.record(7992, 3001, -300, 100, base+2100)
	// mob hits self for 57 (taken)
	ct.record(9215, 7992, -57, -1, base+2500)
	// party member heals self for 88 with a real spell
	ct.record(9215, 3001, 88, 555, base+3000)
	// natural regen (no spell) must NOT count as healing
	ct.record(9215, 9215, 40, -1, base+3200)
	// stranger's damage must not be tracked
	ct.record(7992, 4001, -9999, 42, base+3300)
	// mob-vs-mob noise
	ct.record(7993, 7992, -500, 7, base+3400)

	ct.mu.Lock()
	enc := ct.finalizeLocked("test")
	ct.mu.Unlock()
	if enc == nil {
		t.Fatal("expected an encounter payload, got nil")
	}
	if len(enc.Players) != 2 {
		t.Fatalf("expected 2 tracked players, got %d: %+v", len(enc.Players), enc.Players)
	}
	// sorted by damage desc: self 763, goblin1 300
	if enc.Players[0].Name != "Coldtouch" || enc.Players[0].Damage != 763 {
		t.Errorf("player[0] = %+v, want Coldtouch damage 763", enc.Players[0])
	}
	if !enc.Players[0].Self {
		t.Error("self flag missing on Coldtouch")
	}
	if enc.Players[0].Taken != 57 {
		t.Errorf("Coldtouch taken = %d, want 57", enc.Players[0].Taken)
	}
	if enc.Players[1].Name != "goblin1" || enc.Players[1].Damage != 300 || enc.Players[1].Healing != 88 {
		t.Errorf("player[1] = %+v, want goblin1 damage 300 healing 88", enc.Players[1])
	}
	if enc.PartySize != 2 {
		t.Errorf("partySize = %d, want 2", enc.PartySize)
	}
	if enc.DurationSec < 3 || enc.DurationSec > 5 {
		t.Errorf("durationSec = %d, want ~4", enc.DurationSec)
	}
	if enc.BucketSec != 1 {
		t.Errorf("bucketSec = %d, want 1", enc.BucketSec)
	}
	// bucket series sums must equal totals
	var dSum int64
	for _, v := range enc.Players[0].D {
		dSum += v
	}
	if dSum != enc.Players[0].Damage {
		t.Errorf("bucket damage sum %d != total %d", dSum, enc.Players[0].Damage)
	}
}

func TestCombatTrackerNoiseGate(t *testing.T) {
	ct := newTestTracker()
	// one chip hit while running past a mob — below all thresholds
	ct.record(9215, 7992, -34, -1, 1_000_000)
	ct.mu.Lock()
	enc := ct.finalizeLocked("test")
	ct.mu.Unlock()
	if enc != nil {
		t.Fatalf("expected noise gate to drop encounter, got %+v", enc)
	}
	if ct.active {
		t.Error("tracker should be inactive after finalize")
	}
}

func TestCombatTrackerSoloTracksSelfOnly(t *testing.T) {
	ct := newTestTracker()
	ct.partyNames = map[string]bool{} // solo
	objectIDToName.Store(int64(3001), "goblin1")
	defer objectIDToName.Delete(int64(3001))

	ct.record(7992, 9215, -1000, 3190, 2_000_000)
	ct.record(7992, 3001, -5000, 100, 2_000_500) // nearby non-party player
	ct.mu.Lock()
	enc := ct.finalizeLocked("test")
	ct.mu.Unlock()
	if enc == nil {
		t.Fatal("expected payload")
	}
	if len(enc.Players) != 1 || enc.Players[0].Name != "Coldtouch" {
		t.Fatalf("solo mode should track self only, got %+v", enc.Players)
	}
	if enc.PartySize != 0 {
		t.Errorf("partySize = %d, want 0 (solo)", enc.PartySize)
	}
}

func TestCombatBucketDownsample(t *testing.T) {
	ct := newTestTracker()
	base := int64(3_000_000)
	// 1200s encounter → bucketSec must become 2, buckets 600
	ct.record(7992, 9215, -1000, 3190, base)
	ct.record(7992, 9215, -1000, 3190, base+1_199_000)
	ct.mu.Lock()
	enc := ct.finalizeLocked("test")
	ct.mu.Unlock()
	if enc == nil {
		t.Fatal("expected payload")
	}
	if enc.BucketSec != 2 {
		t.Errorf("bucketSec = %d, want 2", enc.BucketSec)
	}
	if len(enc.Players[0].D) != 600 {
		t.Errorf("bucket count = %d, want 600", len(enc.Players[0].D))
	}
	var sum int64
	for _, v := range enc.Players[0].D {
		sum += v
	}
	if sum != 2000 {
		t.Errorf("downsampled damage sum = %d, want 2000", sum)
	}
}

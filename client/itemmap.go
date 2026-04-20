package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ao-data/albiondata-client/log"
)

var itemNameMap map[string]string
var itemWeightMap map[string]float64

// specialItemNames maps negative/internal numeric IDs to human-readable names.
// These IDs are assigned by the Photon protocol at runtime and are not in ao-bin-dumps.
var specialItemNames = map[int]string{
	-1: "SILVER",
	-2: "GOLD",
	-3: "FAME_CREDIT",
	-4: "FAME_CREDIT_PREMIUM",
	-5: "FACTION_TOKEN",
	-6: "SILVER_POUCH",
	-7: "GOLD_POUCH",
	-8: "TOME_OF_INSIGHT",
	-9: "SEASONAL_TOKEN",
}

// IsSpecialItem returns true for internal/currency items that aren't tradable on the market.
// All negative numeric IDs are internal system items (silver, gold, fame, tokens, essences, etc.)
func IsSpecialItem(numericID int) bool {
	return numericID < 0
}

// IsNonTradeableItem returns true for account-bound cosmetic/unlock items that the game
// sometimes surfaces in a chest tab's slot map (even though they aren't physically in the
// chest). These have no market value, so we filter them out of Loot Buyer captures.
//
// Patterns (all confirmed non-tradeable via ao-bin-dumps):
//   UNIQUE_UNLOCK_*  — one-time skin unlock tokens (mount skins, character avatars, etc.)
//   SKIN_*           — cosmetic mount/character skin variants
//   *_TELLAFRIEND    — recruiter rewards (account-bound by design)
//   UNIQUE_AVATAR*   — character portrait avatars
//   UNIQUE_AVATARRING* — character portrait rings
//   UNIQUE_HIDEOUT*  — hideout-bound infrastructure tokens
//   UNKNOWN_*        — IDs our itemmap couldn't resolve (no market data possible)
func IsNonTradeableItem(itemName string) bool {
	if itemName == "" {
		return false
	}
	if strings.HasPrefix(itemName, "UNIQUE_UNLOCK_") {
		return true
	}
	if strings.HasPrefix(itemName, "SKIN_") {
		return true
	}
	if strings.HasPrefix(itemName, "UNIQUE_AVATAR") {
		return true
	}
	if strings.HasPrefix(itemName, "UNIQUE_HIDEOUT") {
		return true
	}
	if strings.HasPrefix(itemName, "UNKNOWN_") {
		return true
	}
	if strings.Contains(itemName, "_TELLAFRIEND") {
		return true
	}
	return false
}

func init() {
	itemNameMap = make(map[string]string)
	itemWeightMap = make(map[string]float64)
}

// LoadItemMap loads the numeric ID to string name mapping from itemmap.json
func LoadItemMap() {
	// Try multiple locations
	paths := []string{
		"itemmap.json",
		filepath.Join(filepath.Dir(os.Args[0]), "itemmap.json"),
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		err = json.Unmarshal(data, &itemNameMap)
		if err != nil {
			log.Errorf("[ItemMap] Failed to parse %s: %v", path, err)
			continue
		}

		log.Infof("[ItemMap] Loaded %d item mappings from %s", len(itemNameMap), path)
		return
	}

	log.Warn("[ItemMap] itemmap.json not found — item names will show as numeric IDs. Download from ao-bin-dumps.")
}

// LoadWeightMap loads the numeric ID to weight mapping from weightmap.json
func LoadWeightMap() {
	paths := []string{
		"weightmap.json",
		filepath.Join(filepath.Dir(os.Args[0]), "weightmap.json"),
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		err = json.Unmarshal(data, &itemWeightMap)
		if err != nil {
			log.Errorf("[WeightMap] Failed to parse %s: %v", path, err)
			continue
		}

		log.Infof("[WeightMap] Loaded %d weight mappings from %s", len(itemWeightMap), path)
		return
	}

	log.Warn("[WeightMap] weightmap.json not found — item weights will be 0.")
}

// resolveItemWeight returns the weight in kg for a numeric item type ID. Returns 0 if unknown.
func resolveItemWeight(numericID int) float64 {
	if w, ok := itemWeightMap[strconv.Itoa(numericID)]; ok {
		return w
	}
	return 0
}

// resolveItemName converts a numeric item type ID to a string name like "T8_2H_NATURESTAFF@3"
func resolveItemName(numericID int) string {
	// Check special/internal items first (negative IDs)
	if name, ok := specialItemNames[numericID]; ok {
		return name
	}
	if name, ok := itemNameMap[strconv.Itoa(numericID)]; ok {
		return name
	}
	return fmt.Sprintf("UNKNOWN_%d", numericID)
}

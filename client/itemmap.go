package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ao-data/albiondata-client/log"
)

var itemNameMap map[string]string

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

func init() {
	itemNameMap = make(map[string]string)
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

// resolveItemName converts a numeric item type ID to a string name like "T8_2H_NATURESTAFF@3"
func resolveItemName(numericID int) string {
	// Check special/internal items first (negative IDs)
	if name, ok := specialItemNames[numericID]; ok {
		return name
	}
	key := fmt.Sprintf("%d", numericID)
	if name, ok := itemNameMap[key]; ok {
		return name
	}
	return fmt.Sprintf("UNKNOWN_%d", numericID)
}

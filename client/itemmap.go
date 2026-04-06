package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ao-data/albiondata-client/log"
)

var itemNameMap map[string]string

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
	key := fmt.Sprintf("%d", numericID)
	if name, ok := itemNameMap[key]; ok {
		return name
	}
	return fmt.Sprintf("UNKNOWN_%d", numericID)
}

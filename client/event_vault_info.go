package client

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/ao-data/albiondata-client/log"
)

// VaultTab represents a single tab in a chest/vault
type VaultTab struct {
	GUID string `json:"guid"`
	Name string `json:"name"`
	Icon string `json:"icon"`
}

// VaultInfo stores the tab structure for the current open vault
type VaultInfo struct {
	ObjectID   int64      `json:"objectId"`
	Location   string     `json:"location"`
	Tabs       []VaultTab `json:"tabs"`
	IsGuild    bool       `json:"isGuild"`
	ReceivedAt time.Time  `json:"-"` // When this vault info was received
}

// Current vault info — updated when vault events fire
var currentVaultInfo *VaultInfo

// eventGuildVaultInfo fires when approaching/opening a guild chest
type eventGuildVaultInfo struct {
	RawParams map[string]interface{} `mapstructure:",remain"`
}

func (event eventGuildVaultInfo) Process(state *albionState) {
	vi := parseVaultInfo(event.RawParams, true)
	if vi != nil {
		vi.ReceivedAt = time.Now()
		currentVaultInfo = vi
		log.Infof("[GuildVault] %d tabs detected: %v", len(vi.Tabs), tabNames(vi.Tabs))
	}
}

// eventBankVaultInfo fires when approaching/opening a personal bank vault
type eventBankVaultInfo struct {
	RawParams map[string]interface{} `mapstructure:",remain"`
}

func (event eventBankVaultInfo) Process(state *albionState) {
	vi := parseVaultInfo(event.RawParams, false)
	if vi != nil {
		vi.ReceivedAt = time.Now()
		currentVaultInfo = vi
		log.Infof("[BankVault] %d tabs detected: %v", len(vi.Tabs), tabNames(vi.Tabs))
	}
}

func parseVaultInfo(params map[string]interface{}, isGuild bool) *VaultInfo {
	vi := &VaultInfo{IsGuild: isGuild}

	// param 0 = object ID
	if v, ok := params["0"]; ok {
		switch val := v.(type) {
		case int64:
			vi.ObjectID = val
		case int32:
			vi.ObjectID = int64(val)
		case int16:
			vi.ObjectID = int64(val)
		case int8:
			vi.ObjectID = int64(val)
		}
	}

	// param 1 = location GUID string
	if v, ok := params["1"]; ok {
		if s, ok := v.(string); ok {
			vi.Location = s
		}
	}

	// param 2 = array of vault GUIDs ([][]int8, each 16 bytes)
	var guids []string
	if v, ok := params["2"]; ok {
		guids = extractGUIDArray(v)
	}

	// param 3 = array of vault names ([]string)
	var names []string
	if v, ok := params["3"]; ok {
		if arr, ok := v.([]interface{}); ok {
			for _, item := range arr {
				if s, ok := item.(string); ok {
					names = append(names, s)
				}
			}
		}
		// Also try string array directly
		if arr, ok := v.([]string); ok {
			names = arr
		}
	}

	// param 4 = array of icon tags ([]string)
	var icons []string
	if v, ok := params["4"]; ok {
		if arr, ok := v.([]interface{}); ok {
			for _, item := range arr {
				if s, ok := item.(string); ok {
					icons = append(icons, s)
				}
			}
		}
	}

	// Build tabs
	maxLen := len(guids)
	if len(names) > maxLen {
		maxLen = len(names)
	}
	for i := 0; i < maxLen; i++ {
		tab := VaultTab{}
		if i < len(guids) {
			tab.GUID = guids[i]
		}
		if i < len(names) {
			tab.Name = names[i]
		}
		if i < len(icons) {
			tab.Icon = icons[i]
		}
		vi.Tabs = append(vi.Tabs, tab)
	}

	if len(vi.Tabs) == 0 {
		return nil
	}
	return vi
}

func extractGUIDArray(v interface{}) []string {
	var guids []string
	switch arr := v.(type) {
	case []interface{}:
		for _, item := range arr {
			if byteArr, ok := item.([]int8); ok {
				b := make([]byte, len(byteArr))
				for i, v := range byteArr {
					b[i] = byte(v)
				}
				guids = append(guids, hex.EncodeToString(b))
			}
		}
	}
	return guids
}

func tabNames(tabs []VaultTab) []string {
	var names []string
	for _, t := range tabs {
		name := t.Name
		if name == "" {
			name = fmt.Sprintf("Tab %d", len(names)+1)
		}
		names = append(names, name)
	}
	return names
}

// GetCurrentVaultTabs returns the vault tab info if it arrived recently (within 10 seconds).
// Clears the vault info after returning it to prevent stale data on the next capture.
func GetCurrentVaultTabs() *VaultInfo {
	vi := currentVaultInfo
	if vi == nil {
		return nil
	}
	// Only use vault info if it arrived within 30 seconds
	// (vault event fires on approach, items can take 5-15s to fully load + 2s finalize timer)
	if time.Since(vi.ReceivedAt) > 30*time.Second {
		log.Debug("[VaultInfo] Stale vault info (>30s old), ignoring")
		currentVaultInfo = nil
		return nil
	}
	// Clear after use so it doesn't attach to the next unrelated chest
	currentVaultInfo = nil
	return vi
}

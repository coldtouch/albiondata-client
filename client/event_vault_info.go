package client

import (
	"encoding/hex"
	"fmt"
	"sync"
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

// Separate vault info for guild and bank — both can fire simultaneously
var vaultMu sync.RWMutex
var currentGuildVaultInfo *VaultInfo
var currentBankVaultInfo *VaultInfo

// eventGuildVaultInfo fires when approaching/opening a guild chest
type eventGuildVaultInfo struct {
	RawParams map[string]interface{} `mapstructure:",remain"`
}

func (event eventGuildVaultInfo) Process(state *albionState) {
	vi := parseVaultInfo(event.RawParams, true)
	if vi != nil {
		vi.ReceivedAt = time.Now()
		vaultMu.Lock()
		currentGuildVaultInfo = vi
		vaultMu.Unlock()
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
		vaultMu.Lock()
		currentBankVaultInfo = vi
		vaultMu.Unlock()
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
		log.Infof("[VaultInfo] param 2 type: %T", v)
		guids = extractGUIDArray(v)
	} else {
		log.Info("[VaultInfo] param 2 not found in vault event")
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
			guid := extractSingleGUID(item)
			if guid != "" {
				guids = append(guids, guid)
			}
		}
	case [][]int8:
		for _, byteArr := range arr {
			b := make([]byte, len(byteArr))
			for i, v := range byteArr {
				b[i] = byte(v)
			}
			guids = append(guids, hex.EncodeToString(b))
		}
	}
	if len(guids) == 0 {
		log.Infof("[VaultInfo] extractGUIDArray: no GUIDs parsed from type %T", v)
	} else {
		log.Infof("[VaultInfo] extractGUIDArray: parsed %d GUIDs", len(guids))
	}
	return guids
}

func extractSingleGUID(item interface{}) string {
	switch v := item.(type) {
	case []int8:
		b := make([]byte, len(v))
		for i, x := range v {
			b[i] = byte(x)
		}
		return hex.EncodeToString(b)
	case []byte:
		return hex.EncodeToString(v)
	case string:
		return v
	default:
		log.Infof("[VaultInfo] extractSingleGUID: unknown type %T for GUID item", item)
		return ""
	}
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

// matchContainerToVaultTab checks if a container GUID matches any known vault tab GUID.
// Returns (tabName, tabIndex) if matched, ("", -1) if not.
func matchContainerToVaultTab(containerGUID string) (string, int) {
	if containerGUID == "" {
		return "", -1
	}
	// Check guild vault tabs
	if currentGuildVaultInfo != nil {
		for i, tab := range currentGuildVaultInfo.Tabs {
			if tab.GUID != "" && tab.GUID == containerGUID {
				return tab.Name, i
			}
		}
	}
	// Check bank vault tabs
	if currentBankVaultInfo != nil {
		for i, tab := range currentBankVaultInfo.Tabs {
			if tab.GUID != "" && tab.GUID == containerGUID {
				return tab.Name, i
			}
		}
	}
	return "", -1
}

// GetCurrentVaultTabs returns the combined vault tab info.
// Guild island chests fire BOTH GuildVaultInfo (guild tabs) and BankVaultInfo (personal bank tab).
// We merge them so all tabs (guild + personal) are in one list for GUID matching.
func GetCurrentVaultTabs() *VaultInfo {
	vaultMu.RLock()
	defer vaultMu.RUnlock()
	guildFresh := currentGuildVaultInfo != nil
	bankFresh := currentBankVaultInfo != nil

	if guildFresh && bankFresh {
		// Merge: guild tabs + bank tabs into one combined VaultInfo
		merged := &VaultInfo{
			ObjectID: currentGuildVaultInfo.ObjectID,
			Location: currentGuildVaultInfo.Location,
			IsGuild:  true,
			Tabs:     make([]VaultTab, 0, len(currentGuildVaultInfo.Tabs)+len(currentBankVaultInfo.Tabs)),
		}
		merged.Tabs = append(merged.Tabs, currentGuildVaultInfo.Tabs...)
		// Append bank tabs with friendly name for the default bank tab
		for _, tab := range currentBankVaultInfo.Tabs {
			bankTab := tab
			if bankTab.Name == "@BUILDINGS_T1_BANK" || bankTab.Name == "" {
				bankTab.Name = "Bank"
			}
			merged.Tabs = append(merged.Tabs, bankTab)
		}
		log.Infof("[VaultInfo] Using merged vault info with %d tabs (%d guild + %d bank)",
			len(merged.Tabs), len(currentGuildVaultInfo.Tabs), len(currentBankVaultInfo.Tabs))
		return merged
	} else if guildFresh {
		log.Infof("[VaultInfo] Using guild vault info with %d tabs", len(currentGuildVaultInfo.Tabs))
		return currentGuildVaultInfo
	} else if bankFresh {
		// Rename default bank tab
		for i, tab := range currentBankVaultInfo.Tabs {
			if tab.Name == "@BUILDINGS_T1_BANK" || tab.Name == "" {
				currentBankVaultInfo.Tabs[i].Name = "Bank"
			}
		}
		log.Infof("[VaultInfo] Using bank vault info with %d tabs", len(currentBankVaultInfo.Tabs))
		return currentBankVaultInfo
	}

	return nil
}

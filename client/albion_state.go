package client

import (
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ao-data/albiondata-client/lib"
	"github.com/ao-data/albiondata-client/log"
	"github.com/ao-data/albiondata-client/notification"
)

// CacheSize limit size of messages in cache
const CacheSize = 8192

type marketHistoryInfo struct {
	albionId  int32
	timescale lib.Timescale
	quality   uint8
}

type albionState struct {
	mu sync.RWMutex

	LocationId           string
	LocationString       string
	CurrentZone          string
	CharacterId          lib.CharacterID
	CharacterName        string
	GameServerIP         string
	AODataServerID       int
	AODataIngestBaseURL  string
	WaitingForMarketData bool
	BanditEventLastTimeSubmitted time.Time

	// A lot of information is sent out but not contained in the response when requesting marketHistory (e.g. ID)
	// This information is stored in marketHistoryInfo
	// This array acts as a type of cache for that info
	// The index is the message number (param255) % CacheSize
	marketHistoryIDLookup [CacheSize]marketHistoryInfo
	// TODO could this be improved?!
}

// --- Thread-safe getters (use RLock) ---

func (state *albionState) GetLocationId() string {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.LocationId
}

func (state *albionState) GetCharacterName() string {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.CharacterName
}

func (state *albionState) GetCharacterId() lib.CharacterID {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.CharacterId
}

func (state *albionState) GetGameServerIP() string {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.GameServerIP
}

func (state *albionState) GetWaitingForMarketData() bool {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.WaitingForMarketData
}

func (state *albionState) GetAODataServerID() int {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.AODataServerID
}

// GetAODataIngestBaseURL resolves the region-specific AODP upload endpoint for
// the market/map data path. It delegates to GetServer() so VPN/ExitLag users
// inherit the SAME fallback chain the rest of the client uses: -force-server >
// live IP detection > server persisted from a prior non-VPN session. Reading
// the raw AODataIngestBaseURL field directly (as this used to) returned "" for
// anyone behind a tunnel — the source IP never matches an Albion range, so
// SetServerFromIP never populates the field — which silently broke market/map
// uploads even when -force-server was set. For a normal (non-VPN) user
// GetServer() returns exactly the same URL, so this is behaviour-preserving.
func (state *albionState) GetAODataIngestBaseURL() string {
	_, url := state.GetServer()
	return url
}

func (state *albionState) GetBanditEventLastTimeSubmitted() time.Time {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.BanditEventLastTimeSubmitted
}

// --- Thread-safe setters (use Lock) ---

func (state *albionState) GetCurrentZone() string {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.CurrentZone
}

func (state *albionState) SetCurrentZone(v string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.CurrentZone = v
}

func (state *albionState) SetLocationId(v string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.LocationId = v
}

func (state *albionState) SetCharacterName(v string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.CharacterName = v
}

func (state *albionState) SetCharacterId(v lib.CharacterID) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.CharacterId = v
}

func (state *albionState) SetGameServerIP(v string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.GameServerIP = v
}

func (state *albionState) SetWaitingForMarketData(v bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.WaitingForMarketData = v
}

func (state *albionState) SetAODataServerID(v int) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.AODataServerID = v
}

func (state *albionState) SetAODataIngestBaseURL(v string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.AODataIngestBaseURL = v
}

func (state *albionState) SetBanditEventLastTimeSubmitted(v time.Time) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.BanditEventLastTimeSubmitted = v
}

// SetServerFromIP sets GameServerIP and derives AODataServerID and AODataIngestBaseURL
// in a single lock acquisition (GO-H3). If the IP does not match a known Albion server
// range the existing AODataServerID/URL are left unchanged.
func (state *albionState) SetServerFromIP(ip string) {
	state.mu.Lock()

	state.GameServerIP = ip

	matchedID := 0
	matchedURL := ""
	switch {
	case strings.HasPrefix(ip, "5.188.125."):
		matchedID, matchedURL = 1, "https+pow://pow.west.albion-online-data.com"
		log.Tracef("SetServerFromIP: west server (ip: %v)", ip)
	case strings.HasPrefix(ip, "5.45.187."):
		matchedID, matchedURL = 2, "https+pow://pow.east.albion-online-data.com"
		log.Tracef("SetServerFromIP: east server (ip: %v)", ip)
	case strings.HasPrefix(ip, "193.169.238."):
		matchedID, matchedURL = 3, "https+pow://pow.europe.albion-online-data.com"
		log.Tracef("SetServerFromIP: EU server (ip: %v)", ip)
	}
	if matchedID != 0 {
		state.AODataServerID = matchedID
		state.AODataIngestBaseURL = matchedURL
	}
	state.mu.Unlock()

	// Remember positively-identified servers so future VPN sessions (relay
	// IPs, detection impossible) inherit the right server automatically.
	if matchedID != 0 {
		go persistDetectedServer(matchedID, matchedURL)
	}
}

// onlyDigitsRe is package-level so we don't recompile the regex on every
// IsValidLocation call (fires per market query — frequent during browsing).
var onlyDigitsRe = regexp.MustCompile(`^[0-9]+$`)

func (state *albionState) IsValidLocation() bool {
	state.mu.RLock()
	locId := state.LocationId
	state.mu.RUnlock()

	switch {
	case locId == "":
		log.Error("The players location has not yet been set. Please transition zones so the location can be identified.")
		if !ConfigGlobal.Debug {
			notification.Push("The players location has not yet been set. Please transition zones so the location can be identified.")
		}
		return false

	case onlyDigitsRe.MatchString(locId):
		return true
	case strings.HasPrefix(locId, "BLACKBANK-"):
		return true
	case strings.HasSuffix(locId, "-HellDen"):
		return true
	case strings.HasSuffix(locId, "-Auction2"):
		return true
	default:
		log.Error("The players location is not valid. Please transition zones so the location can be fixed.")
		if !ConfigGlobal.Debug {
			notification.Push("The players location is not valid. Please transition zones so the location can be fixed.")
		}
		return false
	}
}

// forcedServer maps the -force-server flag to (serverID, ingestBaseURL).
// Zero value means "not forced".
func forcedServer() (int, string) {
	switch strings.ToLower(strings.TrimSpace(ConfigGlobal.ForceServer)) {
	case "west", "america", "americas", "1":
		return 1, "https+pow://pow.west.albion-online-data.com"
	case "east", "asia", "2":
		return 2, "https+pow://pow.east.albion-online-data.com"
	case "europe", "eu", "3":
		return 3, "https+pow://pow.europe.albion-online-data.com"
	}
	return 0, ""
}

func (state *albionState) GetServer() (int, string) {
	// VPN/ExitLag: packets arrive from relay IPs, so IP-based detection never
	// matches an Albion range. -force-server pins the answer explicitly.
	if fid, furl := forcedServer(); fid != 0 {
		return fid, furl
	}

	state.mu.RLock()
	currentServerID := state.AODataServerID
	currentBaseURL := state.AODataIngestBaseURL
	gameIP := state.GameServerIP
	state.mu.RUnlock()

	// default to 0
	var serverID = 0
	var aoDataIngestBaseURL = ""

	// if we happen to have a server id stored in state, lets re-default to that
	if currentServerID != 0 {
		serverID = currentServerID
	}
	if currentBaseURL != "" {
		aoDataIngestBaseURL = currentBaseURL
	}

	// we get packets from other than game servers, so determine if it's a game server
	// based on soruce ip and if its east/west servers
	var isAlbionIP = false
	if strings.HasPrefix(gameIP, "5.188.125.") {
		// west server class c ip range
		serverID = 1
		isAlbionIP = true
		aoDataIngestBaseURL = "https+pow://pow.west.albion-online-data.com"
	} else if strings.HasPrefix(gameIP, "5.45.187.") {
		// east server class c ip range
		isAlbionIP = true
		serverID = 2
		aoDataIngestBaseURL = "https+pow://pow.east.albion-online-data.com"
	} else if strings.HasPrefix(gameIP, "193.169.238.") {
		// eu server class c ip range
		isAlbionIP = true
		serverID = 3
		aoDataIngestBaseURL = "https+pow://pow.europe.albion-online-data.com"
	}

	// if this was a known albion online server ip, then let's log it
	if isAlbionIP {
		log.Tracef("Returning Server ID %v (ip src: %v)", serverID, gameIP)
		log.Tracef("Returning AODataIngestBaseURL %v (ip src: %v)", aoDataIngestBaseURL, gameIP)
	}

	// VPN fallback: detection produced nothing (relay IPs never match an
	// Albion range) — reuse the server persisted from the last non-VPN
	// session. -force-server (handled above) always wins over this.
	if serverID == 0 {
		if pid, purl := loadPersistedServer(); pid != 0 {
			logServerFallbackOnce(pid)
			return pid, purl
		}
	}

	return serverID, aoDataIngestBaseURL
}

var _serverFallbackLogged atomic.Bool

func logServerFallbackOnce(id int) {
	if _serverFallbackLogged.CompareAndSwap(false, true) {
		names := map[int]string{1: "west", 2: "east", 3: "europe"}
		log.Infof("[VPN-Auto] Game server not detectable from packet IPs (VPN/tunnel?) — using last known server: %s. Override with -force-server.", names[id])
	}
}

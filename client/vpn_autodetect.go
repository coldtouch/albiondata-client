package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/ao-data/albiondata-client/log"
)

// ============================================================================
// VPN/ExitLag auto-detection — zero-config version of run-vpn-mode.bat.
//
// Two independent mechanisms:
//
//  1. Adapter escalation (see albion_watcher.go autoEscalateForVPN): listeners
//     start on physical adapters as always; if NO Photon game traffic decodes
//     within vpnEscalateAfterSec, capture silently widens to every up adapter
//     (tunnel/TAP/virtual included). Safe against double-capture by
//     construction: escalation only fires when the physical path is provably
//     silent, and the BPF port-5056 filter never matches the encrypted tunnel
//     side on the physical NIC.
//
//  2. Server persistence (this file): whenever a REAL Albion source IP is
//     detected (any non-VPN session), the server id is written to
//     logs/server-state.json. When IP detection can't work (VPN relay IPs),
//     GetServer falls back to the persisted value automatically.
//     Precedence: -force-server flag > live IP detection > persisted state.
// ============================================================================

// photonPacketsSeen counts decoded Photon-layer packets across all listeners.
var photonPacketsSeen atomic.Uint64

func notePhotonTraffic() { photonPacketsSeen.Add(1) }

type serverState struct {
	ServerID      int    `json:"serverId"`
	IngestBaseURL string `json:"ingestBaseUrl"`
}

var (
	persistedServerOnce sync.Once
	persistedServer     serverState
	lastPersistedID     atomic.Int64
)

func serverStatePath() string {
	return filepath.Join(LogsDir(), "server-state.json")
}

// persistDetectedServer records a positively-identified game server so future
// VPN sessions (where IP detection fails) inherit it. Best-effort, debounced
// to one write per id change.
func persistDetectedServer(serverID int, ingestBaseURL string) {
	if serverID == 0 || lastPersistedID.Swap(int64(serverID)) == int64(serverID) {
		return
	}
	data, err := json.Marshal(serverState{ServerID: serverID, IngestBaseURL: ingestBaseURL})
	if err != nil {
		return
	}
	if err := os.WriteFile(serverStatePath(), data, 0644); err != nil {
		log.Debugf("[VPN-Auto] Could not persist server state: %v", err)
		return
	}
	log.Debugf("[VPN-Auto] Persisted detected server id %d for future VPN sessions", serverID)
}

// loadPersistedServer returns the last positively-identified server (0 = none).
func loadPersistedServer() (int, string) {
	persistedServerOnce.Do(func() {
		data, err := os.ReadFile(serverStatePath())
		if err != nil {
			return
		}
		var st serverState
		if json.Unmarshal(data, &st) == nil && st.ServerID >= 1 && st.ServerID <= 3 && st.IngestBaseURL != "" {
			persistedServer = st
		}
	})
	return persistedServer.ServerID, persistedServer.IngestBaseURL
}

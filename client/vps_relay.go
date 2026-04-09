package client

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/ao-data/albiondata-client/log"
	"github.com/gorilla/websocket"
)

// VPSRelay manages a WebSocket connection to the private VPS
// for sending chest capture data and other enriched events
type VPSRelay struct {
	mu          sync.Mutex
	conn        *websocket.Conn
	url         string
	token       string
	connected   bool
	reconnectCh chan struct{}
}

var vpsRelay *VPSRelay

// InitVPSRelay creates and starts the VPS relay connection
func InitVPSRelay(captureToken string) {
	if captureToken == "" {
		log.Info("[VPSRelay] No capture token configured — chest captures will only be logged locally")
		return
	}

	vpsRelay = &VPSRelay{
		url:         "wss://albionaitool.xyz",
		token:       captureToken,
		reconnectCh: make(chan struct{}, 1),
	}

	go vpsRelay.connectLoop()
	log.Infof("[VPSRelay] Initialized — will relay chest captures to %s", vpsRelay.url)
}

func (r *VPSRelay) connectLoop() {
	for {
		r.connect()
		// Wait before reconnecting
		time.Sleep(10 * time.Second)
	}
}

func (r *VPSRelay) connect() {
	r.mu.Lock()
	if r.conn != nil {
		r.conn.Close()
		r.conn = nil
	}
	r.connected = false
	r.mu.Unlock()

	log.Debug("[VPSRelay] Connecting...")

	conn, _, err := websocket.DefaultDialer.Dial(r.url, nil)
	if err != nil {
		log.Debugf("[VPSRelay] Connection failed: %v", err)
		return
	}

	r.mu.Lock()
	r.conn = conn
	r.mu.Unlock()

	// Authenticate with capture token
	authMsg := map[string]interface{}{
		"type":  "client-auth",
		"token": r.token,
	}
	authJSON, _ := json.Marshal(authMsg)
	if err := conn.WriteMessage(websocket.TextMessage, authJSON); err != nil {
		log.Debugf("[VPSRelay] Auth send failed: %v", err)
		return
	}

	// Read messages until we get the auth response
	// (server broadcasts NATS market data to all WS clients, so first messages may not be auth)
	authTimeout := time.After(15 * time.Second)
	authDone := make(chan bool, 1)

	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				log.Debug("[VPSRelay] Connection lost during auth")
				authDone <- false
				return
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(msg, &resp); err != nil {
				continue // Not JSON (raw NATS data), skip
			}

			if resp["type"] == "client-auth" {
				if resp["success"] == true {
					r.mu.Lock()
					r.connected = true
					r.mu.Unlock()
					log.Infof("[VPSRelay] Authenticated as: %v", resp["username"])
					authDone <- true
				} else {
					log.Warnf("[VPSRelay] Auth rejected: %v", resp["error"])
					authDone <- false
				}
				return
			}
			// Ignore other messages during auth handshake
		}
	}()

	select {
	case success := <-authDone:
		if !success {
			conn.Close()
			return
		}
	case <-authTimeout:
		log.Warn("[VPSRelay] Auth timed out after 15s")
		conn.Close()
		return
	}

	// Keep connection alive — read and discard server messages
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			log.Debug("[VPSRelay] Connection lost, will reconnect...")
			r.mu.Lock()
			r.connected = false
			r.conn = nil
			r.mu.Unlock()
			return
		}
	}
}

// SendChestCapture sends a captured container to the VPS
func SendChestCapture(capture *ContainerCapture) {
	if vpsRelay == nil {
		return
	}

	r := vpsRelay
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.connected || r.conn == nil {
		log.Debug("[VPSRelay] Not connected — capture not sent")
		return
	}

	msg := map[string]interface{}{
		"type": "chest-capture",
		"data": capture,
	}

	msgJSON, err := json.Marshal(msg)
	if err != nil {
		log.Errorf("[VPSRelay] JSON marshal failed: %v", err)
		return
	}

	if err := r.conn.WriteMessage(websocket.TextMessage, msgJSON); err != nil {
		log.Errorf("[VPSRelay] Send failed: %v", err)
		r.connected = false
		return
	}

	log.Infof("[VPSRelay] Sent chest capture (%d items) to VPS", capture.ItemCount)
}

func SendLootEvent(lootEvent *LootEvent) {
	if vpsRelay == nil {
		return
	}

	r := vpsRelay
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.connected || r.conn == nil {
		return
	}

	msg := map[string]interface{}{
		"type": "loot-event",
		"data": lootEvent,
	}

	msgJSON, err := json.Marshal(msg)
	if err != nil {
		log.Errorf("[VPSRelay] JSON marshal failed: %v", err)
		return
	}

	if err := r.conn.WriteMessage(websocket.TextMessage, msgJSON); err != nil {
		log.Errorf("[VPSRelay] Send failed: %v", err)
		r.connected = false
		return
	}

	log.Debugf("[VPSRelay] Sent loot event: %s looted %s x%d", lootEvent.LootedBy.Name, lootEvent.ItemID, lootEvent.Quantity)
}

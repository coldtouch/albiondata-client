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

	// Read auth response
	_, msg, err := conn.ReadMessage()
	if err != nil {
		log.Debugf("[VPSRelay] Auth read failed: %v", err)
		return
	}

	var resp map[string]interface{}
	json.Unmarshal(msg, &resp)
	if resp["type"] == "client-auth" && resp["success"] == true {
		r.mu.Lock()
		r.connected = true
		r.mu.Unlock()
		log.Info("[VPSRelay] Connected and authenticated to VPS")
	} else {
		log.Warnf("[VPSRelay] Auth failed: %s", string(msg))
		conn.Close()
		return
	}

	// Keep connection alive — read messages (server might send commands)
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

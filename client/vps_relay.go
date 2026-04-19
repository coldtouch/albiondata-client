package client

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"math/rand"
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
	sessionID   string // UUID per game run — survives WS reconnects so loot events don't fragment
	connected   bool
	stopCh      chan struct{} // signals connectLoop to stop
	ctx         context.Context
	ctxCancel   context.CancelFunc
	pendingMsgs [][]byte // bounded queue for messages during disconnect
}

// newSessionUUID returns a UUIDv4 string. Used once per game run; survives WS reconnects.
// Backend uses this as loot_events.session_id so one PvP run = one Loot Logger session,
// instead of N fragments (one per reconnect) as it was before.
func newSessionUUID() string {
	b := make([]byte, 16)
	if _, err := cryptorand.Read(b); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // UUID v4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// maxPendingMsgs caps the offline queue. 500 entries covers a guild vault bulk
// open (100 tabs × a few seconds of disconnect) without unbounded growth (GC-6).
const maxPendingMsgs = 500

// wsDialer uses an explicit handshake timeout so a slow or unresponsive VPS
// does not block indefinitely (GC-7).
var wsDialer = &websocket.Dialer{
	HandshakeTimeout: 15 * time.Second,
}

// wsReadDeadline is reset after every successful message to detect half-open
// TCP connections (GC-7).
const wsReadDeadline = 60 * time.Second

const (
	reconnectBackoffInit = 1 * time.Second
	reconnectBackoffMax  = 60 * time.Second
)

var vpsRelay *VPSRelay

// InitVPSRelay creates and starts the VPS relay connection.
// The WebSocket URL defaults to wss://albionaitool.xyz but can be overridden
// via the VPSRelayURL config key or --vps-url flag (GO-L1).
func InitVPSRelay(captureToken string) {
	if captureToken == "" {
		log.Info("[VPSRelay] No capture token configured — chest captures will only be logged locally")
		return
	}

	url := ConfigGlobal.VPSRelayURL
	if url == "" {
		url = "wss://albionaitool.xyz"
	}

	ctx, cancel := context.WithCancel(context.Background())
	vpsRelay = &VPSRelay{
		url:       url,
		token:     captureToken,
		sessionID: newSessionUUID(),
		stopCh:    make(chan struct{}),
		ctx:       ctx,
		ctxCancel: cancel,
	}

	go vpsRelay.connectLoop()
	log.Infof("[VPSRelay] Initialized — will relay chest captures to %s (sessionID=%s)", vpsRelay.url, vpsRelay.sessionID)
}

// connectLoop retries connect() with exponential backoff + jitter (GO-H2).
// Backoff resets when a connection succeeds (was held long enough for auth to complete).
func (r *VPSRelay) connectLoop() {
	backoff := reconnectBackoffInit
	for {
		wasConnected := r.connect()
		if wasConnected {
			backoff = reconnectBackoffInit // reset after a successful session
		}

		delay := jitter(backoff)
		select {
		case <-r.stopCh:
			return
		case <-time.After(delay):
		}

		if !wasConnected {
			backoff = min(backoff*2, reconnectBackoffMax)
		}
	}
}

// jitter adds up to 20 % random noise to d to spread out reconnect storms.
func jitter(d time.Duration) time.Duration {
	noise := time.Duration(rand.Int63n(int64(d) / 5))
	return d + noise
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// StopVPSRelay gracefully shuts down the VPS relay connection.
func StopVPSRelay() {
	if vpsRelay == nil {
		return
	}
	vpsRelay.ctxCancel() // unblocks any pending reads via conn.Close below
	close(vpsRelay.stopCh)
	vpsRelay.mu.Lock()
	if vpsRelay.conn != nil {
		vpsRelay.conn.Close()
	}
	vpsRelay.mu.Unlock()
}

// sendOrQueue sends a message immediately if connected, otherwise queues it for retry.
func (r *VPSRelay) sendOrQueue(msgJSON []byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.connected || r.conn == nil {
		// Queue for later
		if len(r.pendingMsgs) < maxPendingMsgs {
			r.pendingMsgs = append(r.pendingMsgs, msgJSON)
			log.Debugf("[VPSRelay] Queued message (%d pending)", len(r.pendingMsgs))
		} else {
			log.Warnf("[VPSRelay] Queue full (%d msgs) — dropping message", maxPendingMsgs)
		}
		return false
	}

	if err := r.conn.WriteMessage(websocket.TextMessage, msgJSON); err != nil {
		log.Errorf("[VPSRelay] Send failed: %v", err)
		r.connected = false
		// Queue this message for retry
		if len(r.pendingMsgs) < maxPendingMsgs {
			r.pendingMsgs = append(r.pendingMsgs, msgJSON)
		}
		return false
	}
	return true
}

// flushPending sends all queued messages after a successful reconnect.
//
// Fix (GC-4): copy the queue under lock, clear it, then send without holding
// the lock per-message. On failure, re-queue remaining messages (including the
// failed one) under lock, merging with any messages added by sendOrQueue during
// the flush window.
func (r *VPSRelay) flushPending() {
	r.mu.Lock()
	if !r.connected || r.conn == nil || len(r.pendingMsgs) == 0 {
		r.mu.Unlock()
		return
	}
	// Snapshot and clear atomically so sendOrQueue can enqueue new messages
	// independently while we send the batch.
	toSend := make([][]byte, len(r.pendingMsgs))
	copy(toSend, r.pendingMsgs)
	r.pendingMsgs = r.pendingMsgs[:0]
	conn := r.conn
	r.mu.Unlock()

	sent := 0
	for i, msg := range toSend {
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			log.Errorf("[VPSRelay] Flush send failed at msg %d: %v", i, err)
			// Re-queue remaining (including failed message) at the FRONT,
			// then append anything sendOrQueue added while we were flushing.
			r.mu.Lock()
			r.connected = false
			remaining := toSend[i:] // toSend[i] is the failed message
			combined := make([][]byte, 0, len(remaining)+len(r.pendingMsgs))
			combined = append(combined, remaining...)
			combined = append(combined, r.pendingMsgs...)
			if len(combined) > maxPendingMsgs {
				combined = combined[len(combined)-maxPendingMsgs:]
			}
			r.pendingMsgs = combined
			r.mu.Unlock()
			break
		}
		sent++
	}
	if sent > 0 {
		log.Infof("[VPSRelay] Flushed %d queued messages", sent)
	}
}

// connect dials the VPS, authenticates, flushes pending messages, then reads
// until the connection drops. Returns true if authentication succeeded (even
// if the connection later dropped), false on dial/auth failure.
func (r *VPSRelay) connect() (wasConnected bool) {
	r.mu.Lock()
	if r.conn != nil {
		r.conn.Close()
		r.conn = nil
	}
	r.connected = false
	r.mu.Unlock()

	log.Debug("[VPSRelay] Connecting...")

	// GC-7: explicit handshake timeout via wsDialer (not DefaultDialer).
	conn, _, err := wsDialer.Dial(r.url, nil)
	if err != nil {
		log.Debugf("[VPSRelay] Connection failed: %v", err)
		return false
	}

	// GO-C2: close the connection when the relay context is cancelled so any
	// goroutine blocked on ReadMessage returns promptly without leaking.
	stopConn := make(chan struct{})
	go func(ctx context.Context) {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stopConn:
		}
	}(r.ctx)
	defer close(stopConn)

	r.mu.Lock()
	r.conn = conn
	r.mu.Unlock()

	// Authenticate with capture token + per-run session UUID.
	// The sessionID is generated once at process start (InitVPSRelay) and re-sent on every
	// reconnect; the backend pins ws.lootSessionId to this value so loot events across
	// reconnects land in the SAME session instead of fragmenting into one session per reconnect.
	// GO-C3: check json.Marshal error (authMsg only contains static strings so
	// this never fails in practice, but we must not silently swallow it).
	authMsg := map[string]interface{}{
		"type":      "client-auth",
		"token":     r.token,
		"sessionID": r.sessionID,
	}
	authJSON, err := json.Marshal(authMsg)
	if err != nil {
		log.Errorf("[VPSRelay] Failed to marshal auth message: %v", err)
		conn.Close()
		return false
	}
	if err := conn.WriteMessage(websocket.TextMessage, authJSON); err != nil {
		log.Debugf("[VPSRelay] Auth send failed: %v", err)
		return false
	}

	// Read messages until we get the auth response.
	// (server broadcasts NATS market data to all WS clients, so first messages may not be auth)
	// GO-C2: the goroutine receives ctx explicitly; cancellation closes conn, which
	// causes ReadMessage to return an error and unblocks the goroutine.
	authTimeout := time.After(30 * time.Second)
	authDone := make(chan bool, 1)

	go func(ctx context.Context) {
		for {
			_ = conn.SetReadDeadline(time.Now().Add(wsReadDeadline))
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
	}(r.ctx)

	select {
	case success := <-authDone:
		if !success {
			conn.Close()
			return false
		}
	case <-authTimeout:
		log.Warn("[VPSRelay] Auth timed out after 30s")
		conn.Close()
		return false
	}

	// GO-H1: flush any messages queued during the reconnect window, now that
	// the connection is live. Do this before entering the keep-alive loop.
	r.flushPending()

	// Keep connection alive — read and discard server messages.
	// GC-7: reset read deadline after every successful message.
	for {
		_ = conn.SetReadDeadline(time.Now().Add(wsReadDeadline))
		_, _, err := conn.ReadMessage()
		if err != nil {
			log.Debug("[VPSRelay] Connection lost, will reconnect...")
			r.mu.Lock()
			r.connected = false
			r.conn = nil
			r.mu.Unlock()
			return true // was connected
		}
	}
}

// SendChestCapture sends a captured container to the VPS (queues if disconnected)
func SendChestCapture(capture *ContainerCapture) {
	if vpsRelay == nil {
		return
	}
	msg := map[string]interface{}{"type": "chest-capture", "data": capture}
	msgJSON, err := json.Marshal(msg)
	if err != nil {
		log.Errorf("[VPSRelay] JSON marshal failed: %v", err)
		return
	}
	if vpsRelay.sendOrQueue(msgJSON) {
		log.Infof("[VPSRelay] Sent chest capture (%d items) to VPS", capture.ItemCount)
	}
}

func SendLootEvent(lootEvent *LootEvent) {
	if vpsRelay == nil {
		return
	}
	msg := map[string]interface{}{"type": "loot-event", "data": lootEvent}
	msgJSON, err := json.Marshal(msg)
	if err != nil {
		log.Errorf("[VPSRelay] JSON marshal failed: %v", err)
		return
	}
	if vpsRelay.sendOrQueue(msgJSON) {
		log.Debugf("[VPSRelay] Sent loot event: %s looted %s x%d", lootEvent.LootedBy.Name, lootEvent.ItemID, lootEvent.Quantity)
	}
}

func SendDeathEvent(deathEvent *DeathEvent) {
	if vpsRelay == nil {
		return
	}
	msg := map[string]interface{}{"type": "death-event", "data": deathEvent}
	msgJSON, err := json.Marshal(msg)
	if err != nil {
		log.Errorf("[VPSRelay] JSON marshal failed: %v", err)
		return
	}
	if vpsRelay.sendOrQueue(msgJSON) {
		log.Debugf("[VPSRelay] Sent death event: %s killed by %s", deathEvent.VictimName, deathEvent.KillerName)
	}
}

// SaleNotification represents a marketplace sale detected from in-game mail
type SaleNotification struct {
	Timestamp int64  `json:"timestamp"` // Unix millis
	ItemID    string `json:"itemId"`    // e.g. T4_BAG
	Amount    int    `json:"amount"`
	Price     int    `json:"unitPrice"` // silver per unit
	Total     int    `json:"total"`     // total silver (before tax)
	Location  string `json:"location"`
	MailID    int32  `json:"mailId"`
	OrderType string `json:"orderType"` // FINISHED or EXPIRED
	Sold      int    `json:"sold"`      // for expired orders: how many sold
}

// TradeEvent represents a real-time marketplace transaction (insta-buy, listing, etc.)
type TradeEvent struct {
	Timestamp int64  `json:"timestamp"`
	ItemID    string `json:"itemId"`
	Amount    int    `json:"amount"`
	Price     int    `json:"unitPrice"` // silver per unit
	Total     int    `json:"total"`     // total silver
	Location  string `json:"location"`
	TradeType string `json:"tradeType"` // "insta-buy", "listing-created", "buy-order-placed"
	Quality   int    `json:"quality"`
	OrderID   int64  `json:"orderId,omitempty"`
}

func SendTradeEvent(trade *TradeEvent) {
	if vpsRelay == nil {
		return
	}
	msg := map[string]interface{}{"type": "trade-event", "data": trade}
	msgJSON, err := json.Marshal(msg)
	if err != nil {
		log.Errorf("[VPSRelay] JSON marshal failed: %v", err)
		return
	}
	if vpsRelay.sendOrQueue(msgJSON) {
		log.Infof("[VPSRelay] Sent trade event: %s %s x%d @ %d silver", trade.TradeType, trade.ItemID, trade.Amount, trade.Price)
	}
}

func SendSaleNotification(sale *SaleNotification) {
	if vpsRelay == nil {
		return
	}
	msg := map[string]interface{}{"type": "sale-notification", "data": sale}
	msgJSON, err := json.Marshal(msg)
	if err != nil {
		log.Errorf("[VPSRelay] JSON marshal failed: %v", err)
		return
	}
	if vpsRelay.sendOrQueue(msgJSON) {
		log.Infof("[VPSRelay] Sent sale notification: %s x%d @ %d silver", sale.ItemID, sale.Amount, sale.Price)
	}
}

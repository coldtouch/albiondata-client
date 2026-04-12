package client

import (
	"time"

	"github.com/ao-data/albiondata-client/lib"
	"github.com/ao-data/albiondata-client/log"
)

// === INSTA-BUY TRACKING ===
// When the player buys from a sell order (opAuctionBuyOffer), we look up the
// cached order to get item details, then send a trade event to VPS.

type operationAuctionBuyOfferRequest struct {
	Unknown  int8  `mapstructure:"0"`
	Quantity int8  `mapstructure:"1"`
	OrderID  int64 `mapstructure:"2"`
}

func (op operationAuctionBuyOfferRequest) Process(state *albionState) {
	qty := int(op.Quantity)
	if qty <= 0 {
		qty = 1
	}

	// Look up the order from cache (populated when browsing market)
	cached, ok := marketOrderCache.Load(op.OrderID)
	if !ok {
		log.Infof("[Trade] Insta-buy order %d — not in cache (market not browsed for this item)", op.OrderID)
		return
	}

	order := cached.(*lib.MarketOrder)
	itemName := resolveItemName(0)
	if order.ItemID != "" {
		itemName = order.ItemID
	}

	price := order.Price
	total := price * qty

	log.Infof("[Trade] INSTA-BUY: %s x%d @ %d silver/ea = %d total — location: %s",
		itemName, qty, price, total, order.LocationID)

	SendTradeEvent(&TradeEvent{
		Timestamp: time.Now().UnixMilli(),
		ItemID:    order.ItemID,
		Amount:    qty,
		Price:     price,
		Total:     total,
		Location:  order.LocationID,
		TradeType: "insta-buy",
		Quality:   order.QualityLevel,
		OrderID:   op.OrderID,
	})
}

// === SELL ORDER LISTING TRACKING ===
// When the player creates a sell order (opAuctionCreateOffer), we capture the
// item and price. The item slot ID maps to globalItemCache.

type operationAuctionCreateOfferRequest struct {
	Unknown  int8  `mapstructure:"0"`
	Quantity int8  `mapstructure:"1"`
	SlotID   int32 `mapstructure:"2"` // global item slot ID
	Price    int32 `mapstructure:"3"` // price × 10000
	Duration int16 `mapstructure:"4"` // hours (720 = 30 days)
}

func (op operationAuctionCreateOfferRequest) Process(state *albionState) {
	qty := int(op.Quantity)
	if qty <= 0 {
		qty = 1
	}

	price := int(op.Price) / 10000
	if price <= 0 {
		return
	}

	// Try to resolve item from global cache (populated by item events)
	itemID := "unknown"
	quality := 1
	if val, ok := globalItemCache.Load(int(op.SlotID)); ok {
		item := val.(CapturedItem)
		itemID = item.ItemID
		quality = item.Quality
	}

	total := price * qty
	log.Infof("[Trade] LISTING CREATED: %s x%d @ %d silver/ea = %d total — duration: %dh",
		itemID, qty, price, total, op.Duration)

	SendTradeEvent(&TradeEvent{
		Timestamp: time.Now().UnixMilli(),
		ItemID:    itemID,
		Amount:    qty,
		Price:     price,
		Total:     total,
		Location:  state.LocationId,
		TradeType: "listing-created",
		Quality:   quality,
	})
}

// === BUY ORDER PLACEMENT TRACKING ===
// When the player places a buy order (opAuctionCreateRequest).

type operationAuctionCreateRequestReq struct {
	Unknown  int8  `mapstructure:"0"`
	ItemID   int16 `mapstructure:"1"` // numeric item type ID
	Quality  int8  `mapstructure:"2"`
	Quantity int8  `mapstructure:"3"`
	Duration int16 `mapstructure:"4"` // hours
	Price    int32 `mapstructure:"5"` // price × 10000
}

func (op operationAuctionCreateRequestReq) Process(state *albionState) {
	qty := int(op.Quantity)
	if qty <= 0 {
		qty = 1
	}

	price := int(op.Price) / 10000
	if price <= 0 {
		return
	}

	itemName := resolveItemName(int(op.ItemID))
	total := price * qty

	log.Infof("[Trade] BUY ORDER PLACED: %s x%d @ %d silver/ea = %d total — duration: %dh",
		itemName, qty, price, total, op.Duration)

	SendTradeEvent(&TradeEvent{
		Timestamp: time.Now().UnixMilli(),
		ItemID:    itemName,
		Amount:    qty,
		Price:     price,
		Total:     total,
		Location:  state.LocationId,
		TradeType: "buy-order-placed",
		Quality:   int(op.Quality),
	})
}

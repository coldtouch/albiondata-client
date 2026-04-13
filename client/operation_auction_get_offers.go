package client

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/ao-data/albiondata-client/lib"
	"github.com/ao-data/albiondata-client/log"
	uuid "github.com/nu7hatch/gouuid"
)

// cachedMarketOrder wraps a MarketOrder with a timestamp for TTL-based eviction.
type cachedMarketOrder struct {
	order    *lib.MarketOrder
	cachedAt time.Time
}

// marketOrderCache stores recently seen market orders keyed by order ID.
// Used to resolve item details when opAuctionBuyOffer fires (which only has order ID).
var marketOrderCache sync.Map // map[int64]*cachedMarketOrder

const marketOrderCacheTTL = 10 * time.Minute

func init() {
	go marketOrderCacheCleanup()
}

// marketOrderCacheCleanup sweeps marketOrderCache every 5 minutes, deleting entries older than 10 minutes.
func marketOrderCacheCleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		marketOrderCache.Range(func(key, value interface{}) bool {
			entry := value.(*cachedMarketOrder)
			if now.Sub(entry.cachedAt) > marketOrderCacheTTL {
				marketOrderCache.Delete(key)
			}
			return true
		})
	}
}

type operationAuctionGetOffers struct {
	Category         string   `mapstructure:"1"`
	SubCategory      string   `mapstructure:"2"`
	Quality          string   `mapstructure:"5"`
	Enchantment      uint32   `mapstructure:"6"`
	EnchantmentLevel string   `mapstructure:"10"`
	ItemIds          []uint16 `mapstructure:"8"`
	MaxResults       uint32   `mapstructure:"12"`
	IsAscendingOrder bool     `mapstructure:"14"`
}

func (op operationAuctionGetOffers) Process(state *albionState) {
	log.Debug("Got AuctionGetOffers operation...")
	state.SetWaitingForMarketData(true)
}

type operationAuctionGetOffersResponse struct {
	MarketOrders []string `mapstructure:"0"`
}

func (op operationAuctionGetOffersResponse) Process(state *albionState) {
	log.Debug("Got response to AuctionGetOffers operation...")
	state.SetWaitingForMarketData(false)

	if !state.IsValidLocation() {
		return
	}

	var orders []*lib.MarketOrder

	for _, v := range op.MarketOrders {
		// Unmarshal market order data to map
		var marketOrder map[string]interface{}
		err2 := json.Unmarshal([]byte(v), &marketOrder)
		if err2 != nil {
			log.Fatal(err2)
		}

		// Pull the location
		location, _ := marketOrder["LocationId"].(string)

		// if the location has @, it is either a rest or smugglers den
		if strings.Contains(location, "@") {

			// Set the location in the market order
			marketOrder["LocationId"] = location

			// Marshal the map back to json
			newJson, _ := json.Marshal(marketOrder)

			// Set the new json back to the market order
			v = string(newJson)
		}

		order := &lib.MarketOrder{}

		err := json.Unmarshal([]byte(v), order)
		if err != nil {
			log.Errorf("Problem converting market order to internal struct: %v", err)
		}

		// Set the location only if its string(nil). Smugglers Dens pull locations directly from the market data (above)
		// while the orignal cities have a null location ID and is pulled from the client state.
		if order.LocationID == "" {
			order.LocationID = state.GetLocationId()
		}

		orders = append(orders, order)

		// Cache order for insta-buy tracking (opAuctionBuyOffer uses order ID)
		if order.ID > 0 {
			marketOrderCache.Store(int64(order.ID), &cachedMarketOrder{order: order, cachedAt: time.Now()})
		}
	}

	if len(orders) < 1 {
		return
	}

	upload := lib.MarketUpload{
		Orders: orders,
	}

	identifier, _ := uuid.NewV4()
	log.Infof("Sending %d live market sell orders to ingest (Identifier: %s)", len(orders), identifier)
	sendMsgToPublicUploaders(upload, lib.NatsMarketOrdersIngest, state, identifier.String())
}

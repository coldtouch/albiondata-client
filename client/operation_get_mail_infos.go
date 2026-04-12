package client

import (
	"fmt"
	"time"

	"github.com/ao-data/albiondata-client/log"
)

var MailInfos MailInfosLookup

type MailInfosLookup []MailInfo

func (mi MailInfosLookup) getMailInfo(id int32) *MailInfo {
	for i := range mi {
		if mi[i].ID == id {
			return &mi[i]
		}
	}
	return nil
}

type MailInfo struct {
	ID         int32  `json:"MailId"`
	LocationID string `json:"LocationId"`
	OrderType  string `json:"OrderType"`
	Expires    int64  `json:"Expires"`
}

func (m *MailInfo) StringArray() []string {
	return []string{
		fmt.Sprintf("%d", m.ID),
		m.LocationID,
		m.OrderType,
		m.StringExpires(),
	}
}

func (m *MailInfo) StringExpires() string {
	if m.Expires == 0 {
		return ""
	}
	return time.Unix(m.Expires, 0).Format(time.RFC3339)
}

// operationGetMailInfosResponse is decoded manually (not via mapstructure)
// because the game protocol changed the param layout.
type operationGetMailInfosResponse struct{}

func (op operationGetMailInfosResponse) Process(state *albionState) {
	// This is a no-op — actual processing happens in processMailInfosRaw
}

// processMailInfosRaw decodes the raw params from GetMailInfos response.
// Called directly from decodeResponse before mapstructure runs.
func processMailInfosRaw(params map[uint8]interface{}) {
	// Reset cache
	MailInfos = nil

	// Extract mail IDs from param 3
	mailIDs := extractInt32Slice(params[3])
	if len(mailIDs) == 0 {
		log.Info("[Mail] Mailbox opened — no mails (no IDs in param 3)")
		return
	}

	// Order types are now in param 11 (was param 10)
	orderTypes := extractStringSlice(params[11])

	// Protocol param positions shifted between game versions; try known positions in order
	locationIDs := extractStringSlice(params[4])
	if len(locationIDs) == 0 {
		locationIDs = extractStringSlice(params[7])
	}

	// Protocol param positions shifted between game versions; try known positions in order
	expires := extractInt64Slice(params[8])
	if len(expires) == 0 {
		expires = extractInt64Slice(params[9])
	}

	for i, id := range mailIDs {
		mail := &MailInfo{ID: id}

		if i < len(orderTypes) {
			mail.OrderType = orderTypes[i]
		}
		if i < len(locationIDs) {
			mail.LocationID = locationIDs[i]
		}
		if i < len(expires) {
			mail.Expires = expires[i]
		}

		MailInfos = append(MailInfos, *mail)
	}

	// Count by type
	saleCount := 0
	expiredCount := 0
	otherCount := 0
	for _, m := range MailInfos {
		switch m.OrderType {
		case "MARKETPLACE_SELLORDER_FINISHED_SUMMARY":
			saleCount++
		case "MARKETPLACE_SELLORDER_EXPIRED_SUMMARY":
			expiredCount++
		default:
			otherCount++
		}
	}
	log.Infof("[Mail] Mailbox opened — %d mails cached (%d sold, %d expired, %d other)",
		len(MailInfos), saleCount, expiredCount, otherCount)
}

// Helper: extract []int32 from interface{} (handles []int32, []int16, []int8)
func extractInt32Slice(v interface{}) []int32 {
	if v == nil {
		return nil
	}
	switch arr := v.(type) {
	case []int32:
		return arr
	case []int16:
		out := make([]int32, len(arr))
		for i, val := range arr {
			out[i] = int32(val)
		}
		return out
	case []int8:
		out := make([]int32, len(arr))
		for i, val := range arr {
			out[i] = int32(val)
		}
		return out
	}
	return nil
}

// Helper: extract []string from interface{}
func extractStringSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	if arr, ok := v.([]string); ok {
		return arr
	}
	return nil
}

// Helper: extract []int64 from interface{} (handles []int64, []int32)
func extractInt64Slice(v interface{}) []int64 {
	if v == nil {
		return nil
	}
	switch arr := v.(type) {
	case []int64:
		return arr
	case []int32:
		out := make([]int64, len(arr))
		for i, val := range arr {
			out[i] = int64(val)
		}
		return out
	}
	return nil
}

package client

import (
	"strconv"
	"strings"
	"time"

	"github.com/ao-data/albiondata-client/lib"
	"github.com/ao-data/albiondata-client/log"
	uuid "github.com/nu7hatch/gouuid"
)

type operationReadMail struct {
	ID   int32  `mapstructure:"0"`
	Body string `mapstructure:"1"`
}

func (op operationReadMail) Process(state *albionState) {
	log.Debugf("[Mail] ReadMail id=%d body=%q", op.ID, op.Body)
	var notification lib.MarketNotification

	// split the mail body
	body := strings.Split(op.Body, "|")

	mailInfo := MailInfos.getMailInfo(op.ID)
	if mailInfo == nil {
		log.Info("[Mail] Mail info not cached — open mailbox first or transition zones.")
		return
	}

	log.Infof("[Mail] Reading mail id=%d type=%s location=%s", op.ID, mailInfo.OrderType, mailInfo.LocationID)

	if mailInfo.OrderType == "MARKETPLACE_SELLORDER_FINISHED_SUMMARY" {
		notification = decodeSellNotification(op, body)

	} else if mailInfo.OrderType == "MARKETPLACE_SELLORDER_EXPIRED_SUMMARY" {
		notification = decodeExpiryNotification(op, body)
	} else {
		log.Infof("[Mail] Non-market mail type: %s (body parts: %d)", mailInfo.OrderType, len(body))
		return
	}

	if notification == nil {
		return
	}

	upload := lib.MarketNotificationUpload{
		Type:         notification.Type(),
		Notification: notification,
	}

	identifier, _ := uuid.NewV4()
	sendMsgToPrivateUploaders(&upload, lib.NatsMarketNotifications, state, identifier.String())
}

func decodeSellNotification(op operationReadMail, body []string) lib.MarketNotification {
	notification := &lib.MarketSellNotification{}
	notification.MailID = op.ID

	amount, err := strconv.Atoi(body[0])
	if err != nil {
		log.Error("[Mail] Could not parse amount in market sell notification ", err)
		return nil
	}

	price, err := strconv.Atoi(body[3])
	if err != nil {
		log.Error("[Mail] Could not parse price in market sell notification ", err)
		return nil
	}

	notification.Amount = amount
	notification.ItemID = body[1]
	notification.Price = price / 10000
	notification.TotalAfterTaxes = float32(float32(notification.Price) * float32(notification.Amount) * (1.0 - lib.SalesTax))

	mailInfo := MailInfos.getMailInfo(op.ID)
	notification.LocationID = mailInfo.LocationID
	notification.Expires = mailInfo.StringExpires()

	itemName := resolveItemName(0) // We have string ID already
	if notification.ItemID != "" {
		itemName = notification.ItemID
	}
	log.Infof("[Mail] SALE COMPLETE: %s x%d @ %d silver/ea = %d total (after tax: %.0f) — location: %s",
		itemName, notification.Amount, notification.Price,
		notification.Price*notification.Amount, notification.TotalAfterTaxes, notification.LocationID)

	// Relay to VPS
	SendSaleNotification(&SaleNotification{
		Timestamp: time.Now().UnixMilli(),
		ItemID:    notification.ItemID,
		Amount:    notification.Amount,
		Price:     notification.Price,
		Total:     notification.Price * notification.Amount,
		Location:  notification.LocationID,
		MailID:    op.ID,
		OrderType: "FINISHED",
	})

	return notification
}

func decodeExpiryNotification(op operationReadMail, body []string) lib.MarketNotification {
	notification := &lib.MarketExpiryNotification{}
	notification.MailID = op.ID

	if len(body) < 4 {
		log.Errorf("[Mail] Expiry notification has too few body parts (%d): %v", len(body), body)
		return nil
	}

	sold, err := strconv.Atoi(body[0])
	if err != nil {
		log.Error("[Mail] Could not parse sold count in expiry notification ", err)
		return nil
	}

	amount, err := strconv.Atoi(body[2])
	if err != nil {
		log.Error("[Mail] Could not parse amount in expiry notification ", err)
		return nil
	}

	price, err := strconv.Atoi(body[3])
	if err != nil {
		log.Error("[Mail] Could not parse price in expiry notification ", err)
		return nil
	}

	notification.Amount = amount
	notification.ItemID = body[1]
	notification.Price = price / 10000
	notification.Sold = sold

	mailInfo := MailInfos.getMailInfo(op.ID)
	notification.LocationID = mailInfo.LocationID
	notification.Expires = mailInfo.StringExpires()

	log.Infof("[Mail] ORDER EXPIRED: %s — sold %d/%d @ %d silver/ea — location: %s",
		notification.ItemID, notification.Sold, notification.Amount, notification.Price, notification.LocationID)

	// Relay to VPS
	SendSaleNotification(&SaleNotification{
		Timestamp: time.Now().UnixMilli(),
		ItemID:    notification.ItemID,
		Amount:    notification.Amount,
		Price:     notification.Price,
		Total:     notification.Price * notification.Sold,
		Location:  notification.LocationID,
		MailID:    op.ID,
		OrderType: "EXPIRED",
		Sold:      notification.Sold,
	})

	return notification
}

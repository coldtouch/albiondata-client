package photon_spectator

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	// Command types
	AcknowledgeType          = 1
	ConnectType              = 2
	VerifyConnectType        = 3
	DisconnectType           = 4
	PingType                 = 5
	SendReliableType         = 6
	SendUnreliableType       = 7
	SendReliableFragmentType = 8
	// Message types
	OperationRequest       = 2
	otherOperationResponse = 3
	EventDataType          = 4
	OperationResponse      = 7
)

type PhotonCommand struct {
	// Header
	Type                   uint8
	ChannelID              uint8
	Flags                  uint8
	ReservedByte           uint8
	Length                 int32
	ReliableSequenceNumber int32

	// Body
	Data []byte
}

type ReliableMessage struct {
	// Header
	Signature uint8
	Type      uint8

	// OperationRequest
	OperationCode uint8

	// EventData
	EventCode uint8

	// OperationResponse
	OperationResponseCode uint16
	OperationDebugString  string
	OperationDebugByte    uint8

	ParameterCount int16 // NOTE: read as uint8 in V18 format, cast to int16
	Data           []byte

	// Encryption info
	IsEncrypted bool   // True if the message payload is encrypted
	RawType     uint8  // Original type byte before masking (includes encryption flag)
	RawData     []byte // Raw bytes after type byte (encrypted payload if IsEncrypted)
}

type ReliableFragment struct {
	SequenceNumber int32
	FragmentCount  int32
	FragmentNumber int32
	TotalLength    int32
	FragmentOffset int32

	Data []byte
}

// Returns a structure containing the fields of a reliable message.
// Errors if the type is not SendReliableType.
func (c PhotonCommand) ReliableMessage() (msg ReliableMessage, err error) {
	if c.Type != SendReliableType {
		return msg, fmt.Errorf("Command can't be converted")
	}

	buf := bytes.NewBuffer(c.Data)

	binary.Read(buf, binary.BigEndian, &msg.Signature)
	binary.Read(buf, binary.BigEndian, &msg.Type)

	msg.RawType = msg.Type

	if msg.Type > 128 {
		// Encrypted message — extract what we can without decrypting
		msg.IsEncrypted = true
		msg.Type = msg.Type & 0x7F // Unmask to get the actual message type
		msg.RawData = buf.Bytes()  // Preserve the encrypted payload for inspection

		// Try to read the next byte as event/op code — it IS encrypted,
		// but on some Photon implementations only params are encrypted
		if buf.Len() >= 1 {
			switch msg.Type {
			case OperationRequest:
				binary.Read(buf, binary.BigEndian, &msg.OperationCode)
			case EventDataType:
				binary.Read(buf, binary.BigEndian, &msg.EventCode)
			case OperationResponse:
				binary.Read(buf, binary.BigEndian, &msg.OperationCode)
			}
		}

		// Return with nil error — let caller decide how to handle encrypted messages
		return msg, nil
	}

	if msg.Type == otherOperationResponse {
		msg.Type = OperationResponse
	}

	switch msg.Type {
	case OperationRequest:
		binary.Read(buf, binary.BigEndian, &msg.OperationCode)
	case EventDataType:
		binary.Read(buf, binary.BigEndian, &msg.EventCode)
	case OperationResponse:
		binary.Read(buf, binary.BigEndian, &msg.OperationCode)
		// V18: response code is int16 (2 bytes), then debug byte
		// Try reading — if debug byte decodes to an error, just skip it
		binary.Read(buf, binary.BigEndian, &msg.OperationResponseCode)
		if buf.Len() > 0 {
			binary.Read(buf, binary.BigEndian, &msg.OperationDebugByte)
			if msg.OperationDebugByte > 0 {
				paramValue := decodeType(buf, msg.OperationDebugByte)
				if s, ok := paramValue.(string); ok {
					msg.OperationDebugString = s
				}
			}
		}
	}

	// V18 uses uint8 for parameter count (was int16 in V16)
	var paramCount uint8
	binary.Read(buf, binary.BigEndian, &paramCount)
	msg.ParameterCount = int16(paramCount)
	msg.Data = buf.Bytes()

	return
}

// Returns a structure containing the fields of a reliable fragment
// Errors if the type is not SendReliableFragmentType.
func (c PhotonCommand) ReliableFragment() (msg ReliableFragment, err error) {
	if c.Type != SendReliableFragmentType {
		return msg, fmt.Errorf("Command can't be converted")
	}

	buf := bytes.NewBuffer(c.Data)

	binary.Read(buf, binary.BigEndian, &msg.SequenceNumber)
	binary.Read(buf, binary.BigEndian, &msg.FragmentCount)
	binary.Read(buf, binary.BigEndian, &msg.FragmentNumber)
	binary.Read(buf, binary.BigEndian, &msg.TotalLength)
	binary.Read(buf, binary.BigEndian, &msg.FragmentOffset)

	msg.Data = buf.Bytes()

	return
}

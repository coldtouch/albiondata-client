package photon_spectator

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// === Type codes: Protocol16 (legacy) + Protocol18/GpBinaryV18 (current) ===
const (
	// Protocol16 (legacy — pre-April 2026)
	NilType               = 42
	DictionaryType        = 68
	StringSliceType       = 97
	Int8Type              = 98
	Custom                = 99
	DoubleType            = 100
	EventDateType         = 101
	Float32Type           = 102
	Hashtable             = 104
	Int32Type             = 105
	Int16Type             = 107
	Int64Type             = 108
	Int32SliceType        = 110
	BooleanType           = 111
	OperationResponseType = 112
	OperationRequestType  = 113
	StringType            = 115
	Int8SliceType         = 120
	SliceType             = 121
	ObjectSliceType       = 122

	// Protocol18 / GpBinaryV18 (April 2026+)
	V18_Null          = 0
	V18_Boolean       = 1
	V18_Byte          = 2
	V18_Short         = 3
	V18_Int           = 4
	V18_Long          = 5
	V18_Float         = 6
	V18_Double        = 7
	V18_String        = 8
	V18_ByteArray     = 9
	V18_IntArray      = 10
	V18_CompressedInt = 11
	V18_CompressedLong = 12
	V18_Custom        = 13
	V18_Dictionary    = 14
	V18_Hashtable     = 15
	V18_ObjectArray   = 17

	// High bit flag: value is NULL (no data bytes follow)
	V18_NullFlag = 0x80
)

type ReliableMessageParameters map[uint8]interface{}
type ReliableMessageParamaters = ReliableMessageParameters // Deprecated

// readCompressedInt reads a varint-encoded int32 from the buffer.
func readCompressedInt(buf *bytes.Buffer) (int32, error) {
	var result int32
	for shift := uint(0); shift < 35; shift += 7 {
		b, err := buf.ReadByte()
		if err != nil {
			return 0, err
		}
		result |= int32(b&0x7F) << shift
		if b&0x80 == 0 {
			// Sign extension for negative numbers (zigzag decode)
			return result, nil
		}
	}
	return result, fmt.Errorf("varint too long")
}

// readCompressedLong reads a varint-encoded int64 from the buffer.
func readCompressedLong(buf *bytes.Buffer) (int64, error) {
	var result int64
	for shift := uint(0); shift < 70; shift += 7 {
		b, err := buf.ReadByte()
		if err != nil {
			return 0, err
		}
		result |= int64(b&0x7F) << shift
		if b&0x80 == 0 {
			return result, nil
		}
	}
	return result, fmt.Errorf("varint too long")
}

// Detect protocol version: V18 types are 0-17, V16 types are 42-122
func isV18Type(t uint8) bool {
	raw := t & 0x7F // strip null flag
	return raw <= 17
}

func DecodeReliableMessage(msg ReliableMessage) (params ReliableMessageParameters) {
	// Recover from any panic during decode — return what we have so far
	defer func() {
		if r := recover(); r != nil {
			// Decode failed — return partial params
		}
	}()

	buf := bytes.NewBuffer(msg.Data)
	params = make(map[uint8]interface{})

	for i := 0; i < int(msg.ParameterCount); i++ {
		if buf.Len() < 2 {
			break
		}

		var paramID uint8
		var paramType uint8

		binary.Read(buf, binary.BigEndian, &paramID)
		binary.Read(buf, binary.BigEndian, &paramType)

		// V18: high bit set = NULL value, no data follows
		if paramType&V18_NullFlag != 0 && isV18Type(paramType) {
			params[paramID] = nil
			continue
		}

		params[paramID] = decodeType(buf, paramType)
	}

	return params
}

func decodeType(buf *bytes.Buffer, paramType uint8) interface{} {
	switch paramType {

	// === Null ===
	case NilType, V18_Null:
		return nil

	// === Boolean ===
	case BooleanType, V18_Boolean:
		result, err := decodeBooleanType(buf)
		if err != nil {
			return nil
		}
		return result

	// === Byte / Int8 ===
	case Int8Type, V18_Byte:
		return decodeInt8Type(buf)

	// === Short / Int16 ===
	case Int16Type, V18_Short:
		return decodeInt16Type(buf)

	// === Int / Int32 ===
	case Int32Type, V18_Int:
		return decodeInt32Type(buf)

	// === Long / Int64 ===
	case Int64Type, V18_Long:
		return decodeInt64Type(buf)

	// === Float ===
	case Float32Type, V18_Float:
		return decodeFloat32Type(buf)

	// === Double ===
	case DoubleType, V18_Double:
		var temp float64
		binary.Read(buf, binary.BigEndian, &temp)
		return temp

	// === String ===
	case StringType:
		return decodeStringType(buf)
	case V18_String:
		// V18 uses CompressedInt for string length
		length, err := readCompressedInt(buf)
		if err != nil || length < 0 || length > 65535 {
			return ""
		}
		strBytes := make([]byte, length)
		buf.Read(strBytes)
		return string(strBytes)

	// === ByteArray / Int8Slice ===
	case Int8SliceType:
		result, err := decodeSliceInt8Type(buf)
		if err != nil {
			return nil
		}
		return result
	case V18_ByteArray:
		// V18 uses CompressedInt for length
		length, err := readCompressedInt(buf)
		if err != nil || length < 0 || length > 100000 {
			return nil
		}
		array := make([]int8, length)
		for j := 0; j < int(length); j++ {
			binary.Read(buf, binary.BigEndian, &array[j])
		}
		return array

	// === IntArray (V16 uses generic decodeSlice, V18 has no element type byte) ===
	case Int32SliceType:
		array, err := decodeSlice(buf)
		if err != nil {
			return nil
		}
		return array
	case V18_IntArray:
		// V18 uses CompressedInt for array length
		length, err := readCompressedInt(buf)
		if err != nil || length < 0 || length > 10000 {
			return nil
		}
		array := make([]int32, length)
		for j := 0; j < int(length); j++ {
			binary.Read(buf, binary.BigEndian, &array[j])
		}
		return array

	// === CompressedInt (varint) — V18 only ===
	case V18_CompressedInt:
		val, err := readCompressedInt(buf)
		if err != nil {
			return nil
		}
		// Return as int32 for compatibility with existing code
		return int32(val)

	// === CompressedLong (varint) — V18 only ===
	case V18_CompressedLong:
		val, err := readCompressedLong(buf)
		if err != nil {
			return nil
		}
		return int64(val)

	// === Slice / Array (V16 generic) ===
	case SliceType, ObjectSliceType, StringSliceType:
		array, err := decodeSlice(buf)
		if err != nil {
			return nil
		}
		return array

	// === ObjectArray (V18) — [CompressedInt length][type+value pairs] ===
	case V18_ObjectArray:
		lengthV, _ := readCompressedInt(buf)
		length := uint16(lengthV)
		array := make([]interface{}, length)
		for j := 0; j < int(length); j++ {
			var elemType uint8
			binary.Read(buf, binary.BigEndian, &elemType)
			if elemType&V18_NullFlag != 0 && isV18Type(elemType) {
				array[j] = nil
			} else {
				array[j] = decodeType(buf, elemType)
			}
		}
		return array

	// === Hashtable ===
	case Hashtable, V18_Hashtable:
		dict, err := decodeDictionaryType(buf)
		if err != nil {
			return nil
		}
		return dict

	// === Dictionary ===
	case DictionaryType, V18_Dictionary:
		dict, err := decodeDictionaryType(buf)
		if err != nil {
			return nil
		}
		return dict

	// === V18 Custom type ===
	case V18_Custom, Custom:
		// Read custom type code + data — skip for now
		buf.ReadByte() // custom type byte
		return nil
	}

	// Unknown type — return nil to avoid poisoning params
	return nil
}

func decodeSlice(buf *bytes.Buffer) (interface{}, error) {
	var length uint16
	var sliceType uint8

	binary.Read(buf, binary.BigEndian, &length)
	binary.Read(buf, binary.BigEndian, &sliceType)

	switch sliceType {
	case Float32Type, V18_Float:
		array := make([]float32, length)
		for j := 0; j < int(length); j++ {
			array[j] = decodeFloat32Type(buf)
		}
		return array, nil

	case Int32Type, V18_Int:
		array := make([]int32, length)
		for j := 0; j < int(length); j++ {
			array[j] = decodeInt32Type(buf)
		}
		return array, nil

	case Int16Type, V18_Short:
		array := make([]int16, length)
		for j := 0; j < int(length); j++ {
			var temp int16
			binary.Read(buf, binary.BigEndian, &temp)
			array[j] = temp
		}
		return array, nil

	case Int64Type, V18_Long:
		array := make([]int64, length)
		for j := 0; j < int(length); j++ {
			array[j] = decodeInt64Type(buf)
		}
		return array, nil

	case StringType, V18_String:
		array := make([]string, length)
		for j := 0; j < int(length); j++ {
			array[j] = decodeStringType(buf)
		}
		return array, nil

	case BooleanType, V18_Boolean:
		array := make([]bool, length)
		for j := 0; j < int(length); j++ {
			result, err := decodeBooleanType(buf)
			if err != nil {
				return array, err
			}
			array[j] = result
		}
		return array, nil

	case Int8SliceType, V18_ByteArray:
		array := make([][]int8, length)
		for j := 0; j < int(length); j++ {
			result, err := decodeSliceInt8Type(buf)
			if err != nil {
				return nil, err
			}
			array[j] = result
		}
		return array, nil

	case SliceType, V18_ObjectArray:
		array := make([]interface{}, length)
		for j := 0; j < int(length); j++ {
			subArray, err := decodeSlice(buf)
			if err != nil {
				return nil, err
			}
			array[j] = subArray
		}
		return array, nil

	case Int8Type, V18_Byte:
		array := make([]int8, length)
		for j := 0; j < int(length); j++ {
			array[j] = decodeInt8Type(buf)
		}
		return array, nil

	case V18_CompressedInt:
		array := make([]int32, length)
		for j := 0; j < int(length); j++ {
			val, err := readCompressedInt(buf)
			if err != nil {
				return array, err
			}
			array[j] = val
		}
		return array, nil

	case V18_CompressedLong:
		array := make([]int64, length)
		for j := 0; j < int(length); j++ {
			val, err := readCompressedLong(buf)
			if err != nil {
				return array, err
			}
			array[j] = val
		}
		return array, nil

	default:
		return nil, fmt.Errorf("unknown slice type %d (0x%02x)", sliceType, sliceType)
	}
}

func decodeInt8Type(buf *bytes.Buffer) (temp int8) {
	binary.Read(buf, binary.BigEndian, &temp)
	return
}

func decodeFloat32Type(buf *bytes.Buffer) (temp float32) {
	binary.Read(buf, binary.BigEndian, &temp)
	return
}

func decodeInt16Type(buf *bytes.Buffer) (temp int16) {
	binary.Read(buf, binary.BigEndian, &temp)
	return
}

func decodeInt32Type(buf *bytes.Buffer) (temp int32) {
	binary.Read(buf, binary.BigEndian, &temp)
	return
}

func decodeInt64Type(buf *bytes.Buffer) (temp int64) {
	binary.Read(buf, binary.BigEndian, &temp)
	return
}

func decodeStringType(buf *bytes.Buffer) string {
	var length uint16
	binary.Read(buf, binary.BigEndian, &length)
	strBytes := make([]byte, length)
	buf.Read(strBytes)
	return string(strBytes[:])
}

func decodeBooleanType(buf *bytes.Buffer) (bool, error) {
	var value uint8
	binary.Read(buf, binary.BigEndian, &value)
	if value == 0 {
		return false, nil
	} else if value == 1 {
		return true, nil
	}
	return false, fmt.Errorf("invalid boolean value %d", value)
}

func decodeSliceInt8Type(buf *bytes.Buffer) ([]int8, error) {
	var length uint32
	err := binary.Read(buf, binary.BigEndian, &length)
	if err != nil {
		return nil, err
	}
	array := make([]int8, length)
	for j := 0; j < int(length); j++ {
		var temp int8
		err := binary.Read(buf, binary.BigEndian, &temp)
		if err != nil {
			return nil, err
		}
		array[j] = temp
	}
	return array, nil
}

func decodeDictionaryType(buf *bytes.Buffer) (map[interface{}]interface{}, error) {
	var keyTypeCode uint8
	var valueTypeCode uint8
	var dictionarySize uint16

	err := binary.Read(buf, binary.BigEndian, &keyTypeCode)
	if err != nil {
		return nil, err
	}
	err = binary.Read(buf, binary.BigEndian, &valueTypeCode)
	if err != nil {
		return nil, err
	}
	err = binary.Read(buf, binary.BigEndian, &dictionarySize)
	if err != nil {
		return nil, err
	}

	dictionary := make(map[interface{}]interface{})
	for i := uint16(0); i < dictionarySize; i++ {
		key := decodeType(buf, keyTypeCode)
		value := decodeType(buf, valueTypeCode)
		dictionary[key] = value
	}

	return dictionary, nil
}

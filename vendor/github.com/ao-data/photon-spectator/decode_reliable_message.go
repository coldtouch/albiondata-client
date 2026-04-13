package photon_spectator

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
)

// === Protocol16 type codes (legacy — pre-April 2026) ===
const (
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
)

// === Protocol18 / GpBinaryV18 type codes (April 2026+) ===
// Source: ServerEmus/Plugin.Photon, disctty/FakePhoton
const (
	V18_Unknown         = 0
	V18_Boolean         = 2
	V18_Byte            = 3
	V18_Short           = 4  // int16
	V18_Float           = 5
	V18_Double          = 6
	V18_String          = 7
	V18_Null            = 8
	V18_CompressedInt   = 9  // zigzag varint int32
	V18_CompressedLong  = 10 // zigzag varint int64
	V18_Int1            = 11 // positive int, 1 byte
	V18_Int1Neg         = 12 // negative int, 1 byte
	V18_Int2            = 13 // positive int, 2 bytes LE
	V18_Int2Neg         = 14 // negative int, 2 bytes LE
	V18_Long1           = 15 // positive long, 1 byte
	V18_Long1Neg        = 16 // negative long, 1 byte
	V18_Long2           = 17 // positive long, 2 bytes LE
	V18_Long2Neg        = 18 // negative long, 2 bytes LE
	V18_Custom          = 19
	V18_Dictionary      = 20
	V18_Hashtable       = 21
	V18_ObjectArray     = 23
	V18_OpRequest       = 24
	V18_OpResponse      = 25
	V18_EventData       = 26
	V18_BoolFalse       = 27 // no payload
	V18_BoolTrue        = 28 // no payload
	V18_ShortZero       = 29 // no payload
	V18_IntZero         = 30 // no payload
	V18_LongZero        = 31 // no payload
	V18_FloatZero       = 32 // no payload
	V18_DoubleZero      = 33 // no payload
	V18_ByteZero        = 34 // no payload
	V18_Array           = 64 // array-in-array
	V18_BoolArray       = 66 // bit-packed
	V18_ByteArray       = 67
	V18_ShortArray      = 68
	V18_FloatArray      = 69
	V18_DoubleArray     = 70
	V18_StringArray     = 71
	V18_CompIntArray    = 73
	V18_CompLongArray   = 74
	V18_CustomArray     = 83
	V18_DictArray       = 84
	V18_HashArray       = 85
)

type ReliableMessageParameters map[uint8]interface{}
type ReliableMessageParamaters = ReliableMessageParameters // Deprecated

// --- Varint helpers ---

func readCompressedUInt32(buf *bytes.Buffer) (uint32, error) {
	var result uint32
	for shift := uint(0); shift < 35; shift += 7 {
		b, err := buf.ReadByte()
		if err != nil {
			return 0, err
		}
		result |= uint32(b&0x7F) << shift
		if b&0x80 == 0 {
			return result, nil
		}
	}
	return result, fmt.Errorf("varint too long")
}

func readCompressedUInt64(buf *bytes.Buffer) (uint64, error) {
	var result uint64
	for shift := uint(0); shift < 70; shift += 7 {
		b, err := buf.ReadByte()
		if err != nil {
			return 0, err
		}
		result |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return result, nil
		}
	}
	return result, fmt.Errorf("varint too long")
}

func decodeZigZag32(v uint32) int32 {
	return int32((v >> 1) ^ -(v & 1))
}

func decodeZigZag64(v uint64) int64 {
	return int64((v >> 1) ^ -(v & 1))
}

func readCompressedInt32(buf *bytes.Buffer) (int32, error) {
	v, err := readCompressedUInt32(buf)
	if err != nil {
		return 0, err
	}
	return decodeZigZag32(v), nil
}

func readCompressedInt64(buf *bytes.Buffer) (int64, error) {
	v, err := readCompressedUInt64(buf)
	if err != nil {
		return 0, err
	}
	return decodeZigZag64(v), nil
}

// --- Main decoder ---

func DecodeReliableMessage(msg ReliableMessage) (params ReliableMessageParameters) {
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

		paramID, _ := buf.ReadByte()
		paramType, _ := buf.ReadByte()

		// V18 CustomTypeSlim (128+)
		if paramType >= 128 {
			// Custom type slim — read CompressedUInt32 body length, skip body
			bodyLen, err := readCompressedUInt32(buf)
			if err == nil && bodyLen > 0 && int(bodyLen) <= buf.Len() {
				buf.Next(int(bodyLen))
			}
			params[paramID] = nil
			continue
		}

		params[paramID] = readV18Value(buf, paramType)
	}

	return params
}

// readV18Value reads a single value based on V18 type code
func readV18Value(buf *bytes.Buffer, gpType uint8) interface{} {
	switch gpType {

	// === Null / Unknown ===
	case V18_Null, V18_Unknown, NilType:
		return nil

	// === Zero-value types (no payload) ===
	case V18_BoolFalse:
		return false
	case V18_BoolTrue:
		return true
	case V18_ShortZero:
		return int16(0)
	case V18_IntZero:
		return int32(0)
	case V18_LongZero:
		return int64(0)
	case V18_FloatZero:
		return float32(0)
	case V18_DoubleZero:
		return float64(0)
	case V18_ByteZero:
		return int8(0)

	// === Boolean ===
	case V18_Boolean, BooleanType:
		b, err := buf.ReadByte()
		if err != nil {
			return false
		}
		return b != 0

	// === Byte / Int8 ===
	case V18_Byte, Int8Type:
		var v int8
		binary.Read(buf, binary.BigEndian, &v)
		return v

	// === Short / Int16 ===
	case V18_Short:
		var v int16
		binary.Read(buf, binary.LittleEndian, &v) // V18 uses LE for shorts
		return v
	case Int16Type:
		var v int16
		binary.Read(buf, binary.BigEndian, &v) // V16 uses BE
		return v

	// === Float ===
	case V18_Float, Float32Type:
		var v float32
		binary.Read(buf, binary.BigEndian, &v)
		return v

	// === Double ===
	case V18_Double, DoubleType:
		var v float64
		binary.Read(buf, binary.BigEndian, &v)
		return v

	// === String ===
	case V18_String, StringType:
		length, err := readCompressedUInt32(buf)
		if err != nil || length > 65535 {
			return ""
		}
		strBytes := make([]byte, length)
		buf.Read(strBytes)
		return string(strBytes)

	// === CompressedInt (zigzag varint int32) ===
	case V18_CompressedInt, Int32Type:
		v, err := readCompressedInt32(buf)
		if err != nil {
			return int32(0)
		}
		return v

	// === CompressedLong (zigzag varint int64) ===
	case V18_CompressedLong, Int64Type:
		v, err := readCompressedInt64(buf)
		if err != nil {
			return int64(0)
		}
		return v

	// === Int1: positive int, 1 byte ===
	case V18_Int1:
		b, _ := buf.ReadByte()
		return int32(b)

	// === Int1_: negative int, 1 byte ===
	case V18_Int1Neg:
		b, _ := buf.ReadByte()
		return -int32(b)

	// === Int2: positive int, 2 bytes LE ===
	case V18_Int2:
		var v uint16
		binary.Read(buf, binary.LittleEndian, &v)
		return int32(v)

	// === Int2_: negative int, 2 bytes LE ===
	case V18_Int2Neg:
		var v uint16
		binary.Read(buf, binary.LittleEndian, &v)
		return -int32(v)

	// === Long1: positive long, 1 byte ===
	case V18_Long1:
		b, _ := buf.ReadByte()
		return int64(b)

	// === Long1_: negative long, 1 byte ===
	case V18_Long1Neg:
		b, _ := buf.ReadByte()
		return -int64(b)

	// === Long2: positive long, 2 bytes LE ===
	case V18_Long2:
		var v uint16
		binary.Read(buf, binary.LittleEndian, &v)
		return int64(v)

	// === Long2_: negative long, 2 bytes LE ===
	case V18_Long2Neg:
		var v uint16
		binary.Read(buf, binary.LittleEndian, &v)
		return -int64(v)

	// === ByteArray ===
	case V18_ByteArray, Int8SliceType:
		length, err := readCompressedUInt32(buf)
		if err != nil || length > 1000000 {
			return nil
		}
		array := make([]int8, length)
		for j := uint32(0); j < length; j++ {
			binary.Read(buf, binary.BigEndian, &array[j])
		}
		return array

	// === ShortArray ===
	case V18_ShortArray:
		length, err := readCompressedUInt32(buf)
		if err != nil || length > 100000 {
			return nil
		}
		array := make([]int16, length)
		for j := uint32(0); j < length; j++ {
			binary.Read(buf, binary.LittleEndian, &array[j]) // V18 LE shorts
		}
		return array

	// === FloatArray ===
	case V18_FloatArray:
		length, err := readCompressedUInt32(buf)
		if err != nil || length > 100000 {
			return nil
		}
		array := make([]float32, length)
		for j := uint32(0); j < length; j++ {
			binary.Read(buf, binary.BigEndian, &array[j])
		}
		return array

	// === DoubleArray ===
	case V18_DoubleArray:
		length, err := readCompressedUInt32(buf)
		if err != nil || length > 100000 {
			return nil
		}
		array := make([]float64, length)
		for j := uint32(0); j < length; j++ {
			binary.Read(buf, binary.BigEndian, &array[j])
		}
		return array

	// === StringArray ===
	case V18_StringArray, StringSliceType:
		length, err := readCompressedUInt32(buf)
		if err != nil || length > 10000 {
			return nil
		}
		array := make([]string, length)
		for j := uint32(0); j < length; j++ {
			sLen, err := readCompressedUInt32(buf)
			if err != nil {
				break
			}
			sb := make([]byte, sLen)
			buf.Read(sb)
			array[j] = string(sb)
		}
		return array

	// === CompressedIntArray ===
	case V18_CompIntArray:
		length, err := readCompressedUInt32(buf)
		if err != nil || length > 100000 {
			return nil
		}
		array := make([]int32, length)
		for j := uint32(0); j < length; j++ {
			v, err := readCompressedInt32(buf)
			if err != nil {
				break
			}
			array[j] = v
		}
		return array

	// === CompressedLongArray ===
	case V18_CompLongArray:
		length, err := readCompressedUInt32(buf)
		if err != nil || length > 100000 {
			return nil
		}
		array := make([]int64, length)
		for j := uint32(0); j < length; j++ {
			v, err := readCompressedInt64(buf)
			if err != nil {
				break
			}
			array[j] = v
		}
		return array

	// === BooleanArray (bit-packed) ===
	case V18_BoolArray:
		length, err := readCompressedUInt32(buf)
		if err != nil || length > 100000 {
			return nil
		}
		array := make([]bool, length)
		idx := uint32(0)
		byteCount := length / 8
		for i := uint32(0); i < byteCount; i++ {
			b, _ := buf.ReadByte()
			for j := 0; j < 8 && idx < length; j++ {
				array[idx] = (b & (1 << uint(j))) != 0
				idx++
			}
		}
		if idx < length {
			b, _ := buf.ReadByte()
			for j := 0; idx < length; j++ {
				array[idx] = (b & (1 << uint(j))) != 0
				idx++
			}
		}
		return array

	// === ObjectArray ===
	case V18_ObjectArray:
		length, err := readCompressedUInt32(buf)
		if err != nil || length > 10000 {
			return nil
		}
		array := make([]interface{}, length)
		for j := uint32(0); j < length; j++ {
			elemType, err := buf.ReadByte()
			if err != nil {
				break
			}
			if elemType >= 128 {
				// CustomTypeSlim — skip
				bodyLen, _ := readCompressedUInt32(buf)
				if bodyLen > 0 && int(bodyLen) <= buf.Len() {
					buf.Next(int(bodyLen))
				}
				array[j] = nil
			} else {
				array[j] = readV18Value(buf, elemType)
			}
		}
		return array

	// === Hashtable: [CompressedUInt32 count] [type+key, type+value] * count ===
	case V18_Hashtable, Hashtable:
		count, err := readCompressedUInt32(buf)
		if err != nil || count > 10000 {
			return nil
		}
		dict := make(map[interface{}]interface{})
		for j := uint32(0); j < count; j++ {
			keyType, err := buf.ReadByte()
			if err != nil {
				break
			}
			key := readV18Value(buf, keyType)
			valType, err := buf.ReadByte()
			if err != nil {
				break
			}
			val := readV18Value(buf, valType)
			dict[key] = val
		}
		return dict

	// === Dictionary: [keyType] [valType] [CompressedUInt32 count] [entries] ===
	case V18_Dictionary:
		keyTypeCode, _ := buf.ReadByte()
		valTypeCode, _ := buf.ReadByte()
		count, err := readCompressedUInt32(buf)
		if err != nil || count > 10000 {
			return nil
		}
		dict := make(map[interface{}]interface{})
		for j := uint32(0); j < count; j++ {
			var key, val interface{}
			if keyTypeCode == 0 {
				kt, _ := buf.ReadByte()
				key = readV18Value(buf, kt)
			} else {
				key = readV18Value(buf, keyTypeCode)
			}
			if valTypeCode == 0 {
				vt, _ := buf.ReadByte()
				val = readV18Value(buf, vt)
			} else {
				val = readV18Value(buf, valTypeCode)
			}
			dict[key] = val
		}
		return dict

	// === V18 Array (nested array) ===
	case V18_Array:
		length, err := readCompressedUInt32(buf)
		if err != nil || length > 10000 {
			return nil
		}
		elemType, _ := buf.ReadByte()
		array := make([]interface{}, length)
		for j := uint32(0); j < length; j++ {
			array[j] = readV18Value(buf, elemType)
		}
		return array

	// === Custom type ===
	case V18_Custom, Custom:
		buf.ReadByte() // custom type code
		bodyLen, _ := readCompressedUInt32(buf)
		if bodyLen > 0 && int(bodyLen) <= buf.Len() {
			buf.Next(int(bodyLen))
		}
		return nil

	// === Legacy Protocol16 Slice (generic) ===
	case Int32SliceType, ObjectSliceType, SliceType:
		return readP16Slice(buf)
	}

	// Unknown type — return nil
	_ = math.MaxInt8 // keep math import used
	return nil
}

// readP16Slice reads a Protocol16 style slice: [uint16 length][uint8 elemType][data]
func readP16Slice(buf *bytes.Buffer) interface{} {
	var length uint16
	var sliceType uint8
	binary.Read(buf, binary.BigEndian, &length)
	binary.Read(buf, binary.BigEndian, &sliceType)

	switch sliceType {
	case Float32Type, V18_Float:
		a := make([]float32, length)
		for j := 0; j < int(length); j++ {
			binary.Read(buf, binary.BigEndian, &a[j])
		}
		return a
	case Int32Type, V18_CompressedInt:
		a := make([]int32, length)
		for j := 0; j < int(length); j++ {
			binary.Read(buf, binary.BigEndian, &a[j])
		}
		return a
	case Int16Type, V18_Short:
		a := make([]int16, length)
		for j := 0; j < int(length); j++ {
			binary.Read(buf, binary.BigEndian, &a[j])
		}
		return a
	case Int64Type, V18_CompressedLong:
		a := make([]int64, length)
		for j := 0; j < int(length); j++ {
			binary.Read(buf, binary.BigEndian, &a[j])
		}
		return a
	case StringType, V18_String:
		a := make([]string, length)
		for j := 0; j < int(length); j++ {
			var sLen uint16
			binary.Read(buf, binary.BigEndian, &sLen)
			sb := make([]byte, sLen)
			buf.Read(sb)
			a[j] = string(sb)
		}
		return a
	case BooleanType, V18_Boolean:
		a := make([]bool, length)
		for j := 0; j < int(length); j++ {
			b, _ := buf.ReadByte()
			a[j] = b != 0
		}
		return a
	case Int8SliceType, V18_ByteArray:
		a := make([][]int8, length)
		for j := 0; j < int(length); j++ {
			var bLen uint32
			binary.Read(buf, binary.BigEndian, &bLen)
			arr := make([]int8, bLen)
			for k := uint32(0); k < bLen; k++ {
				binary.Read(buf, binary.BigEndian, &arr[k])
			}
			a[j] = arr
		}
		return a
	case Int8Type, V18_Byte:
		a := make([]int8, length)
		for j := 0; j < int(length); j++ {
			binary.Read(buf, binary.BigEndian, &a[j])
		}
		return a
	default:
		return nil
	}
}

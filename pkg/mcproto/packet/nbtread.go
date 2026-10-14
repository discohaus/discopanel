package packet

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// Guards hostile nesting depth
const maxNBTDepth = 64

// Reads one nameless network nbt value
func ReadNetworkNBT(r io.Reader) (any, error) {
	var t [1]byte
	if _, err := io.ReadFull(r, t[:]); err != nil {
		return nil, err
	}
	if t[0] == tagEnd {
		return nil, nil
	}
	return readNBTPayload(r, t[0], 0)
}

// Reads one payload for a known tag type
func readNBTPayload(r io.Reader, tagType byte, depth int) (any, error) {
	if depth > maxNBTDepth {
		return nil, fmt.Errorf("nbt too deep")
	}
	switch tagType {
	case tagByte:
		var v int8
		err := binary.Read(r, binary.BigEndian, &v)
		return v, err
	case tagShort:
		var v int16
		err := binary.Read(r, binary.BigEndian, &v)
		return v, err
	case tagInt:
		var v int32
		err := binary.Read(r, binary.BigEndian, &v)
		return v, err
	case tagLong:
		var v int64
		err := binary.Read(r, binary.BigEndian, &v)
		return v, err
	case tagFloat:
		var v uint32
		if err := binary.Read(r, binary.BigEndian, &v); err != nil {
			return nil, err
		}
		return math.Float32frombits(v), nil
	case tagDouble:
		var v uint64
		if err := binary.Read(r, binary.BigEndian, &v); err != nil {
			return nil, err
		}
		return math.Float64frombits(v), nil
	case tagString:
		return readNBTString(r)
	case tagByteArray:
		var length int32
		if err := binary.Read(r, binary.BigEndian, &length); err != nil {
			return nil, err
		}
		if length < 0 || length > MaxFrameLength {
			return nil, fmt.Errorf("nbt byte array %d out of bounds", length)
		}
		buf := make([]byte, length)
		_, err := io.ReadFull(r, buf)
		return buf, err
	case tagIntArray:
		var length int32
		if err := binary.Read(r, binary.BigEndian, &length); err != nil {
			return nil, err
		}
		if length < 0 || length > 1<<20 {
			return nil, fmt.Errorf("nbt int array %d out of bounds", length)
		}
		out := make([]int32, length)
		for i := range out {
			if err := binary.Read(r, binary.BigEndian, &out[i]); err != nil {
				return nil, err
			}
		}
		return out, nil
	case tagLongArray:
		var length int32
		if err := binary.Read(r, binary.BigEndian, &length); err != nil {
			return nil, err
		}
		if length < 0 || length > 1<<20 {
			return nil, fmt.Errorf("nbt long array %d out of bounds", length)
		}
		out := make([]int64, length)
		for i := range out {
			if err := binary.Read(r, binary.BigEndian, &out[i]); err != nil {
				return nil, err
			}
		}
		return out, nil
	case tagList:
		var elem [1]byte
		if _, err := io.ReadFull(r, elem[:]); err != nil {
			return nil, err
		}
		var length int32
		if err := binary.Read(r, binary.BigEndian, &length); err != nil {
			return nil, err
		}
		if length < 0 || length > 1<<20 {
			return nil, fmt.Errorf("nbt list %d out of bounds", length)
		}
		out := make([]any, 0, length)
		for range length {
			v, err := readNBTPayload(r, elem[0], depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case tagCompound:
		out := map[string]any{}
		for {
			var t [1]byte
			if _, err := io.ReadFull(r, t[:]); err != nil {
				return nil, err
			}
			if t[0] == tagEnd {
				return out, nil
			}
			name, err := readNBTString(r)
			if err != nil {
				return nil, err
			}
			v, err := readNBTPayload(r, t[0], depth+1)
			if err != nil {
				return nil, err
			}
			out[name] = v
		}
	default:
		return nil, fmt.Errorf("unknown nbt tag %d", tagType)
	}
}

// Reads a short prefixed nbt string
func readNBTString(r io.Reader) (string, error) {
	var length uint16
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return "", err
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// Plain text pulled out of a component value
func ComponentText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		out := ""
		if s, ok := t["text"].(string); ok {
			out = s
		} else if s, ok := t["translate"].(string); ok {
			out = s
		}
		if extra, ok := t["extra"].([]any); ok {
			for _, e := range extra {
				out += ComponentText(e)
			}
		}
		return out
	case []any:
		out := ""
		for _, e := range t {
			out += ComponentText(e)
		}
		return out
	default:
		return ""
	}
}

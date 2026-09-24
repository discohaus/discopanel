package packet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Converts one vanilla json payload into nbt
// Field order follows the json text exactly
func JSONToNBT(data []byte) (Tag, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tag, err := jsonValue(dec)
	if err != nil {
		return nil, err
	}
	return tag, nil
}

// Reads one json value as its nbt shape
func jsonValue(dec *json.Decoder) (Tag, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return jsonToken(dec, tok)
}

// Turns one decoded token into an nbt tag
func jsonToken(dec *json.Decoder, tok json.Token) (Tag, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			out := NBTCompound{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("json key %v not a string", keyTok)
				}
				value, err := jsonValue(dec)
				if err != nil {
					return nil, err
				}
				out = append(out, NBTField{Name: key, Tag: value})
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return out, nil
		case '[':
			out := NBTList{}
			for dec.More() {
				value, err := jsonValue(dec)
				if err != nil {
					return nil, err
				}
				out.Elem = append(out.Elem, value)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return out, nil
		}
		return nil, fmt.Errorf("unexpected json delim %v", t)

	case string:
		return NBTString(t), nil

	case bool:
		if t {
			return NBTByte(1), nil
		}
		return NBTByte(0), nil

	case json.Number:
		text := t.String()
		if strings.ContainsAny(text, ".eE") {
			f, err := t.Float64()
			if err != nil {
				return nil, err
			}
			return NBTDouble(f), nil
		}
		n, err := t.Int64()
		if err != nil {
			return nil, err
		}
		if n >= -1<<31 && n < 1<<31 {
			return NBTInt(int32(n)), nil
		}
		return NBTLong(n), nil

	default:
		return nil, fmt.Errorf("json value %v has no nbt shape", tok)
	}
}

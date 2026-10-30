// Package packet holds shared minecraft wire primitives
package packet

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/discohaus/discopanel/pkg/mcproto"
)

// Caps any single string read
const maxStringBytes = 1 << 20

// Writes a varint prefixed utf8 string
func WriteString(w io.Writer, s string) error {
	if err := mcproto.WriteVarInt(w, mcproto.VarInt(len(s))); err != nil {
		return err
	}
	_, err := w.Write([]byte(s))
	return err
}

// Reads a varint prefixed utf8 string
func ReadString(r io.Reader) (string, error) {
	return mcproto.ReadString(r, maxStringBytes)
}

// Writes a two byte big endian length then bytes
func WriteShortBytes(w io.Writer, b []byte) error {
	if len(b) > math.MaxUint16 {
		return fmt.Errorf("short byte array too long: %d", len(b))
	}
	if err := binary.Write(w, binary.BigEndian, uint16(len(b))); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// Reads a two byte big endian length then bytes
func ReadShortBytes(r io.Reader, max int) ([]byte, error) {
	var length uint16
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return nil, err
	}
	if int(length) > max {
		return nil, fmt.Errorf("short byte array too long: %d", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// Writes a varint prefixed byte array
func WriteVarBytes(w io.Writer, b []byte) error {
	if err := mcproto.WriteVarInt(w, mcproto.VarInt(len(b))); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// Reads a varint prefixed byte array
func ReadVarBytes(r io.Reader, max int) ([]byte, error) {
	length, err := mcproto.ReadVarInt(r)
	if err != nil {
		return nil, err
	}
	if length < 0 || int(length) > max {
		return nil, fmt.Errorf("byte array length %d out of bounds", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// Writes one bool as a single byte
func WriteBool(w io.Writer, v bool) error {
	b := byte(0)
	if v {
		b = 1
	}
	_, err := w.Write([]byte{b})
	return err
}

// Reads one bool from a single byte
func ReadBool(r io.Reader) (bool, error) {
	var buf [1]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return false, err
	}
	return buf[0] != 0, nil
}

// Writes a sixteen byte uuid
func WriteUUID(w io.Writer, id [16]byte) error {
	_, err := w.Write(id[:])
	return err
}

// Reads a sixteen byte uuid
func ReadUUID(r io.Reader) ([16]byte, error) {
	var id [16]byte
	_, err := io.ReadFull(r, id[:])
	return id, err
}

// Writes any fixed size big endian number
func WriteNum(w io.Writer, v any) error {
	return binary.Write(w, binary.BigEndian, v)
}

// Reads any fixed size big endian number
func ReadNum(r io.Reader, v any) error {
	return binary.Read(r, binary.BigEndian, v)
}

// Writes a variable length long
func WriteVarLong(w io.Writer, value int64) error {
	v := uint64(value)
	for {
		if v&^uint64(0x7F) == 0 {
			_, err := w.Write([]byte{byte(v)})
			return err
		}
		if _, err := w.Write([]byte{byte((v & 0x7F) | 0x80)}); err != nil {
			return err
		}
		v >>= 7
	}
}

// Reads a variable length long
func ReadVarLong(r io.Reader) (int64, error) {
	var value uint64
	var position uint
	var buf [1]byte
	for {
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return 0, err
		}
		value |= uint64(buf[0]&0x7F) << position
		if buf[0]&0x80 == 0 {
			return int64(value), nil
		}
		position += 7
		if position >= 64 {
			return 0, fmt.Errorf("varlong too big")
		}
	}
}

// Degrees folded onto the wire angle byte
func Angle(degrees float32) byte {
	return byte(math.Round(float64(degrees) * 256.0 / 360.0))
}

// Packs block coordinates the pre 1.14 way
func PositionOld(x, y, z int) uint64 {
	return (uint64(x)&0x3FFFFFF)<<38 | (uint64(y)&0xFFF)<<26 | (uint64(z) & 0x3FFFFFF)
}

// Packs block coordinates the 1.14 plus way
func PositionNew(x, y, z int) uint64 {
	return (uint64(x)&0x3FFFFFF)<<38 | (uint64(z)&0x3FFFFFF)<<12 | (uint64(y) & 0xFFF)
}

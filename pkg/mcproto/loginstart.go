package mcproto

import (
	"bytes"
	"fmt"
	"io"
)

// Caps a login start frame against garbage floods
const maxLoginStartLength = 4096

// Caps the name field, vanilla allows sixteen
const maxNameBytes = 64

// Leading login packet with its raw body kept
type LoginStart struct {
	Name string
	body []byte
}

// Reads the login start packet after a handshake
func ReadLoginStart(r io.Reader) (*LoginStart, error) {
	length, err := ReadVarInt(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read login start length: %w", err)
	}
	if length < 2 || length > maxLoginStartLength {
		return nil, fmt.Errorf("invalid login start length: %d", length)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("failed to read login start body: %w", err)
	}

	buf := bytes.NewReader(body)
	packetID, err := ReadVarInt(buf)
	if err != nil {
		return nil, fmt.Errorf("failed to read login start id: %w", err)
	}
	if packetID != 0x00 {
		return nil, fmt.Errorf("expected login start (0x00), got %d", packetID)
	}

	name, err := ReadString(buf, maxNameBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read player name: %w", err)
	}
	if name == "" {
		return nil, fmt.Errorf("empty player name")
	}

	return &LoginStart{Name: name, body: body}, nil
}

// Replays the original frame toward a backend
func (l *LoginStart) Replay(w io.Writer) error {
	return WriteFramed(w, l.body)
}

// Reads a varint prefixed utf8 string
func ReadString(r io.Reader, max int) (string, error) {
	length, err := ReadVarInt(r)
	if err != nil {
		return "", err
	}
	if length < 0 || int(length) > max {
		return "", fmt.Errorf("string length %d out of bounds", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

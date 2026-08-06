// Package mcproto implements the Minecraft handshake wire codec
package mcproto

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Bounds handshake size, generous for modded Forge FML data
const MaxHandshakeLength = 2048

// First byte of a legacy pre-1.7 server list ping
const LegacyPingByte = 0xFE

// Reads and writes Minecraft's variable-length integers
type VarInt int32

// Reads a variable-length integer from the reader
func ReadVarInt(r io.Reader) (VarInt, error) {
	var value int32
	var position int

	for {
		b, err := readByte(r)
		if err != nil {
			return 0, fmt.Errorf("failed to read varint byte: %w", err)
		}

		value |= int32(b&0x7F) << position

		if b&0x80 == 0 {
			break
		}

		position += 7

		if position >= 32 {
			return 0, fmt.Errorf("VarInt is too big")
		}
	}

	return VarInt(value), nil
}

// Reads one byte, using ByteReader fast path if available
func readByte(r io.Reader) (byte, error) {
	if br, ok := r.(io.ByteReader); ok {
		return br.ReadByte()
	}
	var buf [1]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return buf[0], nil
}

// Writes a variable-length integer, unsigned shift handles negatives
func WriteVarInt(w io.Writer, value VarInt) error {
	v := uint32(value)
	for {
		if v&^uint32(0x7F) == 0 {
			return binary.Write(w, binary.BigEndian, byte(v))
		}

		if err := binary.Write(w, binary.BigEndian, byte((v&0x7F)|0x80)); err != nil {
			return err
		}

		v >>= 7
	}
}

// Returns encoded length, unsigned shift handles negatives
func (v VarInt) Len() int {
	value := uint32(v)
	length := 0
	for {
		length++
		if value&^uint32(0x7F) == 0 {
			break
		}
		value >>= 7
	}
	return length
}

// Handshake next-state values
const (
	NextStateStatus = 1
	NextStateLogin  = 2
)

// Represents the initial handshake packet from a client
type HandshakePacket struct {
	ProtocolVersion VarInt
	ServerAddress   string
	ServerPort      uint16
	NextState       VarInt // 1 for status, 2 for login
}

// Reads a handshake packet from the connection
func ReadHandshakePacket(r io.Reader) (*HandshakePacket, error) {
	length, err := ReadVarInt(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read packet length: %w", err)
	}

	if length < 1 || length > MaxHandshakeLength {
		return nil, fmt.Errorf("invalid handshake length: %d", length)
	}

	data := make([]byte, length)
	n, err := io.ReadFull(r, data)
	if err != nil {
		return nil, fmt.Errorf("failed to read packet data (got %d/%d bytes): %w", n, length, err)
	}

	buf := bytes.NewReader(data)

	packetID, err := ReadVarInt(buf)
	if err != nil {
		return nil, fmt.Errorf("failed to read packet ID: %w", err)
	}

	if packetID != 0x00 {
		return nil, fmt.Errorf("expected handshake packet (0x00), got %d", packetID)
	}

	packet := &HandshakePacket{}

	packet.ProtocolVersion, err = ReadVarInt(buf)
	if err != nil {
		return nil, fmt.Errorf("failed to read protocol version: %w", err)
	}

	addressLen, err := ReadVarInt(buf)
	if err != nil {
		return nil, fmt.Errorf("failed to read address length: %w", err)
	}
	if addressLen < 0 || int(addressLen) > buf.Len() {
		return nil, fmt.Errorf("invalid address length: %d", addressLen)
	}

	addressBytes := make([]byte, addressLen)
	if _, err := io.ReadFull(buf, addressBytes); err != nil {
		return nil, fmt.Errorf("failed to read address: %w", err)
	}
	packet.ServerAddress = string(addressBytes)

	if err := binary.Read(buf, binary.BigEndian, &packet.ServerPort); err != nil {
		return nil, fmt.Errorf("failed to read port: %w", err)
	}

	packet.NextState, err = ReadVarInt(buf)
	if err != nil {
		return nil, fmt.Errorf("failed to read next state: %w", err)
	}

	return packet, nil
}

// Writes a handshake packet to the connection
func WriteHandshakePacket(w io.Writer, packet *HandshakePacket) error {
	var buf bytes.Buffer

	if err := WriteVarInt(&buf, 0x00); err != nil {
		return err
	}
	if err := WriteVarInt(&buf, packet.ProtocolVersion); err != nil {
		return err
	}
	if err := WriteVarInt(&buf, VarInt(len(packet.ServerAddress))); err != nil {
		return err
	}
	if _, err := buf.WriteString(packet.ServerAddress); err != nil {
		return err
	}
	if err := binary.Write(&buf, binary.BigEndian, packet.ServerPort); err != nil {
		return err
	}
	if err := WriteVarInt(&buf, packet.NextState); err != nil {
		return err
	}

	return WriteFramed(w, buf.Bytes())
}

// Writes a length-prefixed Minecraft packet
func WriteFramed(w io.Writer, data []byte) error {
	var buf bytes.Buffer
	if err := WriteVarInt(&buf, VarInt(len(data))); err != nil {
		return err
	}
	buf.Write(data)
	_, err := w.Write(buf.Bytes())
	return err
}

// Pulls the hostname out of a 1.6 ping payload
func LegacyPingHostname(raw []byte) (string, bool) {
	if len(raw) < 3 || raw[0] != LegacyPingByte || raw[1] != 0x01 || raw[2] != 0xFA {
		return "", false
	}
	buf := raw[3:]
	if len(buf) < 2 {
		return "", false
	}
	channelLen := int(binary.BigEndian.Uint16(buf)) * 2
	buf = buf[2:]
	if len(buf) < channelLen+3 {
		return "", false
	}
	buf = buf[channelLen+3:]
	if len(buf) < 2 {
		return "", false
	}
	hostLen := int(binary.BigEndian.Uint16(buf)) * 2
	buf = buf[2:]
	if len(buf) < hostLen {
		return "", false
	}
	return decodeUTF16BE(buf[:hostLen]), true
}

// Decodes big endian UTF-16 bytes into a string
func decodeUTF16BE(b []byte) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, binary.BigEndian.Uint16(b[i:]))
	}
	return string(utf16.Decode(units))
}

// Sends the pre-1.7 kick packet carrying status fields
func WriteLegacyKick(w io.Writer, modern bool, motd, version string, maxPlayers int) error {
	var payload string
	if modern {
		payload = strings.Join([]string{"§1", "127", version, motd, "0", strconv.Itoa(maxPlayers)}, "\x00")
	} else {
		payload = strings.ReplaceAll(motd, "§", "") + "§0§" + strconv.Itoa(maxPlayers)
	}

	units := utf16.Encode([]rune(payload))
	packet := make([]byte, 3+2*len(units))
	packet[0] = 0xFF
	binary.BigEndian.PutUint16(packet[1:3], uint16(len(units)))
	for i, u := range units {
		binary.BigEndian.PutUint16(packet[3+2*i:], u)
	}
	_, err := w.Write(packet)
	return err
}

// Sends a login disconnect with a chat-component reason
func WriteLoginDisconnect(w io.Writer, message string) error {
	reason, err := json.Marshal(map[string]string{"text": message})
	if err != nil {
		return err
	}
	return WriteLoginDisconnectJSON(w, reason)
}

// Sends a login disconnect with prebuilt reason json
func WriteLoginDisconnectJSON(w io.Writer, reason []byte) error {
	var payload bytes.Buffer
	if err := WriteVarInt(&payload, 0x00); err != nil {
		return err
	}
	if err := WriteVarInt(&payload, VarInt(len(reason))); err != nil {
		return err
	}
	payload.Write(reason)
	return WriteFramed(w, payload.Bytes())
}

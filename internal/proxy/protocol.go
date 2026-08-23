package proxy

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// VarInt reads/writes variable-length integers as used in Minecraft protocol
type VarInt int32

// ReadVarInt reads a variable-length integer from the reader
func ReadVarInt(r io.Reader) (VarInt, error) {
	var value int32
	var position int
	var currentByte byte

	for {
		buf := make([]byte, 1)
		n, err := r.Read(buf)
		if err != nil {
			return 0, fmt.Errorf("failed to read varint byte: %w", err)
		}
		if n != 1 {
			return 0, fmt.Errorf("failed to read full byte")
		}
		currentByte = buf[0]

		value |= int32(currentByte&0x7F) << position

		if currentByte&0x80 == 0 {
			break
		}

		position += 7

		if position >= 32 {
			return 0, fmt.Errorf("VarInt is too big")
		}
	}

	return VarInt(value), nil
}

// WriteVarInt writes a variable-length integer to the writer
func WriteVarInt(w io.Writer, value VarInt) error {
	for {
		if (value & ^0x7F) == 0 {
			return binary.Write(w, binary.BigEndian, byte(value))
		}

		if err := binary.Write(w, binary.BigEndian, byte((value&0x7F)|0x80)); err != nil {
			return err
		}

		value >>= 7
	}
}

func readPacket(r io.Reader) (VarInt, []byte, error) {
	length, err := ReadVarInt(r)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to read packet length: %w", err)
	}
	if length < 1 || length > 1<<20 {
		return 0, nil, fmt.Errorf("invalid packet length: %d", length)
	}

	data := make([]byte, int(length))
	if _, err := io.ReadFull(r, data); err != nil {
		return 0, nil, fmt.Errorf("failed to read packet data: %w", err)
	}

	buf := bytes.NewReader(data)
	packetID, err := ReadVarInt(buf)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to read packet ID: %w", err)
	}

	payload, err := io.ReadAll(buf)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to read packet payload: %w", err)
	}

	return packetID, payload, nil
}

func writePacket(w io.Writer, packetID VarInt, payload []byte) error {
	var packet bytes.Buffer
	if err := WriteVarInt(&packet, packetID); err != nil {
		return err
	}
	if _, err := packet.Write(payload); err != nil {
		return err
	}

	if err := WriteVarInt(w, VarInt(packet.Len())); err != nil {
		return err
	}
	_, err := w.Write(packet.Bytes())
	return err
}

func writeString(w io.Writer, value string) error {
	if err := WriteVarInt(w, VarInt(len(value))); err != nil {
		return err
	}
	_, err := io.WriteString(w, value)
	return err
}

// Len returns the length of the VarInt when encoded
func (v VarInt) Len() int {
	value := int32(v)
	length := 0
	for {
		length++
		if (value & ^0x7F) == 0 {
			break
		}
		value >>= 7
	}
	return length
}

// HandshakePacket represents the initial handshake packet from a Minecraft client
type HandshakePacket struct {
	ProtocolVersion VarInt
	ServerAddress   string
	ServerPort      uint16
	NextState       VarInt // 1 for status, 2 for login
}

// ReadHandshakePacket reads a handshake packet from the connection
func ReadHandshakePacket(r io.Reader) (*HandshakePacket, error) {
	// Read packet length
	length, err := ReadVarInt(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read packet length: %w", err)
	}

	if length < 1 || length > 255 {
		return nil, fmt.Errorf("invalid packet length: %d", length)
	}

	// Read packet data
	data := make([]byte, length)
	n, err := io.ReadFull(r, data)
	if err != nil {
		return nil, fmt.Errorf("failed to read packet data (got %d/%d bytes): %w", n, length, err)
	}

	buf := bytes.NewReader(data)

	// Read packet ID (should be 0x00 for handshake)
	packetID, err := ReadVarInt(buf)
	if err != nil {
		return nil, fmt.Errorf("failed to read packet ID: %w", err)
	}

	if packetID != 0x00 {
		return nil, fmt.Errorf("expected handshake packet (0x00), got %d", packetID)
	}

	packet := &HandshakePacket{}

	// Read protocol version
	packet.ProtocolVersion, err = ReadVarInt(buf)
	if err != nil {
		return nil, fmt.Errorf("failed to read protocol version: %w", err)
	}

	// Read server address string
	addressLen, err := ReadVarInt(buf)
	if err != nil {
		return nil, fmt.Errorf("failed to read address length: %w", err)
	}

	addressBytes := make([]byte, addressLen)
	if _, err := io.ReadFull(buf, addressBytes); err != nil {
		return nil, fmt.Errorf("failed to read address: %w", err)
	}
	packet.ServerAddress = string(addressBytes)

	// Read server port
	if err := binary.Read(buf, binary.BigEndian, &packet.ServerPort); err != nil {
		return nil, fmt.Errorf("failed to read port: %w", err)
	}

	// Read next state
	packet.NextState, err = ReadVarInt(buf)
	if err != nil {
		return nil, fmt.Errorf("failed to read next state: %w", err)
	}

	return packet, nil
}

// WriteHandshakePacket writes a handshake packet to the connection
func WriteHandshakePacket(w io.Writer, packet *HandshakePacket) error {
	var buf bytes.Buffer

	// Write packet ID
	if err := WriteVarInt(&buf, 0x00); err != nil {
		return err
	}

	// Write protocol version
	if err := WriteVarInt(&buf, packet.ProtocolVersion); err != nil {
		return err
	}

	// Write server address
	if err := WriteVarInt(&buf, VarInt(len(packet.ServerAddress))); err != nil {
		return err
	}
	if _, err := buf.WriteString(packet.ServerAddress); err != nil {
		return err
	}

	// Write server port
	if err := binary.Write(&buf, binary.BigEndian, packet.ServerPort); err != nil {
		return err
	}

	// Write next state
	if err := WriteVarInt(&buf, packet.NextState); err != nil {
		return err
	}

	// Write packet length and data
	data := buf.Bytes()
	if err := WriteVarInt(w, VarInt(len(data))); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

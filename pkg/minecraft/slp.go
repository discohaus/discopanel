package minecraft

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/discohaus/discopanel/pkg/mcproto"
)

// Frame bound shared by packet and JSON string
const maxSLPPacketBytes = 1 << 20

// Implements the Minecraft Server List Ping protocol
type SLPClient struct {
	timeout time.Duration
}

// Parsed response from a server list ping
type SLPResult struct {
	Version struct {
		Name     string `json:"name"`
		Protocol int    `json:"protocol"`
	} `json:"version"`
	Players struct {
		Max    int `json:"max"`
		Online int `json:"online"`
		Sample []struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"sample"`
	} `json:"players"`
	Description    json.RawMessage `json:"description"`
	Favicon        string          `json:"favicon,omitempty"`
	EnforcesSecure bool            `json:"enforcesSecureChat,omitempty"`
	LatencyMs      int64
	Motd           string   // Parsed from Description
	PlayerNames    []string // Extracted player names
}

// SLP client with timeout
func NewSLPClient(timeout time.Duration) *SLPClient {
	return &SLPClient{
		timeout: timeout,
	}
}

// Server list ping to host and port
func (c *SLPClient) Ping(ctx context.Context, host string, port int) (*SLPResult, error) {
	// Create connection
	var d net.Dialer
	d.Timeout = c.timeout

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(c.timeout)
	}

	conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("failed to set deadline: %w", err)
	}

	// Protocol -1 is accepted for status pings
	if err := mcproto.WriteHandshakePacket(conn, &mcproto.HandshakePacket{
		ProtocolVersion: -1,
		ServerAddress:   host,
		ServerPort:      uint16(port),
		NextState:       mcproto.NextStateStatus,
	}); err != nil {
		return nil, fmt.Errorf("failed to send handshake: %w", err)
	}

	// Status request is one empty packet with id zero
	if err := mcproto.WriteFramed(conn, []byte{0x00}); err != nil {
		return nil, fmt.Errorf("failed to send status request: %w", err)
	}

	// Read status response
	jsonPayload, err := c.readStatusResponse(conn)
	if err != nil {
		return nil, fmt.Errorf("failed to read status response: %w", err)
	}

	// Send ping with timestamp
	pingTime := time.Now()
	pingPayload := pingTime.UnixMilli()
	if err := c.sendPing(conn, pingPayload); err != nil {
		return nil, fmt.Errorf("failed to send ping: %w", err)
	}

	// Pong payload echo varies by server, only timing matters
	if _, err := c.readPong(conn); err != nil {
		return nil, fmt.Errorf("failed to read pong: %w", err)
	}

	latency := time.Since(pingTime).Milliseconds()

	// Parse JSON response
	var result SLPResult
	if err := json.Unmarshal([]byte(jsonPayload), &result); err != nil {
		return nil, fmt.Errorf("failed to parse status response: %w", err)
	}

	result.LatencyMs = latency
	result.Motd = parseDescription(result.Description)

	// Extract player names
	for _, player := range result.Players.Sample {
		result.PlayerNames = append(result.PlayerNames, player.Name)
	}

	return &result, nil
}

// Read and parse status response
func (c *SLPClient) readStatusResponse(conn net.Conn) (string, error) {
	// Read packet length
	packetLen, err := mcproto.ReadVarInt(conn)
	if err != nil {
		return "", fmt.Errorf("failed to read packet length: %w", err)
	}

	if packetLen < 1 || packetLen > maxSLPPacketBytes {
		return "", fmt.Errorf("invalid packet length: %d", packetLen)
	}

	// Read packet data
	data := make([]byte, packetLen)
	if _, err := io.ReadFull(conn, data); err != nil {
		return "", fmt.Errorf("failed to read packet data: %w", err)
	}

	reader := bytes.NewReader(data)

	// Read packet ID (should be 0x00)
	packetID, err := mcproto.ReadVarInt(reader)
	if err != nil {
		return "", fmt.Errorf("failed to read packet ID: %w", err)
	}

	if packetID != 0x00 {
		return "", fmt.Errorf("unexpected packet ID: %d", packetID)
	}

	// Read JSON string
	jsonStr, err := mcproto.ReadString(reader, maxSLPPacketBytes)
	if err != nil {
		return "", fmt.Errorf("failed to read JSON string: %w", err)
	}

	return jsonStr, nil
}

// Send ping packet with payload
func (c *SLPClient) sendPing(conn net.Conn, payload int64) error {
	var buf bytes.Buffer
	// Packet ID (0x01 for ping)
	mcproto.WriteVarInt(&buf, 0x01)
	// Payload (8 bytes, big endian)
	if err := binary.Write(&buf, binary.BigEndian, payload); err != nil {
		return err
	}
	return mcproto.WriteFramed(conn, buf.Bytes())
}

// Read pong response and return payload
func (c *SLPClient) readPong(conn net.Conn) (int64, error) {
	// Read packet length
	packetLen, err := mcproto.ReadVarInt(conn)
	if err != nil {
		return 0, fmt.Errorf("failed to read packet length: %w", err)
	}

	if packetLen != 9 { // 1 byte VarInt (0x01) + 8 bytes payload
		return 0, fmt.Errorf("unexpected pong packet length: %d", packetLen)
	}

	// Read packet data
	data := make([]byte, packetLen)
	if _, err := io.ReadFull(conn, data); err != nil {
		return 0, fmt.Errorf("failed to read packet data: %w", err)
	}

	reader := bytes.NewReader(data)

	// Read packet ID (should be 0x01)
	packetID, err := mcproto.ReadVarInt(reader)
	if err != nil {
		return 0, fmt.Errorf("failed to read packet ID: %w", err)
	}

	if packetID != 0x01 {
		return 0, fmt.Errorf("unexpected packet ID: %d", packetID)
	}

	// Read payload
	var payload int64
	if err := binary.Read(reader, binary.BigEndian, &payload); err != nil {
		return 0, fmt.Errorf("failed to read payload: %w", err)
	}

	return payload, nil
}

// Extract plain text Motd from description field
func parseDescription(desc json.RawMessage) string {
	if len(desc) == 0 {
		return ""
	}

	// Try parsing as plain string first
	var plainStr string
	if err := json.Unmarshal(desc, &plainStr); err == nil {
		return strings.TrimSpace(plainStr)
	}

	// Try parsing as chat component object
	var component struct {
		Text  string `json:"text"`
		Extra []struct {
			Text string `json:"text"`
		} `json:"extra"`
	}
	if err := json.Unmarshal(desc, &component); err == nil {
		var result strings.Builder
		result.WriteString(component.Text)
		for _, extra := range component.Extra {
			result.WriteString(extra.Text)
		}
		return strings.TrimSpace(result.String())
	}

	// Strips JSON formatting and returns raw text
	return strings.TrimSpace(string(desc))
}

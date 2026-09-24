package mcproto

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestVarIntRoundTrip(t *testing.T) {
	values := []VarInt{0, 1, 127, 128, 255, 25565, 2097151, 2147483647, -1, -2147483648}
	for _, v := range values {
		var buf bytes.Buffer
		if err := WriteVarInt(&buf, v); err != nil {
			t.Fatalf("write %d: %v", v, err)
		}
		if buf.Len() != v.Len() {
			t.Errorf("value %d encoded %d bytes, Len() says %d", v, buf.Len(), v.Len())
		}
		got, err := ReadVarInt(&buf)
		if err != nil {
			t.Fatalf("read %d: %v", v, err)
		}
		if got != v {
			t.Errorf("round trip %d got %d", v, got)
		}
	}
}

func TestHandshakeRoundTrip(t *testing.T) {
	original := &HandshakePacket{
		ProtocolVersion: 763,
		ServerAddress:   "play.example.com\x00FML\x00",
		ServerPort:      25565,
		NextState:       NextStateLogin,
	}

	var buf bytes.Buffer
	if err := WriteHandshakePacket(&buf, original); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ReadHandshakePacket(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if *got != *original {
		t.Errorf("round trip mismatch: %+v vs %+v", got, original)
	}
}

func TestReadHandshakeRejectsWrongPacket(t *testing.T) {
	var buf bytes.Buffer
	WriteVarInt(&buf, 2)
	buf.Write([]byte{0x01, 0x00})
	if _, err := ReadHandshakePacket(&buf); err == nil {
		t.Error("expected error for non-handshake packet")
	}
}

func TestReadHandshakeRejectsOversize(t *testing.T) {
	var buf bytes.Buffer
	WriteVarInt(&buf, MaxHandshakeLength+1)
	if _, err := ReadHandshakePacket(&buf); err == nil {
		t.Error("expected error for oversized handshake")
	}
}

func TestWriteLoginDisconnectFrame(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteLoginDisconnect(&buf, "gone"); err != nil {
		t.Fatalf("write: %v", err)
	}

	length, err := ReadVarInt(&buf)
	if err != nil {
		t.Fatalf("length: %v", err)
	}
	payload := buf.Bytes()
	if int(length) != len(payload) {
		t.Fatalf("frame length %d but %d bytes remain", length, len(payload))
	}
	if payload[0] != 0x00 {
		t.Errorf("expected packet id 0x00, got %#x", payload[0])
	}
	if !bytes.Contains(payload, []byte(`"gone"`)) {
		t.Errorf("payload missing reason: %q", payload)
	}
}

func TestLegacyPingHostname(t *testing.T) {
	host := "mc.example.com"
	channel := utf16.Encode([]rune("MC|PingHost"))
	hostUnits := utf16.Encode([]rune(host))

	var buf bytes.Buffer
	buf.Write([]byte{LegacyPingByte, 0x01, 0xFA})
	binary.Write(&buf, binary.BigEndian, uint16(len(channel)))
	for _, u := range channel {
		binary.Write(&buf, binary.BigEndian, u)
	}
	binary.Write(&buf, binary.BigEndian, uint16(7+2*len(hostUnits)))
	buf.WriteByte(78)
	binary.Write(&buf, binary.BigEndian, uint16(len(hostUnits)))
	for _, u := range hostUnits {
		binary.Write(&buf, binary.BigEndian, u)
	}
	binary.Write(&buf, binary.BigEndian, uint32(25565))

	got, ok := LegacyPingHostname(buf.Bytes())
	if !ok || got != host {
		t.Errorf("expected %q, got %q ok=%v", host, got, ok)
	}
}

func TestLegacyPingHostnameRejectsGarbage(t *testing.T) {
	if _, ok := LegacyPingHostname([]byte{0x10, 0x20}); ok {
		t.Error("accepted non-ping bytes")
	}
	if _, ok := LegacyPingHostname([]byte{LegacyPingByte}); ok {
		t.Error("accepted truncated ping")
	}
}

func TestWriteLegacyKickModern(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteLegacyKick(&buf, true, "hello", "1.6.4", 20); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw := buf.Bytes()
	if raw[0] != 0xFF {
		t.Fatalf("expected kick id 0xFF, got %#x", raw[0])
	}
	units := int(binary.BigEndian.Uint16(raw[1:3]))
	if len(raw) != 3+2*units {
		t.Errorf("declared %d units but %d bytes follow", units, len(raw)-3)
	}
	decoded := make([]uint16, units)
	for i := range decoded {
		decoded[i] = binary.BigEndian.Uint16(raw[3+2*i:])
	}
	text := string(utf16.Decode(decoded))
	if !strings.Contains(text, "hello") || !strings.Contains(text, "1.6.4") {
		t.Errorf("kick payload missing fields: %q", text)
	}
}

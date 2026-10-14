package mcproto

import (
	"bytes"
	"testing"
)

// Builds a login start frame with trailing extras
func loginStartFrame(t *testing.T, name string, extra []byte) []byte {
	t.Helper()
	var body bytes.Buffer
	WriteVarInt(&body, 0x00)
	WriteVarInt(&body, VarInt(len(name)))
	body.WriteString(name)
	body.Write(extra)
	var framed bytes.Buffer
	if err := WriteFramed(&framed, body.Bytes()); err != nil {
		t.Fatalf("frame build failed %v", err)
	}
	return framed.Bytes()
}

// Name parses and the frame replays byte identical
func TestReadLoginStartRoundTrip(t *testing.T) {
	uuid := make([]byte, 16)
	frame := loginStartFrame(t, "Steve", uuid)

	ls, err := ReadLoginStart(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("read failed %v", err)
	}
	if ls.Name != "Steve" {
		t.Fatalf("name = %q, want Steve", ls.Name)
	}

	var out bytes.Buffer
	if err := ls.Replay(&out); err != nil {
		t.Fatalf("replay failed %v", err)
	}
	if !bytes.Equal(out.Bytes(), frame) {
		t.Fatalf("replay differs from original frame")
	}
}

// Bare legacy frames with only a name still parse
func TestReadLoginStartLegacy(t *testing.T) {
	frame := loginStartFrame(t, "Notch", nil)
	ls, err := ReadLoginStart(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("read failed %v", err)
	}
	if ls.Name != "Notch" {
		t.Fatalf("name = %q, want Notch", ls.Name)
	}
}

// Wrong packet ids never pass for login start
func TestReadLoginStartRejectsWrongID(t *testing.T) {
	var body bytes.Buffer
	WriteVarInt(&body, 0x01)
	WriteVarInt(&body, VarInt(4))
	body.WriteString("nope")
	var framed bytes.Buffer
	if err := WriteFramed(&framed, body.Bytes()); err != nil {
		t.Fatalf("frame build failed %v", err)
	}
	if _, err := ReadLoginStart(bytes.NewReader(framed.Bytes())); err == nil {
		t.Fatal("wrong id must fail")
	}
}

// Empty names get refused
func TestReadLoginStartRejectsEmptyName(t *testing.T) {
	frame := loginStartFrame(t, "", nil)
	if _, err := ReadLoginStart(bytes.NewReader(frame)); err == nil {
		t.Fatal("empty name must fail")
	}
}

// Oversized name lengths get refused
func TestReadLoginStartRejectsHugeName(t *testing.T) {
	frame := loginStartFrame(t, string(make([]byte, 200)), nil)
	if _, err := ReadLoginStart(bytes.NewReader(frame)); err == nil {
		t.Fatal("oversized name must fail")
	}
}

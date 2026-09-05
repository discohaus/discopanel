package packet

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Zero vectors ride one byte both ways
func TestLpVec3Zero(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteLpVec3Zero(&buf); err != nil {
		t.Fatalf("write failed %v", err)
	}
	if buf.Len() != 1 || buf.Bytes()[0] != 0 {
		t.Fatalf("zero vector wire %v", buf.Bytes())
	}
	x, y, z, err := ReadLpVec3(&buf)
	if err != nil || x != 0 || y != 0 || z != 0 {
		t.Fatalf("zero vector read %v %v %v %v", x, y, z, err)
	}
	if buf.Len() != 0 {
		t.Fatalf("zero vector leaves %d bytes", buf.Len())
	}
}

// Packed components come back inside quantized error
func TestLpVec3Packed(t *testing.T) {
	// Scale one with mid range quantized values
	pack := func(v float64) uint64 {
		return uint64((v*0.5 + 0.5) * 32766)
	}
	packed := uint64(1) | pack(0.5)<<3 | pack(-0.25)<<18 | pack(1)<<33
	var wire bytes.Buffer
	wire.WriteByte(byte(packed))
	wire.WriteByte(byte(packed >> 8))
	var high [4]byte
	binary.BigEndian.PutUint32(high[:], uint32(packed>>16))
	wire.Write(high[:])

	x, y, z, err := ReadLpVec3(&wire)
	if err != nil {
		t.Fatalf("read failed %v", err)
	}
	near := func(got, want float64) bool {
		d := got - want
		return d < 0.001 && d > -0.001
	}
	if !near(x, 0.5) || !near(y, -0.25) || !near(z, 1) {
		t.Fatalf("unpacked %v %v %v", x, y, z)
	}
}

// Json payloads become ordered network nbt
func TestJSONToNBTRoundTrip(t *testing.T) {
	src := `{"b_first": 1.5, "a_second": {"flag": true, "count": 12000}, "list": ["x", "y"], "big": 5000000000}`
	tag, err := JSONToNBT([]byte(src))
	if err != nil {
		t.Fatalf("convert failed %v", err)
	}
	var wire bytes.Buffer
	if err := WriteNetworkNBT(&wire, tag); err != nil {
		t.Fatalf("nbt write failed %v", err)
	}
	back, err := ReadNetworkNBT(&wire)
	if err != nil {
		t.Fatalf("nbt read failed %v", err)
	}
	root := back.(map[string]any)
	if root["b_first"] != float64(1.5) {
		t.Fatalf("double lost, %v", root["b_first"])
	}
	inner := root["a_second"].(map[string]any)
	if inner["flag"] != int8(1) || inner["count"] != int32(12000) {
		t.Fatalf("inner lost, %v", inner)
	}
	if root["big"] != int64(5000000000) {
		t.Fatalf("long lost, %v", root["big"])
	}
	list := root["list"].([]any)
	if len(list) != 2 || list[0] != "x" {
		t.Fatalf("list lost, %v", list)
	}
}

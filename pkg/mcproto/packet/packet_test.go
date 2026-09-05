package packet

import (
	"bytes"
	"crypto/rand"
	"testing"
)

// Varlong survives the trip both ways
func TestVarLongRoundTrip(t *testing.T) {
	values := []int64{0, 1, -1, 127, 128, 255, 2147483647, -2147483648, 9223372036854775807, -9223372036854775808}
	for _, v := range values {
		var buf bytes.Buffer
		if err := WriteVarLong(&buf, v); err != nil {
			t.Fatalf("write %d failed %v", v, err)
		}
		got, err := ReadVarLong(&buf)
		if err != nil || got != v {
			t.Fatalf("roundtrip %d = %d, err %v", v, got, err)
		}
	}
}

// Strings and byte arrays survive the trip
func TestStringAndBytesRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteString(&buf, "hello world"); err != nil {
		t.Fatalf("string write failed %v", err)
	}
	got, err := ReadString(&buf)
	if err != nil || got != "hello world" {
		t.Fatalf("string roundtrip = %q, err %v", got, err)
	}

	payload := []byte{1, 2, 3, 4, 5}
	buf.Reset()
	if err := WriteShortBytes(&buf, payload); err != nil {
		t.Fatalf("short write failed %v", err)
	}
	sb, err := ReadShortBytes(&buf, 16)
	if err != nil || !bytes.Equal(sb, payload) {
		t.Fatalf("short roundtrip = %v, err %v", sb, err)
	}

	buf.Reset()
	if err := WriteVarBytes(&buf, payload); err != nil {
		t.Fatalf("var write failed %v", err)
	}
	vb, err := ReadVarBytes(&buf, 16)
	if err != nil || !bytes.Equal(vb, payload) {
		t.Fatalf("var roundtrip = %v, err %v", vb, err)
	}
}

// Position packing matches the two wire eras
func TestPositionPacking(t *testing.T) {
	x, y, z := 100, -60, -200
	old := PositionOld(x, y, z)
	if int(int64(old)>>38) != x {
		t.Fatal("old x wrong")
	}
	if int(int64(old)<<26>>52) != y {
		t.Fatal("old y wrong")
	}
	if int(int64(old)<<38>>38) != z {
		t.Fatal("old z wrong")
	}
	modern := PositionNew(x, y, z)
	if int(int64(modern)>>38) != x {
		t.Fatal("new x wrong")
	}
	if int(int64(modern)<<26>>38) != z {
		t.Fatal("new z wrong")
	}
	if int(int64(modern)<<52>>52) != y {
		t.Fatal("new y wrong")
	}
}

// Frames survive both compression modes
func TestFrameZRoundTrip(t *testing.T) {
	small := []byte{0x01, 0x02, 0x03}
	big := bytes.Repeat([]byte{0xAB}, 1024)

	for _, threshold := range []int{-1, 64} {
		for _, body := range [][]byte{small, big} {
			var buf bytes.Buffer
			if err := WriteFrameZ(&buf, body, threshold); err != nil {
				t.Fatalf("write failed %v", err)
			}
			got, err := ReadFrameZ(&buf, threshold)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("threshold %d len %d roundtrip failed, err %v", threshold, len(body), err)
			}
		}
	}
}

// Cipher streams agree with each other
func TestCFB8RoundTrip(t *testing.T) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("key gen failed %v", err)
	}
	plain := []byte("the quick brown fox jumps over the lazy dog")

	var wire bytes.Buffer
	cw, err := NewCipherWriter(&wire, key)
	if err != nil {
		t.Fatalf("writer build failed %v", err)
	}
	// Byte at a time writes must match bulk reads
	for _, b := range plain {
		if _, err := cw.Write([]byte{b}); err != nil {
			t.Fatalf("write failed %v", err)
		}
	}
	if bytes.Equal(wire.Bytes(), plain) {
		t.Fatal("ciphertext equals plaintext")
	}

	cr, err := NewCipherReader(&wire, key)
	if err != nil {
		t.Fatalf("reader build failed %v", err)
	}
	got := make([]byte, len(plain))
	n := 0
	for n < len(plain) {
		m, err := cr.Read(got[n:])
		if err != nil {
			t.Fatalf("read failed %v", err)
		}
		n += m
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("decrypt = %q", got)
	}
}

// Nbt output matches hand built bytes
func TestNBTKnownBytes(t *testing.T) {
	root := NBTCompound{{Name: "a", Tag: NBTByte(1)}}
	var buf bytes.Buffer
	if err := WriteNBT(&buf, "", root); err != nil {
		t.Fatalf("write failed %v", err)
	}
	want := []byte{0x0A, 0x00, 0x00, 0x01, 0x00, 0x01, 'a', 0x01, 0x00}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("nbt = %x, want %x", buf.Bytes(), want)
	}

	buf.Reset()
	if err := WriteNetworkNBT(&buf, root); err != nil {
		t.Fatalf("network write failed %v", err)
	}
	want = []byte{0x0A, 0x01, 0x00, 0x01, 'a', 0x01, 0x00}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("network nbt = %x, want %x", buf.Bytes(), want)
	}
}

// Lists and arrays write their element forms
func TestNBTListAndArrays(t *testing.T) {
	root := NBTCompound{
		{Name: "l", Tag: NBTList{Elem: []Tag{NBTInt(7)}}},
		{Name: "ia", Tag: NBTIntArray{1, 2}},
		{Name: "la", Tag: NBTLongArray{3}},
		{Name: "s", Tag: NBTString("x")},
	}
	var buf bytes.Buffer
	if err := WriteNetworkNBT(&buf, root); err != nil {
		t.Fatalf("write failed %v", err)
	}
	want := []byte{
		0x0A,
		0x09, 0x00, 0x01, 'l', 0x03, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x07,
		0x0B, 0x00, 0x02, 'i', 'a', 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x02,
		0x0C, 0x00, 0x02, 'l', 'a', 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x03,
		0x08, 0x00, 0x01, 's', 0x00, 0x01, 'x',
		0x00,
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("nbt = %x, want %x", buf.Bytes(), want)
	}
}

// Bit packers place values where the eras expect
func TestBitPacking(t *testing.T) {
	values := []uint32{1, 2, 3, 4, 5}

	// Thirteen bit spanning crosses the first long boundary
	spanned := PackSpanning(values, 13)
	if len(spanned) != 2 {
		t.Fatalf("spanning longs = %d", len(spanned))
	}
	for i, want := range values {
		bitIndex := i * 13
		long := bitIndex / 64
		offset := bitIndex % 64
		got := spanned[long] >> offset
		if offset+13 > 64 {
			got |= spanned[long+1] << (64 - offset)
		}
		if uint32(got)&0x1FFF != want {
			t.Fatalf("spanned value %d = %d", i, uint32(got)&0x1FFF)
		}
	}

	// Padded packing keeps four bit values in one long
	padded := PackPadded(values, 4)
	if len(padded) != 1 {
		t.Fatalf("padded longs = %d", len(padded))
	}
	for i, want := range values {
		got := uint32(padded[0]>>(i*4)) & 0xF
		if got != want {
			t.Fatalf("padded value %d = %d", i, got)
		}
	}

	if BitsFor(16, 4) != 4 || BitsFor(17, 4) != 5 || BitsFor(2, 4) != 4 {
		t.Fatal("bits sizing wrong")
	}
}

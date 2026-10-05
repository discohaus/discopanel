package packet

import (
	"encoding/binary"
	"io"

	"github.com/discohaus/discopanel/pkg/mcproto"
)

// Largest quantized component the packing holds
const lpMaxQuantized = 32766.0

// Reads one packed low precision vector
// Zero vectors ride a single zero byte
func ReadLpVec3(r io.Reader) (x, y, z float64, err error) {
	var head [1]byte
	if _, err = io.ReadFull(r, head[:]); err != nil {
		return 0, 0, 0, err
	}
	if head[0] == 0 {
		return 0, 0, 0, nil
	}

	var rest [5]byte
	if _, err = io.ReadFull(r, rest[:]); err != nil {
		return 0, 0, 0, err
	}
	packed := uint64(head[0]) | uint64(rest[0])<<8 |
		uint64(binary.BigEndian.Uint32(rest[1:]))<<16

	scale := float64(head[0] & 3)
	if head[0]&4 != 0 {
		wide, err := mcproto.ReadVarInt(r)
		if err != nil {
			return 0, 0, 0, err
		}
		scale = float64(wide)*4 + scale
	}

	unpack := func(shift uint) float64 {
		quantized := float64((packed >> shift) & 0x7fff)
		if quantized > lpMaxQuantized {
			quantized = lpMaxQuantized
		}
		return (quantized*2)/lpMaxQuantized - 1
	}
	return unpack(3) * scale, unpack(18) * scale, unpack(33) * scale, nil
}

// Writes one zero packed vector
func WriteLpVec3Zero(w io.Writer) error {
	_, err := w.Write([]byte{0})
	return err
}

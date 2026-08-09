package packet

// Packs values across long boundaries, pre 1.16 style
func PackSpanning(values []uint32, bits int) []uint64 {
	if bits <= 0 || len(values) == 0 {
		return nil
	}
	total := (len(values)*bits + 63) / 64
	out := make([]uint64, total)
	for i, v := range values {
		bitIndex := i * bits
		long := bitIndex / 64
		offset := bitIndex % 64
		out[long] |= uint64(v) << offset
		if offset+bits > 64 {
			out[long+1] |= uint64(v) >> (64 - offset)
		}
	}
	return out
}

// Packs values without spanning, 1.16 plus style
func PackPadded(values []uint32, bits int) []uint64 {
	if bits <= 0 || len(values) == 0 {
		return nil
	}
	perLong := 64 / bits
	total := (len(values) + perLong - 1) / perLong
	out := make([]uint64, total)
	for i, v := range values {
		long := i / perLong
		offset := (i % perLong) * bits
		out[long] |= uint64(v) << offset
	}
	return out
}

// Smallest bit width covering one palette size
func BitsFor(paletteSize, min int) int {
	bits := 0
	for 1<<bits < paletteSize {
		bits++
	}
	if bits < min {
		return min
	}
	return bits
}

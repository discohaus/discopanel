package utils

// CurseForge file fingerprint, murmur2 over whitespace-stripped bytes
func CFFingerprint(data []byte) uint32 {
	filtered := make([]byte, 0, len(data))
	for _, b := range data {
		if b == 9 || b == 10 || b == 13 || b == 32 {
			continue
		}
		filtered = append(filtered, b)
	}
	return murmur2(filtered, 1)
}

func murmur2(data []byte, seed uint32) uint32 {
	const m = 0x5bd1e995
	const r = 24

	h := seed ^ uint32(len(data))
	i := 0
	for ; i+4 <= len(data); i += 4 {
		k := uint32(data[i]) | uint32(data[i+1])<<8 | uint32(data[i+2])<<16 | uint32(data[i+3])<<24
		k *= m
		k ^= k >> r
		k *= m
		h *= m
		h ^= k
	}
	switch len(data) - i {
	case 3:
		h ^= uint32(data[i+2]) << 16
		fallthrough
	case 2:
		h ^= uint32(data[i+1]) << 8
		fallthrough
	case 1:
		h ^= uint32(data[i])
		h *= m
	}
	h ^= h >> 13
	h *= m
	h ^= h >> 15
	return h
}

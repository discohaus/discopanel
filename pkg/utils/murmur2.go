package utils

import (
	"io"
	"os"
)

// Murmur2 mixing constants
const (
	murmur2M = 0x5bd1e995
	murmur2R = 24
)

// Seed CurseForge uses for file fingerprints
const cfFingerprintSeed = 1

// Read size for streaming a jar through the hasher
const cfReadChunk = 64 << 10

// Streaming murmur2 state fed in any chunking
type murmur2Hasher struct {
	h    uint32
	tail [4]byte
	n    int
}

// Starts a murmur2 hash for the given total length
func newMurmur2(seed, length uint32) *murmur2Hasher {
	return &murmur2Hasher{h: seed ^ length}
}

// Mixes one little endian block into the running hash
func (m *murmur2Hasher) block(b0, b1, b2, b3 byte) {
	k := uint32(b0) | uint32(b1)<<8 | uint32(b2)<<16 | uint32(b3)<<24
	k *= murmur2M
	k ^= k >> murmur2R
	k *= murmur2M
	m.h *= murmur2M
	m.h ^= k
}

// Feeds bytes, carrying a partial block across calls
func (m *murmur2Hasher) Write(p []byte) {
	i := 0
	for m.n > 0 && i < len(p) {
		m.tail[m.n] = p[i]
		m.n++
		i++
		if m.n == 4 {
			m.block(m.tail[0], m.tail[1], m.tail[2], m.tail[3])
			m.n = 0
		}
	}
	for ; i+4 <= len(p); i += 4 {
		m.block(p[i], p[i+1], p[i+2], p[i+3])
	}
	for ; i < len(p); i++ {
		m.tail[m.n] = p[i]
		m.n++
	}
}

// Finishes the hash with the carried tail bytes
func (m *murmur2Hasher) Sum32() uint32 {
	h := m.h
	switch m.n {
	case 3:
		h ^= uint32(m.tail[2]) << 16
		fallthrough
	case 2:
		h ^= uint32(m.tail[1]) << 8
		fallthrough
	case 1:
		h ^= uint32(m.tail[0])
		h *= murmur2M
	}
	h ^= h >> 13
	h *= murmur2M
	h ^= h >> 15
	return h
}

// Bytes CurseForge strips before fingerprinting
func cfWhitespace(b byte) bool {
	return b == 9 || b == 10 || b == 13 || b == 32
}

// Feeds non whitespace runs so no filtered copy is made
func feedFiltered(m *murmur2Hasher, p []byte) {
	start := 0
	for i, b := range p {
		if cfWhitespace(b) {
			if i > start {
				m.Write(p[start:i])
			}
			start = i + 1
		}
	}
	if start < len(p) {
		m.Write(p[start:])
	}
}

// Counts the bytes that survive whitespace stripping
func countFiltered(p []byte) uint64 {
	var count uint64
	for _, b := range p {
		if !cfWhitespace(b) {
			count++
		}
	}
	return count
}

// CurseForge file fingerprint, murmur2 over whitespace-stripped bytes
func CFFingerprint(data []byte) uint32 {
	m := newMurmur2(cfFingerprintSeed, uint32(countFiltered(data)))
	feedFiltered(m, data)
	return m.Sum32()
}

// CurseForge fingerprint of a file, two passes in constant memory
func CFFingerprintFile(path string) (uint32, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	buf := make([]byte, cfReadChunk)
	var count uint64
	for {
		n, err := file.Read(buf)
		count += countFiltered(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	m := newMurmur2(cfFingerprintSeed, uint32(count))
	for {
		n, err := file.Read(buf)
		feedFiltered(m, buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	return m.Sum32(), nil
}
